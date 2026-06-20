package ingress

import (
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func ip(host string, port uint32, src genv1.IngressSource, conf uint32) *genv1.Ingress {
	return &genv1.Ingress{
		Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
		Host:       host,
		Port:       port,
		Source:     src,
		Confidence: conf,
	}
}

func TestMerge_DedupKeepsHighestConfidence(t *testing.T) {
	in := []*genv1.Ingress{
		ip("1.2.3.4", 443, genv1.IngressSource_INGRESS_SOURCE_STUN, 70),
		ip("1.2.3.4", 443, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
		ip("1.2.3.4", 443, genv1.IngressSource_INGRESS_SOURCE_URL, 50),
	}
	out := Merge(in)
	if len(out) != 1 {
		t.Fatalf("want 1 merged ingress, got %d", len(out))
	}
	if out[0].Confidence != 90 {
		t.Errorf("want confidence 90, got %d", out[0].Confidence)
	}
	if out[0].Source != genv1.IngressSource_INGRESS_SOURCE_NETIF {
		t.Errorf("want source NETIF, got %v", out[0].Source)
	}
}

func TestMerge_KeepsNatTypeOfWinner(t *testing.T) {
	stun := ip("5.6.7.8", 9000, genv1.IngressSource_INGRESS_SOURCE_STUN, 70)
	stun.NatType = genv1.NatType_NAT_TYPE_FULL_CONE
	in := []*genv1.Ingress{
		ip("5.6.7.8", 9000, genv1.IngressSource_INGRESS_SOURCE_URL, 50),
		stun,
	}
	out := Merge(in)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].NatType != genv1.NatType_NAT_TYPE_FULL_CONE {
		t.Errorf("want nat_type preserved from STUN winner, got %v", out[0].NatType)
	}
}

func TestMerge_TieBreakBySourcePriority(t *testing.T) {
	// 相同 confidence，NETIF 应优先于 OBSERVED。
	in := []*genv1.Ingress{
		ip("9.9.9.9", 80, genv1.IngressSource_INGRESS_SOURCE_OBSERVED, 80),
		ip("9.9.9.9", 80, genv1.IngressSource_INGRESS_SOURCE_NETIF, 80),
	}
	out := Merge(in)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].Source != genv1.IngressSource_INGRESS_SOURCE_NETIF {
		t.Errorf("tie should resolve to NETIF, got %v", out[0].Source)
	}
}

func TestMerge_CDNKeptSeparateFromDirect(t *testing.T) {
	cdn := &genv1.Ingress{
		Kind:       genv1.IngressKind_INGRESS_KIND_CDN,
		Host:       "edge.example.com",
		Port:       443,
		Source:     genv1.IngressSource_INGRESS_SOURCE_CONFIG,
		Confidence: 95,
		Sni:        "edge.example.com",
	}
	direct := ip("edge.example.com", 443, genv1.IngressSource_INGRESS_SOURCE_STUN, 70)
	out := Merge([]*genv1.Ingress{cdn, direct})
	if len(out) != 2 {
		t.Fatalf("CDN and IP_DIRECT with same host:port must stay separate, got %d", len(out))
	}
}

func TestMerge_DifferentPortNotMerged(t *testing.T) {
	in := []*genv1.Ingress{
		ip("1.1.1.1", 443, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
		ip("1.1.1.1", 8443, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
	}
	out := Merge(in)
	if len(out) != 2 {
		t.Fatalf("different ports must not merge, got %d", len(out))
	}
}

func TestMerge_DeterministicSort(t *testing.T) {
	in := []*genv1.Ingress{
		ip("3.3.3.3", 443, genv1.IngressSource_INGRESS_SOURCE_URL, 50),
		ip("1.1.1.1", 443, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
		ip("2.2.2.2", 443, genv1.IngressSource_INGRESS_SOURCE_OBSERVED, 80),
		ip("1.1.1.1", 80, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
	}
	out := Merge(in)
	// 期望排序: confidence desc, host asc, port asc, kind asc。
	wantOrder := []struct {
		host string
		port uint32
		conf uint32
	}{
		{"1.1.1.1", 80, 90},
		{"1.1.1.1", 443, 90},
		{"2.2.2.2", 443, 80},
		{"3.3.3.3", 443, 50},
	}
	if len(out) != len(wantOrder) {
		t.Fatalf("want %d, got %d", len(wantOrder), len(out))
	}
	for i, w := range wantOrder {
		if out[i].Host != w.host || out[i].Port != w.port || out[i].Confidence != w.conf {
			t.Errorf("pos %d: want %s:%d conf=%d, got %s:%d conf=%d",
				i, w.host, w.port, w.conf, out[i].Host, out[i].Port, out[i].Confidence)
		}
	}
}

func TestMerge_EmptyInput(t *testing.T) {
	if out := Merge(nil); len(out) != 0 {
		t.Errorf("nil input should give empty, got %d", len(out))
	}
	if out := Merge([]*genv1.Ingress{}); len(out) != 0 {
		t.Errorf("empty input should give empty, got %d", len(out))
	}
}

func TestMerge_SkipsNilEntries(t *testing.T) {
	in := []*genv1.Ingress{
		nil,
		ip("1.1.1.1", 443, genv1.IngressSource_INGRESS_SOURCE_NETIF, 90),
		nil,
	}
	out := Merge(in)
	if len(out) != 1 {
		t.Fatalf("nil entries must be skipped, got %d", len(out))
	}
}
