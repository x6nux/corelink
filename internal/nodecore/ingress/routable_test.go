package ingress

import (
	"net/netip"
	"testing"
)

func TestIsPubliclyRoutable(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
		desc string
	}{
		// 真正的公网地址 → 可路由。
		{"8.8.8.8", true, "公共 DNS"},
		{"203.0.113.5", true, "TEST-NET-3（文档用，但 GlobalUnicast，视为可路由）"},
		{"2606:4700:4700::1111", true, "公网 IPv6"},

		// 标准库 IsPrivate 已覆盖。
		{"10.1.2.3", false, "RFC1918 10/8"},
		{"172.16.0.1", false, "RFC1918 172.16/12"},
		{"192.168.1.1", false, "RFC1918 192.168/16"},
		{"fd00::1", false, "IPv6 ULA"},

		// 标准库 IsLoopback / LinkLocal / Multicast / Unspecified 已覆盖。
		{"127.0.0.1", false, "回环"},
		{"169.254.1.1", false, "IPv4 链路本地"},
		{"fe80::1", false, "IPv6 链路本地"},
		{"224.0.0.1", false, "multicast"},
		{"0.0.0.0", false, "未指定"},

		// 标准库 *不* 覆盖、本函数必须额外排除的保留段（bug #8 核心）。
		{"100.64.1.1", false, "CGNAT 100.64/10（RFC6598）"},
		{"100.127.255.255", false, "CGNAT 100.64/10 边界"},
		{"198.18.0.1", false, "Benchmark 198.18/15（RFC2544）"},
		{"198.19.255.255", false, "Benchmark 198.18/15 边界"},
		{"240.0.0.1", false, "保留 240/4（RFC1112）"},
		{"255.255.255.255", false, "受限广播"},
	}

	for _, c := range cases {
		addr := netip.MustParseAddr(c.ip)
		if got := isPubliclyRoutable(addr); got != c.want {
			t.Errorf("isPubliclyRoutable(%s) = %v, 期望 %v（%s）", c.ip, got, c.want, c.desc)
		}
	}

	// 零值地址不可路由。
	if isPubliclyRoutable(netip.Addr{}) {
		t.Errorf("零值 Addr 应不可路由")
	}
}
