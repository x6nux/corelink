package nodemethods

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDepsZeroValue 验证 Deps 零值各字段为空
func TestDepsZeroValue(t *testing.T) {
	var d Deps
	if d.NodeID != "" {
		t.Errorf("零值 NodeID 应为空，got %q", d.NodeID)
	}
	if d.VIP != "" {
		t.Errorf("零值 VIP 应为空，got %q", d.VIP)
	}
	if d.Role != nil {
		t.Error("零值 Role 应为 nil")
	}
	if d.LogBuffer != nil {
		t.Error("零值 LogBuffer 应为 nil")
	}
}

// TestIngressInfoJSON 验证 IngressInfo JSON 序列化字段名
func TestIngressInfoJSON(t *testing.T) {
	info := IngressInfo{
		Host:       "1.2.3.4",
		Port:       443,
		Source:     "STUN",
		Confidence: 90,
		NATType:    "FULL_CONE",
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 验证 JSON 字段名
	if _, ok := m["host"]; !ok {
		t.Error("JSON 应包含 host 字段")
	}
	if _, ok := m["nat_type"]; !ok {
		t.Error("JSON 应包含 nat_type 字段")
	}
}

// TestConnectionInfoJSON 验证 ConnectionInfo JSON 字段映射
func TestConnectionInfoJSON(t *testing.T) {
	ci := ConnectionInfo{
		PeerID:     "peer-1",
		VIP:        "100.64.0.2",
		PeerIP:     "203.0.113.10:51820",
		InternalIP: "192.168.1.10:51820",
		LinkType:   "node",
		RTTms:      25,
		RTTValid:   true,
		Loss:       100,
		LossValid:  true,
		State:      "established",
	}
	b, err := json.Marshal(ci)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["peer_id"] != "peer-1" {
		t.Errorf("peer_id = %v", m["peer_id"])
	}
	if m["vip"] != "100.64.0.2" {
		t.Errorf("vip = %v", m["vip"])
	}
	if m["peer_ip"] != "203.0.113.10:51820" {
		t.Errorf("peer_ip = %v", m["peer_ip"])
	}
	if m["internal_ip"] != "192.168.1.10:51820" {
		t.Errorf("internal_ip = %v", m["internal_ip"])
	}
	if m["link_type"] != "node" {
		t.Errorf("link_type = %v", m["link_type"])
	}
	if m["rtt_valid"] != true {
		t.Errorf("rtt_valid = %v", m["rtt_valid"])
	}
	if m["loss_valid"] != true {
		t.Errorf("loss_valid = %v", m["loss_valid"])
	}
	// loss_permille 是 JSON 字段名
	if _, ok := m["loss_permille"]; !ok {
		t.Error("JSON 应包含 loss_permille 字段")
	}
}

// TestRouteInfoJSON 验证 RouteInfo 和 HopInfo 序列化
func TestRouteInfoJSON(t *testing.T) {
	ri := RouteInfo{
		Hops: []HopInfo{
			{NodeID: "n1", IngressID: "i1", Host: "1.1.1.1", Port: 443},
		},
		TotalHops: 1,
		Active:    true,
	}
	b, err := json.Marshal(ri)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RouteInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Active {
		t.Error("active 应为 true")
	}
	if len(got.Hops) != 1 || got.Hops[0].NodeID != "n1" {
		t.Errorf("hops 不匹配: %+v", got.Hops)
	}
}

// TestPeerInfoJSON 验证 PeerInfo 序列化
func TestPeerInfoJSON(t *testing.T) {
	pi := PeerInfo{NodeID: "p1", Hostname: "host-1", VIP: "100.64.0.5"}
	b, _ := json.Marshal(pi)
	var got PeerInfo
	_ = json.Unmarshal(b, &got)
	if got.NodeID != "p1" || got.Hostname != "host-1" || got.VIP != "100.64.0.5" {
		t.Errorf("PeerInfo roundtrip 失败: %+v", got)
	}
}

// TestBuildTestDeps_Sanity 验证 buildTestDeps 构造的 Deps 基本一致
func TestBuildTestDeps_Sanity(t *testing.T) {
	deps := buildTestDeps()
	if deps.NodeID != "node-test-1" {
		t.Errorf("NodeID = %q", deps.NodeID)
	}
	if deps.Role() != "TRANSIT" {
		t.Errorf("Role() = %q", deps.Role())
	}
	if deps.TopoVer() != 42 {
		t.Errorf("TopoVer() = %d", deps.TopoVer())
	}
	want := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	if !deps.TopoUpdatedAt().Equal(want) {
		t.Errorf("TopoUpdatedAt() = %v", deps.TopoUpdatedAt())
	}
}
