package gen_test

import (
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestEnumsCompileAndHaveExpectedValues(t *testing.T) {
	if genv1.NodeRole_NODE_ROLE_RELAY != 2 {
		t.Fatalf("relay role = %d, want 2", genv1.NodeRole_NODE_ROLE_RELAY)
	}
	if genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC != 5 {
		t.Fatalf("grpc proto = %d, want 5", genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC)
	}
	ep := &genv1.RelayEndpoint{RelayId: "r1", Priority: 0}
	if ep.GetRelayId() != "r1" {
		t.Fatal("getter 异常")
	}
}

func TestEnrollMessagesShape(t *testing.T) {
	req := &genv1.EnrollRequest{EnrollmentKey: "k", Role: genv1.NodeRole_NODE_ROLE_AGENT}
	if req.GetRole() != genv1.NodeRole_NODE_ROLE_AGENT {
		t.Fatal("role getter 异常")
	}
	_ = &genv1.EnrollResponse{NodeId: "n1", VirtualIp: "100.64.0.2"}
}

func TestNodeConfigShape(t *testing.T) {
	cfg := &genv1.NodeConfig{
		Generation: 7,
		Peers:      []*genv1.Peer{{NodeId: "n2", AllowedIps: []string{"100.64.0.3/32"}}},
		Routes:     []*genv1.Route{{DestCidr: "100.64.0.3/32", ViaRelayId: "r1"}},
	}
	if cfg.GetGeneration() != 7 || len(cfg.GetPeers()) != 1 {
		t.Fatal("NodeConfig 字段异常")
	}
}

func TestEnvelopeAndLinkStateShape(t *testing.T) {
	env := &genv1.Envelope{SrcNode: "a", DstNode: "b", DstRelay: "r2", Ttl: 8}
	if env.GetTtl() != 8 || env.GetDstRelay() != "r2" {
		t.Fatal("Envelope 字段异常")
	}
	ls := &genv1.LinkState{RelayId: "r1", Version: 3, Links: []*genv1.LinkMetric{{NeighborRelayId: "r2", RttMs: 12}}}
	if ls.GetVersion() != 3 || ls.GetLinks()[0].GetRttMs() != 12 {
		t.Fatal("LinkState 字段异常")
	}
}
