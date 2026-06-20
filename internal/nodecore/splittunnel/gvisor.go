package splittunnel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	coretun "github.com/x6nux/corelink/internal/nodecore/tun"
	"github.com/x6nux/corelink/pkg/tunnel"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	gvipv4 "gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	gvicmp "gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
)

const (
	// gvisorNICID 是 gVisor 虚拟 NIC 编号。
	gvisorNICID = 1
	// gvisorChannelSize 是 channel endpoint 的队列深度。
	gvisorChannelSize = 1024
	// gvisorMTU 是虚拟 NIC 的 MTU。
	gvisorMTU = 1500
	// udpIdleTimeout UDP 转发空闲超时。
	udpIdleTimeout = 30 * time.Second
	// tcpForwarderMaxInFlight TCP forwarder 最大并发半开连接数。
	tcpForwarderMaxInFlight = 256
)

// icmpMaxConcurrent 限制并发 ICMP 请求数量，避免高频 ping 耗尽文件描述符。
const icmpMaxConcurrent = 16

// GVisorStack 用户态 TCP/IP 协议栈，处理 direct 流量。
// 从 TUN wrapper 收到 L3 IP 包后注入 gVisor，
// TCP/UDP forwarder 将其解封为 L4 连接并通过物理网卡拨出。
// gVisor 回复包（SYN-ACK/数据）通过 WriteNotify 回写 TUN。
type GVisorStack struct {
	s            *stack.Stack
	ep           *channel.Endpoint
	notifyHandle *channel.NotificationHandle
	physIfce     string
	tunDev       coretun.Device // 用于回写 TUN（gVisor 回复 + ICMP）
	tunOffset    int            // TUN Write 偏移（需覆盖 virtio header 空间）
	icmpSem      chan struct{}  // 并发 ICMP 请求信号量
	wg           sync.WaitGroup
	closed       chan struct{}
}

// NewGVisorStack 创建用户态 TCP/IP 协议栈。
// physIfce 是物理出口网卡名（用于 BindControl 绑定），tunDev 用于写回 ICMP 回复（可为 nil）。
func NewGVisorStack(physIfce string, tunDev coretun.Device) (*GVisorStack, error) {
	opts := stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			gvipv4.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
			gvicmp.NewProtocol4,
		},
		HandleLocal: false,
	}

	s := stack.New(opts)

	// 启用 TCP SACK
	sackEnabled := tcpip.TCPSACKEnabled(true)
	if tcpipErr := s.SetTransportProtocolOption(tcp.ProtocolNumber, &sackEnabled); tcpipErr != nil {
		s.Close()
		return nil, fmt.Errorf("splittunnel: 启用 TCP SACK: %v", tcpipErr)
	}

	ep := channel.New(gvisorChannelSize, gvisorMTU, "")

	if tcpipErr := s.CreateNIC(gvisorNICID, ep); tcpipErr != nil {
		s.Close()
		return nil, fmt.Errorf("splittunnel: CreateNIC: %v", tcpipErr)
	}

	// 添加默认路由，让所有 IPv4 流量都能路由到此 NIC
	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: gvisorNICID})

	// 设置混杂模式——接受所有目标 IP 的包（因为目标是互联网各 IP，不是本机）
	if tcpipErr := s.SetPromiscuousMode(gvisorNICID, true); tcpipErr != nil {
		s.Close()
		return nil, fmt.Errorf("splittunnel: SetPromiscuousMode: %v", tcpipErr)
	}
	// 欺骗模式——允许从任意源 IP 发包
	if tcpipErr := s.SetSpoofing(gvisorNICID, true); tcpipErr != nil {
		s.Close()
		return nil, fmt.Errorf("splittunnel: SetSpoofing: %v", tcpipErr)
	}

	// 计算 TUN Write offset：需覆盖可能的 virtio header 空间。
	// 对于开启 vnetHdr 的 Linux TUN，Write 内部会 offset -= virtioNetHdrLen(10)，
	// 所以我们的 offset 至少需要 10 字节。保险起见使用与 wireguard-go 一致的偏移量。
	tunOffset := 4
	if tunDev != nil {
		mtu, _ := tunDev.MTU()
		if mtu > 0 {
			// BatchSize() > 1 表示 vnetHdr 已启用，需要更大的 offset
			if tunDev.BatchSize() > 1 {
				tunOffset = 10 // virtioNetHdrLen
			}
		}
	}
	gs := &GVisorStack{
		s:         s,
		ep:        ep,
		physIfce:  physIfce,
		tunDev:    tunDev,
		tunOffset: tunOffset,
		icmpSem:   make(chan struct{}, icmpMaxConcurrent),
		closed:    make(chan struct{}),
	}

	// 注册 TCP forwarder
	tcpFwd := tcp.NewForwarder(s, 0, tcpForwarderMaxInFlight, gs.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// 注册 UDP forwarder
	udpFwd := udp.NewForwarder(s, gs.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// 注册 channel endpoint 写通知：gVisor 发包时回写 TUN
	gs.notifyHandle = ep.AddNotify(gs)

	slog.Info("splittunnel: gVisor 用户态协议栈已创建", "physIfce", physIfce)
	return gs, nil
}

// WriteNotify 实现 channel.Notification 接口。
// gVisor 协议栈发出的包（SYN-ACK/ACK/数据回复）通过此回调写回 TUN，
// 完成 app → TUN → gVisor → (处理) → channel → TUN → kernel 的闭环。
func (gs *GVisorStack) WriteNotify() {
	pkt := gs.ep.Read()
	if pkt == nil {
		return
	}
	view := pkt.ToView()
	pkt.DecRef()

	if gs.tunDev == nil {
		return
	}

	data := view.AsSlice()
	if len(data) == 0 {
		return
	}

	off := gs.tunOffset
	bufs := [][]byte{make([]byte, len(data)+off)}
	copy(bufs[0][off:], data)
	if _, err := gs.tunDev.Write(bufs, off); err != nil {
		slog.Debug("splittunnel: gVisor 回写 TUN 失败", "err", err)
	}
}

// InjectPacket 将一个完整的 IP 包注入 gVisor 协议栈进行处理。
func (gs *GVisorStack) InjectPacket(pkt []byte) {
	if len(pkt) < 20 {
		return
	}
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	gs.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
	pkb.DecRef()
}

// HandleICMP 处理 ICMP Echo Request：通过物理网卡发出并将回复写回 TUN。
func (gs *GVisorStack) HandleICMP(pkt []byte) {
	if len(pkt) < 28 { // IP(20) + ICMP(8)
		return
	}
	// 解析目标 IP
	dstIP := net.IP(pkt[16:20]).String()

	// 解析 ICMP 载荷（跳过 IP 头）
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+8 {
		return
	}
	icmpPayload := pkt[ihl:]

	msg, err := icmp.ParseMessage(1 /* ICMPv4 */, icmpPayload)
	if err != nil {
		slog.Debug("splittunnel: 解析 ICMP 失败", "err", err)
		return
	}
	if msg.Type != ipv4.ICMPTypeEcho {
		return
	}

	// 防止 Close() 完成 wg.Wait() 后执行 wg.Add(1) 导致 panic
	select {
	case <-gs.closed:
		return
	default:
	}
	// 信号量限流：避免高频 ping 耗尽 raw socket 文件描述符
	select {
	case gs.icmpSem <- struct{}{}:
	default:
		slog.Debug("splittunnel: ICMP 并发达上限，丢弃", "dst", dstIP)
		return
	}
	gs.wg.Add(1)
	go func() {
		defer gs.wg.Done()
		defer func() { <-gs.icmpSem }()

		// 使用 BindControl 绑定物理网卡
		lc := net.ListenConfig{Control: tunnel.BindControl}
		pc, err := lc.ListenPacket(context.Background(), "ip4:icmp", "0.0.0.0")
		if err != nil {
			slog.Debug("splittunnel: ICMP listen 失败", "err", err)
			return
		}
		defer pc.Close()

		// 序列化原始 Echo Request 并发送
		reqBytes, err := msg.Marshal(nil)
		if err != nil {
			slog.Debug("splittunnel: 序列化 ICMP 失败", "err", err)
			return
		}
		dst := &net.IPAddr{IP: net.ParseIP(dstIP)}
		if _, err := pc.WriteTo(reqBytes, dst); err != nil {
			slog.Debug("splittunnel: ICMP 发送失败", "dst", dstIP, "err", err)
			return
		}

		// 等待回复（带超时）
		if err := pc.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		buf := make([]byte, 1500)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			slog.Debug("splittunnel: ICMP 读取回复超时/失败", "dst", dstIP, "err", err)
			return
		}

		// 验证回复类型：仅转发 Echo Reply（type 0），忽略其他 ICMP 消息
		replyMsg, parseErr := icmp.ParseMessage(1, buf[:n])
		if parseErr != nil {
			slog.Debug("splittunnel: 解析 ICMP 回复失败", "dst", dstIP, "err", parseErr)
			return
		}
		if replyMsg.Type != ipv4.ICMPTypeEchoReply {
			slog.Debug("splittunnel: 收到非 Echo Reply ICMP，忽略", "dst", dstIP, "type", replyMsg.Type)
			return
		}

		// 将回复写回 TUN
		if gs.tunDev == nil {
			return
		}

		reply, err := buildICMPReplyPacket(pkt, buf[:n])
		if err != nil {
			slog.Debug("splittunnel: 构建 ICMP 回复包失败", "err", err)
			return
		}
		off := gs.tunOffset
		bufs := [][]byte{make([]byte, len(reply)+off)}
		copy(bufs[0][off:], reply)
		if _, err := gs.tunDev.Write(bufs, off); err != nil {
			slog.Debug("splittunnel: ICMP 写回 TUN 失败", "err", err)
		}
	}()
}

// Close 关闭协议栈并等待所有转发 goroutine 结束。
func (gs *GVisorStack) Close() {
	select {
	case <-gs.closed:
		return
	default:
	}
	close(gs.closed)

	if gs.notifyHandle != nil {
		gs.ep.RemoveNotify(gs.notifyHandle)
	}
	gs.s.RemoveNIC(gvisorNICID)
	gs.s.Close()
	gs.ep.Close()
	gs.wg.Wait()
	slog.Info("splittunnel: gVisor 用户态协议栈已关闭")
}

// handleTCP 处理 TCP forwarder 请求：从 gVisor 接受连接后，通过物理网卡拨出并双向转发。
func (gs *GVisorStack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))

	var wq waiter.Queue
	ep, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		r.Complete(true) // 发送 RST
		slog.Debug("splittunnel: TCP CreateEndpoint 失败", "dst", dst, "err", tcpipErr)
		return
	}
	r.Complete(false) // 确认握手
	gConn := gonet.NewTCPConn(&wq, ep)

	select {
	case <-gs.closed:
		gConn.Close()
		return
	default:
	}
	gs.wg.Add(1)
	go func() {
		defer gs.wg.Done()
		defer gConn.Close()

		dialer := net.Dialer{Control: tunnel.BindControl}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rConn, err := dialer.DialContext(ctx, "tcp", dst)
		if err != nil {
			slog.Debug("splittunnel: direct TCP 拨号失败", "dst", dst, "err", err)
			return
		}
		defer rConn.Close()

		done := make(chan struct{})
		go func() {
			io.Copy(rConn, gConn)
			close(done)
		}()
		io.Copy(gConn, rConn)
		<-done
	}()
}

// handleUDP 处理 UDP forwarder 请求：从 gVisor 接受 UDP 后，通过物理网卡拨出并双向转发。
func (gs *GVisorStack) handleUDP(r *udp.ForwarderRequest) {
	id := r.ID()
	dst := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))

	var wq waiter.Queue
	ep, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		slog.Debug("splittunnel: UDP CreateEndpoint 失败", "dst", dst, "err", tcpipErr)
		return
	}
	gConn := gonet.NewUDPConn(&wq, ep)

	select {
	case <-gs.closed:
		gConn.Close()
		return
	default:
	}
	gs.wg.Add(1)
	go func() {
		defer gs.wg.Done()
		defer gConn.Close()

		dialer := net.Dialer{Control: tunnel.BindControl}
		rConn, err := dialer.DialContext(context.Background(), "udp", dst)
		if err != nil {
			slog.Debug("splittunnel: direct UDP 拨号失败", "dst", dst, "err", err)
			return
		}
		defer rConn.Close()

		done := make(chan struct{})

		// gVisor -> 物理网卡
		go func() {
			defer close(done)
			buf := make([]byte, 65535)
			for {
				gConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
				n, err := gConn.Read(buf)
				if err != nil {
					return
				}
				rConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := rConn.Write(buf[:n]); err != nil {
					return
				}
			}
		}()

		// 物理网卡 -> gVisor
		buf := make([]byte, 65535)
		for {
			rConn.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			n, err := rConn.Read(buf)
			if err != nil {
				break
			}
			gConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := gConn.Write(buf[:n]); err != nil {
				break
			}
		}
		<-done
	}()
}

// buildICMPReplyPacket 用原始请求包头信息和 ICMP 回复载荷构造完整的 IP+ICMP 回复包。
// origPkt 是原始 IP 包（用于提取源/目标 IP），icmpReply 是收到的 ICMP 回复载荷。
func buildICMPReplyPacket(origPkt []byte, icmpReply []byte) ([]byte, error) {
	if len(origPkt) < 20 {
		return nil, fmt.Errorf("原始包太短")
	}

	totalLen := 20 + len(icmpReply)
	pkt := make([]byte, totalLen)

	// IP 头
	pkt[0] = 0x45 // version=4, IHL=5
	pkt[1] = 0    // DSCP/ECN
	pkt[2] = byte(totalLen >> 8)
	pkt[3] = byte(totalLen)
	pkt[4] = 0 // identification
	pkt[5] = 0
	pkt[6] = 0x40 // flags: DF
	pkt[7] = 0
	pkt[8] = 64 // TTL
	pkt[9] = 1  // protocol: ICMP

	// 源 IP = 原始目标 IP，目标 IP = 原始源 IP（回复方向）
	copy(pkt[12:16], origPkt[16:20]) // src <- orig dst
	copy(pkt[16:20], origPkt[12:16]) // dst <- orig src

	// 校验和
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(pkt[i])<<8 | uint32(pkt[i+1])
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	cs := ^uint16(sum)
	pkt[10] = byte(cs >> 8)
	pkt[11] = byte(cs)

	// ICMP 载荷
	copy(pkt[20:], icmpReply)
	return pkt, nil
}
