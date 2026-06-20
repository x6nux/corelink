package route

import (
	"net/netip"
	"sync"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/dpi"
	"github.com/x6nux/corelink/internal/nodecore/flowtrack"
	"github.com/x6nux/corelink/internal/nodecore/metadata"
)

// mustPrefix 解析 CIDR 前缀，失败时 panic。
func mustPrefix(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic("无效前缀: " + s + ": " + err.Error())
	}
	return p
}

// mustAddr 解析 IP 地址，失败时 panic。
func mustAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		panic("无效地址: " + s + ": " + err.Error())
	}
	return a
}

func TestRouteL3FIB(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-a", Via: HopDirect},
		},
	})

	key := flowtrack.FlowKey{DstIP: mustAddr("10.1.2.3"), Proto: 6, DstPort: 80}
	d := e.Route(key, dpi.Result{})

	if d.NextHop != "node-a" {
		t.Fatalf("期望 NextHop=node-a，实际=%s", d.NextHop)
	}
	if d.Via != HopDirect {
		t.Fatalf("期望 Via=HopDirect，实际=%d", d.Via)
	}
	if d.Priority != 3 {
		t.Fatalf("期望 Priority=3（L3），实际=%d", d.Priority)
	}
}

func TestRouteL3LongestPrefix(t *testing.T) {
	e := NewEngine()
	// 故意乱序添加，验证排序逻辑
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-short", Via: HopDirect},
			{Prefix: mustPrefix("10.1.0.0/16"), NextHop: "node-mid", Via: HopDirect},
			{Prefix: mustPrefix("10.1.2.0/24"), NextHop: "node-long", Via: HopRelay, RelayID: "relay-1"},
		},
	})

	// 10.1.2.3 应匹配 /24（最长前缀）
	key := flowtrack.FlowKey{DstIP: mustAddr("10.1.2.3")}
	d := e.Route(key, dpi.Result{})
	if d.NextHop != "node-long" {
		t.Fatalf("期望最长前缀匹配 node-long，实际=%s", d.NextHop)
	}
	if d.Via != HopRelay || d.RelayID != "relay-1" {
		t.Fatalf("期望 HopRelay/relay-1，实际 Via=%d RelayID=%s", d.Via, d.RelayID)
	}

	// 10.1.3.1 应匹配 /16
	key2 := flowtrack.FlowKey{DstIP: mustAddr("10.1.3.1")}
	d2 := e.Route(key2, dpi.Result{})
	if d2.NextHop != "node-mid" {
		t.Fatalf("期望 /16 匹配 node-mid，实际=%s", d2.NextHop)
	}

	// 10.2.0.1 应匹配 /8
	key3 := flowtrack.FlowKey{DstIP: mustAddr("10.2.0.1")}
	d3 := e.Route(key3, dpi.Result{})
	if d3.NextHop != "node-short" {
		t.Fatalf("期望 /8 匹配 node-short，实际=%s", d3.NextHop)
	}
}

func TestRouteL4OverridesL3(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-default", Via: HopDirect},
		},
		L4Rules: []L4Rule{
			{
				Name:      "https-via-relay",
				DstPrefix: mustPrefix("10.0.0.0/8"),
				DstPort:   443,
				Proto:     6, // TCP
				NextHop:   "node-proxy",
				Via:       HopRelay,
				RelayID:   "relay-https",
			},
		},
	})

	// TCP:443 应命中 L4 规则
	key443 := flowtrack.FlowKey{DstIP: mustAddr("10.1.2.3"), Proto: 6, DstPort: 443}
	d := e.Route(key443, dpi.Result{})
	if d.NextHop != "node-proxy" {
		t.Fatalf("期望 L4 规则命中 node-proxy，实际=%s", d.NextHop)
	}
	if d.Priority != 4 {
		t.Fatalf("期望 Priority=4（L4），实际=%d", d.Priority)
	}

	// TCP:80 应回退到 L3 FIB
	key80 := flowtrack.FlowKey{DstIP: mustAddr("10.1.2.3"), Proto: 6, DstPort: 80}
	d2 := e.Route(key80, dpi.Result{})
	if d2.NextHop != "node-default" {
		t.Fatalf("期望 L3 回退 node-default，实际=%s", d2.NextHop)
	}
	if d2.Priority != 3 {
		t.Fatalf("期望 Priority=3（L3），实际=%d", d2.Priority)
	}
}

func TestRouteL5OverridesAll(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("0.0.0.0/0"), NextHop: "node-default", Via: HopDirect},
		},
		L4Rules: []L4Rule{
			{
				Name:      "all-443",
				DstPrefix: mustPrefix("0.0.0.0/0"),
				DstPort:   443,
				NextHop:   "node-l4",
				Via:       HopDirect,
			},
		},
		L5Rules: []L5Rule{
			{
				Name:       "openai-sni",
				SNIPattern: "*.openai.com",
				NextHop:    "node-ai-proxy",
				Via:        HopRelay,
				RelayID:    "relay-ai",
			},
		},
	})

	// SNI 匹配 L5 → 优先于 L4 和 L3
	key := flowtrack.FlowKey{DstIP: mustAddr("104.18.1.1"), Proto: 6, DstPort: 443}
	dpiRes := dpi.Result{Protocol: metadata.ProtocolTLS, Domain: "api.openai.com"}
	d := e.Route(key, dpiRes)
	if d.NextHop != "node-ai-proxy" {
		t.Fatalf("期望 L5 SNI 命中 node-ai-proxy，实际=%s", d.NextHop)
	}
	if d.Priority != 5 {
		t.Fatalf("期望 Priority=5（L5），实际=%d", d.Priority)
	}
	if d.RelayID != "relay-ai" {
		t.Fatalf("期望 RelayID=relay-ai，实际=%s", d.RelayID)
	}

	// 不匹配的 SNI → 回退到 L4
	dpiRes2 := dpi.Result{Protocol: metadata.ProtocolTLS, Domain: "www.google.com"}
	d2 := e.Route(key, dpiRes2)
	if d2.NextHop != "node-l4" {
		t.Fatalf("期望不匹配 SNI 回退到 L4 node-l4，实际=%s", d2.NextHop)
	}
	if d2.Priority != 4 {
		t.Fatalf("期望 Priority=4（L4），实际=%d", d2.Priority)
	}
}

func TestRouteL5HostPattern(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("0.0.0.0/0"), NextHop: "node-default", Via: HopDirect},
		},
		L5Rules: []L5Rule{
			{
				Name:        "example-host",
				HostPattern: "*.example.com",
				NextHop:     "node-example",
				Via:         HopDirect,
			},
		},
	})

	// HTTP Host 匹配
	key := flowtrack.FlowKey{DstIP: mustAddr("93.184.216.34"), Proto: 6, DstPort: 80}
	dpiRes := dpi.Result{Protocol: metadata.ProtocolHTTP, Domain: "www.example.com"}
	d := e.Route(key, dpiRes)
	if d.NextHop != "node-example" {
		t.Fatalf("期望 Host 模式命中 node-example，实际=%s", d.NextHop)
	}
	if d.Priority != 5 {
		t.Fatalf("期望 Priority=5（L5），实际=%d", d.Priority)
	}

	// 不匹配的 Host → 回退到 L3
	dpiRes2 := dpi.Result{Protocol: metadata.ProtocolHTTP, Domain: "www.other.com"}
	d2 := e.Route(key, dpiRes2)
	if d2.NextHop != "node-default" {
		t.Fatalf("期望不匹配 Host 回退到 L3，实际=%s", d2.NextHop)
	}
}

func TestRouteNoMatch(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-a", Via: HopDirect},
		},
	})

	// 192.168.1.1 不在 10.0.0.0/8 内
	key := flowtrack.FlowKey{DstIP: mustAddr("192.168.1.1")}
	d := e.Route(key, dpi.Result{})
	if d.NextHop != "" {
		t.Fatalf("期望无路由（空 NextHop），实际=%s", d.NextHop)
	}
}

func TestRouteUpdateAtomic(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-v1", Via: HopDirect},
		},
	})

	key := flowtrack.FlowKey{DstIP: mustAddr("10.1.1.1")}
	var wg sync.WaitGroup

	// 并发路由查询
	wg.Go(func() {
		for range 10000 {
			d := e.Route(key, dpi.Result{})
			// 每次查询必须得到完整的快照：要么全是 v1 要么全是 v2
			if d.NextHop != "node-v1" && d.NextHop != "node-v2" {
				t.Errorf("观察到非法的 NextHop=%s", d.NextHop)
				return
			}
		}
	})

	// 并发更新配置
	wg.Go(func() {
		for i := range 10000 {
			if i%2 == 0 {
				e.Update(&RouteConfig{
					FIB: []FIBEntry{
						{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-v2", Via: HopDirect},
					},
				})
			} else {
				e.Update(&RouteConfig{
					FIB: []FIBEntry{
						{Prefix: mustPrefix("10.0.0.0/8"), NextHop: "node-v1", Via: HopDirect},
					},
				})
			}
		}
	})

	wg.Wait()
}

func TestRouteL4AnyPort(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		L4Rules: []L4Rule{
			{
				Name:      "catch-all-tcp",
				DstPrefix: mustPrefix("172.16.0.0/12"),
				DstPort:   0, // 匹配所有端口
				Proto:     6, // TCP
				NextHop:   "node-tcp",
				Via:       HopDirect,
			},
		},
	})

	// 任意 TCP 端口都应命中
	for _, port := range []uint16{22, 80, 443, 8080, 65535} {
		key := flowtrack.FlowKey{DstIP: mustAddr("172.16.1.1"), Proto: 6, DstPort: port}
		d := e.Route(key, dpi.Result{})
		if d.NextHop != "node-tcp" {
			t.Fatalf("端口 %d: 期望 node-tcp，实际=%s", port, d.NextHop)
		}
	}

	// UDP 不应命中（Proto=6 限定 TCP）
	keyUDP := flowtrack.FlowKey{DstIP: mustAddr("172.16.1.1"), Proto: 17, DstPort: 53}
	dUDP := e.Route(keyUDP, dpi.Result{})
	if dUDP.NextHop != "" {
		t.Fatalf("UDP 不应命中 TCP 规则，实际=%s", dUDP.NextHop)
	}
}

func TestRouteL4AnyProto(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		L4Rules: []L4Rule{
			{
				Name:      "all-proto-80",
				DstPrefix: mustPrefix("10.0.0.0/8"),
				DstPort:   80,
				Proto:     0, // 匹配所有协议
				NextHop:   "node-web",
				Via:       HopDirect,
			},
		},
	})

	// TCP:80 和 UDP:80 都应命中
	for _, proto := range []uint8{6, 17} {
		key := flowtrack.FlowKey{DstIP: mustAddr("10.1.1.1"), Proto: proto, DstPort: 80}
		d := e.Route(key, dpi.Result{})
		if d.NextHop != "node-web" {
			t.Fatalf("Proto=%d: 期望 node-web，实际=%s", proto, d.NextHop)
		}
	}
}

func TestRouteL5SNIAndHostBothSet(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		L5Rules: []L5Rule{
			{
				Name:        "dual-pattern",
				SNIPattern:  "*.tls.io",
				HostPattern: "*.http.io",
				NextHop:     "node-dual",
				Via:         HopDirect,
			},
		},
	})

	// 仅 SNI 匹配 → 命中
	d1 := e.Route(flowtrack.FlowKey{DstIP: mustAddr("1.1.1.1")},
		dpi.Result{Domain: "api.tls.io"})
	if d1.NextHop != "node-dual" {
		t.Fatalf("仅 SNI 匹配: 期望 node-dual，实际=%s", d1.NextHop)
	}

	// 仅 Host 匹配 → 命中
	d2 := e.Route(flowtrack.FlowKey{DstIP: mustAddr("1.1.1.1")},
		dpi.Result{Domain: "www.http.io"})
	if d2.NextHop != "node-dual" {
		t.Fatalf("仅 Host 匹配: 期望 node-dual，实际=%s", d2.NextHop)
	}

	// 两者都不匹配 → 无路由
	d3 := e.Route(flowtrack.FlowKey{DstIP: mustAddr("1.1.1.1")},
		dpi.Result{Domain: "other.com"})
	if d3.NextHop != "" {
		t.Fatalf("无匹配: 期望空 NextHop，实际=%s", d3.NextHop)
	}
}

func TestRouteEmptyEngine(t *testing.T) {
	e := NewEngine()
	// 空引擎：没有任何规则
	key := flowtrack.FlowKey{DstIP: mustAddr("10.1.1.1"), Proto: 6, DstPort: 80}
	d := e.Route(key, dpi.Result{})
	if d.NextHop != "" {
		t.Fatalf("空引擎应返回空 Decision，实际 NextHop=%s", d.NextHop)
	}
}

func TestRouteL5OnlyCheckedWithDPIResult(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{
			{Prefix: mustPrefix("0.0.0.0/0"), NextHop: "node-default", Via: HopDirect},
		},
		L5Rules: []L5Rule{
			{
				Name:       "catch-sni",
				SNIPattern: "*",
				NextHop:    "node-l5",
				Via:        HopDirect,
			},
		},
	})

	// 无 DPI 结果时 L5 不应被尝试，直接回退 L3
	key := flowtrack.FlowKey{DstIP: mustAddr("10.1.1.1")}
	d := e.Route(key, dpi.Result{})
	if d.NextHop != "node-default" {
		t.Fatalf("无 DPI 结果时应回退 L3，实际 NextHop=%s Priority=%d", d.NextHop, d.Priority)
	}
	if d.Priority != 3 {
		t.Fatalf("期望 Priority=3（L3），实际=%d", d.Priority)
	}
}

func TestRouteCtx_FIB(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		FIB: []FIBEntry{{
			Prefix:  netip.MustParsePrefix("100.64.0.0/10"),
			NextHop: "node-a", Via: HopDirect,
		}},
	})
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkTCP,
		Source:      netip.MustParseAddrPort("100.64.0.1:1234"),
		Destination: netip.MustParseAddrPort("100.64.0.2:80"),
	}
	e.RouteCtx(ctx)
	if ctx.NextHop != "node-a" {
		t.Errorf("NextHop = %q, want node-a", ctx.NextHop)
	}
	if ctx.Via != "direct" {
		t.Errorf("Via = %q, want direct", ctx.Via)
	}
	if ctx.Priority != 3 {
		t.Errorf("Priority = %d, want 3", ctx.Priority)
	}
}

func TestRouteCtx_L4(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		L4Rules: []L4Rule{{
			Name: "dns-rule", DstPrefix: netip.MustParsePrefix("0.0.0.0/0"),
			DstPort: 53, Proto: 17,
			NextHop: "dns-node", Via: HopRelay, RelayID: "relay-1",
		}},
		FIB: []FIBEntry{{
			Prefix:  netip.MustParsePrefix("0.0.0.0/0"),
			NextHop: "default", Via: HopDirect,
		}},
	})
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkUDP,
		Source:      netip.MustParseAddrPort("10.0.0.1:1234"),
		Destination: netip.MustParseAddrPort("8.8.8.8:53"),
	}
	e.RouteCtx(ctx)
	if ctx.NextHop != "dns-node" {
		t.Errorf("NextHop = %q, want dns-node", ctx.NextHop)
	}
	if ctx.Priority != 4 {
		t.Errorf("Priority = %d, want 4", ctx.Priority)
	}
}

func TestRouteCtx_L5(t *testing.T) {
	e := NewEngine()
	e.Update(&RouteConfig{
		L5Rules: []L5Rule{{
			Name: "google", SNIPattern: "*.google.com",
			NextHop: "proxy-node", Via: HopRelay, RelayID: "relay-2",
		}},
		FIB: []FIBEntry{{
			Prefix:  netip.MustParsePrefix("0.0.0.0/0"),
			NextHop: "default", Via: HopDirect,
		}},
	})
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkTCP,
		Source:      netip.MustParseAddrPort("10.0.0.1:1234"),
		Destination: netip.MustParseAddrPort("142.250.80.14:443"),
		Domain:      "www.google.com",
	}
	e.RouteCtx(ctx)
	if ctx.NextHop != "proxy-node" {
		t.Errorf("NextHop = %q, want proxy-node", ctx.NextHop)
	}
	if ctx.Priority != 5 {
		t.Errorf("Priority = %d, want 5", ctx.Priority)
	}
}

func TestRouteCtx_NoMatch(t *testing.T) {
	e := NewEngine()
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkTCP,
		Source:      netip.MustParseAddrPort("10.0.0.1:1234"),
		Destination: netip.MustParseAddrPort("192.168.1.1:80"),
	}
	e.RouteCtx(ctx)
	if ctx.NextHop != "" {
		t.Errorf("NextHop = %q, want empty", ctx.NextHop)
	}
}
