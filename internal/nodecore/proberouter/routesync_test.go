package proberouter

import (
	"net/netip"
	"testing"

	"github.com/x6nux/corelink/internal/transport"
)

var (
	vipA = netip.MustParseAddr("100.64.0.1")
	vipB = netip.MustParseAddr("100.64.0.2")
	vipC = netip.MustParseAddr("100.64.0.3")
	vipD = netip.MustParseAddr("100.64.0.4")
)

func newTestRouter(selfVIP netip.Addr) *ProbeRouter {
	return &ProbeRouter{
		cfg:         Config{SelfVIP: selfVIP},
		vipToNodeID: make(map[netip.Addr]string),
		best:        make(map[netip.Addr]BestRoute),
		peerRoutes:  make(map[netip.Addr][]transport.RouteSyncEntry),
		triggerCh:   make(chan struct{}, 1),
	}
}

// ── ReceiveRouteSync 测试 ──

// TestReceiveRouteSync_FirstEntry 首次收到 peer 路由
func TestReceiveRouteSync_FirstEntry(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipA, RTTMs: 10})

	pr.peerRoutesMu.Lock()
	entries := pr.peerRoutes[vipB]
	pr.peerRoutesMu.Unlock()

	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(entries))
	}
	if entries[0].DstVIP != vipC || entries[0].NextHopVIP != vipA {
		t.Errorf("entry: got %+v", entries[0])
	}
}

// TestReceiveRouteSync_UpsertSameDst 同 dst 更新覆盖
func TestReceiveRouteSync_UpsertSameDst(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipA, RTTMs: 10})
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipD, RTTMs: 5})

	pr.peerRoutesMu.Lock()
	entries := pr.peerRoutes[vipB]
	pr.peerRoutesMu.Unlock()

	if len(entries) != 1 {
		t.Fatalf("upsert 后 entries: got %d, want 1", len(entries))
	}
	if entries[0].NextHopVIP != vipD || entries[0].RTTMs != 5 {
		t.Errorf("upsert 未生效: got %+v", entries[0])
	}
}

// TestReceiveRouteSync_MultipleDsts 不同 dst 累积
func TestReceiveRouteSync_MultipleDsts(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipA, RTTMs: 10})
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 20})

	pr.peerRoutesMu.Lock()
	entries := pr.peerRoutes[vipB]
	pr.peerRoutesMu.Unlock()

	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}
}

// TestReceiveRouteSync_MultiplePeers 不同 peer 独立存储
func TestReceiveRouteSync_MultiplePeers(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipC, RTTMs: 10})
	pr.ReceiveRouteSync(vipC, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipB, RTTMs: 15})

	pr.peerRoutesMu.Lock()
	bEntries := pr.peerRoutes[vipB]
	cEntries := pr.peerRoutes[vipC]
	pr.peerRoutesMu.Unlock()

	if len(bEntries) != 1 || len(cEntries) != 1 {
		t.Fatalf("B entries=%d, C entries=%d, want 1,1", len(bEntries), len(cEntries))
	}
}

// ── checkLoopAndYield 测试 ──

// TestCheckLoop_NoConflict 无冲突：B 到 D 不经过 A
func TestCheckLoop_NoConflict(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipC, RTTMs: 10})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 20, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 20, route: enumRoute{label: "via B"}},
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 30, route: enumRoute{label: "via C"}},
	}

	_, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if yielded {
		t.Error("不应让步：B 到 D 走 C，不经过 A")
	}
}

// TestCheckLoop_LoopDetected_IYield A→D via B(RTT=20)，B→D via A(RTT=10) → A 让步（A更慢）
func TestCheckLoop_LoopDetected_IYield(t *testing.T) {
	pr := newTestRouter(vipA)
	// B 告诉我：B 到 D 走 A（RTT=10）
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 10})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 20, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 20, route: enumRoute{label: "via B"}},
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 30, route: enumRoute{label: "via C"}},
	}

	alt, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if !yielded {
		t.Fatal("应让步：A(RTT=20) > B(RTT=10)")
	}
	if alt.NextHopVIP != vipC {
		t.Errorf("让步后应选 via C: got %v", alt.NextHopVIP)
	}
	if alt.NextHopID != "nodeC" {
		t.Errorf("NextHopID: got %q, want nodeC", alt.NextHopID)
	}
}

// TestCheckLoop_LoopDetected_PeerYields A→D via B(RTT=10)，B→D via A(RTT=20) → B 让步，A 不动
func TestCheckLoop_LoopDetected_PeerYields(t *testing.T) {
	pr := newTestRouter(vipA)
	// B 到 D 走 A（RTT=20），但我到 D 走 B 只要 10ms
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 20})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 10, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 10, route: enumRoute{label: "via B"}},
	}

	_, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if yielded {
		t.Error("不应让步：A(RTT=10) < B(RTT=20)，B 应让步")
	}
}

// TestCheckLoop_EqualRTT_TiebreakByVIP RTT 相等时 VIP 大的让步
func TestCheckLoop_EqualRTT_TiebreakByVIP(t *testing.T) {
	// A(100.64.0.1) vs B(100.64.0.2)：A.Compare(B) < 0，所以 B 让步
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 15})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 15, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 15, route: enumRoute{label: "via B"}},
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 25, route: enumRoute{label: "via C"}},
	}

	_, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	// A.Compare(B) < 0 → A 不让步（B 的 VIP 更大，B 让步）
	if yielded {
		t.Error("A(VIP 小) 不应让步，B(VIP 大) 应让步")
	}

	// 反向：从 B 的视角看
	prB := newTestRouter(vipB)
	prB.ReceiveRouteSync(vipA, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipB, RTTMs: 15})

	selectedB := BestRoute{NextHopVIP: vipA, NextHopID: "nodeA", RTTMs: 15, Label: "via A"}
	rankedB := []rankedRoute{
		{nextHopVIP: vipA, nextHopID: "nodeA", rttMs: 15, route: enumRoute{label: "via A"}},
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 25, route: enumRoute{label: "via C"}},
	}

	altB, yieldedB := prB.checkLoopAndYield(vipD, selectedB, rankedB)
	if !yieldedB {
		t.Fatal("B(VIP 大) 应让步")
	}
	if altB.NextHopVIP != vipC {
		t.Errorf("B 让步后应选 via C: got %v", altB.NextHopVIP)
	}
}

// TestCheckLoop_DirectRoute 直连路由不检查环路
func TestCheckLoop_DirectRoute(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipB, NextHopVIP: vipA, RTTMs: 5})

	// 直连路由：NextHopVIP == DstVIP，调用方应跳过 checkLoopAndYield
	// 这里模拟非直连的情况确认逻辑正确
	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 10, Label: "direct"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 10, route: enumRoute{label: "direct"}},
	}

	// B 到 B 走 A（这是非法的，但测试不 panic）
	_, yielded := pr.checkLoopAndYield(vipB, selected, ranked)
	// RTT: 10 > 5 → 我让步，但没有次优 → 返回 false
	if yielded {
		t.Error("无次优路由时不应返回 yielded=true")
	}
}

// TestCheckLoop_NoPeerData 没有 peer 路由数据时不检查
func TestCheckLoop_NoPeerData(t *testing.T) {
	pr := newTestRouter(vipA)
	// peerRoutes 为空

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 10, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 10, route: enumRoute{label: "via B"}},
	}

	_, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if yielded {
		t.Error("无 peer 路由数据时不应让步")
	}
}

// TestCheckLoop_NoAlternative 环路但无次优路由可选 → 不让步（保持有路由好过无路由）
func TestCheckLoop_NoAlternative(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 5})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 20, Label: "via B"}
	// 只有一条路由
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 20, route: enumRoute{label: "via B"}},
	}

	_, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if yielded {
		t.Error("唯一路由环路时不应让步（无替代）")
	}
}

// TestCheckLoop_SkipConflictHopInAlternatives 次优中跳过冲突 hop
func TestCheckLoop_SkipConflictHopInAlternatives(t *testing.T) {
	pr := newTestRouter(vipA)
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 5})

	selected := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 20, Label: "via B"}
	ranked := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 20, route: enumRoute{label: "via B"}},
		// 次优也是 via B（不同路径但同 hop）→ 应跳过
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 22, route: enumRoute{label: "via B(.2)"}},
		// 第三选 via C → 应选这个
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 30, route: enumRoute{label: "via C"}},
	}

	alt, yielded := pr.checkLoopAndYield(vipD, selected, ranked)
	if !yielded {
		t.Fatal("应让步")
	}
	if alt.NextHopVIP != vipC {
		t.Errorf("应跳过冲突 hop B 选 C: got %v", alt.NextHopVIP)
	}
}

// TestCheckLoop_ThreeNodeLoop A→D via B，B→D via C，C→D via A → 三节点环路
// A 只能检测直接冲突（A-B），不检测 B-C-A 这种间接环路
// 间接环路由数据面 TTL 兜底
func TestCheckLoop_ThreeNodeLoop(t *testing.T) {
	pr := newTestRouter(vipA)
	// B 到 D 走 C（不直接冲突 A）
	pr.ReceiveRouteSync(vipB, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipC, RTTMs: 10})
	// C 到 D 走 A（直接冲突 A！但需要 A→D via C 才能触发）
	pr.ReceiveRouteSync(vipC, transport.RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipA, RTTMs: 10})

	// A→D via B：B 不走 A，不冲突
	selected1 := BestRoute{NextHopVIP: vipB, NextHopID: "nodeB", RTTMs: 15, Label: "via B"}
	ranked1 := []rankedRoute{
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 15, route: enumRoute{label: "via B"}},
	}
	_, yielded := pr.checkLoopAndYield(vipD, selected1, ranked1)
	if yielded {
		t.Error("A→D via B 与 B 不直接冲突，不应让步")
	}

	// A→D via C：C 走 A → 直接冲突！
	selected2 := BestRoute{NextHopVIP: vipC, NextHopID: "nodeC", RTTMs: 20, Label: "via C"}
	ranked2 := []rankedRoute{
		{nextHopVIP: vipC, nextHopID: "nodeC", rttMs: 20, route: enumRoute{label: "via C"}},
		{nextHopVIP: vipB, nextHopID: "nodeB", rttMs: 25, route: enumRoute{label: "via B"}},
	}
	alt, yielded2 := pr.checkLoopAndYield(vipD, selected2, ranked2)
	if !yielded2 {
		t.Fatal("A→D via C 与 C→D via A 冲突，A(RTT=20) > C(RTT=10) 应让步")
	}
	if alt.NextHopVIP != vipB {
		t.Errorf("让步后应选 via B: got %v", alt.NextHopVIP)
	}
}
