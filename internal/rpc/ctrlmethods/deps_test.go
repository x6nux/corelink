package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestDepsZeroValue 验证 Deps 零值可安全构造
func TestDepsZeroValue(t *testing.T) {
	var d Deps
	if d.Store != nil {
		t.Error("零值 Deps 的 Store 应为 nil")
	}
	if d.CA != nil {
		t.Error("零值 Deps 的 CA 应为 nil")
	}
	if d.Config != nil {
		t.Error("零值 Deps 的 Config 应为 nil")
	}
	if d.LogBuffer != nil {
		t.Error("零值 Deps 的 LogBuffer 应为 nil")
	}
}

// TestConfigSummaryJSON 验证 ConfigSummary 可序列化/反序列化
func TestConfigSummaryJSON(t *testing.T) {
	cs := ConfigSummary{
		DBDSN:       "sqlite:///test.db",
		VirtualCIDR: "100.64.0.0/10",
		TLSMode:     "mtls",
	}
	b, err := json.Marshal(cs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ConfigSummary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DBDSN != cs.DBDSN {
		t.Errorf("db_dsn = %q, want %q", got.DBDSN, cs.DBDSN)
	}
	if got.VirtualCIDR != cs.VirtualCIDR {
		t.Errorf("virtual_cidr = %q, want %q", got.VirtualCIDR, cs.VirtualCIDR)
	}
}

// TestTopoStatusJSON 验证 TopoStatus 可序列化/反序列化
func TestTopoStatusJSON(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ts := TopoStatus{
		Version:       99,
		TransitCount:  5,
		LeafCount:     20,
		LastRecompute: now,
	}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got TopoStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 99 {
		t.Errorf("version = %d, want 99", got.Version)
	}
	if got.TransitCount != 5 {
		t.Errorf("transit_count = %d, want 5", got.TransitCount)
	}
}

// TestRegisterAll_WithFullDeps 验证完整依赖注册不 panic
func TestRegisterAll_WithFullDeps(t *testing.T) {
	srv := rpc.NewServer()
	deps := Deps{
		Store:     &mockStore{},
		CA:        &mockCA{pem: []byte("pem"), fingerprint: "fp"},
		Online:    &mockOnline{onlineIDs: map[string]bool{}},
		Notify:    &mockNotify{},
		Topo:      &mockTopo{},
		Ingress:   &mockIngress{sets: map[string]*genv1.IngressSet{}},
		StartTime: time.Now(),
		Version:   "test",
	}
	RegisterAll(srv, deps)
}
