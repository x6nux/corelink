package ecmp

import (
	"math"
	"testing"
)

func TestRendezvousHash_Deterministic(t *testing.T) {
	nhs := []string{"relay-0", "relay-1", "relay-2"}
	weights := []uint32{100, 100, 100}
	flowKey := uint64(12345)
	pick1 := RendezvousSelect(nhs, weights, flowKey)
	pick2 := RendezvousSelect(nhs, weights, flowKey)
	if pick1 != pick2 {
		t.Fatalf("same flowKey must pick same peer: %d vs %d", pick1, pick2)
	}
}

func TestRendezvousHash_Distribution(t *testing.T) {
	nhs := []string{"relay-0", "relay-1", "relay-2"}
	weights := []uint32{100, 100, 100}
	counts := make([]int, len(nhs))
	const N = 30000
	for i := uint64(0); i < N; i++ {
		idx := RendezvousSelect(nhs, weights, i)
		counts[idx]++
	}
	expected := float64(N) / float64(len(nhs))
	for i, c := range counts {
		deviation := math.Abs(float64(c)-expected) / expected
		if deviation > 0.05 {
			t.Errorf("peer %d: count=%d, expected ~%.0f, deviation=%.2f%%", i, c, expected, deviation*100)
		}
	}
}

func TestRendezvousHash_MinimalRemapping(t *testing.T) {
	nhs3 := []string{"relay-0", "relay-1", "relay-2"}
	nhs2 := []string{"relay-0", "relay-2"}
	weights3 := []uint32{100, 100, 100}
	weights2 := []uint32{100, 100}
	remapped := 0
	const N = 10000
	for i := uint64(0); i < N; i++ {
		old := nhs3[RendezvousSelect(nhs3, weights3, i)]
		new_ := nhs2[RendezvousSelect(nhs2, weights2, i)]
		if old != "relay-1" && old != new_ {
			remapped++
		}
	}
	remapRate := float64(remapped) / float64(N)
	if remapRate > 0.05 {
		t.Errorf("remap rate %.2f%% exceeds 5%% threshold", remapRate*100)
	}
}

func TestRendezvousHash_WeightedDistribution(t *testing.T) {
	nhs := []string{"relay-0", "relay-1"}
	weights := []uint32{200, 100}
	counts := make([]int, len(nhs))
	const N = 30000
	for i := uint64(0); i < N; i++ {
		idx := RendezvousSelect(nhs, weights, i)
		counts[idx]++
	}
	ratio := float64(counts[0]) / float64(counts[1])
	if ratio < 1.7 || ratio > 2.3 {
		t.Errorf("weight 200:100 should yield ~2:1 ratio, got %.2f (counts: %v)", ratio, counts)
	}
}

func TestFlowHash_Deterministic(t *testing.T) {
	srcIP := []byte{10, 0, 0, 1}
	dstIP := []byte{10, 0, 0, 2}
	h1 := FlowHash(srcIP, dstIP, 6, 12345, 80)
	h2 := FlowHash(srcIP, dstIP, 6, 12345, 80)
	if h1 != h2 {
		t.Fatalf("same 5-tuple must produce same hash: %d vs %d", h1, h2)
	}
}

func TestFlowHash_DifferentFlows(t *testing.T) {
	srcIP := []byte{10, 0, 0, 1}
	dstIP := []byte{10, 0, 0, 2}
	h1 := FlowHash(srcIP, dstIP, 6, 12345, 80)
	h2 := FlowHash(srcIP, dstIP, 6, 12346, 80)
	if h1 == h2 {
		t.Fatal("different 5-tuples should produce different hashes")
	}
}

func TestRendezvousSelect_Empty(t *testing.T) {
	idx := RendezvousSelect(nil, nil, 42)
	if idx != -1 {
		t.Fatalf("empty peerIDs should return -1, got %d", idx)
	}
}
