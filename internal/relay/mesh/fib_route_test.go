package mesh

import (
	"net/netip"
	"testing"

	"github.com/x6nux/corelink/internal/transport/fib"
)

func TestFIBRoute_Pick(t *testing.T) {
	r := NewFIBRoute()
	r.UpdateFIB(netip.MustParsePrefix("10.0.0.2/32"), []fib.NextHop{
		{PeerID: "relay-1", Weight: 100},
	})
	hop, ok := r.Route(netip.MustParseAddr("10.0.0.2"), 12345)
	if !ok {
		t.Fatal("should route to 10.0.0.2")
	}
	if hop.PeerID != "relay-1" {
		t.Errorf("got %q, want relay-1", hop.PeerID)
	}
}

func TestFIBRoute_ECMPFlowAffinity(t *testing.T) {
	r := NewFIBRoute()
	r.UpdateFIB(netip.MustParsePrefix("10.0.0.2/32"), []fib.NextHop{
		{PeerID: "relay-1", Weight: 100},
		{PeerID: "relay-2", Weight: 100},
	})
	hop1, _ := r.Route(netip.MustParseAddr("10.0.0.2"), 99999)
	hop2, _ := r.Route(netip.MustParseAddr("10.0.0.2"), 99999)
	if hop1.PeerID != hop2.PeerID {
		t.Error("same flowKey should pick same peer")
	}
}

func TestFIBRoute_ECMPDistribution(t *testing.T) {
	r := NewFIBRoute()
	r.UpdateFIB(netip.MustParsePrefix("10.0.0.2/32"), []fib.NextHop{
		{PeerID: "relay-1", Weight: 100},
		{PeerID: "relay-2", Weight: 100},
	})
	counts := map[string]int{}
	for i := range uint64(1000) {
		hop, _ := r.Route(netip.MustParseAddr("10.0.0.2"), i)
		counts[hop.PeerID]++
	}
	if counts["relay-1"] == 0 || counts["relay-2"] == 0 {
		t.Errorf("ECMP should distribute: %v", counts)
	}
}

func TestFIBRoute_Miss(t *testing.T) {
	r := NewFIBRoute()
	_, ok := r.Route(netip.MustParseAddr("10.0.0.99"), 1)
	if ok {
		t.Error("unknown dst should miss")
	}
}

func TestFIBRoute_Remove(t *testing.T) {
	r := NewFIBRoute()
	p := netip.MustParsePrefix("10.0.0.2/32")
	r.UpdateFIB(p, []fib.NextHop{{PeerID: "relay-1", Weight: 100}})
	r.RemoveFIB(p)
	_, ok := r.Route(netip.MustParseAddr("10.0.0.2"), 1)
	if ok {
		t.Error("removed entry should miss")
	}
}
