package topoadapter

import (
	"reflect"
	"sort"
	"testing"

	"github.com/x6nux/corelink/internal/controller/ingress"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/controller/topology"
	"github.com/x6nux/corelink/internal/controller/topostore"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"gorm.io/gorm"
)

// fillReceiver 用一组 IngressSet + QualityReport 填充一个真实 Receiver（sink=nil）。
func fillReceiver(t *testing.T, sets []*genv1.IngressSet, reports []*genv1.QualityReport) *ingress.Receiver {
	t.Helper()
	r := ingress.New(nil)
	for _, s := range sets {
		if _, err := r.ReportIngress(nil, s); err != nil {
			t.Fatalf("ReportIngress: %v", err)
		}
	}
	for _, q := range reports {
		if _, err := r.ReportQuality(nil, q); err != nil {
			t.Fatalf("ReportQuality: %v", err)
		}
	}
	return r
}

func TestIngressSourceAdapter_Snapshot_Eligibility(t *testing.T) {
	// nodeA：一个高置信入口（confidence>=60）→ Reachable 经 confidence 判定。
	// nodeB：一个低置信入口（confidence<60），但被质量上报覆盖 → Reachable 经覆盖判定。
	// nodeC：一个低置信入口，无质量覆盖 → 不可达。
	sets := []*genv1.IngressSet{
		{NodeId: "A", Ingresses: []*genv1.Ingress{
			{Id: "a1", Confidence: 80, NatType: genv1.NatType_NAT_TYPE_FULL_CONE},
		}},
		{NodeId: "B", Ingresses: []*genv1.Ingress{
			{Id: "b1", Confidence: 10, NatType: genv1.NatType_NAT_TYPE_SYMMETRIC},
		}},
		{NodeId: "C", Ingresses: []*genv1.Ingress{
			{Id: "c1", Confidence: 10, NatType: genv1.NatType_NAT_TYPE_SYMMETRIC},
		}},
	}
	reports := []*genv1.QualityReport{
		// 某节点上报到 B 的 b1 入口质量 → 覆盖 B.b1，使其 Reachable。
		{SrcNode: "A", Samples: []*genv1.EdgeSample{
			{DstNode: "B", IngressId: "b1", RttMs: 30, LossPermille: 5},
		}},
	}
	r := fillReceiver(t, sets, reports)
	adapter := NewIngressSourceAdapter(r)

	nodes, qm := adapter.Snapshot()

	// 转成 map 便于断言（Snapshot 不保证顺序）。
	byID := make(map[string]topology.NodeEligibilityInput)
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if len(byID) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(byID))
	}

	// A：高置信 → Reachable。
	if a := byID["A"]; len(a.Ingresses) != 1 || !a.Ingresses[0].Reachable {
		t.Errorf("A.a1 应 Reachable（confidence>=60），got %+v", a.Ingresses)
	}
	if byID["A"].Nat != genv1.NatType_NAT_TYPE_FULL_CONE {
		t.Errorf("A 代表 NAT 应为 FULL_CONE，got %v", byID["A"].Nat)
	}
	// B：低置信但被质量覆盖 → Reachable。
	if b := byID["B"]; len(b.Ingresses) != 1 || !b.Ingresses[0].Reachable {
		t.Errorf("B.b1 应 Reachable（被质量上报覆盖），got %+v", b.Ingresses)
	}
	// C：低置信无覆盖 → 不可达。
	if c := byID["C"]; len(c.Ingresses) != 1 || c.Ingresses[0].Reachable {
		t.Errorf("C.c1 应不可达，got %+v", c.Ingresses)
	}

	// 质量矩阵：(A→B,b1) 应存在，W = rtt + loss*penalty = 30 + 5*lossPenalty。
	w, ok := qm[topology.QKey{SrcNode: "A", DstNode: "B", DstIngress: "b1"}]
	if !ok {
		t.Fatalf("质量矩阵缺 (A→B,b1)")
	}
	wantW := uint64(30) + uint64(5)*lossPenalty
	if w != wantW {
		t.Errorf("质量边权 = %d, want %d", w, wantW)
	}
}

func TestIngressSourceAdapter_IngressDetail(t *testing.T) {
	sets := []*genv1.IngressSet{
		{NodeId: "A", Ingresses: []*genv1.Ingress{
			{Id: "a1", Host: "1.2.3.4", Port: 443, Confidence: 90},
			{Id: "a2", Host: "5.6.7.8", Port: 8443, Confidence: 70},
		}},
	}
	r := fillReceiver(t, sets, nil)
	adapter := NewIngressSourceAdapter(r)

	det, ok := adapter.IngressDetail("A", "a2")
	if !ok || det == nil {
		t.Fatalf("IngressDetail(A,a2) 未找到")
	}
	if det.Host != "5.6.7.8" || det.Port != 8443 {
		t.Errorf("IngressDetail 明细错误：%+v", det)
	}

	if _, ok := adapter.IngressDetail("A", "nope"); ok {
		t.Errorf("不存在的入口应返回 false")
	}
	if _, ok := adapter.IngressDetail("ZZZ", "a1"); ok {
		t.Errorf("不存在的节点应返回 false")
	}
}

func TestIngressSourceAdapter_QualityAggregation_MinRTT(t *testing.T) {
	// 同一 (src,dst,ingress) 多份样本（来自不同 src 的报告各含一条）应取最优（最小 W）。
	// 这里两个不同 src 报告到同一目标 → 两条独立 QKey（src 不同），各自独立。
	// 单个 src 报告内若有重复入口样本，取最小 W。
	sets := []*genv1.IngressSet{
		{NodeId: "B", Ingresses: []*genv1.Ingress{{Id: "b1", Confidence: 80}}},
	}
	reports := []*genv1.QualityReport{
		{SrcNode: "A", Samples: []*genv1.EdgeSample{
			{DstNode: "B", IngressId: "b1", RttMs: 50, LossPermille: 0},
			{DstNode: "B", IngressId: "b1", RttMs: 20, LossPermille: 0}, // 更优
		}},
	}
	r := fillReceiver(t, sets, reports)
	adapter := NewIngressSourceAdapter(r)
	_, qm := adapter.Snapshot()
	w := qm[topology.QKey{SrcNode: "A", DstNode: "B", DstIngress: "b1"}]
	if w != 20 {
		t.Errorf("重复样本应取最小 W=20, got %d", w)
	}
}

func TestEdgeEventToDelta(t *testing.T) {
	cases := []struct {
		ev   *genv1.EdgeEvent
		want topology.EdgeDelta
	}{
		{
			ev:   &genv1.EdgeEvent{SrcNode: "A", DstNode: "B", IngressId: "b1", Kind: genv1.EdgeEventKind_EDGE_EVENT_KIND_DOWN},
			want: topology.EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: topology.EdgeDown},
		},
		{
			ev:   &genv1.EdgeEvent{SrcNode: "A", DstNode: "B", IngressId: "b1", Kind: genv1.EdgeEventKind_EDGE_EVENT_KIND_DEGRADED, RttMs: 100, LossPermille: 10},
			want: topology.EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: topology.EdgeDegraded, W: 100 + 10*lossPenalty},
		},
		{
			ev:   &genv1.EdgeEvent{SrcNode: "A", DstNode: "B", IngressId: "b1", Kind: genv1.EdgeEventKind_EDGE_EVENT_KIND_RECOVERED, RttMs: 15, LossPermille: 0},
			want: topology.EdgeDelta{Src: "A", Dst: "B", Ingress: "b1", Kind: topology.EdgeRecovered, W: 15},
		},
	}
	for i, c := range cases {
		got := EdgeEventToDelta(c.ev)
		if got != c.want {
			t.Errorf("case %d: got %+v, want %+v", i, got, c.want)
		}
	}
}

func TestResultStoreAdapter_RoundTrip(t *testing.T) {
	db := newMemDB(t)
	ts := topostore.New(db)
	adapter := NewResultStoreAdapter(ts)

	// 空库：LoadLatestResultObj → ok=false。
	if _, ok, err := adapter.LoadLatestResultObj(); err != nil || ok {
		t.Fatalf("空库应 ok=false err=nil, got ok=%v err=%v", ok, err)
	}

	res := topology.Result{
		Version: 7,
		Roles:   map[string]topology.Role{"A": topology.RoleTransit, "B": topology.RoleLeaf},
		Neighbors: map[string][]topology.NeighborSpec{
			"A": {{NodeID: "B", Ingresses: []string{"b1"}}},
		},
		Baseline: map[topology.RoutePair][][]topology.Hop{
			{Src: "A", Dst: "B"}: {{{Node: "A", Ingress: ""}, {Node: "B", Ingress: "b1"}}},
		},
		ProbeSets: map[string][]topology.ProbeTarget{
			"A": {{NodeID: "B", IngressIDs: []string{"b1"}}},
		},
	}
	if err := adapter.SaveResultObj(res); err != nil {
		t.Fatalf("SaveResultObj: %v", err)
	}

	got, ok, err := adapter.LoadLatestResultObj()
	if err != nil || !ok {
		t.Fatalf("LoadLatestResultObj: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, res) {
		t.Errorf("round-trip 不一致：\n got %+v\nwant %+v", got, res)
	}
}

// newMemDB 打开一个内存 sqlite 库并迁移建表。
func newMemDB(t *testing.T) *gorm.DB {
	t.Helper()
	st, err := store.Open("sqlite://file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return st.DB()
}

// 校验 Snapshot 节点排序稳定（确定性，便于上层 golden）。
func TestSnapshot_NodesSorted(t *testing.T) {
	sets := []*genv1.IngressSet{
		{NodeId: "C", Ingresses: []*genv1.Ingress{{Id: "c1", Confidence: 90}}},
		{NodeId: "A", Ingresses: []*genv1.Ingress{{Id: "a1", Confidence: 90}}},
		{NodeId: "B", Ingresses: []*genv1.Ingress{{Id: "b1", Confidence: 90}}},
	}
	r := fillReceiver(t, sets, nil)
	adapter := NewIngressSourceAdapter(r)
	nodes, _ := adapter.Snapshot()
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.NodeID
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("Snapshot 节点应按 NodeID 排序, got %v", ids)
	}
}
