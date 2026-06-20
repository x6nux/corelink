package firewall

import (
	"context"
	"testing"
)

func TestNoopManager(t *testing.T) {
	var mgr FirewallManager = &NoopManager{}
	ctx := context.Background()
	if err := mgr.EnsureChains(ctx); err != nil {
		t.Errorf("EnsureChains: %v", err)
	}
	if err := mgr.ApplyDNS(ctx, nil); err != nil {
		t.Errorf("ApplyDNS: %v", err)
	}
	if err := mgr.ApplyEgress(ctx, nil); err != nil {
		t.Errorf("ApplyEgress: %v", err)
	}
	if err := mgr.Cleanup(ctx); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
}

func TestFactory(t *testing.T) {
	mgr := New()
	if mgr == nil {
		t.Fatal("New() returned nil")
	}
}
