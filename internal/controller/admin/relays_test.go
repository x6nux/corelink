package admin

import (
	"encoding/json"
	"testing"
)

// 测试 relayDTO / setTopologyRequest 结构体。

func TestRelayDTOJSONTags(t *testing.T) {
	// 验证 relayDTO JSON 序列化键名正确。
	dto := relayDTO{
		NodeID: "r1", TunnelEndpoint: "r1:443", UDPEndpoint: "r1:3478",
		Protocols: "TLS_RAW", Priority: 5, Online: true,
		Neighbors: []string{"r2"},
	}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	expected := []string{"node_id", "tunnel_endpoint", "udp_endpoint", "protocols", "priority", "online", "neighbors"}
	for _, key := range expected {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON 缺少键 %q", key)
		}
	}
}

func TestSetTopologyRequestDeserialization(t *testing.T) {
	// 验证 setTopologyRequest JSON 反序列化。
	raw := `{"relay_id":"r1","neighbors":["r2","r3"]}`
	var req setTopologyRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.RelayID != "r1" {
		t.Errorf("RelayID = %q", req.RelayID)
	}
	if len(req.Neighbors) != 2 || req.Neighbors[0] != "r2" {
		t.Errorf("Neighbors = %v", req.Neighbors)
	}
}

func TestRelayDTONeighborsNeverNull(t *testing.T) {
	// 空 neighbors 列表序列化时应为 [] 而非 null。
	dto := relayDTO{NodeID: "r1", Neighbors: []string{}}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	neighbors, ok := m["neighbors"].([]any)
	if !ok {
		t.Fatalf("neighbors 不是数组类型: %T", m["neighbors"])
	}
	if len(neighbors) != 0 {
		t.Errorf("neighbors 应为空数组, 实际 %v", neighbors)
	}
}
