package ingress

import "net/netip"

// 标准库 net/netip 的 IsPrivate/IsLoopback 等不覆盖的保留/特殊段，
// 这些地址虽然 IsGlobalUnicast()==true，但不可作为对外可路由的公网入口。
// 用 netip.Prefix 表驱动统一排除，避免 CGNAT/Benchmark/保留段被误判为高置信入口。
var nonRoutablePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT 共享地址（RFC6598）
	netip.MustParsePrefix("198.18.0.0/15"), // Benchmark 测试网（RFC2544）
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF 协议分配（RFC6890）
	netip.MustParsePrefix("240.0.0.0/4"),   // 保留段（RFC1112），含 255.255.255.255 受限广播
}

// isPubliclyRoutable 判定地址是否可作为对外可路由的公网入口。
//
// 在标准库的私有/回环/链路本地/multicast/未指定判定之上，额外排除
// CGNAT(100.64/10)、Benchmark(198.18/15)、IETF(192.0.0/24)、保留(240/4) 等
// 标准库不覆盖但同样不可对外可达的段。
//
// 供 EnumInterfaces / isUsablePublicIP / StunProbe 等共享，避免各处判定不一致。
func isPubliclyRoutable(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap() // IPv4-mapped IPv6 归一化，避免前缀匹配落空。
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, p := range nonRoutablePrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}
