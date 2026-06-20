package ingress

import (
	"net"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// fakeAddr 实现 net.Addr，承载一个 CIDR 字符串（如 net.Interfaces 返回的 *net.IPNet）。
type fakeAddr struct{ s string }

func (a fakeAddr) Network() string { return "ip+net" }
func (a fakeAddr) String() string  { return a.s }

func fakeLister(addrs []string) InterfaceLister {
	return func() ([]net.Addr, error) {
		out := make([]net.Addr, 0, len(addrs))
		for _, s := range addrs {
			out = append(out, fakeAddr{s})
		}
		return out, nil
	}
}

func TestEnumInterfaces_ClassifiesAddresses(t *testing.T) {
	lister := fakeLister([]string{
		"203.0.113.5/24",  // 公网 IPv4 → 高置信度
		"10.1.2.3/8",      // 私有 LAN → 低置信度
		"192.168.1.10/24", // 私有 LAN → 低置信度
		"127.0.0.1/8",     // 回环 → 跳过
		"169.254.1.1/16",  // 链路本地 → 跳过
	})

	got := EnumInterfaces(lister)

	byHost := map[string]*genv1.Ingress{}
	for _, ing := range got {
		byHost[ing.Host] = ing
	}

	// 回环/链路本地必须跳过。
	if _, ok := byHost["127.0.0.1"]; ok {
		t.Errorf("loopback 127.0.0.1 不应出现在结果中")
	}
	if _, ok := byHost["169.254.1.1"]; ok {
		t.Errorf("link-local 169.254.1.1 不应出现在结果中")
	}

	pub, ok := byHost["203.0.113.5"]
	if !ok {
		t.Fatalf("公网 IP 203.0.113.5 缺失，got=%+v", got)
	}
	if pub.Source != genv1.IngressSource_INGRESS_SOURCE_NETIF {
		t.Errorf("公网 source = %v, 期望 NETIF", pub.Source)
	}
	if pub.Kind != genv1.IngressKind_INGRESS_KIND_IP_DIRECT {
		t.Errorf("公网 kind = %v, 期望 IP_DIRECT", pub.Kind)
	}
	if pub.Confidence != netifPublicConfidence {
		t.Errorf("公网 confidence = %d, 期望 %d", pub.Confidence, netifPublicConfidence)
	}

	for _, h := range []string{"10.1.2.3", "192.168.1.10"} {
		priv, ok := byHost[h]
		if !ok {
			t.Fatalf("私有 IP %s 缺失", h)
		}
		if priv.Source != genv1.IngressSource_INGRESS_SOURCE_NETIF {
			t.Errorf("私有 %s source = %v, 期望 NETIF", h, priv.Source)
		}
		if priv.Confidence != netifPrivateConfidence {
			t.Errorf("私有 %s confidence = %d, 期望 %d", h, priv.Confidence, netifPrivateConfidence)
		}
		if priv.Confidence >= pub.Confidence {
			t.Errorf("私有置信度 (%d) 应低于公网 (%d)", priv.Confidence, pub.Confidence)
		}
	}

	if len(got) != 3 {
		t.Errorf("期望 3 个 ingress (1 公网 + 2 私有)，got %d", len(got))
	}
}

func TestEnumInterfaces_ListerError(t *testing.T) {
	lister := InterfaceLister(func() ([]net.Addr, error) {
		return nil, net.UnknownNetworkError("boom")
	})
	if got := EnumInterfaces(lister); len(got) != 0 {
		t.Errorf("lister 出错时应返回空，got %d", len(got))
	}
}

func TestEnumInterfaces_NilLister(t *testing.T) {
	// nil lister 应回退到默认实现且不 panic。
	_ = EnumInterfaces(nil)
}

func TestEnumInterfaces_ReservedNotPublic(t *testing.T) {
	lister := fakeLister([]string{
		"100.64.1.1/10",  // CGNAT → 不应高置信
		"198.18.0.1/15",  // Benchmark → 不应高置信
		"240.0.0.1/4",    // 保留段 → 不应高置信
		"203.0.113.5/24", // 真公网 → 高置信
	})

	got := EnumInterfaces(lister)

	byHost := map[string]*genv1.Ingress{}
	for _, ing := range got {
		byHost[ing.Host] = ing
	}

	// 真公网仍为高置信。
	pub, ok := byHost["203.0.113.5"]
	if !ok {
		t.Fatalf("公网 203.0.113.5 缺失，got=%+v", got)
	}
	if pub.Confidence != netifPublicConfidence {
		t.Errorf("公网 confidence = %d, 期望 %d", pub.Confidence, netifPublicConfidence)
	}

	// CGNAT/Benchmark/保留段不得带公网高置信度。
	for _, h := range []string{"100.64.1.1", "198.18.0.1", "240.0.0.1"} {
		ing, ok := byHost[h]
		if !ok {
			continue // 跳过或降级均可接受，关键是不得为高置信公网。
		}
		if ing.Confidence == netifPublicConfidence {
			t.Errorf("保留段 %s 被误判为公网高置信度 %d", h, ing.Confidence)
		}
	}
}
