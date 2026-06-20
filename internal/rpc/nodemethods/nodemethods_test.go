package nodemethods

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func buildTestDeps() Deps {
	return Deps{
		NodeID:        "node-test-1",
		VIP:           "100.64.0.1",
		Role:          func() string { return "TRANSIT" },
		TopoVer:       func() uint64 { return 42 },
		TopoUpdatedAt: func() time.Time { return time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC) },
		Uptime:        func() time.Duration { return 3661 * time.Second }, // 1h1m1s
		Connected:     func() bool { return true },
		Config: func() any {
			return map[string]any{
				"data_dir":  "/var/lib/corelink",
				"log_level": "info",
			}
		},
		Ingresses: func() []IngressInfo {
			return []IngressInfo{
				{Host: "1.2.3.4", Port: 443, Source: "STUN", Confidence: 90, NATType: "FULL_CONE"},
				{Host: "5.6.7.8", Port: 8443, Source: "UPNP", Confidence: 100, NATType: "NONE"},
				{Host: "192.168.1.1", Port: 443, Source: "NETIF", Confidence: 50, NATType: "UNKNOWN"},
			}
		},
		PortmapMappings: func() []MappingInfo {
			return []MappingInfo{
				{Protocol: "NAT-PMP", ExternalIP: "1.2.3.4", ExternalPort: 443, InternalPort: 443, Transport: "TCP", TTL: "7200s", RenewIn: "3600s"},
				{Protocol: "UPnP", ExternalIP: "1.2.3.4", ExternalPort: 51820, InternalPort: 51820, Transport: "UDP", TTL: "7200s", RenewIn: "3500s"},
			}
		},
		PortmapStatus: func() PortmapStatusInfo {
			return PortmapStatusInfo{Active: true, ManagedCount: 2}
		},
		Connections: func() []ConnectionInfo {
			return []ConnectionInfo{
				{PeerID: "peer-a", PeerIP: "100.64.0.2", LinkType: "direct", RTTms: 5, RTTValid: true, Loss: 0, LossValid: true, State: "established"},
				{PeerID: "peer-b", PeerIP: "100.64.0.3", LinkType: "node", RTTms: 30, RTTValid: true, Loss: 10, LossValid: true, State: "established"},
			}
		},
		Routes: func(dst string) []RouteInfo {
			if dst == "dst-node" {
				return []RouteInfo{
					{
						Hops: []HopInfo{
							{NodeID: "relay-a", IngressID: "ing-1", Host: "10.0.0.1", Port: 443},
							{NodeID: "dst-node", IngressID: "ing-2", Host: "10.0.0.10", Port: 443},
						},
						TotalHops: 2,
						Active:    true,
					},
					{
						Hops: []HopInfo{
							{NodeID: "relay-b", IngressID: "ing-3", Host: "10.0.0.2", Port: 8443},
							{NodeID: "relay-c", IngressID: "ing-4", Host: "10.0.0.3", Port: 443},
							{NodeID: "dst-node", IngressID: "ing-2", Host: "10.0.0.10", Port: 443},
						},
						TotalHops: 3,
						Active:    false,
					},
				}
			}
			return nil
		},
		Peers: func() []PeerInfo {
			return []PeerInfo{
				{NodeID: "peer-a", Hostname: "host-a", VIP: "100.64.0.2"},
				{NodeID: "peer-b", Hostname: "host-b", VIP: "100.64.0.3"},
			}
		},
	}
}

// ── system tests ────────────────────────────────────────────────────────────

func TestSystemStatus(t *testing.T) {
	deps := buildTestDeps()
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
	if got.NodeID != "node-test-1" {
		t.Errorf("node_id = %q, want %q", got.NodeID, "node-test-1")
	}
	if got.VIP != "100.64.0.1" {
		t.Errorf("vip = %q, want %q", got.VIP, "100.64.0.1")
	}
	if got.Role != "TRANSIT" {
		t.Errorf("role = %q, want %q", got.Role, "TRANSIT")
	}
	if got.TopoVer != 42 {
		t.Errorf("topo_version = %d, want 42", got.TopoVer)
	}
	if got.Uptime < 3660 {
		t.Errorf("uptime_seconds = %f, want >= 3660", got.Uptime)
	}
	if !got.Connected {
		t.Error("connected should be true")
	}
}

func TestSystemStatus_ExtendedFields(t *testing.T) {
	// 验证新增字段：peer_count, connection_count, avg_rtt_ms, ingress_count, portmap_active
	deps := buildTestDeps()
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
	if got.PeerCount != 2 {
		t.Errorf("peer_count = %d, want 2", got.PeerCount)
	}
	if got.ConnectionCount != 2 {
		t.Errorf("connection_count = %d, want 2", got.ConnectionCount)
	}
	// avg_rtt_ms = (5 + 30) / 2 = 17
	if got.AvgRTTms != 17 {
		t.Errorf("avg_rtt_ms = %d, want 17", got.AvgRTTms)
	}
	if got.IngressCount != 3 {
		t.Errorf("ingress_count = %d, want 3", got.IngressCount)
	}
	if !got.PortmapActive {
		t.Error("portmap_active 应为 true")
	}
}

func TestSystemStatus_NilOptionalClosures(t *testing.T) {
	// 所有可选闭包为 nil 时不应 panic
	deps := Deps{
		NodeID:    "minimal",
		VIP:       "100.64.0.99",
		Role:      func() string { return "LEAF" },
		TopoVer:   func() uint64 { return 0 },
		Uptime:    func() time.Duration { return time.Second },
		Connected: func() bool { return false },
		Config:    func() any { return nil },
		// Peers, Connections, Ingresses, PortmapStatus, TopoUpdatedAt 全部 nil
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
	if got.PeerCount != 0 {
		t.Errorf("peer_count = %d, want 0", got.PeerCount)
	}
	if got.ConnectionCount != 0 {
		t.Errorf("connection_count = %d, want 0", got.ConnectionCount)
	}
	if got.AvgRTTms != 0 {
		t.Errorf("avg_rtt_ms = %d, want 0", got.AvgRTTms)
	}
	if got.IngressCount != 0 {
		t.Errorf("ingress_count = %d, want 0", got.IngressCount)
	}
	if got.PortmapActive {
		t.Error("portmap_active 应为 false")
	}
}

func TestSystemStatus_TopoUpdatedAt(t *testing.T) {
	deps := buildTestDeps()
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
	want := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	if !got.TopoUpdatedAt.Equal(want) {
		t.Errorf("topo_updated_at = %v, want %v", got.TopoUpdatedAt, want)
	}
}

// ── system.logs tests ──────────────────────────────────────────────────────

func TestSystemLogs_NilLogBuffer(t *testing.T) {
	deps := Deps{
		NodeID:    "n1",
		VIP:       "100.64.0.1",
		Role:      func() string { return "LEAF" },
		TopoVer:   func() uint64 { return 0 },
		Uptime:    func() time.Duration { return 0 },
		Connected: func() bool { return false },
		Config:    func() any { return nil },
		// LogBuffer nil
	}
	h := handleSystemLogs(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("nil LogBuffer 应返回空数组，got %d", len(got.Entries))
	}
}

func TestSystemLogs_WithLogBuffer(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	buf.Add(rpc.LogEntry{Level: "INFO", Message: "hello"})
	buf.Add(rpc.LogEntry{Level: "ERROR", Message: "oops"})

	deps := buildTestDeps()
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("len = %d, want 2", len(got.Entries))
	}
	if got.Entries[0].Message != "hello" || got.Entries[1].Message != "oops" {
		t.Errorf("entries = %+v", got.Entries)
	}
}

func TestSystemLogs_WithCountParam(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	for i := range 5 {
		buf.Add(rpc.LogEntry{Level: "INFO", Message: fmt.Sprintf("line%d", i)})
	}

	deps := buildTestDeps()
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(mustMarshal(t, logsParams{Count: 3}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("len = %d, want 3", len(got.Entries))
	}
	// 最近 3 条: line2, line3, line4
	if got.Entries[0].Message != "line2" {
		t.Errorf("entries[0] = %q, want line2", got.Entries[0].Message)
	}
}

func TestSystemLogs_DefaultCount(t *testing.T) {
	// 不传 count 参数应默认 100
	buf := rpc.NewLogBuffer(200)
	for i := range 150 {
		buf.Add(rpc.LogEntry{Level: "INFO", Message: fmt.Sprintf("l%d", i)})
	}

	deps := buildTestDeps()
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 100 {
		t.Errorf("默认应返回 100 条，got %d", len(got.Entries))
	}
}

// ── ingress tests ───────────────────────────────────────────────────────────

func TestIngressList(t *testing.T) {
	deps := buildTestDeps()
	h := handleIngressList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []IngressInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Host != "1.2.3.4" || got[0].Port != 443 || got[0].Source != "STUN" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Source != "UPNP" || got[1].Confidence != 100 {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Source != "NETIF" || got[2].NATType != "UNKNOWN" {
		t.Errorf("got[2] = %+v", got[2])
	}
}

// ── portmap tests ───────────────────────────────────────────────────────────

func TestPortmapList(t *testing.T) {
	deps := buildTestDeps()
	h := handlePortmapList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []MappingInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Protocol != "NAT-PMP" || got[0].ExternalPort != 443 || got[0].Transport != "TCP" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Protocol != "UPnP" || got[1].ExternalPort != 51820 || got[1].Transport != "UDP" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestPortmapStatus(t *testing.T) {
	deps := buildTestDeps()
	h := handlePortmapStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got PortmapStatusInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Active {
		t.Error("active should be true")
	}
	if got.ManagedCount != 2 {
		t.Errorf("managed_count = %d, want 2", got.ManagedCount)
	}
}

// ── connections tests ───────────────────────────────────────────────────────

func TestConnectionsList(t *testing.T) {
	deps := buildTestDeps()
	h := handleConnectionsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []ConnectionInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].PeerID != "peer-a" || got[0].LinkType != "direct" || got[0].RTTms != 5 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].PeerID != "peer-b" || got[1].Loss != 10 || got[1].State != "established" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// ── route tests ─────────────────────────────────────────────────────────────

func TestRouteTrace(t *testing.T) {
	deps := buildTestDeps()
	h := handleRouteTrace(deps)
	result, err := h(mustMarshal(t, routeTraceParams{Dst: "dst-node"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got routeTraceResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 2 {
		t.Fatalf("paths len = %d, want 2", len(got.Paths))
	}

	p0 := got.Paths[0]
	if p0.TotalHops != 2 {
		t.Errorf("path[0] total_hops = %d, want 2", p0.TotalHops)
	}
	if !p0.Active {
		t.Error("path[0] should be active")
	}
	if p0.Hops[0].NodeID != "relay-a" || p0.Hops[0].Host != "10.0.0.1" {
		t.Errorf("path[0].hops[0] = %+v", p0.Hops[0])
	}

	p1 := got.Paths[1]
	if p1.TotalHops != 3 {
		t.Errorf("path[1] total_hops = %d, want 3", p1.TotalHops)
	}
	if p1.Active {
		t.Error("path[1] should not be active")
	}
}

func TestRouteTrace_MissingDst(t *testing.T) {
	deps := buildTestDeps()
	h := handleRouteTrace(deps)
	_, err := h(nil)
	if err == nil {
		t.Fatal("expected error for missing dst")
	}
}

func TestRoutePeers(t *testing.T) {
	deps := buildTestDeps()
	h := handleRoutePeers(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []PeerInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].NodeID != "peer-a" || got[0].Hostname != "host-a" || got[0].VIP != "100.64.0.2" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].NodeID != "peer-b" || got[1].Hostname != "host-b" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestRouteTrace_UnknownDst(t *testing.T) {
	deps := buildTestDeps()
	h := handleRouteTrace(deps)
	result, err := h(mustMarshal(t, routeTraceParams{Dst: "no-such-node"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got routeTraceResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 0 {
		t.Errorf("未知 dst 应返回空路径，got %d", len(got.Paths))
	}
}

func TestRouteTrace_InvalidJSON(t *testing.T) {
	deps := buildTestDeps()
	h := handleRouteTrace(deps)
	_, err := h(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}

// ── nil 闭包返回空列表测试 ─────────────────────────────────────────────────────

func TestIngressList_NilReturn(t *testing.T) {
	deps := buildTestDeps()
	deps.Ingresses = func() []IngressInfo { return nil }
	h := handleIngressList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []IngressInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil 返回应转为空数组，got %d", len(got))
	}
}

func TestConnectionsList_NilReturn(t *testing.T) {
	deps := buildTestDeps()
	deps.Connections = func() []ConnectionInfo { return nil }
	h := handleConnectionsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []ConnectionInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil 返回应转为空数组，got %d", len(got))
	}
}

func TestPortmapList_NilReturn(t *testing.T) {
	deps := buildTestDeps()
	deps.PortmapMappings = func() []MappingInfo { return nil }
	h := handlePortmapList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []MappingInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil 返回应转为空数组，got %d", len(got))
	}
}

func TestRoutePeers_NilReturn(t *testing.T) {
	deps := buildTestDeps()
	deps.Peers = func() []PeerInfo { return nil }
	h := handleRoutePeers(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []PeerInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil 返回应转为空数组，got %d", len(got))
	}
}

// ── config tests ────────────────────────────────────────────────────────────

func TestConfigGet(t *testing.T) {
	deps := buildTestDeps()
	h := handleConfigGet(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["data_dir"] != "/var/lib/corelink" {
		t.Errorf("data_dir = %v", got["data_dir"])
	}
	if got["log_level"] != "info" {
		t.Errorf("log_level = %v", got["log_level"])
	}
}

// ── RegisterAll integration test ────────────────────────────────────────────

func TestRegisterAll(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()

	// Should not panic.
	RegisterAll(srv, deps)
}
