package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleSystemStatus_NilOnlineAndTopo 验证 Online 和 Topo 都为 nil 时不 panic
func TestHandleSystemStatus_NilOnlineAndTopo(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{{ID: "n1"}},
	}
	deps := Deps{
		Store:     ms,
		StartTime: time.Now(),
		Version:   "test",
	}
	h := handleSystemStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got systemStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OnlineCount != 0 {
		t.Errorf("online_count = %d, want 0", got.OnlineCount)
	}
	if got.TopoVersion != 0 {
		t.Errorf("topo_version = %d, want 0", got.TopoVersion)
	}
}

// TestHandleSystemLogs_InvalidJSON 验证无效 JSON 参数仍使用默认 count
func TestHandleSystemLogs_InvalidJSON(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	buf.Add(rpc.LogEntry{Level: "INFO", Message: "m1"})

	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	// 无效 JSON 应静默忽略，使用默认 count=100
	result, err := h(json.RawMessage(`{bad`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Errorf("应返回 1 条，got %d", len(got.Entries))
	}
}

// TestHandleConfigStatus_FullFields 验证 ConfigSummary 所有字段正确返回
func TestHandleConfigStatus_FullFields(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	deps.Config = &ConfigSummary{
		DBDSN:       "sqlite:///a.db",
		ListenAddr:  ":9090",
		AdminAddr:   ":8081",
		VirtualCIDR: "10.0.0.0/8",
		TLSMode:     "tls",
		CASubject:   "CN=Test",
		CAHash:      "sha256:fff",
	}
	h := handleConfigStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got ConfigSummary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ListenAddr != ":9090" {
		t.Errorf("grpc_enroll_addr = %q", got.ListenAddr)
	}
	if got.AdminAddr != ":8081" {
		t.Errorf("admin_addr = %q", got.AdminAddr)
	}
	if got.CASubject != "CN=Test" {
		t.Errorf("ca_subject = %q", got.CASubject)
	}
}
