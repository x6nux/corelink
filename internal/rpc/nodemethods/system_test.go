package nodemethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleSystemStatus_UptimeRounding 验证 uptime 四舍五入到两位小数
func TestHandleSystemStatus_UptimeRounding(t *testing.T) {
	deps := buildTestDeps()
	// 设置 123.456 秒的 uptime
	deps.Uptime = func() time.Duration {
		return 123456 * time.Millisecond
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
	// math.Round(123.456*100)/100 = 123.46
	if got.Uptime != 123.46 {
		t.Errorf("uptime = %f, want 123.46", got.Uptime)
	}
}

// TestHandleSystemStatus_ZeroPeers 验证零 peer 时 avg_rtt_ms 为 0
func TestHandleSystemStatus_ZeroPeers(t *testing.T) {
	deps := buildTestDeps()
	deps.Peers = func() []PeerInfo { return []PeerInfo{} }
	deps.Connections = func() []ConnectionInfo { return []ConnectionInfo{} }
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
	if got.AvgRTTms != 0 {
		t.Errorf("avg_rtt_ms = %d, want 0", got.AvgRTTms)
	}
}

func TestHandleSystemStatus_AvgRTTIgnoresInvalidMetrics(t *testing.T) {
	deps := buildTestDeps()
	deps.Connections = func() []ConnectionInfo {
		return []ConnectionInfo{
			{PeerID: "peer-a", RTTms: 20, RTTValid: true, State: "已连接"},
			{PeerID: "peer-b", RTTms: 0, RTTValid: false, State: "未连接"},
		}
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
	if got.ConnectionCount != 2 {
		t.Fatalf("connection_count = %d, want 2", got.ConnectionCount)
	}
	if got.AvgRTTms != 20 {
		t.Errorf("avg_rtt_ms = %d, want 20", got.AvgRTTms)
	}
}

// TestHandleSystemLogs_ZeroCount 验证 count=0 使用默认值
func TestHandleSystemLogs_ZeroCount(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	buf.Add(rpc.LogEntry{Level: "INFO", Message: "test"})

	deps := buildTestDeps()
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	// count=0 应被忽略，使用默认 100
	result, err := h(mustMarshal(t, logsParams{Count: 0}))
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

// TestHandleSystemLogs_InvalidJSON 验证无效 JSON 参数使用默认 count
func TestHandleSystemLogs_InvalidJSON(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	buf.Add(rpc.LogEntry{Level: "WARN", Message: "warning"})

	deps := buildTestDeps()
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 无效 JSON 时应忽略，使用默认 count
	if len(got.Entries) != 1 {
		t.Errorf("应返回 1 条，got %d", len(got.Entries))
	}
}

// TestRegisterSystemMethods_NoPanic 验证注册不 panic
func TestRegisterSystemMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerSystemMethods(srv, deps)
}
