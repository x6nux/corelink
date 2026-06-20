package topology

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/protobuf/proto"
)

// errSaveBoom 是测试用的 Save 失败哨兵错误。
var errSaveBoom = errors.New("save boom")

// --- mocks ---

// mockIngressSource 实现 IngressSource：返回固定快照 + 入口明细。
type mockIngressSource struct {
	nodes   []NodeEligibilityInput
	qm      QualityMatrix
	details map[string]*genv1.Ingress // key: nodeID+"/"+ingressID
	vips    map[string]string         // nodeID -> VIP（可选，nil 时 NodeVIPs 返回空）
}

func (m *mockIngressSource) Snapshot() ([]NodeEligibilityInput, QualityMatrix) {
	return m.nodes, m.qm
}

func (m *mockIngressSource) IngressDetail(nodeID, ingressID string) (*genv1.Ingress, bool) {
	d, ok := m.details[nodeID+"/"+ingressID]
	return d, ok
}

// NodeFingerprint 基础 mock：无指纹（未注入；测试无需指纹时可直接用此 mock）。
func (m *mockIngressSource) NodeFingerprint(_ string) (string, bool) { return "", false }

// NodeVIPs 基础 mock：返回预置 VIP 映射（nil 时返回空 map）。
func (m *mockIngressSource) NodeVIPs() map[string]string { return m.vips }

// mockResultStore 实现 ResultStore：内存存取 Result 对象。
type mockResultStore struct {
	saved      []Result // 记录每次 SaveResultObj
	latest     *Result  // LoadLatestResultObj 返回值
	saveErr    error
	loadErr    error
	saveCalled int
}

func (m *mockResultStore) SaveResultObj(r Result) error {
	m.saveCalled++
	if m.saveErr != nil {
		return m.saveErr
	}
	cp := copyResult(r)
	m.saved = append(m.saved, cp)
	m.latest = &cp
	return nil
}

func (m *mockResultStore) LoadLatestResultObj() (Result, bool, error) {
	if m.loadErr != nil {
		return Result{}, false, m.loadErr
	}
	if m.latest == nil {
		return Result{}, false, nil
	}
	return copyResult(*m.latest), true, nil
}

// mockNotifier 实现 Notifier：记录收到的受影响节点集。
type mockNotifier struct {
	calls [][]string
}

func (m *mockNotifier) RecomputeAndNotify(nodeIDs ...string) {
	cp := append([]string(nil), nodeIDs...)
	m.calls = append(m.calls, cp)
}

func (m *mockNotifier) lastNotified() []string {
	if len(m.calls) == 0 {
		return nil
	}
	return m.calls[len(m.calls)-1]
}

// --- 测试夹具 ---

// 构造一个简单的 3 中转拓扑：A,B,C 各有一个 reachable 高置信入口，互联质量对称。
func fixtureSource() *mockIngressSource {
	mkNode := func(id, ing string) NodeEligibilityInput {
		return NodeEligibilityInput{
			NodeID: id,
			Nat:    genv1.NatType_NAT_TYPE_FULL_CONE,
			Ingresses: []IngressMeta{
				{ID: ing, Confidence: 90, Reachable: true},
			},
		}
	}
	nodes := []NodeEligibilityInput{
		mkNode("A", "a1"),
		mkNode("B", "b1"),
		mkNode("C", "c1"),
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "B", DstNode: "A", DstIngress: "a1"}: 10,
		{SrcNode: "A", DstNode: "C", DstIngress: "c1"}: 20,
		{SrcNode: "C", DstNode: "A", DstIngress: "a1"}: 20,
		{SrcNode: "B", DstNode: "C", DstIngress: "c1"}: 15,
		{SrcNode: "C", DstNode: "B", DstIngress: "b1"}: 15,
	}
	details := map[string]*genv1.Ingress{
		"A/a1": {Id: "a1", Host: "hostA", Port: 1111},
		"B/b1": {Id: "b1", Host: "hostB", Port: 2222},
		"C/c1": {Id: "c1", Host: "hostC", Port: 3333},
	}
	return &mockIngressSource{nodes: nodes, qm: qm, details: details}
}

func defaultParams() OptimizerParams {
	return OptimizerParams{
		MaxPeers:    8,
		IngressK:    2,
		RouteK:      3,
		LeafUplinks: 2,
		ProbeFull:   true,
		ProbeLimit:  0,
	}
}

// fakeClock 提供可控时钟。
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func newTestService(src IngressSource, store ResultStore, notify Notifier, clk *fakeClock, damping DampingParams) *TopoService {
	return NewTopoService(TopoServiceDeps{
		Recv:    src,
		Store:   store,
		Notify:  notify,
		Clock:   clk.Now,
		Params:  defaultParams(),
		Damping: damping,
	})
}

// --- Recompute 正确性 ---

func TestRecompute_BuildsAssignmentsAndPersistsAndNotifies(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	notify := &mockNotifier{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	svc := newTestService(src, store, notify, clk, DampingParams{MinInterval: time.Minute})

	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// SaveResultObj 被调用一次。
	if store.saveCalled != 1 {
		t.Fatalf("SaveResultObj called %d times, want 1", store.saveCalled)
	}

	// notify 收到全部 3 个节点。
	got := append([]string(nil), notify.lastNotified()...)
	sort.Strings(got)
	want := []string{"A", "B", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notified %v, want %v", got, want)
	}

	// AssignmentForNode 返回 A 的 assignment。
	a, ok := svc.AssignmentForNode("A")
	if !ok {
		t.Fatal("AssignmentForNode(A) not found")
	}
	if a.Version != 1 {
		t.Errorf("version = %d, want 1", a.Version)
	}
	if a.Role != genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		t.Errorf("role = %v, want TRANSIT", a.Role)
	}
	// A 的邻居含 B/C，且 NeighborRef.Ingresses 是查询到的完整 Ingress。
	if len(a.Neighbors) == 0 {
		t.Fatal("A has no neighbors")
	}
	for _, nb := range a.Neighbors {
		if len(nb.Ingresses) == 0 {
			t.Errorf("neighbor %s has empty Ingresses (detail not resolved)", nb.NodeId)
			continue
		}
		for _, ing := range nb.Ingresses {
			if ing.Id == "" || ing.Host == "" {
				t.Errorf("neighbor %s ingress not fully resolved: %+v", nb.NodeId, ing)
			}
		}
	}
	// BaselineRoutes 中应含到 B、C 的路由，且 Hop 字段正确映射。
	if len(a.BaselineRoutes) == 0 {
		t.Fatal("A has no baseline routes")
	}
	for _, r := range a.BaselineRoutes {
		if r.DstNode == "" {
			t.Error("route with empty DstNode")
		}
		for _, h := range r.Hops {
			if h.NodeId == "" {
				t.Error("hop with empty NodeId (mapping Hop.Node->NodeId failed)")
			}
		}
	}
	// ProbeTargets 非空。
	if len(a.ProbeTargets) == 0 {
		t.Error("A has no probe targets")
	}
}

// 确定性：两次 Recompute 后 assignments 逐字段相等。
func TestRecompute_Deterministic(t *testing.T) {
	build := func() map[string]*genv1.TopologyAssignment {
		src := fixtureSource()
		svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1000, 0)}, DampingParams{})
		if err := svc.Recompute(7); err != nil {
			t.Fatalf("Recompute err: %v", err)
		}
		return svc.snapshotAssignments()
	}
	a1 := build()
	a2 := build()
	if !reflect.DeepEqual(a1, a2) {
		t.Fatal("assignments not deterministic across runs")
	}
}

// --- damping ---

func TestDamping_SuppressesWithinMinInterval(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	notify := &mockNotifier{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	svc := newTestService(src, store, notify, clk, DampingParams{MinInterval: time.Minute})

	// 第一次 Tick：执行重算。
	svc.Tick()
	if store.saveCalled != 1 {
		t.Fatalf("after first Tick saveCalled=%d, want 1", store.saveCalled)
	}

	// MinInterval 内连续 Tick：被抑制，不重算。
	clk.advance(10 * time.Second)
	svc.Tick()
	clk.advance(10 * time.Second)
	svc.Tick()
	if store.saveCalled != 1 {
		t.Fatalf("within MinInterval saveCalled=%d, want still 1 (suppressed)", store.saveCalled)
	}

	// Snapshot 不变时，超过 MinInterval 后 Tick 也不应重复保存（变更检测）。
	clk.advance(time.Minute)
	svc.Tick()
	if store.saveCalled != 1 {
		t.Fatalf("unchanged snapshot: saveCalled=%d, want still 1 (no-op)", store.saveCalled)
	}

	// 修改 Snapshot 后 Tick 应触发保存。
	src.nodes = append(src.nodes, NodeEligibilityInput{
		NodeID: "D",
		Nat:    genv1.NatType_NAT_TYPE_FULL_CONE,
		Ingresses: []IngressMeta{
			{ID: "d1", Confidence: 90, Reachable: true},
		},
	})
	src.qm[QKey{SrcNode: "A", DstNode: "D", DstIngress: "d1"}] = 10
	src.qm[QKey{SrcNode: "D", DstNode: "A", DstIngress: "a1"}] = 10
	src.details["D/d1"] = &genv1.Ingress{Id: "d1", Host: "hostD", Port: 4444}
	clk.advance(time.Minute)
	svc.Tick()
	if store.saveCalled != 2 {
		t.Fatalf("changed snapshot: saveCalled=%d, want 2", store.saveCalled)
	}
}

// TestDamping_NoChangeSkipsVersionBump 验证 Snapshot 未变时不递增版本号。
func TestDamping_NoChangeSkipsVersionBump(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	svc := newTestService(src, store, &mockNotifier{}, clk, DampingParams{})

	svc.Tick()
	v1 := svc.Status().Version

	// 连续 Tick，Snapshot 不变 → 版本不递增。
	clk.advance(time.Minute)
	svc.Tick()
	clk.advance(time.Minute)
	svc.Tick()
	v2 := svc.Status().Version

	if v2 != v1 {
		t.Fatalf("version bumped without change: v1=%d v2=%d", v1, v2)
	}
}

// --- 转换正确性：Result -> genv1.TopologyAssignment 字段映射 ---

func TestBuildAssignments_FieldMapping(t *testing.T) {
	src := fixtureSource()
	svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1000, 0)}, DampingParams{})

	r := Optimize(OptimizerInput{
		Version:     42,
		Nodes:       src.nodes,
		Quality:     src.qm,
		MaxPeers:    8,
		IngressK:    2,
		RouteK:      3,
		LeafUplinks: 2,
		ProbeFull:   true,
	})
	assigns := svc.buildAssignments(r)

	// 每节点 version 透传。
	for id, a := range assigns {
		if a.Version != 42 {
			t.Errorf("node %s version=%d want 42", id, a.Version)
		}
	}

	// Baseline 中 (A,B) 的全部 K 条路由 Hop.Node -> genv1.Hop.NodeId, Hop.Ingress -> IngressId。
	a := assigns["A"]
	allTopoRoutes := r.Baseline[RoutePair{Src: "A", Dst: "B"}]
	var routesToB []*genv1.Route2
	for _, route := range a.BaselineRoutes {
		if route.DstNode == "B" {
			routesToB = append(routesToB, route)
		}
	}
	if len(routesToB) == 0 {
		t.Fatal("no baseline route to B in A's assignment")
	}
	if len(routesToB) != len(allTopoRoutes) {
		t.Fatalf("routes to B count=%d want %d (all K routes)", len(routesToB), len(allTopoRoutes))
	}
	// 逐条校验：每条 genv1.Route2 对应 Result.Baseline 中的某条拓扑路由。
	for ri, route := range routesToB {
		matched := false
		for _, topoHops := range allTopoRoutes {
			if len(route.Hops) != len(topoHops) {
				continue
			}
			hopMatch := true
			for i, h := range route.Hops {
				if h.NodeId != topoHops[i].Node || h.IngressId != topoHops[i].Ingress {
					hopMatch = false
					break
				}
			}
			if hopMatch {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("routesToB[%d] (hops=%d) has no matching topology route", ri, len(route.Hops))
		}
	}

	// 角色映射：RoleTransit -> TRANSIT。
	if a.Role != genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		t.Errorf("A role=%v want TRANSIT", a.Role)
	}
}

// 叶子角色映射 RoleLeaf -> LEAF。
func TestBuildAssignments_LeafRole(t *testing.T) {
	// L 为叶子：无 reachable 入口。
	nodes := []NodeEligibilityInput{
		{NodeID: "A", Nat: genv1.NatType_NAT_TYPE_FULL_CONE, Ingresses: []IngressMeta{{ID: "a1", Confidence: 90, Reachable: true}}},
		{NodeID: "B", Nat: genv1.NatType_NAT_TYPE_FULL_CONE, Ingresses: []IngressMeta{{ID: "b1", Confidence: 90, Reachable: true}}},
		{NodeID: "L", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{{ID: "l1", Confidence: 10, Reachable: false}}},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "B", DstNode: "A", DstIngress: "a1"}: 10,
		{SrcNode: "L", DstNode: "A", DstIngress: "a1"}: 5,
		{SrcNode: "L", DstNode: "B", DstIngress: "b1"}: 5,
	}
	details := map[string]*genv1.Ingress{
		"A/a1": {Id: "a1", Host: "hA", Port: 1},
		"B/b1": {Id: "b1", Host: "hB", Port: 2},
	}
	src := &mockIngressSource{nodes: nodes, qm: qm, details: details}
	svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}
	l, ok := svc.AssignmentForNode("L")
	if !ok {
		t.Fatal("L assignment not found")
	}
	if l.Role != genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF {
		t.Errorf("L role=%v want LEAF", l.Role)
	}
}

// --- Load 加载即服务 ---

func TestLoad_RebuildsFromStore(t *testing.T) {
	// 先用一个 service 算出 Result 存进 store。
	src := fixtureSource()
	store := &mockResultStore{}
	svc1 := newTestService(src, store, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	if err := svc1.Recompute(5); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// 新 service 从同一 store Load：应重建 assignments 且可服务。
	svc2 := newTestService(src, store, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	loaded, err := svc2.Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if !loaded {
		t.Fatal("Load returned false despite persisted result")
	}
	a, ok := svc2.AssignmentForNode("A")
	if !ok {
		t.Fatal("after Load, AssignmentForNode(A) not found")
	}
	if a.Version != 5 {
		t.Errorf("loaded version=%d want 5", a.Version)
	}
	// 与 svc1 的 assignment 逐节点一致（proto.Equal 而非 reflect.DeepEqual：
	// proto 消息含 sizeCache 等内部字段，DeepEqual 不可靠）。
	orig := svc1.snapshotAssignments()
	got := svc2.snapshotAssignments()
	if len(orig) != len(got) {
		t.Fatalf("assignment count differ: orig=%d loaded=%d", len(orig), len(got))
	}
	for id, oa := range orig {
		ga, ok := got[id]
		if !ok {
			t.Fatalf("loaded missing node %s", id)
		}
		if !proto.Equal(oa, ga) {
			t.Fatalf("node %s assignment differ:\n orig=%v\n got =%v", id, oa, ga)
		}
	}
}

func TestLoad_EmptyStore(t *testing.T) {
	src := fixtureSource()
	svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	loaded, err := svc.Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if loaded {
		t.Fatal("Load returned true on empty store")
	}
	if _, ok := svc.AssignmentForNode("A"); ok {
		t.Fatal("AssignmentForNode should be empty before any Recompute")
	}
}

// --- 事件触发：OnEvent 攒边，到间隔批量 ApplyEdgeEvents ---

func TestOnEvent_AccumulatesAndAppliesAtInterval(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	notify := &mockNotifier{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	svc := newTestService(src, store, notify, clk, DampingParams{MinInterval: time.Minute})

	// 初始重算建立基线。
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}
	baseSaves := store.saveCalled

	// MinInterval 内连续 OnEvent：攒边，不立即重算。
	clk.advance(5 * time.Second)
	svc.OnEvent(EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: EdgeDegraded, W: 100})
	svc.OnEvent(EdgeDelta{Src: "B", Dst: "A", Ingress: "a1", Kind: EdgeDegraded, W: 100})
	if store.saveCalled != baseSaves {
		t.Fatalf("OnEvent within MinInterval triggered recompute: saveCalled=%d want %d", store.saveCalled, baseSaves)
	}

	// 超过 MinInterval 后 Tick：批量 ApplyEdgeEvents 重算。
	clk.advance(time.Minute)
	svc.Tick()
	if store.saveCalled != baseSaves+1 {
		t.Fatalf("after interval saveCalled=%d want %d (batched apply)", store.saveCalled, baseSaves+1)
	}
	// 攒边队列应清空。
	if svc.pendingEventCount() != 0 {
		t.Errorf("pending events not drained: %d", svc.pendingEventCount())
	}
}

// --- Save 失败：保留 pending + 上报错误，不静默丢数据 ---

func TestFlush_SaveFailureRetainsPendingAndReportsError(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	var gotErr error
	svc := NewTopoService(TopoServiceDeps{
		Recv:    src,
		Store:   store,
		Notify:  &mockNotifier{},
		Clock:   clk.Now,
		Params:  defaultParams(),
		Damping: DampingParams{MinInterval: time.Minute},
		OnError: func(e error) { gotErr = e },
	})

	// 建立基线（成功）。
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// 攒边，未到间隔不重算。
	clk.advance(5 * time.Second)
	svc.OnEvent(EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: EdgeDegraded, W: 100})
	if svc.pendingEventCount() != 1 {
		t.Fatalf("pending events = %d, want 1", svc.pendingEventCount())
	}

	// 让后续 Save 失败。
	store.saveErr = errSaveBoom

	// 到间隔 Tick：flush 触发但 Save 失败 → pending 保留 + OnError 被调。
	clk.advance(time.Minute)
	svc.Tick()

	if gotErr == nil {
		t.Fatal("OnError not invoked on save failure")
	}
	// pending 边事件应被保留（未 drain）。
	if svc.pendingEventCount() != 1 {
		t.Fatalf("after save failure pending events = %d, want 1 (retained)", svc.pendingEventCount())
	}

	// 恢复 Save，下次 Tick 应成功 drain（增量基线已被丢弃，走全量重算）。
	store.saveErr = nil
	clk.advance(time.Minute)
	svc.Tick()
	if svc.pendingEventCount() != 0 {
		t.Fatalf("after recovery pending events = %d, want 0 (drained)", svc.pendingEventCount())
	}
	// 重算成功后 assignments 可服务（数据未丢）。
	if _, ok := svc.AssignmentForNode("A"); !ok {
		t.Fatal("AssignmentForNode(A) lost after recovery")
	}
}

// Recompute（外部调用）的 Save 失败仍把 error 透传给调用方。
func TestRecompute_SaveFailurePropagatesError(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{saveErr: errSaveBoom}
	svc := newTestService(src, store, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	if err := svc.Recompute(1); err == nil {
		t.Fatal("Recompute should propagate save error")
	}
}

// --- AssignmentForNode 返回独立副本（clone 隔离）---

func TestAssignmentForNode_ReturnsIsolatedClone(t *testing.T) {
	src := fixtureSource()
	svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	a1, ok := svc.AssignmentForNode("A")
	if !ok {
		t.Fatal("AssignmentForNode(A) not found")
	}
	// 两次取得的是不同对象（独立副本，非共享指针）。
	a2, _ := svc.AssignmentForNode("A")
	if a1 == a2 {
		t.Fatal("AssignmentForNode returned shared pointer, want independent clone")
	}

	// mutate 返回的副本不影响内部状态。
	a1.Version = 9999
	if len(a1.Neighbors) > 0 {
		a1.Neighbors[0].NodeId = "MUTATED"
	}
	a3, _ := svc.AssignmentForNode("A")
	if a3.Version == 9999 {
		t.Error("mutation on returned clone leaked into internal state (Version)")
	}
	if len(a3.Neighbors) > 0 && a3.Neighbors[0].NodeId == "MUTATED" {
		t.Error("mutation on returned clone leaked into internal state (Neighbors)")
	}
}

// OnEvent 立即满足间隔时直接应用。
func TestOnEvent_AppliesImmediatelyWhenIntervalElapsed(t *testing.T) {
	src := fixtureSource()
	store := &mockResultStore{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	svc := newTestService(src, store, &mockNotifier{}, clk, DampingParams{MinInterval: time.Minute})
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}
	baseSaves := store.saveCalled

	// 已超过 MinInterval：OnEvent 直接触发应用。
	clk.advance(2 * time.Minute)
	svc.OnEvent(EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: EdgeDegraded, W: 100})
	if store.saveCalled != baseSaves+1 {
		t.Fatalf("OnEvent past interval saveCalled=%d want %d", store.saveCalled, baseSaves+1)
	}
}

// --- buildAssignments 下发全部 K 条 baseline 路由 ---

// TestBuildAssignments_AllKRoutesPerDst 验证 buildAssignments 对每个目的节点下发
// 全部 K 条路由（而非仅首选），供节点侧 SessionRouter 一致性哈希选路 + Degrade 降级。
//
// 拓扑：T1, T2 各有 2 个 reachable 入口（i1/i2），互联质量对称，RouteK=2。
// 优化器为 (T1,T2) 应产生 2 条不同路由（经不同入口），buildAssignments 应全部下发。
func TestBuildAssignments_AllKRoutesPerDst(t *testing.T) {
	// 构造 2 中转、各 2 入口的拓扑。
	nodes := []NodeEligibilityInput{
		{
			NodeID: "T1",
			Nat:    genv1.NatType_NAT_TYPE_FULL_CONE,
			Ingresses: []IngressMeta{
				{ID: "t1-i1", Confidence: 90, Reachable: true},
				{ID: "t1-i2", Confidence: 85, Reachable: true},
			},
		},
		{
			NodeID: "T2",
			Nat:    genv1.NatType_NAT_TYPE_FULL_CONE,
			Ingresses: []IngressMeta{
				{ID: "t2-i1", Confidence: 90, Reachable: true},
				{ID: "t2-i2", Confidence: 85, Reachable: true},
			},
		},
	}
	qm := QualityMatrix{
		{SrcNode: "T1", DstNode: "T2", DstIngress: "t2-i1"}: 10,
		{SrcNode: "T1", DstNode: "T2", DstIngress: "t2-i2"}: 12,
		{SrcNode: "T2", DstNode: "T1", DstIngress: "t1-i1"}: 10,
		{SrcNode: "T2", DstNode: "T1", DstIngress: "t1-i2"}: 12,
	}
	details := map[string]*genv1.Ingress{
		"T1/t1-i1": {Id: "t1-i1", Host: "h1", Port: 1},
		"T1/t1-i2": {Id: "t1-i2", Host: "h1", Port: 2},
		"T2/t2-i1": {Id: "t2-i1", Host: "h2", Port: 1},
		"T2/t2-i2": {Id: "t2-i2", Host: "h2", Port: 2},
	}
	src := &mockIngressSource{nodes: nodes, qm: qm, details: details}

	svc := newTestService(src, &mockResultStore{}, &mockNotifier{},
		&fakeClock{now: time.Unix(1, 0)}, DampingParams{})

	// 用 Optimize 直接产出 Result，RouteK=2。
	r := Optimize(OptimizerInput{
		Version:     1,
		Nodes:       nodes,
		Quality:     qm,
		MaxPeers:    8,
		IngressK:    2,
		RouteK:      2,
		LeafUplinks: 2,
		ProbeFull:   true,
	})

	// 前置断言：Optimize 确实为 (T1,T2) 产出 >=2 条路由（拆点图两入口 → 两条路径）。
	pairT1T2 := RoutePair{Src: "T1", Dst: "T2"}
	if got := len(r.Baseline[pairT1T2]); got < 2 {
		t.Fatalf("Optimize produced %d routes for T1->T2, want >=2 (need K>=2 for test)", got)
	}

	assigns := svc.buildAssignments(r)

	// T1 的 BaselineRoutes 到 T2 应有 >=2 条 Route2。
	a1 := assigns["T1"]
	if a1 == nil {
		t.Fatal("T1 assignment is nil")
	}
	var countT2 int
	for _, route := range a1.BaselineRoutes {
		if route.DstNode == "T2" {
			countT2++
			// 每条路由都有非空 Hops。
			if len(route.Hops) == 0 {
				t.Error("route T1->T2 has empty Hops")
			}
		}
	}
	wantK := len(r.Baseline[pairT1T2])
	if countT2 != wantK {
		t.Errorf("T1 BaselineRoutes to T2: got %d Route2, want %d (all K routes)", countT2, wantK)
	}

	// 同样验证 T2 → T1。
	pairT2T1 := RoutePair{Src: "T2", Dst: "T1"}
	a2 := assigns["T2"]
	if a2 == nil {
		t.Fatal("T2 assignment is nil")
	}
	var countT1 int
	for _, route := range a2.BaselineRoutes {
		if route.DstNode == "T1" {
			countT1++
		}
	}
	wantK2 := len(r.Baseline[pairT2T1])
	if countT1 != wantK2 {
		t.Errorf("T2 BaselineRoutes to T1: got %d Route2, want %d (all K routes)", countT1, wantK2)
	}

	// 路由应按 DstNode 分组、同 DstNode 内确定性排序（不出现交叉）。
	for nodeID, a := range assigns {
		routes := a.BaselineRoutes
		for i := 1; i < len(routes); i++ {
			if routes[i].DstNode < routes[i-1].DstNode {
				t.Errorf("node %s: BaselineRoutes not sorted by DstNode at index %d", nodeID, i)
			}
		}
	}
}

// --- A4：buildAssignments 填充 NeighborRef.Fingerprint ---

// mockIngressSourceWithFP 扩展 mockIngressSource，支持 NodeFingerprint 查询。
type mockIngressSourceWithFP struct {
	mockIngressSource
	fps map[string]string // nodeID -> fingerprint
}

func (m *mockIngressSourceWithFP) NodeFingerprint(nodeID string) (string, bool) {
	fp, ok := m.fps[nodeID]
	return fp, ok
}

// TestBuildAssignments_FillsNeighborFingerprint 验证 buildAssignments 把邻居
// 证书指纹填进 NeighborRef.Fingerprint。
func TestBuildAssignments_FillsNeighborFingerprint(t *testing.T) {
	// 用 fixtureSource 的快照数据（A,B,C 三节点互联），
	// 覆盖 NodeFingerprint 返回预置指纹。
	base := fixtureSource()
	src := &mockIngressSourceWithFP{
		mockIngressSource: *base,
		fps: map[string]string{
			"A": "fpA",
			"B": "fpB",
			"C": "fpC",
		},
	}

	svc := newTestService(src, &mockResultStore{}, &mockNotifier{}, &fakeClock{now: time.Unix(1, 0)}, DampingParams{})
	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// 取节点 A 的 assignment，校验邻居中 B 的指纹为 "fpB"。
	a, ok := svc.AssignmentForNode("A")
	if !ok {
		t.Fatal("AssignmentForNode(A) not found")
	}
	var foundB bool
	for _, nb := range a.Neighbors {
		if nb.NodeId == "B" {
			foundB = true
			if nb.GetFingerprint() != "fpB" {
				t.Errorf("neighbor B Fingerprint = %q, want %q", nb.GetFingerprint(), "fpB")
			}
		}
	}
	if !foundB {
		t.Error("node A has no neighbor B in assignment")
	}
}

// --- P5 FIB：buildAssignments 填充 TopologyAssignment.Fib ---

// TestBuildAssignments_PopulatesFIB 验证 buildAssignments 在 NodeVIPs 非空时
// 调用 computeFIB 并将结果填入每节点的 TopologyAssignment.Fib 字段。
//
// 注意：computeFIB 内部经 DAG 验证后会剪除环路边，全 mesh 拓扑中部分节点的
// FIB 条目可能被完全剪除（DAG 约束下无法所有节点都互为 next-hop）。因此测试
// 只校验至少有一个节点的 Fib 被填充，且填充的条目格式正确。
func TestBuildAssignments_PopulatesFIB(t *testing.T) {
	// 使用 fixtureSource（A,B,C 三节点互联），注入 VIP 映射。
	base := fixtureSource()
	base.vips = map[string]string{
		"A": "100.64.0.1",
		"B": "100.64.0.2",
		"C": "100.64.0.3",
	}

	svc := newTestService(base, &mockResultStore{}, &mockNotifier{},
		&fakeClock{now: time.Unix(1, 0)}, DampingParams{})

	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// 至少有一个节点 Fib 被填充（DAG 剪枝后不保证全部）。
	var fibCount int
	for _, nodeID := range []string{"A", "B", "C"} {
		a, ok := svc.AssignmentForNode(nodeID)
		if !ok {
			t.Fatalf("AssignmentForNode(%s) not found", nodeID)
		}
		if a.Fib == nil {
			continue
		}
		fibCount++
		if a.Fib.Version != 1 {
			t.Errorf("node %s: Fib.Version = %d, want 1", nodeID, a.Fib.Version)
		}
		if len(a.Fib.Entries) == 0 {
			t.Errorf("node %s: Fib.Entries is empty, want at least 1 entry", nodeID)
		}
		// 校验条目格式正确：prefix 非空、每个 NextHop 的 PeerId 非空。
		for _, entry := range a.Fib.Entries {
			if entry.Prefix == "" {
				t.Errorf("node %s: FIB entry has empty prefix", nodeID)
			}
			if len(entry.NextHops) == 0 {
				t.Errorf("node %s: FIB entry %s has no next-hops", nodeID, entry.Prefix)
			}
			for _, nh := range entry.NextHops {
				if nh.PeerId == "" {
					t.Errorf("node %s: FIB entry %s has next-hop with empty PeerId", nodeID, entry.Prefix)
				}
			}
		}
	}
	if fibCount == 0 {
		t.Fatal("no node has FIB populated despite VIPs being provided")
	}

	// 校验被填充的节点的 FIB prefix 都是其他节点 VIP/32。
	validPrefixes := map[string]bool{
		"100.64.0.1/32": true,
		"100.64.0.2/32": true,
		"100.64.0.3/32": true,
	}
	for _, nodeID := range []string{"A", "B", "C"} {
		a, _ := svc.AssignmentForNode(nodeID)
		if a.Fib == nil {
			continue
		}
		for _, entry := range a.Fib.Entries {
			if !validPrefixes[entry.Prefix] {
				t.Errorf("node %s: unexpected FIB prefix %q", nodeID, entry.Prefix)
			}
		}
	}
}

// TestBuildAssignments_NoVIPs_FIBNil 验证 NodeVIPs 为空时 Fib 字段为 nil（向后兼容）。
func TestBuildAssignments_NoVIPs_FIBNil(t *testing.T) {
	base := fixtureSource()
	// vips 不设置（nil），NodeVIPs() 返回 nil。

	svc := newTestService(base, &mockResultStore{}, &mockNotifier{},
		&fakeClock{now: time.Unix(1, 0)}, DampingParams{})

	if err := svc.Recompute(1); err != nil {
		t.Fatalf("Recompute err: %v", err)
	}

	// 无 VIP 时 Fib 应为 nil。
	a, ok := svc.AssignmentForNode("A")
	if !ok {
		t.Fatal("AssignmentForNode(A) not found")
	}
	if a.Fib != nil {
		t.Errorf("node A: Fib should be nil when no VIPs, got %+v", a.Fib)
	}
}
