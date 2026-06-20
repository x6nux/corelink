package discovery

import (
	"testing"
)

const sampleIPNeigh = `10.1.0.1 dev eth0 lladdr aa:bb:cc:dd:ee:01 REACHABLE
10.1.0.8 dev eth0 lladdr aa:bb:cc:dd:ee:08 STALE
10.1.0.9 dev eth0 lladdr aa:bb:cc:dd:ee:09 FAILED
10.2.0.1 dev eth1 lladdr aa:bb:cc:dd:ee:11 REACHABLE
192.168.1.1 dev wlan0 lladdr ff:ff:ff:ff:ff:ff REACHABLE`

func TestParseIPNeigh(t *testing.T) {
	entries := ParseIPNeigh(sampleIPNeigh)
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}
}

func TestFilterReachable(t *testing.T) {
	entries := ParseIPNeigh(sampleIPNeigh)
	reachable := FilterReachable(entries)
	if len(reachable) != 4 {
		t.Fatalf("reachable = %d, want 4 (FAILED excluded)", len(reachable))
	}
}

func TestFilterByCIDR(t *testing.T) {
	entries := ParseIPNeigh(sampleIPNeigh)
	reachable := FilterReachable(entries)
	inRange := FilterByCIDR(reachable, "10.1.0.0/16")
	if len(inRange) != 2 {
		t.Fatalf("in CIDR range = %d, want 2", len(inRange))
	}
}

func TestMapToVIP(t *testing.T) {
	vip, err := MapToVIP("10.1.0.8", "10.1.0.0/24", "100.64.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if vip != "100.64.0.8" {
		t.Fatalf("VIP = %s, want 100.64.0.8", vip)
	}
}

func TestMapToVIPLargeOffset(t *testing.T) {
	vip, err := MapToVIP("10.1.1.100", "10.1.0.0/16", "100.64.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if vip != "100.64.1.100" {
		t.Fatalf("VIP = %s, want 100.64.1.100", vip)
	}
}

func TestParseIPNeighEmpty(t *testing.T) {
	entries := ParseIPNeigh("")
	if entries != nil {
		t.Fatalf("entries = %v, want nil for empty input", entries)
	}
}

func TestFilterByCIDR_NoMatch(t *testing.T) {
	entries := ParseIPNeigh(sampleIPNeigh)
	reachable := FilterReachable(entries)
	inRange := FilterByCIDR(reachable, "172.16.0.0/12")
	if len(inRange) != 0 {
		t.Fatalf("in CIDR range = %d, want 0 (no match)", len(inRange))
	}
}

func TestMapToVIP_CrossSubnetBoundary(t *testing.T) {
	vip, err := MapToVIP("10.1.0.255", "10.1.0.0/24", "100.64.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if vip != "100.64.0.255" {
		t.Fatalf("VIP = %s, want 100.64.0.255", vip)
	}
}
