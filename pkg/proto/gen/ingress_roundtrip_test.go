package genv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// roundTrip marshals src, unmarshals into a fresh dst, and fails on error.
func roundTrip[T proto.Message](t *testing.T, src T, dst T) {
	t.Helper()
	raw, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := proto.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(src, dst) {
		t.Fatalf("roundtrip mismatch:\n src=%v\n dst=%v", src, dst)
	}
}

func sampleIngress() *Ingress {
	return &Ingress{
		Id:         "ing-1",
		Kind:       IngressKind_INGRESS_KIND_CDN,
		Host:       "edge.example.com",
		Port:       443,
		Protocols:  []TunnelProtocol{TunnelProtocol_TUNNEL_PROTOCOL_TLS, TunnelProtocol_TUNNEL_PROTOCOL_WSS},
		UdpPort:    51820,
		Sni:        "sni.example.com",
		Source:     IngressSource_INGRESS_SOURCE_STUN,
		Confidence: 87,
		NatType:    NatType_NAT_TYPE_PORT_RESTRICTED,
	}
}

func TestIngressRoundTrip(t *testing.T) {
	roundTrip(t, sampleIngress(), &Ingress{})
}

func TestIngressSetRoundTrip(t *testing.T) {
	src := &IngressSet{
		NodeId:    "node-a",
		Ingresses: []*Ingress{sampleIngress(), {Id: "ing-2", Kind: IngressKind_INGRESS_KIND_IP_DIRECT, Host: "1.2.3.4", Port: 80, NatType: NatType_NAT_TYPE_OPEN}},
	}
	roundTrip(t, src, &IngressSet{})
}

func TestQualityReportRoundTrip(t *testing.T) {
	src := &QualityReport{
		SrcNode: "node-a",
		Samples: []*EdgeSample{
			{DstNode: "node-b", IngressId: "ing-1", RttMs: 42, LossPermille: 5, TsUnix: 1700000000},
			{DstNode: "node-c", IngressId: "ing-2", RttMs: 99, LossPermille: 0, TsUnix: 1700000123},
		},
	}
	roundTrip(t, src, &QualityReport{})
}

func TestEdgeEventRoundTrip(t *testing.T) {
	src := &EdgeEvent{
		SrcNode:      "node-a",
		DstNode:      "node-b",
		IngressId:    "ing-1",
		Kind:         EdgeEventKind_EDGE_EVENT_KIND_DEGRADED,
		RttMs:        250,
		LossPermille: 120,
	}
	roundTrip(t, src, &EdgeEvent{})
}

func sampleTopology() *TopologyAssignment {
	return &TopologyAssignment{
		Version: 7,
		Role:    NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
		Neighbors: []*NeighborRef{
			{NodeId: "node-b", Ingresses: []*Ingress{sampleIngress()}},
		},
		BaselineRoutes: []*Route2{
			{DstNode: "node-c", Hops: []*Hop{
				{NodeId: "node-b", IngressId: "ing-1"},
				{NodeId: "node-c", IngressId: "ing-9"},
			}},
		},
		ProbeTargets: []*ProbeTarget{
			{NodeId: "node-b", IngressIds: []string{"ing-1", "ing-2"}},
		},
	}
}

func TestTopologyAssignmentRoundTrip(t *testing.T) {
	roundTrip(t, sampleTopology(), &TopologyAssignment{})
}

func TestNodeConfigEmbeddedTopologyRoundTrip(t *testing.T) {
	src := &NodeConfig{
		Generation: 3,
		VirtualIp:  "10.0.0.2",
		SubnetCidr: "10.0.0.0/24",
		Topology:   sampleTopology(),
	}
	dst := &NodeConfig{}
	roundTrip(t, src, dst)
	if dst.GetTopology().GetRole() != NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		t.Fatalf("embedded topology role lost: %v", dst.GetTopology().GetRole())
	}
}

func TestSourceAddrRoundTrip(t *testing.T) {
	src := &SourceAddr{Host: "203.0.113.5", Port: 12345}
	roundTrip(t, src, &SourceAddr{})
}

func TestAckRoundTrip(t *testing.T) {
	roundTrip(t, &Ack{Ok: true}, &Ack{})
}
