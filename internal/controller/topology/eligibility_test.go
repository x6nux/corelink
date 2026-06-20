package topology

import (
	"reflect"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestEligible(t *testing.T) {
	cases := []struct {
		name      string
		nat       genv1.NatType
		ingresses []IngressMeta
		want      bool
	}{
		{
			name:      "reachable high-confidence ingress -> eligible",
			nat:       genv1.NatType_NAT_TYPE_OPEN,
			ingresses: []IngressMeta{{ID: "e1", Confidence: 80, Reachable: true}},
			want:      true,
		},
		{
			name:      "all unreachable -> leaf",
			nat:       genv1.NatType_NAT_TYPE_OPEN,
			ingresses: []IngressMeta{{ID: "e1", Confidence: 90, Reachable: false}},
			want:      false,
		},
		{
			name:      "reachable but low confidence -> leaf",
			nat:       genv1.NatType_NAT_TYPE_OPEN,
			ingresses: []IngressMeta{{ID: "e1", Confidence: 59, Reachable: true}},
			want:      false,
		},
		{
			name:      "confidence exactly at threshold -> eligible",
			nat:       genv1.NatType_NAT_TYPE_OPEN,
			ingresses: []IngressMeta{{ID: "e1", Confidence: minConfidence, Reachable: true}},
			want:      true,
		},
		{
			name:      "symmetric without stable ingress -> leaf",
			nat:       genv1.NatType_NAT_TYPE_SYMMETRIC,
			ingresses: []IngressMeta{{ID: "e1", Confidence: 90, Reachable: false}},
			want:      false,
		},
		{
			name:      "symmetric with reachable CDN ingress -> eligible",
			nat:       genv1.NatType_NAT_TYPE_SYMMETRIC,
			ingresses: []IngressMeta{{ID: "cdn", Confidence: 95, Reachable: true}},
			want:      true,
		},
		{
			name:      "no ingresses -> leaf",
			nat:       genv1.NatType_NAT_TYPE_OPEN,
			ingresses: nil,
			want:      false,
		},
		{
			name: "mixed: one qualifying ingress is enough",
			nat:  genv1.NatType_NAT_TYPE_PORT_RESTRICTED,
			ingresses: []IngressMeta{
				{ID: "e1", Confidence: 10, Reachable: true},
				{ID: "e2", Confidence: 70, Reachable: false},
				{ID: "e3", Confidence: 65, Reachable: true},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Eligible(tc.nat, tc.ingresses); got != tc.want {
				t.Fatalf("Eligible(%v, %v) = %v, want %v", tc.nat, tc.ingresses, got, tc.want)
			}
		})
	}
}

func TestClassifyNodes(t *testing.T) {
	nodes := []NodeEligibilityInput{
		{NodeID: "nodeC", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{{ID: "e", Confidence: 80, Reachable: true}}},
		{NodeID: "nodeA", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{{ID: "e", Confidence: 90, Reachable: false}}},
		{NodeID: "nodeB", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{{ID: "e", Confidence: 70, Reachable: true}}},
		{NodeID: "nodeD", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{{ID: "cdn", Confidence: 95, Reachable: true}}},
		{NodeID: "nodeE", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: nil},
	}
	transits, leaves := ClassifyNodes(nodes)

	wantTransits := []string{"nodeB", "nodeC", "nodeD"}
	wantLeaves := []string{"nodeA", "nodeE"}
	if !reflect.DeepEqual(transits, wantTransits) {
		t.Errorf("transits = %v, want %v", transits, wantTransits)
	}
	if !reflect.DeepEqual(leaves, wantLeaves) {
		t.Errorf("leaves = %v, want %v", leaves, wantLeaves)
	}
}

func TestClassifyNodes_empty(t *testing.T) {
	transits, leaves := ClassifyNodes(nil)
	if len(transits) != 0 || len(leaves) != 0 {
		t.Fatalf("expected empty, got transits=%v leaves=%v", transits, leaves)
	}
}
