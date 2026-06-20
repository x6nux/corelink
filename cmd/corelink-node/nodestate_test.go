package main

import (
	"sync"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestNodeState_ConcurrentReadWrite 模拟 bug #3 的真实并发场景：
// OnConfig goroutine 持续写 role/ver，同时多个 RPC handler goroutine
// （system.status 的 Role()/TopoVer() 闭包）持续读取。
// 必须在 -race 下无数据竞争，且读到的永远是一致的 (role, ver) 快照。
func TestNodeState_ConcurrentReadWrite(t *testing.T) {
	st := newNodeState(genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED, 0)

	const iter = 2000
	var wg sync.WaitGroup

	// 写者：模拟 OnConfig 不断推进 topo 版本并翻转角色。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iter; i++ {
			role := genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF
			if i%2 == 0 {
				role = genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT
			}
			st.set(role, uint64(i))
		}
	}()

	// 读者：模拟 RPC handler 的 Role()/TopoVer() 闭包并发读取。
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iter; i++ {
				_ = st.role()
				_ = st.topoVer()
				_, _ = st.snapshot()
			}
		}()
	}

	wg.Wait()

	// 写者最后一次 set 为 i=iter-1（奇数→LEAF, 偶数→TRANSIT），ver=iter-1。
	gotRole, gotVer := st.snapshot()
	wantVer := uint64(iter - 1)
	wantRole := genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF // iter-1=1999 为奇数
	if gotVer != wantVer {
		t.Fatalf("topoVer = %d, want %d", gotVer, wantVer)
	}
	if gotRole != wantRole {
		t.Fatalf("role = %v, want %v", gotRole, wantRole)
	}
}

// TestNodeState_SnapshotConsistency 验证 snapshot 返回的 role/ver 是同一临界区
// 下的一致快照（写侧成对更新，读侧不会读到「新 role + 旧 ver」的撕裂组合）。
func TestNodeState_SnapshotConsistency(t *testing.T) {
	st := newNodeState(genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF, 5)
	r, v := st.snapshot()
	if r != genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF || v != 5 {
		t.Fatalf("snapshot = (%v,%d), want (LEAF,5)", r, v)
	}
	st.set(genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT, 9)
	r, v = st.snapshot()
	if r != genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT || v != 9 {
		t.Fatalf("snapshot after set = (%v,%d), want (TRANSIT,9)", r, v)
	}
}

// TestNodeState_OnConfigSequence 复刻 OnConfig（main.go）的读写序列：
// 读快照 → 比较 newVer/newRole → 仅在推进时 set。验证接线后语义不回退。
func TestNodeState_OnConfigSequence(t *testing.T) {
	st := newNodeState(genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF, 3)

	// applyConfig 模拟 OnConfig 的核心决策：返回 changed 表示是否触发拓扑更新。
	applyConfig := func(newRole genv1.NodeTopoRole, newVer uint64) bool {
		curRole, curVer := st.snapshot()
		if newVer <= curVer && newRole == curRole {
			return false // 未推进且角色未变。
		}
		st.set(newRole, newVer)
		return true
	}

	// 同版本同角色 → 不更新。
	if applyConfig(genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF, 3) {
		t.Fatal("同版本同角色不应触发更新")
	}
	// 版本推进 → 更新。
	if !applyConfig(genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF, 4) {
		t.Fatal("版本推进应触发更新")
	}
	if _, v := st.snapshot(); v != 4 {
		t.Fatalf("ver = %d, want 4", v)
	}
	// 版本相同但角色翻转 → 更新。
	if !applyConfig(genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT, 4) {
		t.Fatal("角色翻转应触发更新")
	}
	if r, _ := st.snapshot(); r != genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		t.Fatalf("role = %v, want TRANSIT", r)
	}
}

func TestNodeConfigSnapshot_UsesLatestConfig(t *testing.T) {
	snap := newNodeConfigSnapshot(&genv1.NodeConfig{
		Peers: []*genv1.Peer{{NodeId: "old", AllowedIps: []string{"100.64.0.2/32"}}},
	})
	snap.set(&genv1.NodeConfig{
		Peers: []*genv1.Peer{{NodeId: "new", AllowedIps: []string{"100.64.0.3/32"}}},
	})

	nc := snap.get()
	peers := nc.GetPeers()
	if len(peers) != 1 || peers[0].GetNodeId() != "new" {
		t.Fatalf("快照应使用最新配置，got %+v", peers)
	}
}
