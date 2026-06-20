package topology

import "testing"

func TestDAGValidate_NoCycle(t *testing.T) {
	graph := map[string][]string{
		"A":       {"relay-0"},
		"relay-0": {"relay-1"},
		"relay-1": {"B"},
		"B":       {},
	}
	if err := validateDAG(graph); err != nil {
		t.Fatalf("linear path should be acyclic: %v", err)
	}
}

func TestDAGValidate_DetectCycle(t *testing.T) {
	graph := map[string][]string{
		"A":       {"relay-0"},
		"relay-0": {"relay-1"},
		"relay-1": {"relay-0"},
	}
	if err := validateDAG(graph); err == nil {
		t.Fatal("cycle should be detected")
	}
}

func TestDAGValidate_ECMPNoCycle(t *testing.T) {
	graph := map[string][]string{
		"A":       {"relay-0", "relay-1"},
		"relay-0": {"B"},
		"relay-1": {"B"},
		"B":       {},
	}
	if err := validateDAG(graph); err != nil {
		t.Fatalf("ECMP diamond should be acyclic: %v", err)
	}
}

func TestDAGValidate_ECMPWithCycle(t *testing.T) {
	graph := map[string][]string{
		"A":       {"relay-0", "relay-1"},
		"relay-0": {"relay-1"},
		"relay-1": {"relay-0"},
	}
	if err := validateDAG(graph); err == nil {
		t.Fatal("ECMP path with cycle should be detected")
	}
}
