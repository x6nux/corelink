package ingress

import (
	"context"
	"errors"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func findBySource(set *genv1.IngressSet, src genv1.IngressSource) *genv1.Ingress {
	for _, ing := range set.GetIngresses() {
		if ing.GetSource() == src {
			return ing
		}
	}
	return nil
}

func TestDiscover_AllSources(t *testing.T) {
	opts := DiscoverOptions{
		NodeID: "node-A",
		ConfigIngresses: []*genv1.Ingress{
			{
				Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
				Host:       "10.0.0.1",
				Port:       7000,
				Source:     genv1.IngressSource_INGRESS_SOURCE_CONFIG,
				Confidence: 95,
			},
		},
		Observed: &genv1.Endpoint{Host: "203.0.113.5", Port: 8000},
		StunFn: func(ctx context.Context) (string, uint32, genv1.NatType, error) {
			return "198.51.100.7", 9000, genv1.NatType_NAT_TYPE_FULL_CONE, nil
		},
		NetifFn: func() []*genv1.Ingress {
			return []*genv1.Ingress{
				{
					Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
					Host:       "192.168.1.2",
					Source:     genv1.IngressSource_INGRESS_SOURCE_NETIF,
					Confidence: 40,
				},
			}
		},
		UrlFn: func(ctx context.Context) (string, error) {
			return "198.51.100.99", nil
		},
	}

	set := Discover(context.Background(), opts)
	if set == nil {
		t.Fatal("Discover returned nil")
	}
	if set.GetNodeId() != "node-A" {
		t.Errorf("want node_id node-A, got %q", set.GetNodeId())
	}

	// 5 路各贡献至少一条（host/port 互不冲突，不应被合并掉）。
	if len(set.GetIngresses()) != 5 {
		t.Fatalf("want 5 ingresses (one per source), got %d", len(set.GetIngresses()))
	}

	cfg := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_CONFIG)
	if cfg == nil || cfg.GetHost() != "10.0.0.1" || cfg.GetConfidence() != 95 {
		t.Errorf("CONFIG ingress wrong: %+v", cfg)
	}

	obs := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_OBSERVED)
	if obs == nil {
		t.Fatal("missing OBSERVED ingress")
	}
	if obs.GetHost() != "203.0.113.5" || obs.GetPort() != 8000 {
		t.Errorf("OBSERVED host/port wrong: %+v", obs)
	}
	if obs.GetKind() != genv1.IngressKind_INGRESS_KIND_IP_DIRECT {
		t.Errorf("OBSERVED kind should be IP_DIRECT, got %v", obs.GetKind())
	}
	if obs.GetConfidence() != observedConfidence {
		t.Errorf("OBSERVED confidence want %d, got %d", observedConfidence, obs.GetConfidence())
	}

	stun := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_STUN)
	if stun == nil {
		t.Fatal("missing STUN ingress")
	}
	if stun.GetHost() != "198.51.100.7" || stun.GetPort() != 9000 {
		t.Errorf("STUN host/port wrong: %+v", stun)
	}
	if stun.GetNatType() != genv1.NatType_NAT_TYPE_FULL_CONE {
		t.Errorf("STUN nat_type wrong: %v", stun.GetNatType())
	}
	if stun.GetConfidence() != stunConfidence {
		t.Errorf("STUN confidence want %d, got %d", stunConfidence, stun.GetConfidence())
	}

	netif := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_NETIF)
	if netif == nil || netif.GetHost() != "192.168.1.2" {
		t.Errorf("NETIF ingress wrong: %+v", netif)
	}

	url := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_URL)
	if url == nil {
		t.Fatal("missing URL ingress")
	}
	if url.GetHost() != "198.51.100.99" || url.GetPort() != 0 {
		t.Errorf("URL host/port wrong (should have no port): %+v", url)
	}
	if url.GetConfidence() != urlConfidence {
		t.Errorf("URL confidence want %d, got %d", urlConfidence, url.GetConfidence())
	}
}

func TestDiscover_FaultTolerantPerSource(t *testing.T) {
	opts := DiscoverOptions{
		NodeID:   "node-B",
		Observed: nil, // 空源
		StunFn: func(ctx context.Context) (string, uint32, genv1.NatType, error) {
			return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, errors.New("stun timeout")
		},
		NetifFn: func() []*genv1.Ingress {
			return []*genv1.Ingress{
				{
					Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
					Host:       "192.168.0.5",
					Source:     genv1.IngressSource_INGRESS_SOURCE_NETIF,
					Confidence: 40,
				},
			}
		},
		UrlFn: func(ctx context.Context) (string, error) {
			return "", errors.New("url 503")
		},
	}
	set := Discover(context.Background(), opts)
	// STUN/URL 失败、Observed 空、Config 空 → 只剩 NETIF 一条。
	if len(set.GetIngresses()) != 1 {
		t.Fatalf("only NETIF should survive, got %d", len(set.GetIngresses()))
	}
	if set.GetIngresses()[0].GetSource() != genv1.IngressSource_INGRESS_SOURCE_NETIF {
		t.Errorf("surviving source should be NETIF, got %v", set.GetIngresses()[0].GetSource())
	}
}

func TestDiscover_NilFnsSkipped(t *testing.T) {
	// 所有注入 fn 为 nil、所有源为空。
	opts := DiscoverOptions{NodeID: "node-C"}
	set := Discover(context.Background(), opts)
	if set == nil {
		t.Fatal("Discover must not return nil even when everything empty")
	}
	if set.GetNodeId() != "node-C" {
		t.Errorf("node_id wrong: %q", set.GetNodeId())
	}
	if len(set.GetIngresses()) != 0 {
		t.Errorf("want empty ingress set, got %d", len(set.GetIngresses()))
	}
}

func TestDiscover_ObservedWithoutPort(t *testing.T) {
	opts := DiscoverOptions{
		NodeID:   "node-D",
		Observed: &genv1.Endpoint{Host: "203.0.113.9"}, // 无端口
	}
	set := Discover(context.Background(), opts)
	if len(set.GetIngresses()) != 1 {
		t.Fatalf("want 1 OBSERVED ingress, got %d", len(set.GetIngresses()))
	}
	obs := set.GetIngresses()[0]
	if obs.GetHost() != "203.0.113.9" || obs.GetPort() != 0 {
		t.Errorf("OBSERVED wrong: %+v", obs)
	}
}

func TestDiscover_ObservedEmptyHostSkipped(t *testing.T) {
	opts := DiscoverOptions{
		NodeID:   "node-E",
		Observed: &genv1.Endpoint{Host: "", Port: 80}, // host 空 → 跳过
	}
	set := Discover(context.Background(), opts)
	if len(set.GetIngresses()) != 0 {
		t.Errorf("empty-host Observed must be skipped, got %d", len(set.GetIngresses()))
	}
}

func TestDiscoverWithPortmapFn(t *testing.T) {
	opts := DiscoverOptions{
		NodeID: "node-PM",
		PortmapFn: func(ctx context.Context) ([]*genv1.Ingress, error) {
			return []*genv1.Ingress{
				{
					Id:         "upnp-203.0.113.50-51820-udp",
					Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
					Host:       "203.0.113.50",
					Port:       51820,
					Source:     genv1.IngressSource_INGRESS_SOURCE_UPNP,
					Confidence: upnpConfidence,
					UdpPort:    51820,
				},
			}, nil
		},
	}
	set := Discover(context.Background(), opts)
	if len(set.GetIngresses()) != 1 {
		t.Fatalf("want 1 UPNP ingress, got %d", len(set.GetIngresses()))
	}
	upnp := findBySource(set, genv1.IngressSource_INGRESS_SOURCE_UPNP)
	if upnp == nil {
		t.Fatal("missing UPNP ingress")
	}
	if upnp.GetHost() != "203.0.113.50" {
		t.Errorf("UPNP host wrong: %q", upnp.GetHost())
	}
	if upnp.GetConfidence() != upnpConfidence {
		t.Errorf("UPNP confidence want %d, got %d", upnpConfidence, upnp.GetConfidence())
	}
	if upnp.GetUdpPort() != 51820 {
		t.Errorf("UPNP udp_port want 51820, got %d", upnp.GetUdpPort())
	}
}

func TestDiscoverPortmapFnErr(t *testing.T) {
	opts := DiscoverOptions{
		NodeID: "node-PME",
		NetifFn: func() []*genv1.Ingress {
			return []*genv1.Ingress{
				{
					Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
					Host:       "192.168.1.100",
					Source:     genv1.IngressSource_INGRESS_SOURCE_NETIF,
					Confidence: 40,
				},
			}
		},
		PortmapFn: func(ctx context.Context) ([]*genv1.Ingress, error) {
			return nil, errors.New("portmap unavailable")
		},
	}
	set := Discover(context.Background(), opts)
	// PortmapFn 失败不影响其它路。
	if len(set.GetIngresses()) != 1 {
		t.Fatalf("PortmapFn error should not affect other sources, want 1, got %d", len(set.GetIngresses()))
	}
	if set.GetIngresses()[0].GetSource() != genv1.IngressSource_INGRESS_SOURCE_NETIF {
		t.Errorf("surviving source should be NETIF, got %v", set.GetIngresses()[0].GetSource())
	}
}

func TestDiscover_MergesOverlappingSources(t *testing.T) {
	// STUN 与 OBSERVED 给出相同 host:port → 合并为一条，取高 confidence（OBSERVED 80 > STUN 70）。
	opts := DiscoverOptions{
		NodeID:   "node-F",
		Observed: &genv1.Endpoint{Host: "198.51.100.50", Port: 9000},
		StunFn: func(ctx context.Context) (string, uint32, genv1.NatType, error) {
			return "198.51.100.50", 9000, genv1.NatType_NAT_TYPE_FULL_CONE, nil
		},
	}
	set := Discover(context.Background(), opts)
	if len(set.GetIngresses()) != 1 {
		t.Fatalf("overlapping STUN+OBSERVED should merge to 1, got %d", len(set.GetIngresses()))
	}
	if set.GetIngresses()[0].GetSource() != genv1.IngressSource_INGRESS_SOURCE_OBSERVED {
		t.Errorf("higher-confidence OBSERVED should win, got %v", set.GetIngresses()[0].GetSource())
	}
}
