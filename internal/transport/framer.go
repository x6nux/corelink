package transport

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// framerMode 标识 Framer 的工作模式。
type framerMode int

const (
	modeStream   framerMode = iota // TCP/TLS/WS 等面向连接的 stream
	modeDatagram                   // UDP 等面向数据报的连接
)

// Framer 封装一条连接的帧读写，提供线程安全的写入串行化。
//
// Stream 模式委托给 WriteStreamFrame/ReadStreamFrame；
// Datagram 模式委托给 EncodeDatagram/DecodeDatagram + PacketConn。
//
// Keepalive 处理：ReadPacket 内部自动过滤 keepalive 帧——
// 收到 keepalive 请求时自动回 echo，收到 echo 时记录 RTT，均不返回给上层。
type Framer struct {
	mode framerMode

	// stream 模式字段
	conn net.Conn
	br   *bufio.Reader

	// datagram 模式字段
	pc   net.PacketConn
	addr net.Addr

	// writeMu 串行化写入，防止并发 Write 交错破坏帧边界。
	writeMu sync.Mutex

	// keepalive 状态
	kaMu         sync.Mutex
	kaPending    map[uint64]time.Time // seq → 发送时间
	OnRTT        func(time.Duration)  // 收到 echo 时回调 RTT（可选）
	kaSeqCounter uint64

	// DNS 帧回调：收到 DNS 帧时调用（可选）。参数: srcVIP, dnsPayload。
	OnDNS func(srcVIP netip.Addr, dnsPayload []byte)
	// Probe 帧回调：收到路径探测帧时调用（可选）。参数: sourceVIP, payload。
	// payload 为 ProbeFrame 编码数据，由上层 DecodeProbePayload 解码后决定转发或回复。
	OnProbe func(sourceVIP netip.Addr, payload []byte)
	// Bandwidth 帧回调：收到带宽测速帧时调用（可选）。参数: dstVIP, payload。
	OnBandwidth func(dstVIP netip.Addr, payload []byte)
}

// NewStreamFramer 为面向连接的 stream（TCP/TLS/WS/gRPC）创建 Framer。
func NewStreamFramer(conn net.Conn) *Framer {
	return &Framer{
		mode:      modeStream,
		conn:      conn,
		br:        bufio.NewReader(conn),
		kaPending: make(map[uint64]time.Time),
	}
}

// NewDatagramFramer 为面向数据报的 PacketConn（UDP）创建 Framer。
// addr 是默认发送目标地址。
func NewDatagramFramer(pc net.PacketConn, addr net.Addr) *Framer {
	return &Framer{
		mode: modeDatagram,
		pc:   pc,
		addr: addr,
	}
}

// WritePacket 将一个数据包帧化后写入底层连接。
// 并发安全（内部 writeMu 串行化）。
func (f *Framer) WritePacket(dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()

	switch f.mode {
	case modeStream:
		if f.conn == nil {
			return fmt.Errorf("transport: stream 连接已关闭")
		}
		return WriteStreamFrame(f.conn, dstVIP, dstRelay, ttl, payload)
	case modeDatagram:
		if f.pc == nil {
			return fmt.Errorf("transport: datagram 连接已关闭")
		}
		data := EncodeDatagram(dstVIP, dstRelay, ttl, payload)
		_, err := f.pc.WriteTo(data, f.addr)
		return err
	default:
		return fmt.Errorf("transport: 未知 framer 模式 %d", f.mode)
	}
}

// ReadPacket 从底层连接读取并解帧一个数据包。
// Keepalive 帧在内部自动处理（请求→回 echo，echo→记录 RTT），不返回给上层。
func (f *Framer) ReadPacket() (dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte, err error) {
	for {
		var frameFlags byte
		switch f.mode {
		case modeStream:
			if f.conn == nil {
				return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: stream 连接已关闭")
			}
			dstVIP, dstRelay, ttl, payload, frameFlags, err = readStreamFrameRaw(f.br)
		case modeDatagram:
			if f.pc == nil {
				return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: datagram 连接已关闭")
			}
			buf := make([]byte, MaxFrameLen)
			n, _, readErr := f.pc.ReadFrom(buf)
			if readErr != nil {
				return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 读 datagram: %w", readErr)
			}
			dstVIP, dstRelay, ttl, payload, err = DecodeDatagram(buf[:n])
		default:
			return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 未知 framer 模式 %d", f.mode)
		}
		if err != nil {
			return
		}

		// 检查是否为 keepalive 帧（TTL=0 + VIP=0.0.0.0 + payload=8B）
		// 注意：netip.AddrFrom4({0,0,0,0}) 是 valid 的，需用 IsUnspecified() 判断
		if ttl == 0 && dstVIP.IsUnspecified() && len(payload) == 8 {
			seq := binary.BigEndian.Uint64(payload)
			// 判断是 echo 还是请求：echo 帧的 dstRelay 高位标记（用 dstRelay != 0 区分）
			// 实际用 flags 区分——但 ReadStreamFrame 不返回 flags。
			// 简化：用 seq 是否在 pending 表中区分。
			f.kaMu.Lock()
			if sent, ok := f.kaPending[seq]; ok {
				// 是 echo 回复——测量 RTT
				rtt := time.Since(sent)
				delete(f.kaPending, seq)
				cb := f.OnRTT
				f.kaMu.Unlock()
				if cb != nil {
					cb(rtt)
				}
			} else {
				f.kaMu.Unlock()
				// 是对端发来的 keepalive 请求——回 echo
				_ = f.WriteKeepaliveEcho(seq)
			}
			continue // 不返回给上层，继续读下一帧
		}

		// DNS 帧检测：FlagDNS 位置位时调用 OnDNS 回调
		if IsDNS(frameFlags) && f.OnDNS != nil {
			f.OnDNS(dstVIP, payload)
			continue // 不返回给上层
		}

		// Probe 帧检测：FlagProbe 位置位时调用 OnProbe 回调
		if IsProbe(frameFlags) && f.OnProbe != nil {
			f.OnProbe(dstVIP, payload)
			continue
		}

		// Bandwidth 帧检测：FlagBandwidth 位置位时调用 OnBandwidth 回调
		if IsBandwidth(frameFlags) && f.OnBandwidth != nil {
			f.OnBandwidth(dstVIP, payload)
			continue
		}

		return
	}
}

// WriteKeepalive 发送 keepalive 探测帧并记录发送时间（用于 RTT 测量）。
func (f *Framer) WriteKeepalive(seq uint64) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()

	if f.mode != modeStream {
		return fmt.Errorf("transport: datagram 模式不支持 keepalive")
	}
	if f.conn == nil {
		return fmt.Errorf("transport: stream 连接已关闭")
	}
	// 自动递增 seq
	if seq == 0 {
		f.kaMu.Lock()
		f.kaSeqCounter++
		seq = f.kaSeqCounter
		f.kaMu.Unlock()
	}
	// 记录发送时间
	f.kaMu.Lock()
	if f.kaPending == nil {
		f.kaPending = make(map[uint64]time.Time)
	}
	f.kaPending[seq] = time.Now()
	f.kaMu.Unlock()

	return WriteStreamKeepalive(f.conn, seq)
}

// WriteKeepaliveEcho 回复 keepalive echo（内部自动调用，对端收到后测 RTT）。
func (f *Framer) WriteKeepaliveEcho(seq uint64) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()

	if f.conn == nil {
		return fmt.Errorf("transport: stream 连接已关闭")
	}
	return WriteStreamKeepaliveEcho(f.conn, seq)
}

// WriteDNS 发送 DNS 帧（原始 DNS 报文封装为 FlagDNS 帧）。
func (f *Framer) WriteDNS(dstVIP netip.Addr, dnsPayload []byte) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if f.mode != modeStream || f.conn == nil {
		return fmt.Errorf("transport: DNS 帧仅支持 stream 模式")
	}
	return WriteStreamDNS(f.conn, dstVIP, dnsPayload)
}

// WriteProbe 发送 Probe 帧（路径探测，带 FlagProbe 标记和预存路由）。
// dstVIP 为帧头路由目标（当前跳的 VIP），用于 relay 场景正确转发。
func (f *Framer) WriteProbe(p *ProbeFrame, dstVIP netip.Addr) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if f.mode != modeStream || f.conn == nil {
		return fmt.Errorf("transport: Probe 帧仅支持 stream 模式")
	}
	return WriteProbeFrame(f.conn, p, dstVIP)
}

// Close 关闭底层连接。
func (f *Framer) Close() error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()

	switch f.mode {
	case modeStream:
		if f.conn == nil {
			return nil
		}
		err := f.conn.Close()
		f.conn = nil
		return err
	case modeDatagram:
		if f.pc == nil {
			return nil
		}
		err := f.pc.Close()
		f.pc = nil
		return err
	default:
		return nil
	}
}
