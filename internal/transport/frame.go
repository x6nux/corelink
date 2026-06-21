// Package transport 实现节点和 relay 共用的帧编解码。
//
// 支持两种帧格式：
//   - Stream 帧（TCP/TLS/WS/WSS/gRPC）：4B 大端长度前缀 + 头部 + payload
//   - Datagram 帧（UDP）：无长度前缀，头部 + payload
//
// 帧头部包含 flags、TTL、DstVIP（4B IPv4 或 16B IPv6）和 DstRelay（2B relay 索引）。
package transport

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"sync"
)

// MaxFrameLen 是单帧允许的最大长度（长度字段之后的总字节数）。
// 与 bind/channel.go 中 maxFrameLen 保持一致，防御恶意/损坏长度前缀。
const MaxFrameLen = 0xFFFF

// 帧头部固定大小（不含 VIP 可变部分）。
const (
	// streamLenSize 是 stream 帧长度前缀字节数。
	streamLenSize = 4
	// flagsSize + ttlSize + relaySize = 1 + 1 + 2 = 4 字节固定开销。
	flagsSize = 1
	ttlSize   = 1
	relaySize = 2
	// fixedHdrSize = flags + TTL + relay（不含 VIP）。
	fixedHdrSize = flagsSize + ttlSize + relaySize
	// IPv4 VIP 字节数。
	vipV4Size = 4
	// IPv6 VIP 字节数。
	vipV6Size = 16
	// streamHdrV4 是 stream 帧 IPv4 头部总大小（不含长度前缀、不含 payload）。
	streamHdrV4 = fixedHdrSize + vipV4Size // 8
	// streamHdrV6 是 stream 帧 IPv6 头部总大小。
	streamHdrV6 = fixedHdrSize + vipV6Size // 20
	// datagramHdrV4 = streamHdrV4（datagram 无长度前缀，头部相同）。
	datagramHdrV4 = streamHdrV4 // 8
	// datagramHdrV6 同理。
	datagramHdrV6 = streamHdrV6 // 20
)

// Flags 位定义。
const (
	FlagIPv6      byte = 1 << 0 // bit 0: DstVIP 为 16 字节 IPv6
	FlagKeepalive byte = 1 << 1 // bit 1: Keepalive 帧
	FlagControl   byte = 1 << 2 // bit 2: Control 帧
	FlagDNS       byte = 1 << 3 // bit 3: DNS 查询/响应帧
	FlagProbe     byte = 1 << 5 // bit 5: 路径探测帧（预存路由，逐跳转发）
	FlagBandwidth byte = 1 << 6 // bit 6: 带宽测速帧（Probe 填充测速）
)

// IsKeepalive 检查 flags 是否设置了 keepalive 位。
func IsKeepalive(flags byte) bool { return flags&FlagKeepalive != 0 }

// IsControl 检查 flags 是否设置了 control 位。
func IsControl(flags byte) bool { return flags&FlagControl != 0 }

// IsDNS 检查 flags 是否设置了 DNS 位。
func IsDNS(flags byte) bool { return flags&FlagDNS != 0 }

// framePool 复用编码缓冲区，减少数据面热路径 per-packet 分配。
// 初始容量 64 KiB，覆盖绝大多数 WG 数据报；超大帧走 fresh 分配不入池。
var framePool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 65536)
		return &buf
	},
}


// vipBytes 返回 VIP 的原始字节（4B 或 16B）和对应的 flags 位。
func vipBytes(addr netip.Addr) ([]byte, byte) {
	if !addr.IsValid() {
		// 零值地址（keepalive 等场景）：返回 4 字节全零。
		return make([]byte, 4), 0
	}
	if addr.Is6() && !addr.Is4In6() {
		b := addr.As16()
		return b[:], FlagIPv6
	}
	// IPv4 用 4 字节。
	b := addr.As4()
	return b[:], 0
}

// vipSize 根据 flags 返回 VIP 字段字节数。
func vipSize(flags byte) int {
	if flags&FlagIPv6 != 0 {
		return vipV6Size
	}
	return vipV4Size
}

// ─────────────────── Stream 帧 ───────────────────

// WriteStreamFrame 将一帧写入 stream：4B 大端长度 + flags + TTL + VIP + relay + payload。
func WriteStreamFrame(w io.Writer, dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte) error {
	vip, f := vipBytes(dstVIP)
	hdrLen := fixedHdrSize + len(vip) // flags+TTL+VIP+relay
	totalAfterLen := hdrLen + len(payload)

	if totalAfterLen > MaxFrameLen {
		return fmt.Errorf("transport: 帧过长 %d > %d", totalAfterLen, MaxFrameLen)
	}

	need := streamLenSize + totalAfterLen

	// 超大帧走 fresh 分配，不污染池。
	if need > 65536 {
		return writeStreamFrameFresh(w, f, ttl, vip, dstRelay, payload, totalAfterLen, need)
	}

	bp := framePool.Get().(*[]byte)
	buf := (*bp)[:need]

	binary.BigEndian.PutUint32(buf[0:4], uint32(totalAfterLen))
	off := streamLenSize
	buf[off] = f
	off++
	buf[off] = ttl
	off++
	copy(buf[off:], vip)
	off += len(vip)
	binary.BigEndian.PutUint16(buf[off:off+2], dstRelay)
	off += 2
	copy(buf[off:], payload)

	_, err := w.Write(buf)
	*bp = (*bp)[:0]
	framePool.Put(bp)
	return err
}

// writeStreamFrameFresh 处理超大帧（>64KiB），不使用 pool。
func writeStreamFrameFresh(w io.Writer, flags byte, ttl uint8, vip []byte, dstRelay uint16, payload []byte, totalAfterLen, need int) error {
	buf := make([]byte, need)
	binary.BigEndian.PutUint32(buf[0:4], uint32(totalAfterLen))
	off := streamLenSize
	buf[off] = flags
	off++
	buf[off] = ttl
	off++
	copy(buf[off:], vip)
	off += len(vip)
	binary.BigEndian.PutUint16(buf[off:off+2], dstRelay)
	off += 2
	copy(buf[off:], payload)
	_, err := w.Write(buf)
	return err
}

// ReadStreamFrame 从 stream 中读出一帧。
// 先读 4B 长度，再读该长度的数据，解析出 dstVIP、dstRelay、ttl、payload。
//
// 若 r 已是 *bufio.Reader 则直接复用（避免多帧连续读取时 buffered 数据丢失）；
// 否则内部包装一个。调用方若需从同一连接连续读多帧，应传入同一个 *bufio.Reader
// 或使用 Framer。
func ReadStreamFrame(r io.Reader) (dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte, err error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 4096)
	}

	// 读 4B 长度前缀。
	var lenBuf [streamLenSize]byte
	if _, err = io.ReadFull(br, lenBuf[:]); err != nil {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 读长度前缀: %w", err)
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n > MaxFrameLen {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 收到超长帧 %d", n)
	}
	if n < fixedHdrSize+vipV4Size {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 帧过短 %d", n)
	}

	// 读完整帧体。
	frame := make([]byte, n)
	if _, err = io.ReadFull(br, frame); err != nil {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 读帧体: %w", err)
	}

	return parseFrame(frame)
}

// readStreamFrameRaw 读取 stream 帧并额外返回 flags 字节（内部使用）。
// reuseBuf 非空时复用该缓冲区（避免每帧 make），返回的 payload 是 reuseBuf 的切片。
func readStreamFrameRaw(br *bufio.Reader, reuseBuf *[]byte) (dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte, flags byte, err error) {
	var lenBuf [streamLenSize]byte
	if _, err = io.ReadFull(br, lenBuf[:]); err != nil {
		return netip.Addr{}, 0, 0, nil, 0, fmt.Errorf("transport: 读长度前缀: %w", err)
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n > MaxFrameLen {
		return netip.Addr{}, 0, 0, nil, 0, fmt.Errorf("transport: 收到超长帧 %d", n)
	}
	if n < fixedHdrSize+vipV4Size {
		return netip.Addr{}, 0, 0, nil, 0, fmt.Errorf("transport: 帧过短 %d", n)
	}
	var frame []byte
	if reuseBuf != nil && cap(*reuseBuf) >= n {
		frame = (*reuseBuf)[:n]
	} else {
		frame = make([]byte, n)
		if reuseBuf != nil {
			*reuseBuf = frame
		}
	}
	if _, err = io.ReadFull(br, frame); err != nil {
		return netip.Addr{}, 0, 0, nil, 0, fmt.Errorf("transport: 读帧体: %w", err)
	}
	flags = frame[0]
	dstVIP, dstRelay, ttl, payload, err = parseFrame(frame)
	return
}

// ─────────────────── Datagram 帧 ───────────────────

// EncodeDatagram 将 datagram 帧编码为字节切片（无长度前缀）。
func EncodeDatagram(dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte) []byte {
	vip, f := vipBytes(dstVIP)
	hdrLen := fixedHdrSize + len(vip)
	total := hdrLen + len(payload)

	buf := make([]byte, total)
	buf[0] = f
	buf[1] = ttl
	copy(buf[2:], vip)
	off := 2 + len(vip)
	binary.BigEndian.PutUint16(buf[off:off+2], dstRelay)
	off += 2
	copy(buf[off:], payload)
	return buf
}

// DecodeDatagram 解析 datagram 帧。
func DecodeDatagram(data []byte) (dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte, err error) {
	if len(data) < fixedHdrSize+vipV4Size {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: datagram 过短 %d", len(data))
	}
	return parseFrame(data)
}

// parseFrame 解析帧体（不含长度前缀）：flags + TTL + VIP + relay + payload。
func parseFrame(frame []byte) (dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte, err error) {
	flags := frame[0]
	ttl = frame[1]

	vs := vipSize(flags)
	minLen := fixedHdrSize + vs
	if len(frame) < minLen {
		return netip.Addr{}, 0, 0, nil, fmt.Errorf("transport: 帧头部不足 %d < %d", len(frame), minLen)
	}

	off := 2 // 跳过 flags + TTL
	if flags&FlagIPv6 != 0 {
		var b [16]byte
		copy(b[:], frame[off:off+16])
		dstVIP = netip.AddrFrom16(b)
	} else {
		var b [4]byte
		copy(b[:], frame[off:off+4])
		dstVIP = netip.AddrFrom4(b)
	}
	off += vs

	dstRelay = binary.BigEndian.Uint16(frame[off : off+2])
	off += 2

	payload = frame[off:]
	return dstVIP, dstRelay, ttl, payload, nil
}

// ─────────────────── Keepalive 帧 ───────────────────

// WriteStreamKeepalive 写入 stream keepalive 帧。
// flags=FlagKeepalive, TTL=0, DstVIP=0.0.0.0, DstRelay=0, payload=8B 大端 seq。
func WriteStreamKeepalive(w io.Writer, seq uint64) error {
	var seqBuf [8]byte
	binary.BigEndian.PutUint64(seqBuf[:], seq)
	return writeStreamKeepaliveRaw(w, FlagKeepalive, seqBuf[:])
}

// WriteStreamKeepaliveEcho 写入 stream keepalive echo 帧。
// flags=FlagKeepalive|FlagControl。
func WriteStreamKeepaliveEcho(w io.Writer, seq uint64) error {
	var seqBuf [8]byte
	binary.BigEndian.PutUint64(seqBuf[:], seq)
	return writeStreamKeepaliveRaw(w, FlagKeepalive|FlagControl, seqBuf[:])
}

// writeStreamKeepaliveRaw 写入带指定 flags 的 keepalive/echo 帧。
func writeStreamKeepaliveRaw(w io.Writer, flags byte, seqPayload []byte) error {
	// keepalive 总是用 IPv4 零地址：flags(1) + TTL(1) + VIP(4) + relay(2) + seq(8) = 16
	const totalAfterLen = fixedHdrSize + vipV4Size + 8 // 16
	const need = streamLenSize + totalAfterLen         // 20

	var buf [need]byte
	binary.BigEndian.PutUint32(buf[0:4], totalAfterLen)
	buf[4] = flags
	buf[5] = 0 // TTL = 0
	// buf[6:10] = 0（VIP = 0.0.0.0，零值已满足）
	// buf[10:12] = 0（DstRelay = 0，零值已满足）
	copy(buf[12:20], seqPayload)

	_, err := w.Write(buf[:])
	return err
}

// ─────────────────── DNS 帧 ───────────────────

// WriteStreamDNS 写入 stream DNS 帧。
// flags=FlagDNS, TTL=64, DstVIP=目标 VIP, payload=原始 DNS 报文。
func WriteStreamDNS(w io.Writer, dstVIP netip.Addr, dnsPayload []byte) error {
	vip, f := vipBytes(dstVIP)
	f |= FlagDNS
	hdrLen := fixedHdrSize + len(vip)
	totalAfterLen := hdrLen + len(dnsPayload)

	var lenbuf [streamLenSize]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(totalAfterLen))
	if _, err := w.Write(lenbuf[:]); err != nil {
		return err
	}

	hdr := make([]byte, hdrLen)
	hdr[0] = f
	hdr[1] = 64 // TTL
	copy(hdr[2:], vip)
	off := 2 + len(vip)
	binary.BigEndian.PutUint16(hdr[off:off+2], 0) // DstRelay = 0

	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(dnsPayload)
	return err
}

// ─────────────────── Probe 路径探测帧 ───────────────────
//
// Probe 帧（FlagProbe）自带预存路由列表，逐跳转发测量指定路径累计延迟。
// 每个节点收到后递增 hopIndex 并转发到 Route[hopIndex]。
// 到达终点后标记 isReply=1，通过 ConnPool 发回源端。
//
// 使用标准帧头（flags=FlagProbe, TTL=剩余跳数, VIP=源端VIP, relay=0），
// payload 部分为 Probe 载荷。

// MaxProbeHops 路径探测最大跳数。
// 64 跳 × 4B(IPv4) = 256B 载荷 + 19B 固定头 = 275B，远小于 MTU 1400。
const MaxProbeHops = 64

// ProbeFrame 路径探测帧（紧凑格式，64 跳仅 287B 含帧头）。
// Route 存储每跳的 VIP（IPv4 4 字节），不依赖 nodeID 字符串格式。
//
// 回包模式（AutoReply）：
//   - false（默认）：reply 沿正向路径反转逐跳返回（原路回包，用于 --via 指定路径测量）
//   - true：reply 走 FIB 自然路由回源端（auto 回包，用于自然路由测量）
type ProbeFrame struct {
	IsReply     bool           // false=请求, true=回复
	AutoReply   bool           // true=回包走自然路由, false=回包原路返回
	IsRouteSync bool           // true=路由同步帧（广播路由表，源路由投递）
	Nonce       uint64         // 探测 ID / RouteSync 版本号
	TimestampNs int64          // 发送时间戳（纳秒）
	SourceVIP   netip.Addr     // 源端 VIP（reply 目标 / RouteSync 发送者）
	HopIndex    uint8          // 路由索引（request: 递增标记到达跳; reply: 从 0 递增用于 replyRoute 路由）
	OrigHop     uint8          // 原始请求跳索引（reply 携带，源端据此识别"这是第几跳回复的"）
	Route       []netip.Addr   // 预存路由（VIP 列表，max 64 跳）
	SyncEntry   RouteSyncEntry // RouteSync 单条路由（仅 IsRouteSync=true 时有值）
}

// RouteSyncEntry 路由同步条目（10 字节紧凑格式）。
type RouteSyncEntry struct {
	DstVIP     netip.Addr // 目标 VIP（4B uint32）
	NextHopVIP netip.Addr // 第一跳 VIP（4B uint32，直连时 = DstVIP）
	RTTMs      float64    // 延迟 ms（编码为 uint16，1ms 精度，范围 0-65535ms）
}

// routeSyncPayloadSize 单条 RouteSync 载荷大小（直接追加在 Probe 末尾，无 count 字节）。
const routeSyncPayloadSize = routeSyncEntrySize // 10B: dstVIP(4) + nextHopVIP(4) + rttMs(2)

// NextHop 返回下一跳 VIP，已到终点返回零值。
func (p *ProbeFrame) NextHop() netip.Addr {
	if int(p.HopIndex) < len(p.Route) {
		return p.Route[p.HopIndex]
	}
	return netip.Addr{}
}

// Advance 递增跳索引。
func (p *ProbeFrame) Advance() { p.HopIndex++ }

// probeFixedSize = 1(flags) + 8(nonce) + 8(timestamp) + 4(sourceVIP) + 1(hopIndex) + 1(origHop) + 1(hopCount) = 24B
const probeFixedSize = 24

// routeSyncEntrySize = 4(dstVIP) + 4(nextHopVIP) + 2(rttHundredths) = 10B
const routeSyncEntrySize = 10

// EncodeProbePayload 编码 Probe 载荷。
// 格式: [1B flags][8B nonce][8B timestamp][4B sourceVIP][1B hopIndex][1B origHop][1B hopCount][hopCount × 4B IPv4]
// IsRouteSync=true 时追加: [1B entryCount][entryCount × 10B entries]
func EncodeProbePayload(p *ProbeFrame) []byte {
	syncSize := 0
	if p.IsRouteSync {
		syncSize = routeSyncPayloadSize
	}
	buf := make([]byte, probeFixedSize+len(p.Route)*4+syncSize)
	off := 0
	var flags byte
	if p.IsReply {
		flags |= 1
	}
	if p.AutoReply {
		flags |= 2
	}
	if p.IsRouteSync {
		flags |= 4
	}
	buf[off] = flags
	off++
	binary.BigEndian.PutUint64(buf[off:], p.Nonce)
	off += 8
	binary.BigEndian.PutUint64(buf[off:], uint64(p.TimestampNs))
	off += 8
	v4 := p.SourceVIP.As4()
	copy(buf[off:], v4[:])
	off += 4
	buf[off] = p.HopIndex
	off++
	buf[off] = p.OrigHop
	off++
	buf[off] = byte(len(p.Route))
	off++
	for _, vip := range p.Route {
		a := vip.As4()
		copy(buf[off:], a[:])
		off += 4
	}
	// RouteSync 附加单条路由（10B）
	if p.IsRouteSync {
		d := p.SyncEntry.DstVIP.As4()
		copy(buf[off:], d[:])
		off += 4
		n := p.SyncEntry.NextHopVIP.As4()
		copy(buf[off:], n[:])
		off += 4
		binary.BigEndian.PutUint16(buf[off:], uint16(p.SyncEntry.RTTMs))
		off += 2
	}
	return buf
}

// DecodeProbePayload 从 payload 解码 ProbeFrame（SourceVIP 来自 payload，不依赖帧头）。
// IsRouteSync=true 时额外解码附加的 RouteSyncEntry 列表。
func DecodeProbePayload(data []byte) (*ProbeFrame, error) {
	if len(data) < probeFixedSize {
		return nil, fmt.Errorf("transport: probe 帧过短 %d", len(data))
	}
	p := &ProbeFrame{}
	off := 0
	flags := data[off]
	p.IsReply = flags&1 != 0
	p.AutoReply = flags&2 != 0
	p.IsRouteSync = flags&4 != 0
	off++
	p.Nonce = binary.BigEndian.Uint64(data[off:])
	off += 8
	p.TimestampNs = int64(binary.BigEndian.Uint64(data[off:]))
	off += 8
	p.SourceVIP = netip.AddrFrom4([4]byte{data[off], data[off+1], data[off+2], data[off+3]})
	off += 4
	p.HopIndex = data[off]
	off++
	p.OrigHop = data[off]
	off++
	hopCount := int(data[off])
	off++
	if off+hopCount*4 > len(data) {
		return nil, fmt.Errorf("transport: probe 路由越界")
	}
	p.Route = make([]netip.Addr, hopCount)
	for i := range hopCount {
		p.Route[i] = netip.AddrFrom4([4]byte{data[off], data[off+1], data[off+2], data[off+3]})
		off += 4
	}
	// RouteSync 单条路由（10B）
	if p.IsRouteSync && off+routeSyncPayloadSize <= len(data) {
		p.SyncEntry.DstVIP = netip.AddrFrom4([4]byte{data[off], data[off+1], data[off+2], data[off+3]})
		off += 4
		p.SyncEntry.NextHopVIP = netip.AddrFrom4([4]byte{data[off], data[off+1], data[off+2], data[off+3]})
		off += 4
		p.SyncEntry.RTTMs = float64(binary.BigEndian.Uint16(data[off:]))
		off += 2
	}
	return p, nil
}

// WriteProbeFrame 写入 Probe 帧（标准帧头 + FlagProbe 位 + Probe 载荷）。
// dstVIP 为帧头路由目标（当前跳的 VIP），用于 relay 场景正确转发。
// SourceVIP 仅存储在 payload 中。
func WriteProbeFrame(w io.Writer, p *ProbeFrame, dstVIP netip.Addr) error {
	payload := EncodeProbePayload(p)
	vip, f := vipBytes(dstVIP)
	f |= FlagProbe
	hdrLen := fixedHdrSize + len(vip)
	totalAfterLen := hdrLen + len(payload)
	need := streamLenSize + totalAfterLen
	buf := make([]byte, need)
	binary.BigEndian.PutUint32(buf[0:4], uint32(totalAfterLen))
	off := streamLenSize
	buf[off] = f
	off++
	buf[off] = byte(len(p.Route) - int(p.HopIndex)) // TTL = 剩余跳数
	off++
	copy(buf[off:], vip)
	off += len(vip)
	binary.BigEndian.PutUint16(buf[off:off+2], 0)
	off += 2
	copy(buf[off:], payload)
	_, err := w.Write(buf)
	return err
}

// IsProbe 检查是否为 Probe 帧（通过 flags）。
func IsProbe(flags byte) bool { return flags&FlagProbe != 0 }

// IsBandwidth 检查是否为带宽测速帧。
func IsBandwidth(flags byte) bool { return flags&FlagBandwidth != 0 }

// ─────────────────── Bandwidth 带宽测速帧 ───────────────────
//
// 发送端连续发 MTU 大小填充帧，接收端统计吞吐量后发 Reply。
// 终止条件：收到 IsLast=true 或超时（2s 无新包）。

// bandwidthFixedSize = 8(nonce) + 4(seqNo) + 4(totalPackets) + 1(flags) + 4(sourceVIP) = 21B
const bandwidthFixedSize = 21

// BandwidthFrame 带宽测速数据帧。
type BandwidthFrame struct {
	Nonce        uint64
	SeqNo        uint32
	TotalPackets uint32
	IsLast       bool
	SourceVIP    netip.Addr
}

// EncodeBandwidthFrame 编码带宽帧（不含填充 payload，调用方补充到 MTU）。
func EncodeBandwidthFrame(f *BandwidthFrame) []byte {
	buf := make([]byte, bandwidthFixedSize)
	binary.BigEndian.PutUint64(buf[0:], f.Nonce)
	binary.BigEndian.PutUint32(buf[8:], f.SeqNo)
	binary.BigEndian.PutUint32(buf[12:], f.TotalPackets)
	var flags byte
	if f.IsLast {
		flags |= 1
	}
	buf[16] = flags
	v4 := f.SourceVIP.As4()
	copy(buf[17:], v4[:])
	return buf
}

// DecodeBandwidthFrame 从 payload 解码带宽帧头部。
func DecodeBandwidthFrame(data []byte) (*BandwidthFrame, error) {
	if len(data) < bandwidthFixedSize {
		return nil, fmt.Errorf("transport: bandwidth 帧过短 %d", len(data))
	}
	f := &BandwidthFrame{
		Nonce:        binary.BigEndian.Uint64(data[0:]),
		SeqNo:        binary.BigEndian.Uint32(data[8:]),
		TotalPackets: binary.BigEndian.Uint32(data[12:]),
		IsLast:       data[16]&1 != 0,
		SourceVIP:    netip.AddrFrom4([4]byte{data[17], data[18], data[19], data[20]}),
	}
	return f, nil
}

// bandwidthReplySize = 8(nonce) + 8(throughputBps) + 8(durationNs) + 4(receivedCount) = 28B
const bandwidthReplySize = 28

// BandwidthReply 带宽测速回复。
type BandwidthReply struct {
	Nonce         uint64
	ThroughputBps uint64
	DurationNs    int64
	ReceivedCount uint32
}

// EncodeBandwidthReply 编码带宽回复。
func EncodeBandwidthReply(r *BandwidthReply) []byte {
	buf := make([]byte, bandwidthReplySize)
	binary.BigEndian.PutUint64(buf[0:], r.Nonce)
	binary.BigEndian.PutUint64(buf[8:], r.ThroughputBps)
	binary.BigEndian.PutUint64(buf[16:], uint64(r.DurationNs))
	binary.BigEndian.PutUint32(buf[24:], r.ReceivedCount)
	return buf
}

// DecodeBandwidthReply 解码带宽回复。
func DecodeBandwidthReply(data []byte) (*BandwidthReply, error) {
	if len(data) < bandwidthReplySize {
		return nil, fmt.Errorf("transport: bandwidth reply 过短 %d", len(data))
	}
	return &BandwidthReply{
		Nonce:         binary.BigEndian.Uint64(data[0:]),
		ThroughputBps: binary.BigEndian.Uint64(data[8:]),
		DurationNs:    int64(binary.BigEndian.Uint64(data[16:])),
		ReceivedCount: binary.BigEndian.Uint32(data[24:]),
	}, nil
}
