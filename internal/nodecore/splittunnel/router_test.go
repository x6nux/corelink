package splittunnel

import (
	"net/netip"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/geoip"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestRouter_GatewayForceDirect(t *testing.T) {
	r := &Router{
		forceDirectIPs: map[netip.Addr]bool{netip.MustParseAddr("10.0.0.1"): true},
		defaultAct:     ActionProxy,
	}
	if r.Decide(netip.MustParseAddr("10.0.0.1")) != ActionDirect {
		t.Fatal("网关应强制直连")
	}
	if r.Decide(netip.MustParseAddr("8.8.8.8")) != ActionProxy {
		t.Fatal("非网关应走 default")
	}
}

func TestRouter_GeoIP(t *testing.T) {
	matcher, _ := geoip.LoadBytes(geoip.BuildTestDat(t))
	r := &Router{
		forceDirectIPs: map[netip.Addr]bool{},
		matcher:        matcher,
		rules:          []*genv1.SplitRule{{Match: "geoip:cn", Action: "direct"}},
		defaultAct:     ActionProxy,
	}
	if r.Decide(netip.MustParseAddr("1.2.3.4")) != ActionDirect {
		t.Fatal("CN IP 应直连")
	}
	if r.Decide(netip.MustParseAddr("9.9.9.9")) != ActionProxy {
		t.Fatal("非 CN 应走 proxy")
	}
}

func TestRouter_CIDR(t *testing.T) {
	r := &Router{
		forceDirectIPs: map[netip.Addr]bool{},
		rules:          []*genv1.SplitRule{{Match: "cidr:192.168.0.0/16", Action: "direct"}},
		defaultAct:     ActionProxy,
	}
	if r.Decide(netip.MustParseAddr("192.168.1.1")) != ActionDirect {
		t.Fatal("192.168.x.x 应直连")
	}
	if r.Decide(netip.MustParseAddr("8.8.8.8")) != ActionProxy {
		t.Fatal("8.8.8.8 应走 proxy")
	}
}

func TestRouter_VIPAlwaysProxy(t *testing.T) {
	r := &Router{
		forceDirectIPs: map[netip.Addr]bool{},
		defaultAct:     ActionDirect,
	}
	// VIP 地址应始终走 proxy，即使默认动作为 direct
	if r.Decide(netip.MustParseAddr("100.64.0.1")) != ActionProxy {
		t.Fatal("VIP 100.64.0.1 应始终走 proxy")
	}
	if r.Decide(netip.MustParseAddr("100.127.255.254")) != ActionProxy {
		t.Fatal("VIP 100.127.255.254 应始终走 proxy")
	}
	// 非 VIP 走默认
	if r.Decide(netip.MustParseAddr("8.8.8.8")) != ActionDirect {
		t.Fatal("非 VIP 应走默认动作")
	}
}

func TestRouter_Negation(t *testing.T) {
	matcher, _ := geoip.LoadBytes(geoip.BuildTestDat(t))
	r := &Router{
		forceDirectIPs: map[netip.Addr]bool{},
		matcher:        matcher,
		rules:          []*genv1.SplitRule{{Match: "geoip:!cn", Action: "proxy"}},
		defaultAct:     ActionDirect,
	}
	// 1.2.3.4 属于 CN → !cn 不匹配 → 走默认动作(direct)
	if r.Decide(netip.MustParseAddr("1.2.3.4")) != ActionDirect {
		t.Fatal("CN IP 不应被 !cn 规则匹配")
	}
	// 9.9.9.9 不属于 CN → !cn 匹配 → proxy
	if r.Decide(netip.MustParseAddr("9.9.9.9")) != ActionProxy {
		t.Fatal("非 CN 应被 !cn 规则匹配为 proxy")
	}
}
