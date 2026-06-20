package dataplane

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/transport"
)

// Listener 数据面专用 TLS 监听器。
// 接受 mTLS 连接 → 读 Transport 帧 → 按 DstVIP 路由（本地投递或转发）。
// 不经过 AccessListener，无 OU 判断、无协议检测。
type Listener struct {
	ln               net.Listener
	tlsConf          *tls.Config
	onFrame          func(nodeID string, dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte)
	onDNS            func(srcVIP netip.Addr, dnsPayload []byte, replyFramer *transport.Framer)
	onProbe          func(nodeID string, sourceVIP netip.Addr, payload []byte)
	onPeerConnect    func(nodeID string, f *transport.Framer)
	onPeerDisconnect func(nodeID string, f *transport.Framer)
	wg               sync.WaitGroup
	closeOnce        sync.Once
	closeCh          chan struct{}
	closeErr         error

	connMu      sync.Mutex
	activeConns map[net.Conn]struct{} // 跟踪活跃连接，Close 时遍历关闭
}

// ListenerConfig 数据面监听器配置。
type ListenerConfig struct {
	Addr    string      // 监听地址，如 ":7447"
	TLSConf *tls.Config // mTLS 配置（含服务端证书 + CA 池）
	// OnFrame 收到帧时回调（nodeID 来自 mTLS 证书 CN）
	OnFrame func(nodeID string, dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte)
	// OnDNS 收到 DNS 帧时回调（srcVIP=请求方 VIP，payload=原始 DNS 报文）
	OnDNS func(srcVIP netip.Addr, dnsPayload []byte, replyFramer *transport.Framer)
	// OnProbe 收到路径探测帧时回调（逐跳转发或终点回复）。
	// sourceVIP 为帧头 DstVIP（WriteProbeFrame 填充的目标 VIP），payload 为 ProbeFrame 编码数据。
	OnProbe func(nodeID string, sourceVIP netip.Addr, payload []byte)
	// OnPeerConnect/OnPeerDisconnect 注册/注销入站连接的 Framer（双向连接复用）
	OnPeerConnect    func(nodeID string, f *transport.Framer)
	OnPeerDisconnect func(nodeID string, f *transport.Framer)
}

// NewListener 创建并启动数据面 TLS 监听器。
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.TLSConf == nil {
		return nil, fmt.Errorf("dataplane: ListenerConfig.TLSConf 不能为 nil")
	}
	ln, err := tls.Listen("tcp", cfg.Addr, cfg.TLSConf)
	if err != nil {
		return nil, err
	}
	l := &Listener{
		ln:               ln,
		tlsConf:          cfg.TLSConf,
		onFrame:          cfg.OnFrame,
		onDNS:            cfg.OnDNS,
		onProbe:          cfg.OnProbe,
		onPeerConnect:    cfg.OnPeerConnect,
		onPeerDisconnect: cfg.OnPeerDisconnect,
		closeCh:          make(chan struct{}),
		activeConns:      make(map[net.Conn]struct{}),
	}
	l.wg.Add(1)
	go l.acceptLoop()
	slog.Info("dataplane: 数据面监听器已启动", "addr", ln.Addr())
	return l, nil
}

// Addr 返回监听地址。
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Close 关闭监听器（并发安全，使用 sync.Once 保证仅执行一次）。
// 关闭底层 listener 并遍历关闭所有活跃连接，解除 handleConn 的 Read 阻塞。
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closeCh)
		l.closeErr = l.ln.Close()
		// 关闭所有活跃连接以解除 handleConn goroutine 的 Read 阻塞
		l.connMu.Lock()
		for c := range l.activeConns {
			c.Close()
		}
		l.activeConns = nil
		l.connMu.Unlock()
		// 带超时等待，避免连接阻塞导致 Close 永久挂起
		done := make(chan struct{})
		go func() { l.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			slog.Warn("listener: Close 等待 goroutine 超时（可能有连接仍阻塞）")
		}
	})
	return l.closeErr
}

func (l *Listener) acceptLoop() {
	defer l.wg.Done()
	for {
		c, err := l.ln.Accept()
		if err != nil {
			select {
			case <-l.closeCh:
				return
			default:
				slog.Debug("dataplane: listener accept 失败", "err", err)
				continue
			}
		}
		l.wg.Add(1)
		go l.handleConn(c)
	}
}

func (l *Listener) handleConn(c net.Conn) {
	defer l.wg.Done()
	// 注册活跃连接（Close 时遍历关闭以解除 Read 阻塞）
	l.connMu.Lock()
	if l.activeConns != nil {
		l.activeConns[c] = struct{}{}
	}
	l.connMu.Unlock()
	defer func() {
		c.Close()
		l.connMu.Lock()
		delete(l.activeConns, c)
		l.connMu.Unlock()
	}()

	// 提取 nodeID（mTLS 证书 CN）
	tlsConn, ok := c.(*tls.Conn)
	if !ok {
		return
	}

	// 关闭 Nagle + 设置握手超时（防 slowloris 攻击）
	if tc, ok := tlsConn.NetConn().(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		slog.Debug("dataplane: listener TLS 握手失败", "err", err)
		return
	}
	c.SetDeadline(time.Time{}) // 清除握手超时
	// 握手完成后关闭 Nagle 算法，降低小包延迟
	if tcpConn, ok2 := tlsConn.NetConn().(*net.TCPConn); ok2 {
		tcpConn.SetNoDelay(true)
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return
	}
	nodeID := state.PeerCertificates[0].Subject.CommonName

	slog.Info("dataplane: 数据面连接接入", "nodeID", nodeID[:min(8, len(nodeID))], "remote", c.RemoteAddr())

	// 注册入站连接的 Framer（用于反向发帧给该 peer）。
	// 重要：此后所有读取必须通过 framer（内部 bufio.Reader），
	// 不能再对 c 直接 io.ReadFull，否则 bufio.Reader 缓冲与直接读竞争导致丢字节。
	framer := transport.NewStreamFramer(c)
	// DNS 帧回调：收到 DNS 帧时转交给 onDNS 处理
	if l.onDNS != nil {
		replyFramer := framer
		framer.OnDNS = func(srcVIP netip.Addr, dnsPayload []byte) {
			l.onDNS(srcVIP, dnsPayload, replyFramer)
		}
	}
	// Probe 帧回调：收到路径探测帧时转交给 onProbe
	if l.onProbe != nil {
		framer.OnProbe = func(sourceVIP netip.Addr, payload []byte) {
			l.onProbe(nodeID, sourceVIP, payload)
		}
	}
	if l.onPeerConnect != nil {
		l.onPeerConnect(nodeID, framer)
		defer func() {
			if l.onPeerDisconnect != nil {
				l.onPeerDisconnect(nodeID, framer)
			}
		}()
	}

	// 读帧循环：统一通过 framer.ReadPacket() 读取，
	// keepalive 和 DNS 帧由 framer 内部自动处理（回调 OnDNS / 自动回 echo）。
	for {
		dstVIP, dstRelay, ttl, payload, err := framer.ReadPacket()
		if err != nil {
			select {
			case <-l.closeCh:
			default:
				slog.Debug("dataplane: listener 读帧失败", "nodeID", nodeID[:min(8, len(nodeID))], "err", err)
			}
			return
		}

		slog.Debug("dataplane: listener 收到帧", "from", nodeID[:min(8, len(nodeID))], "dstVIP", dstVIP, "payloadLen", len(payload))
		if l.onFrame != nil {
			l.onFrame(nodeID, dstVIP, dstRelay, ttl, payload)
		}
	}
}
