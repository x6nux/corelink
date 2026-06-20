package acl

import (
	"sort"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 测试辅助 ─────────────────────────────────────────────────────────────────

func mustParsePolicy(t *testing.T, json string) *Policy {
	t.Helper()
	p, err := ParsePolicy([]byte(json))
	if err != nil {
		t.Fatalf("mustParsePolicy: %v", err)
	}
	return p
}

func peerIDs(cfg *genv1.NodeConfig) []string {
	ids := make([]string, 0, len(cfg.Peers))
	for _, p := range cfg.Peers {
		ids = append(ids, p.NodeId)
	}
	sort.Strings(ids)
	return ids
}

func routeDests(cfg *genv1.NodeConfig) []string {
	dests := make([]string, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		dests = append(dests, r.DestCidr)
	}
	sort.Strings(dests)
	return dests
}

func routeVia(cfg *genv1.NodeConfig, dest string) string {
	for _, r := range cfg.Routes {
		if r.DestCidr == dest {
			return r.ViaRelayId
		}
	}
	return ""
}

func relayIDs(cfg *genv1.NodeConfig) []string {
	ids := make([]string, 0, len(cfg.Relays))
	for _, r := range cfg.Relays {
		ids = append(ids, r.RelayId)
	}
	return ids
}

func relayPriorities(cfg *genv1.NodeConfig) []uint32 {
	pris := make([]uint32, 0, len(cfg.Relays))
	for _, r := range cfg.Relays {
		pris = append(pris, r.Priority)
	}
	return pris
}

func assertSliceEq(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %q, want %q", label, i, got[i], want[i])
		}
	}
}

// ─── 场景 1：空策略 = 无 peer ──────────────────────────────────────────────────

func TestGenerate_EmptyPolicy_NoPeers(t *testing.T) {
	snap := Snapshot{
		Policy: nil,
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
		},
	}
	result := Generate(snap)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	// 空策略 → 默认全 mesh 互通
	for id, cfg := range result {
		if len(cfg.Peers) != 1 {
			t.Errorf("node %s: expected 1 peer (full mesh), got %d", id, len(cfg.Peers))
		}
	}
}

func TestGenerate_EmptyACLs_FullMesh(t *testing.T) {
	snap := Snapshot{
		Policy: mustParsePolicy(t, `{}`),
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		},
	}
	result := Generate(snap)
	if len(result["n1"].Peers) != 0 {
		t.Error("expected no peers with empty ACLs")
	}
}

// ─── 场景 2：group 内互通 ──────────────────────────────────────────────────────

func TestGenerate_GroupIntraComm(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"groups": { "group:dev": ["alice", "bob"] },
		"acls": [
			{ "action": "accept", "src": ["group:dev"], "dst": ["group:dev"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
			{ID: "n3", User: "carol", WGPubKey: "pk3", VirtualIP: "100.64.0.3/32"},
		},
	}
	result := Generate(snap)

	// n1 和 n2 互相看到对方，carol (n3) 不在 group:dev
	assertSliceEq(t, "n1 peers", peerIDs(result["n1"]), []string{"n2"})
	assertSliceEq(t, "n2 peers", peerIDs(result["n2"]), []string{"n1"})
	if len(result["n3"].Peers) != 0 {
		t.Errorf("n3 (carol) should have 0 peers, got %d", len(result["n3"].Peers))
	}
}

// ─── 场景 3：单向 ACL 经双向可见性后两端都看见对方 ───────────────────────────────

func TestGenerate_OneWayACL_BothSeeEachOther(t *testing.T) {
	// group:dev → tag:server 单向
	policy := mustParsePolicy(t, `{
		"groups":    { "group:dev": ["alice"] },
		"tagOwners": { "tag:server": ["carol"] },
		"acls": [
			{ "action": "accept", "src": ["group:dev"], "dst": ["tag:server"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n-alice", User: "alice", WGPubKey: "pk-alice", VirtualIP: "100.64.0.1/32"},
			// tag:server 节点通过 Tags 字段标注
			{ID: "n-srv", User: "carol", Tags: []string{"tag:server"}, WGPubKey: "pk-srv", VirtualIP: "100.64.0.2/32"},
		},
	}
	result := Generate(snap)

	// alice 可以访问 srv（ACL 允许）
	assertSliceEq(t, "alice peers", peerIDs(result["n-alice"]), []string{"n-srv"})
	// 双向可见性：srv 也能看到 alice
	assertSliceEq(t, "srv peers", peerIDs(result["n-srv"]), []string{"n-alice"})
}

// ─── 场景 4：tag 内互通（通过 *:* 对全节点开放） ────────────────────────────────

func TestGenerate_WildcardACL_AllSeeAll(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"acls": [
			{ "action": "accept", "src": ["*"], "dst": ["*:*"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
			{ID: "n3", User: "carol", WGPubKey: "pk3", VirtualIP: "100.64.0.3/32"},
		},
	}
	result := Generate(snap)

	// 每个节点应看到其他所有节点
	for id, cfg := range result {
		if len(cfg.Peers) != 2 {
			t.Errorf("node %s: expected 2 peers with wildcard ACL, got %d", id, len(cfg.Peers))
		}
	}
}

// ─── 场景 5：端口规则解析（AllowedIPs 保持 /32，端口信息记录于注释） ────────────

func TestGenerate_PortRules_AllowedIPsIsSlash32(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"groups":    { "group:dev": ["alice"] },
		"tagOwners": { "tag:server": ["carol"] },
		"acls": [
			{ "action": "accept", "src": ["group:dev"], "dst": ["tag:server:22,443"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n-alice", User: "alice", WGPubKey: "pk-alice", VirtualIP: "100.64.0.1/32"},
			{ID: "n-srv", Tags: []string{"tag:server"}, WGPubKey: "pk-srv", VirtualIP: "100.64.0.2/32"},
		},
	}
	result := Generate(snap)

	// AllowedIPs 应为对端 /32（S2 粒度到 IP）
	aliceCfg := result["n-alice"]
	if len(aliceCfg.Peers) != 1 {
		t.Fatalf("alice: expected 1 peer, got %d", len(aliceCfg.Peers))
	}
	peer := aliceCfg.Peers[0]
	if len(peer.AllowedIps) != 1 || peer.AllowedIps[0] != "100.64.0.2/32" {
		t.Errorf("alice peer AllowedIps: got %v, want [100.64.0.2/32]", peer.AllowedIps)
	}
}

// ─── 场景 6：relay 按优先级排序 ───────────────────────────────────────────────

func TestGenerate_RelaysSortedByPriority(t *testing.T) {
	snap := Snapshot{
		Policy: mustParsePolicy(t, `{}`),
		Nodes: []NodeView{
			{ID: "n1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		},
		Relays: []RelayView{
			{ID: "relay-c", Priority: 30},
			{ID: "relay-a", Priority: 10},
			{ID: "relay-b", Priority: 20},
		},
	}
	result := Generate(snap)
	n1 := result["n1"]
	if len(n1.Relays) != 3 {
		t.Fatalf("expected 3 relays, got %d", len(n1.Relays))
	}
	// 应按 Priority 升序（小=优先）
	pris := relayPriorities(n1)
	if pris[0] != 10 || pris[1] != 20 || pris[2] != 30 {
		t.Errorf("relay priorities should be [10 20 30], got %v", pris)
	}
	ids := relayIDs(n1)
	if ids[0] != "relay-a" || ids[1] != "relay-b" || ids[2] != "relay-c" {
		t.Errorf("relay IDs should be [relay-a relay-b relay-c], got %v", ids)
	}
}

// ─── 场景 7：相同优先级时按 ID 字典序（确定性） ────────────────────────────────

func TestGenerate_RelaysSamePriority_DeterministicOrder(t *testing.T) {
	snap := Snapshot{
		Policy: mustParsePolicy(t, `{}`),
		Nodes: []NodeView{
			{ID: "n1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		},
		Relays: []RelayView{
			{ID: "zzz", Priority: 1},
			{ID: "aaa", Priority: 1},
			{ID: "mmm", Priority: 1},
		},
	}
	result := Generate(snap)
	ids := relayIDs(result["n1"])
	if ids[0] != "aaa" || ids[1] != "mmm" || ids[2] != "zzz" {
		t.Errorf("same priority: expected alphabetical order [aaa mmm zzz], got %v", ids)
	}
}

// ─── 场景 8：路由表 ViaRelayId 来自 NodeRelay ────────────────────────────────

func TestGenerate_Routes_ViaRelay(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"acls": [
			{ "action": "accept", "src": ["*"], "dst": ["*:*"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
		},
		NodeRelay: map[string]string{
			"n1": "relay-1",
			"n2": "relay-2",
		},
	}
	result := Generate(snap)

	// n1 的路由：100.64.0.2/32 经 relay-2
	n1 := result["n1"]
	dests := routeDests(n1)
	assertSliceEq(t, "n1 route dests", dests, []string{"100.64.0.2/32"})
	via := routeVia(n1, "100.64.0.2/32")
	if via != "relay-2" {
		t.Errorf("n1 route via: got %q, want %q", via, "relay-2")
	}

	// n2 的路由：100.64.0.1/32 经 relay-1
	n2 := result["n2"]
	via2 := routeVia(n2, "100.64.0.1/32")
	if via2 != "relay-1" {
		t.Errorf("n2 route via: got %q, want %q", via2, "relay-1")
	}
}

// ─── 场景 9：无 NodeRelay 时 ViaRelayId 为空 ─────────────────────────────────

func TestGenerate_Routes_NoRelay(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"acls": [
			{ "action": "accept", "src": ["*"], "dst": ["*:*"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
		},
	}
	result := Generate(snap)

	n1 := result["n1"]
	via := routeVia(n1, "100.64.0.2/32")
	if via != "" {
		t.Errorf("expected empty ViaRelayId when no NodeRelay, got %q", via)
	}
}

// ─── 场景 10：VirtualIP 字段被 strip 为纯 IP ─────────────────────────────────

func TestGenerate_VirtualIP_Stripped(t *testing.T) {
	snap := Snapshot{
		Policy: mustParsePolicy(t, `{}`),
		Nodes: []NodeView{
			{ID: "n1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		},
	}
	result := Generate(snap)
	if result["n1"].VirtualIp != "100.64.0.1" {
		t.Errorf("VirtualIp should strip /32, got %q", result["n1"].VirtualIp)
	}
}

// ─── 场景 11：多 ACL 规则合并（group:ops → *:*） ──────────────────────────────

func TestGenerate_MultiACL_OpsSeesAll(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"groups": {
			"group:dev": ["alice", "bob"],
			"group:ops": ["carol"]
		},
		"acls": [
			{ "action": "accept", "src": ["group:dev"], "dst": ["group:dev"] },
			{ "action": "accept", "src": ["group:ops"], "dst": ["*:*"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n-alice", User: "alice", WGPubKey: "pk-a", VirtualIP: "100.64.0.1/32"},
			{ID: "n-bob", User: "bob", WGPubKey: "pk-b", VirtualIP: "100.64.0.2/32"},
			{ID: "n-carol", User: "carol", WGPubKey: "pk-c", VirtualIP: "100.64.0.3/32"},
		},
	}
	result := Generate(snap)

	// carol (ops) 看到所有其他节点
	carolPeers := peerIDs(result["n-carol"])
	assertSliceEq(t, "carol peers", carolPeers, []string{"n-alice", "n-bob"})

	// alice (dev) 看到 bob + carol（carol→alice 双向）
	alicePeers := peerIDs(result["n-alice"])
	assertSliceEq(t, "alice peers", alicePeers, []string{"n-bob", "n-carol"})

	// bob (dev) 看到 alice + carol（carol→bob 双向）
	bobPeers := peerIDs(result["n-bob"])
	assertSliceEq(t, "bob peers", bobPeers, []string{"n-alice", "n-carol"})
}

// ─── 场景 12：节点集为空时返回空 map ──────────────────────────────────────────

func TestGenerate_EmptyNodes(t *testing.T) {
	snap := Snapshot{
		Policy: mustParsePolicy(t, `{
			"acls": [{ "action": "accept", "src": ["*"], "dst": ["*:*"] }]
		}`),
		Nodes: nil,
	}
	result := Generate(snap)
	if len(result) != 0 {
		t.Errorf("expected empty result for no nodes, got %d", len(result))
	}
}

// ─── 场景 13：用户名（非 group）精确匹配 ────────────────────────────────────────

func TestGenerate_UserExact_OnlyMatchingUser(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"acls": [
			{ "action": "accept", "src": ["alice"], "dst": ["bob"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n-alice", User: "alice", WGPubKey: "pk-a", VirtualIP: "100.64.0.1/32"},
			{ID: "n-bob", User: "bob", WGPubKey: "pk-b", VirtualIP: "100.64.0.2/32"},
			{ID: "n-carol", User: "carol", WGPubKey: "pk-c", VirtualIP: "100.64.0.3/32"},
		},
	}
	result := Generate(snap)

	// alice 看到 bob（ACL 允许）
	assertSliceEq(t, "alice peers", peerIDs(result["n-alice"]), []string{"n-bob"})
	// bob 看到 alice（双向可见性）
	assertSliceEq(t, "bob peers", peerIDs(result["n-bob"]), []string{"n-alice"})
	// carol 无 peer
	if len(result["n-carol"].Peers) != 0 {
		t.Errorf("carol should have 0 peers, got %d", len(result["n-carol"].Peers))
	}
}

// ─── 场景 14：节点无 tag 时 tag 规则不匹配 ────────────────────────────────────

func TestGenerate_TagRule_NoMatch_NoTags(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"tagOwners": { "tag:server": ["alice"] },
		"acls": [
			{ "action": "accept", "src": ["alice"], "dst": ["tag:server"] }
		]
	}`)
	snap := Snapshot{
		Policy: policy,
		Nodes: []NodeView{
			{ID: "n-alice", User: "alice", WGPubKey: "pk-a", VirtualIP: "100.64.0.1/32"},
			// 没有节点带 tag:server
			{ID: "n-srv", User: "carol", WGPubKey: "pk-c", VirtualIP: "100.64.0.2/32"},
		},
	}
	result := Generate(snap)

	// alice 没有看到任何 peer（没有节点有 tag:server）
	if len(result["n-alice"].Peers) != 0 {
		t.Errorf("alice should have 0 peers when no node has tag:server, got %d", len(result["n-alice"].Peers))
	}
}

// ─── 场景 15：Generate 输出确定性（多次调用同输入结果相同） ─────────────────────

func TestGenerate_Deterministic(t *testing.T) {
	policy := mustParsePolicy(t, `{
		"groups": { "group:all": ["alice", "bob", "carol"] },
		"acls": [
			{ "action": "accept", "src": ["group:all"], "dst": ["group:all"] }
		]
	}`)
	nodes := []NodeView{
		{ID: "n3", User: "carol", WGPubKey: "pk3", VirtualIP: "100.64.0.3/32"},
		{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
		{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
	}
	snap := Snapshot{Policy: policy, Nodes: nodes}

	first := Generate(snap)
	second := Generate(snap)

	for id := range first {
		a := peerIDs(first[id])
		b := peerIDs(second[id])
		assertSliceEq(t, "deterministic peers for "+id, a, b)
	}
}

// ─── 场景：PublishedPrefixes 注入 AllowedIPs ─────────────────────────────────

func TestGenerate_PublishedPrefixes_InAllowedIPs(t *testing.T) {
	snap := Snapshot{
		Policy: nil, // 全 mesh
		Nodes: []NodeView{
			{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"},
			{ID: "n2", User: "bob", WGPubKey: "pk2", VirtualIP: "100.64.0.2/32"},
		},
		PublishedPrefixes: map[string][]string{
			"n1": {"10.0.0.0/16", "100.64.2.0/24"},
		},
	}
	result := Generate(snap)

	// n2 的 peer n1 应该包含 n1 的 VIP 和 published prefixes
	n2cfg := result["n2"]
	if len(n2cfg.Peers) != 1 {
		t.Fatalf("n2 peers = %d, want 1", len(n2cfg.Peers))
	}
	peer := n2cfg.Peers[0]
	if peer.NodeId != "n1" {
		t.Fatalf("peer node_id = %q, want n1", peer.NodeId)
	}
	want := []string{"10.0.0.0/16", "100.64.0.1/32", "100.64.2.0/24"}
	sort.Strings(peer.AllowedIps)
	assertSliceEq(t, "n2 peer n1 AllowedIPs", peer.AllowedIps, want)

	// n1 的 peer n2：AllowedIPs 应为 VIP/32（n2 无 published prefixes）
	n1cfg := result["n1"]
	if len(n1cfg.Peers) != 1 || n1cfg.Peers[0].AllowedIps[0] != "100.64.0.2/32" {
		t.Fatalf("n1 peer n2 AllowedIPs = %v, want [100.64.0.2/32]", n1cfg.Peers[0].AllowedIps)
	}
}
