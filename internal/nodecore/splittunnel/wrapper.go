package splittunnel

import (
	"encoding/binary"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/tun"
)

// localSubnets 用于检测本地子网流量是否误入 TUN（RFC 1918 全覆盖）。
// 注：100.64.0.0/10（CGNAT/VIP 段）不在此列表——已被上游 vipPrefix.Contains 过滤。
// 169.254.0.0/16（链路本地）不检测——正常情况不会出现在 TUN 中。
var localSubnets = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

const (
	// proxyEncapMagic 封装头魔数，用于识别分流封装包。
	proxyEncapMagic = 0xCE01
	// proxyEncapHdrLen 封装头长度：magic(2) + flags(1) + orig_proto(1) + exit_vip(4) = 8 字节。
	proxyEncapHdrLen = 8
)

// localSubnetWarnLimit 本地子网误路由日志限流间隔（5秒）
const localSubnetWarnLimit = 5 * time.Second

// lastLocalSubnetWarn 上次日志时间（原子时间戳，用于限流）
var lastLocalSubnetWarn atomic.Int64

// vipConfig 存储 VIP 配置的不可变快照（用于 atomic.Pointer 无锁读取）。
type vipConfig struct {
	localVIP netip.Addr
	exitVIP  netip.Addr
}

// SplitTunWrapper 实现 tun.Device 接口，插入真实 TUN 和 WireGuard 之间。
//
// 分流模式下：
//   - Read 路径：direct 流量送 gVisor，proxy 流量封装为 8B 自定义头 + 原始包
//   - Write 路径：解封装收到的封装包，让内核 ip_forward 转发内层包到互联网
type SplitTunWrapper struct {
	real   tun.Device
	router *Router
	cache  *connCache
	active atomic.Bool

	gstack atomic.Pointer[GVisorStack] // 无锁读取 gstack 引用（Cleanup 原子置 nil）

	vips     atomic.Pointer[vipConfig] // 无锁读取 localVIP/exitVIP
	vipMu    sync.Mutex                // 保护 SetLocalVIP 和 Apply 中的 VIP 配置修改
	dnsRelay atomic.Pointer[DNSRelay]  // TUN 层 DNS 拦截中继（原子读写，避免竞态）
}

// loadVIPs 安全读取当前 VIP 配置快照。
func (w *SplitTunWrapper) loadVIPs() vipConfig {
	if v := w.vips.Load(); v != nil {
		return *v
	}
	return vipConfig{}
}

// storeVIPs 原子更新 VIP 配置。
func (w *SplitTunWrapper) storeVIPs(cfg vipConfig) {
	w.vips.Store(&cfg)
}

// loadGStack 安全读取 gstack 引用（无锁原子操作，适合热路径）。
func (w *SplitTunWrapper) loadGStack() *GVisorStack {
	return w.gstack.Load()
}

func newWrapper(real tun.Device) *SplitTunWrapper {
	return &SplitTunWrapper{real: real}
}

// SetDNSRelay 注入 DNS 中继（TUN 拦截模式，原子写入）。
func (w *SplitTunWrapper) SetDNSRelay(r *DNSRelay) {
	w.dnsRelay.Store(r)
}

func (w *SplitTunWrapper) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := w.real.Read(bufs, sizes, offset)
	if err != nil {
		return n, err
	}

	// 读取 VIP 配置快照（无锁）
	vcfg := w.loadVIPs()

	// 回包封装：所有节点（含出口节点）都需要执行。
	// 出口节点收到转发回复（src=互联网, dst=远端VIP）时，封装为 VIP→VIP，
	// 使 WireGuard AllowedIPs 源过滤通过。
	if vcfg.localVIP.IsValid() {
		for i := 0; i < n; i++ {
			if sizes[i] == 0 {
				continue
			}
			pkt := bufs[i][offset : offset+sizes[i]]
			dstIP := parseDstIPv4(pkt)
			srcIP := parseSrcIPv4(pkt)
			if !dstIP.IsValid() || !srcIP.IsValid() {
				continue
			}
			// 转发回复特征：src 不在 VIP 网段 + dst 在 VIP 网段但不是本机
			if !vipPrefix.Contains(srcIP) && vipPrefix.Contains(dstIP) && dstIP != vcfg.localVIP {
				encapped := encapProxy(vcfg.localVIP, dstIP, pkt)
				if len(encapped) > len(bufs[i])-offset {
					continue
				}
				copy(bufs[i][offset:], encapped)
				sizes[i] = len(encapped)
			}
		}
	}

	// 分流逻辑（仅分流节点活跃时执行）
	// 先取 gstack 快照再检查 active，避免 Cleanup 并发置 nil 导致的 panic。
	gs := w.loadGStack()
	if !w.active.Load() || gs == nil {
		return n, nil
	}
	for i := 0; i < n; i++ {
		if sizes[i] == 0 {
			continue
		}
		pkt := bufs[i][offset : offset+sizes[i]]
		dstIP := parseDstIPv4(pkt)
		if !dstIP.IsValid() {
			continue
		}
		// VIP 网段包始终走数据面，分流引擎不截获。
		if vipPrefix.Contains(dstIP) {
			continue
		}

		// DNS 拦截：dst port 53（UDP/TCP）透明转发到出口节点解析（防 DNS 污染）
		if dr := w.dnsRelay.Load(); dr != nil && isDNSPort(pkt) {
			if dnsPayload := extractDNSPayload(pkt); dnsPayload != nil {
				dr.InterceptFromTUN(pkt, dnsPayload)
				sizes[i] = 0
				continue
			}
		}

		srcIP := parseSrcIPv4(pkt)
		if isLocalSubnet(dstIP) {
			now := time.Now().UnixNano()
			if now-lastLocalSubnetWarn.Load() >= int64(localSubnetWarnLimit) {
				lastLocalSubnetWarn.Store(now)
				slog.Warn("splittunnel: 本地子网包误入 TUN", "src", srcIP, "dst", dstIP, "proto", pkt[9])
			}
		}
		key := extractConnKey(pkt)
		act, ok := w.cache.get(key)
		if !ok {
			act = w.router.Decide(dstIP)
			w.cache.put(key, act)
		}
		switch act {
		case ActionDirect:
			proto := pkt[9]
			if proto == 1 {
				gs.HandleICMP(pkt)
			} else {
				gs.InjectPacket(pkt)
			}
			sizes[i] = 0

		case ActionProxy:
			// localVIP 或 exitVIP 无效时不封装，直接走 WireGuard
			// 注：vipPrefix.Contains(dstIP) 已在上方 L124 统一跳过，此处无需重复检查
			if !vcfg.localVIP.IsValid() || !vcfg.exitVIP.IsValid() {
				continue
			}
			encapped := encapProxy(vcfg.localVIP, vcfg.exitVIP, pkt)
			if len(encapped) > len(bufs[i])-offset {
				continue
			}
			copy(bufs[i][offset:], encapped)
			sizes[i] = len(encapped)
		}
	}
	return n, nil
}

// Write 将 WireGuard 解密后的包写入 TUN。
// 若检测到分流封装头且外层 dst 是本机 VIP，则解封装——让内核 ip_forward 转发。
func (w *SplitTunWrapper) Write(bufs [][]byte, offset int) (int, error) {
	vcfg := w.loadVIPs()
	if !vcfg.localVIP.IsValid() {
		return w.real.Write(bufs, offset)
	}
	for i := range bufs {
		pkt := bufs[i][offset:]
		if !isProxyEncapForMe(pkt, vcfg.localVIP) {
			continue
		}
		// 外层 IP 头 + 8B 封装头 → 内层原始包
		outerIHL := int(pkt[0]&0x0f) * 4
		innerStart := outerIHL + proxyEncapHdrLen
		if len(pkt) < innerStart+20 {
			continue
		}
		inner := pkt[innerStart:]
		copy(bufs[i][offset:], inner)
		bufs[i] = bufs[i][:offset+len(inner)]
	}
	return w.real.Write(bufs, offset)
}

func (w *SplitTunWrapper) Close() error             { return w.real.Close() }
func (w *SplitTunWrapper) File() *os.File           { return w.real.File() }
func (w *SplitTunWrapper) MTU() (int, error)        { return w.real.MTU() }
func (w *SplitTunWrapper) Name() (string, error)    { return w.real.Name() }
func (w *SplitTunWrapper) Events() <-chan tun.Event { return w.real.Events() }
func (w *SplitTunWrapper) BatchSize() int           { return w.real.BatchSize() }

// isLocalSubnet 检查 IP 是否属于 RFC 1918 私网段（10/8、172.16/12、192.168/16）。
func isLocalSubnet(ip netip.Addr) bool {
	for _, prefix := range localSubnets {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// ── 封装/解封装 ───────────────────────────────────────────────────────────────
//
// 封装格式（外层 IP 头 + 8B 自定义头 + 内层原始包）：
//
//	[ 标准 IPv4 头 20B (proto=UDP/253, dst=exitVIP) ]
//	[ magic 2B = 0xCE01 ][ flags 1B ][ orig_proto 1B ][ exit_vip 4B ]
//	[ 内层原始 IP 包 ]
//
// 外层 IP proto 使用 253（实验/测试用途，RFC 3692），避免与内核 IPIP(4) 冲突。
// orig_proto 保存内层原始 IP 协议号（便于日志/调试）。

const proxyOuterProto = 253 // 外层 IP 协议号（实验用途）

// encapProxy 封装 proxy 包：外层 IP + 8B 自定义头 + 内层原始包。
func encapProxy(srcVIP, exitVIP netip.Addr, inner []byte) []byte {
	totalLen := 20 + proxyEncapHdrLen + len(inner)
	out := make([]byte, totalLen)

	// 外层 IP 头
	out[0] = 0x45
	binary.BigEndian.PutUint16(out[2:4], uint16(totalLen))
	out[6] = 0x40 // DF
	out[8] = 64   // TTL
	out[9] = proxyOuterProto
	s4 := srcVIP.As4()
	d4 := exitVIP.As4()
	copy(out[12:16], s4[:])
	copy(out[16:20], d4[:])
	binary.BigEndian.PutUint16(out[10:12], ipChecksum(out[:20]))

	// 8B 封装头
	hdr := out[20:]
	binary.BigEndian.PutUint16(hdr[0:2], proxyEncapMagic)
	hdr[2] = 0 // flags 预留
	if len(inner) >= 10 {
		hdr[3] = inner[9] // 内层 IP 协议号
	}
	copy(hdr[4:8], d4[:]) // exit VIP

	// 内层原始包
	copy(out[20+proxyEncapHdrLen:], inner)
	return out
}

// isProxyEncapForMe 检查是否是发给本机 VIP 的分流封装包。
func isProxyEncapForMe(pkt []byte, localVIP netip.Addr) bool {
	if len(pkt) < 20+proxyEncapHdrLen {
		return false
	}
	if pkt[0]>>4 != 4 || pkt[9] != proxyOuterProto {
		return false
	}
	outerIHL := int(pkt[0]&0x0f) * 4
	if len(pkt) < outerIHL+proxyEncapHdrLen {
		return false
	}
	magic := binary.BigEndian.Uint16(pkt[outerIHL : outerIHL+2])
	if magic != proxyEncapMagic {
		return false
	}
	dst := netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]})
	return dst == localVIP
}

// ipChecksum 计算 IP 头校验和。
func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i < len(hdr)-1; i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	if len(hdr)%2 == 1 {
		sum += uint32(hdr[len(hdr)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

// ── IP 包解析工具 ─────────────────────────────────────────────────────────────

func parseSrcIPv4(pkt []byte) netip.Addr {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte{pkt[12], pkt[13], pkt[14], pkt[15]})
}

func parseDstIPv4(pkt []byte) netip.Addr {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]})
}

func extractConnKey(pkt []byte) connKey {
	if len(pkt) < 20 {
		return connKey{}
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 {
		return connKey{}
	}
	proto := pkt[9]
	key := connKey{
		srcIP: netip.AddrFrom4([4]byte{pkt[12], pkt[13], pkt[14], pkt[15]}),
		dstIP: netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]}),
		proto: proto,
	}
	if (proto == 6 || proto == 17) && len(pkt) >= ihl+4 {
		key.srcPort = uint16(pkt[ihl])<<8 | uint16(pkt[ihl+1])
		key.dstPort = uint16(pkt[ihl+2])<<8 | uint16(pkt[ihl+3])
	}
	return key
}

// ── DNS 拦截工具 ─────────────────────────────────────────────────────────────

// isDNSPort 快速检测 IPv4 包是否为 dst port 53（UDP 或 TCP）。
func isDNSPort(pkt []byte) bool {
	if len(pkt) < 24 { // IP(20) + L4(4)
		return false
	}
	proto := pkt[9]
	if proto != 17 && proto != 6 { // 仅 UDP/TCP
		return false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+4 {
		return false
	}
	dstPort := uint16(pkt[ihl+2])<<8 | uint16(pkt[ihl+3])
	return dstPort == 53
}

// extractDNSPayload 从 dst port 53 的 UDP/TCP 包中提取 DNS 报文。
func extractDNSPayload(pkt []byte) []byte {
	ihl := int(pkt[0]&0x0f) * 4
	proto := pkt[9]
	switch proto {
	case 17: // UDP：跳过 8B UDP 头
		if len(pkt) < ihl+8+12 {
			return nil
		}
		return pkt[ihl+8:]
	case 6: // TCP：跳过 TCP 头 + 2B DNS 长度前缀
		if len(pkt) < ihl+20 {
			return nil
		}
		tcpDataOff := int(pkt[ihl+12]>>4) * 4
		if ihl+tcpDataOff > len(pkt) {
			return nil // TCP 数据偏移越界
		}
		tcpPayload := pkt[ihl+tcpDataOff:]
		if len(tcpPayload) < 14 { // 2B 长度 + 12B DNS 头
			return nil
		}
		dnsLen := int(tcpPayload[0])<<8 | int(tcpPayload[1])
		if dnsLen > 0 && len(tcpPayload) >= 2+dnsLen {
			return tcpPayload[2 : 2+dnsLen]
		}
	}
	return nil
}
