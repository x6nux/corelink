// Package portmap 实现端口映射相关的平台无关辅助逻辑（NAT-PMP/PCP 等）。
//
// 本文件提供默认网关推断：Go 标准库无跨平台默认网关 API，故采用平台无关启发式——
// 从本机私网网卡 IP 推断同段常见网关地址候选，供 NAT-PMP/PCP 请求使用。
package portmap

import (
	"net"
	"net/netip"
	"sort"
)

// InterfaceLister 返回本机所有接口的地址列表。
// 默认实现使用 net.InterfaceAddrs()；测试可注入 fake。
// 与 ingress/netif.go 的 InterfaceLister 同形态，本包独立定义、不跨包依赖。
type InterfaceLister func() ([]net.Addr, error)

// DefaultGateways 从本机私网网卡 IP 推断同段常见默认网关候选地址。
//
// 启发式（纯标准库，不读路由表、不用平台 syscall）：
//   - 只处理私网 IPv4（IsPrivate && Is4），跳过回环/链路本地/IPv6/公网。
//   - 把主机段末段换成 .1 作为同 /24 网关候选：
//     192.168.x.y → 192.168.x.1；172.16-31.x.y → 172.a.b.1；10.x.y.z → 10.x.y.1。
//   - 对 10/8 额外补充 10.0.0.1（许多 10.x 网络网关位于此）。
//
// ifaceFn 为 nil 时使用 net.InterfaceAddrs。ifaceFn 返回 error 时返回空列表（不 panic）。
// 结果去重并按字典序排序，便于确定性测试。
func DefaultGateways(ifaceFn InterfaceLister) []string {
	if ifaceFn == nil {
		ifaceFn = net.InterfaceAddrs
	}
	addrs, err := ifaceFn()
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		if _, dup := seen[s]; dup {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	for _, a := range addrs {
		ip := addrToIP(a)
		if !ip.IsValid() {
			continue
		}
		ip = ip.Unmap() // IPv4-mapped IPv6 归一化为 IPv4。

		// 只处理私网 IPv4。
		if !ip.Is4() || !ip.IsPrivate() {
			continue
		}

		b := ip.As4()
		// 同 /24 段网关候选：末段换成 .1。
		add(netip.AddrFrom4([4]byte{b[0], b[1], b[2], 1}).String())

		// 10/8 额外补充 10.0.0.1。
		if b[0] == 10 {
			add("10.0.0.1")
		}
	}

	sort.Strings(out)
	return out
}

// addrToIP 从 net.Addr 提取 netip.Addr，兼容 *net.IPNet / *net.IPAddr 以及
// 仅实现 String() 的 fake（返回 CIDR 或裸 IP）。无法解析时返回零值（IsValid()==false）。
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
