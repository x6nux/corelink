package ingress

import (
	"net"
	"net/netip"
	"strings"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// vipPrefix 是 CoreLink VIP 网段（100.64.0.0/10 CGNAT），入口枚举时排除。
var vipPrefix = netip.MustParsePrefix("100.64.0.0/10")

// netif confidence 取值（0-100）。
const (
	// netifPublicConfidence 公网网卡 IP：直连可达性高。
	netifPublicConfidence uint32 = 90
	// netifPrivateConfidence 私有 LAN IP：同内网节点可达，置信度须 ≥60
	// 以通过拓扑资格判定（reachableConfidence=60），供同内网就近直连。
	netifPrivateConfidence uint32 = 60
)

// InterfaceLister 返回本机所有接口的地址列表。
// 默认实现聚合 net.Interfaces() 各接口的 Addrs()；测试可注入 fake。
type InterfaceLister func() ([]net.Addr, error)

// defaultInterfaceLister 遍历本机接口收集所有地址。
// 跳过 "corelink" 前缀的 TUN 接口——VIP 地址不应作为入口上报。
func defaultInterfaceLister() ([]net.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.Addr
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "corelink") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		out = append(out, addrs...)
	}
	return out, nil
}

// EnumInterfaces 枚举本机网卡地址，产出候选 ingress（每个可用 IP 一条）。
//
// 规则：
//   - 公网 IP（非私有/非回环/非链路本地/非 multicast）→ source=NETIF, kind=IP_DIRECT, confidence=90
//   - 私有 LAN IP（10/172.16-31/192.168 等）→ source=NETIF, kind=IP_DIRECT, confidence=40
//   - 回环 / 链路本地 / multicast / 未指定地址 → 跳过
//
// port/protocols 此处留空，由上层补充实际监听端口。ifaceFn 为 nil 时回退默认实现。
func EnumInterfaces(ifaceFn InterfaceLister) []*genv1.Ingress {
	if ifaceFn == nil {
		ifaceFn = defaultInterfaceLister
	}
	addrs, err := ifaceFn()
	if err != nil {
		return nil
	}

	var out []*genv1.Ingress
	seen := map[string]struct{}{}
	for _, a := range addrs {
		ip := addrToIP(a)
		if !ip.IsValid() {
			continue
		}
		ip = ip.Unmap() // IPv4-mapped IPv6 归一化为 IPv4。

		// 跳过不可作为对外入口的地址。
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		// 跳过 VIP 网段（100.64.0.0/10 CGNAT）——VIP 是 TUN 虚拟地址，不可用于建连。
		if vipPrefix.Contains(ip) {
			continue
		}

		host := ip.String()
		if _, dup := seen[host]; dup {
			continue
		}
		seen[host] = struct{}{}

		// 可公网路由 → 高置信；否则（私有 LAN / CGNAT / 保留段等）→ 低置信，
		// 仅供同内网就近，不作为高置信公网入口（bug #8）。
		conf := netifPrivateConfidence
		if isPubliclyRoutable(ip) {
			conf = netifPublicConfidence
		}

		out = append(out, &genv1.Ingress{
			Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
			Host:       host,
			Source:     genv1.IngressSource_INGRESS_SOURCE_NETIF,
			Confidence: conf,
		})
	}
	return out
}

// addrToIP 从 net.Addr 提取 netip.Addr，兼容 *net.IPNet / *net.IPAddr 以及
// fake 实现（仅有 String() 返回 CIDR 或裸 IP）。无法解析时返回零值（IsValid()==false）。
func addrToIP(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.IPNet:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip
		}
	case *net.IPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip
		}
	}
	// 回退：解析字符串形式（可能是 "ip/prefix" 或裸 "ip"）。
	s := a.String()
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return pfx.Addr()
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip
	}
	return netip.Addr{}
}
