package admin

import (
	"testing"

	"github.com/x6nux/corelink/internal/controller/store"
)

// 测试 buildAdminSnapshot 把 store 数据正确组装成 acl.Snapshot。

func TestBuildAdminSnapshot_EmptyInputs(t *testing.T) {
	// 全空输入不 panic，返回空快照。
	snap := buildAdminSnapshot(nil, nil, nil)
	if snap.Policy != nil {
		t.Error("空输入时 Policy 应为 nil")
	}
	if len(snap.Nodes) != 0 {
		t.Errorf("空输入时 Nodes 应为空, 实际 %d", len(snap.Nodes))
	}
	if len(snap.Relays) != 0 {
		t.Errorf("空输入时 Relays 应为空, 实际 %d", len(snap.Relays))
	}
}

func TestBuildAdminSnapshot_NodesMapping(t *testing.T) {
	// 验证 store.Node 字段正确映射到 acl.NodeView。
	nodes := []store.Node{
		{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
	}
	snap := buildAdminSnapshot(nodes, nil, nil)
	if len(snap.Nodes) != 2 {
		t.Fatalf("Nodes 数量 = %d, 期望 2", len(snap.Nodes))
	}
	nv := snap.Nodes[0]
	if nv.ID != "n1" || nv.User != "alice" || nv.WGPubKey != "pk1" || nv.VirtualIP != "100.64.0.1/32" {
		t.Errorf("NodeView 映射错误: %+v", nv)
	}
}

func TestBuildAdminSnapshot_RelaysMapping(t *testing.T) {
	// 验证 RelayInfo 正确映射到 RelayView。
	relays := []store.RelayInfo{
		{NodeID: "r1", Priority: 5},
		{NodeID: "r2", Priority: 10},
	}
	snap := buildAdminSnapshot(nil, relays, nil)
	if len(snap.Relays) != 2 {
		t.Fatalf("Relays 数量 = %d, 期望 2", len(snap.Relays))
	}
	if snap.Relays[0].ID != "r1" || snap.Relays[0].Priority != 5 {
		t.Errorf("RelayView[0] 映射错误: %+v", snap.Relays[0])
	}
}
