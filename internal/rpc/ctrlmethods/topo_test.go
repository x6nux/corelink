package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestHandleTopoGraph_OnlineStatus 验证 topo.graph 中节点在线状态映射
func TestHandleTopoGraph_OnlineStatus(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "a", Hostname: "ha", VirtualIP: "10.0.0.1", Role: "node"},
			{ID: "b", Hostname: "hb", VirtualIP: "10.0.0.2", Role: "node"},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"b": true}}
	deps := buildTestDeps(ms, nil, on, nil, nil, nil)
	h := handleTopoGraph(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got topoGraphResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	nodeMap := make(map[string]topoGraphNode)
	for _, n := range got.Nodes {
		nodeMap[n.ID] = n
	}
	if nodeMap["a"].Online {
		t.Error("节点 a 应离线")
	}
	if !nodeMap["b"].Online {
		t.Error("节点 b 应在线")
	}
}

// TestHandleTopoStatus_FieldMapping 验证 topo.status 返回字段与 TopoStatus 一致
func TestHandleTopoStatus_FieldMapping(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	topo := &mockTopo{
		status: TopoStatus{
			Version:       100,
			TransitCount:  10,
			LeafCount:     50,
			LastRecompute: now,
		},
	}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, topo, nil)
	h := handleTopoStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got topoStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 100 {
		t.Errorf("version = %d, want 100", got.Version)
	}
	if got.LeafCount != 50 {
		t.Errorf("leaf_count = %d, want 50", got.LeafCount)
	}
}

// TestHandleTopoTraceroute_InvalidJSON 验证无效 JSON 参数返回错误
func TestHandleTopoTraceroute_InvalidJSON(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, &mockTopo{}, nil)
	h := handleTopoTraceroute(deps)
	_, err := h(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}

// TestHandleTopoTraceroute_MatchesOnlyDst 验证 traceroute 只返回目标节点匹配的路径
func TestHandleTopoTraceroute_MatchesOnlyDst(t *testing.T) {
	topo := &mockTopo{
		assignments: map[string]*genv1.TopologyAssignment{
			"src": {
				Version: 1,
				BaselineRoutes: []*genv1.Route2{
					{DstNode: "dst-a", Hops: []*genv1.Hop{{NodeId: "dst-a", IngressId: "i1"}}},
					{DstNode: "dst-b", Hops: []*genv1.Hop{{NodeId: "dst-b", IngressId: "i2"}}},
					{DstNode: "dst-a", Hops: []*genv1.Hop{{NodeId: "node", IngressId: "i3"}, {NodeId: "dst-a", IngressId: "i1"}}},
				},
			},
		},
	}
	ing := &mockIngress{sets: map[string]*genv1.IngressSet{}}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, topo, ing)
	h := handleTopoTraceroute(deps)
	result, err := h(mustMarshal(t, tracerouteParams{Src: "src", Dst: "dst-a"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got tracerouteResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 应只返回 dst-a 的 2 条路径，不含 dst-b
	if len(got.Paths) != 2 {
		t.Fatalf("paths len = %d, want 2", len(got.Paths))
	}
	// 第一条 active，第二条不 active
	if !got.Paths[0].Active {
		t.Error("paths[0] 应为 active")
	}
	if got.Paths[1].Active {
		t.Error("paths[1] 不应为 active")
	}
}
