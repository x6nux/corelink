package topology

import (
	"reflect"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// reachIng 构造一个 Reachable 高置信入口。
func reachIng(id string) IngressMeta {
	return IngressMeta{ID: id, Confidence: 90, Reachable: true}
}

// unreachIng 构造一个不可达入口（不计资格、不计入口索引）。
func unreachIng(id string) IngressMeta {
	return IngressMeta{ID: id, Confidence: 90, Reachable: false}
}

// fixtureInput 构造一个确定性的 golden 输入：
//
//   - A,B,C：OPEN，各 1 个 Reachable 入口（中转）。
//   - L：SYMMETRIC，无 Reachable 入口（叶子）。
//   - 质量矩阵：A-B-C 两两互通；L 可拨 A、B（A 更优）。
//
// 入口命名：节点小写 + "-i"，如 A→"a-i"。
func fixtureInput() OptimizerInput {
	nodes := []NodeEligibilityInput{
		{NodeID: "A", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("a-i")}},
		{NodeID: "B", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("b-i")}},
		{NodeID: "C", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("c-i")}},
		{NodeID: "L", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{unreachIng("l-i")}},
	}
	qm := QualityMatrix{
		// A-B 互通（权 10）。
		{SrcNode: "A", DstNode: "B", DstIngress: "b-i"}: 10,
		{SrcNode: "B", DstNode: "A", DstIngress: "a-i"}: 10,
		// B-C 互通（权 10）。
		{SrcNode: "B", DstNode: "C", DstIngress: "c-i"}: 10,
		{SrcNode: "C", DstNode: "B", DstIngress: "b-i"}: 10,
		// A-C 互通（权 30，较差，使 A→C 走 A→B→C 更优）。
		{SrcNode: "A", DstNode: "C", DstIngress: "c-i"}: 30,
		{SrcNode: "C", DstNode: "A", DstIngress: "a-i"}: 30,
		// 叶子 L 接入：L→A 权 5（最优），L→B 权 20。
		{SrcNode: "L", DstNode: "A", DstIngress: "a-i"}: 5,
		{SrcNode: "L", DstNode: "B", DstIngress: "b-i"}: 20,
	}
	return OptimizerInput{
		Version:    42,
		Nodes:      nodes,
		Quality:    qm,
		MaxPeers:   8,
		IngressK:   2,
		RouteK:     3,
		ProbeFull:  true,
		ProbeLimit: 0,
	}
}

func TestOptimize_Roles(t *testing.T) {
	r := Optimize(fixtureInput())
	want := map[string]Role{"A": RoleTransit, "B": RoleTransit, "C": RoleTransit, "L": RoleLeaf}
	if !reflect.DeepEqual(r.Roles, want) {
		t.Errorf("Roles = %v, want %v", r.Roles, want)
	}
}

func TestOptimize_VersionPassthrough(t *testing.T) {
	in := fixtureInput()
	in.Version = 12345
	if got := Optimize(in).Version; got != 12345 {
		t.Errorf("Version = %d, want 12345", got)
	}
}

func TestOptimize_Idempotent(t *testing.T) {
	in := fixtureInput()
	r1 := Optimize(in)
	r2 := Optimize(in)
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("Optimize not idempotent:\n r1=%#v\n r2=%#v", r1, r2)
	}
}

func TestOptimize_IdempotentSampling(t *testing.T) {
	in := fixtureInput()
	in.ProbeFull = false
	in.ProbeLimit = 1
	r1 := Optimize(in)
	r2 := Optimize(in)
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("Optimize (sampling) not idempotent:\n r1=%#v\n r2=%#v", r1, r2)
	}
}

func TestOptimize_TransitNeighbors(t *testing.T) {
	r := Optimize(fixtureInput())
	// B 应与 A、C 互联（B 是中心节点）。
	bn := neighborMap(r.Neighbors["B"])
	if _, ok := bn["A"]; !ok {
		t.Errorf("B missing neighbor A; got %v", r.Neighbors["B"])
	}
	if _, ok := bn["C"]; !ok {
		t.Errorf("B missing neighbor C; got %v", r.Neighbors["B"])
	}
	// B→A 选用 A 的入口 a-i。
	if got := bn["A"]; !reflect.DeepEqual(got, []string{"a-i"}) {
		t.Errorf("B->A ingresses = %v, want [a-i]", got)
	}
	// 对称：A 也应有 B 作为邻居。
	an := neighborMap(r.Neighbors["A"])
	if _, ok := an["B"]; !ok {
		t.Errorf("A missing neighbor B; got %v", r.Neighbors["A"])
	}
	// 叶子 L 不应出现在任何中转的互联邻居里（接入边独立）。
	for _, tr := range []string{"A", "B", "C"} {
		if _, ok := neighborMap(r.Neighbors[tr])["L"]; ok {
			t.Errorf("transit %s should not have leaf L as interconnect neighbor", tr)
		}
	}
}

func TestOptimize_LeafUplinks(t *testing.T) {
	r := Optimize(fixtureInput())
	// L 接入 top-2 就近中转：L→A(5) 与 L→B(20)，按质量选中 A、B（C 无 qm）。
	ln := r.Neighbors["L"]
	got := neighborMap(ln)
	if len(got) != 2 {
		t.Fatalf("L uplinks count = %d, want 2; got %v", len(got), ln)
	}
	if ings, ok := got["A"]; !ok || !reflect.DeepEqual(ings, []string{"a-i"}) {
		t.Errorf("L->A = %v (ok=%v), want [a-i]", ings, ok)
	}
	if ings, ok := got["B"]; !ok || !reflect.DeepEqual(ings, []string{"b-i"}) {
		t.Errorf("L->B = %v (ok=%v), want [b-i]", ings, ok)
	}
	if _, ok := got["C"]; ok {
		t.Errorf("L should not uplink to C (no qm); got %v", ln)
	}
}

func TestOptimize_LeafUplinkTopN(t *testing.T) {
	// 3 个可达中转，N=2 时只选最优 2 个。
	in := fixtureInput()
	// 给 L→C 也配置质量（较差，权 50），验证 top-2 仍只选 A、B。
	in.Quality[QKey{SrcNode: "L", DstNode: "C", DstIngress: "c-i"}] = 50
	r := Optimize(in)
	got := neighborMap(r.Neighbors["L"])
	if len(got) != 2 {
		t.Fatalf("L uplinks = %v, want exactly 2 (top-N)", r.Neighbors["L"])
	}
	if _, ok := got["C"]; ok {
		t.Errorf("L should not select C (worst of 3, N=2); got %v", r.Neighbors["L"])
	}
}

func TestOptimize_Baseline(t *testing.T) {
	r := Optimize(fixtureInput())
	// A→C 应有基准路由（直连 30 或经 B 的 10+10=20）。
	paths, ok := r.Baseline[RoutePair{Src: "A", Dst: "C"}]
	if !ok || len(paths) == 0 {
		t.Fatalf("missing baseline A->C; got %v", r.Baseline)
	}
	// 最优路由应经 B（A→B→C 权 20 < 直连 30）。第一条 hop 序列末跳到 C。
	first := paths[0]
	if len(first) == 0 || first[len(first)-1].Node != "C" {
		t.Errorf("A->C first route should end at C; got %v", first)
	}
	// 经 B 中转：hop 序列应含到 B 的跳。
	hasB := false
	for _, h := range first {
		if h.Node == "B" {
			hasB = true
		}
	}
	if !hasB {
		t.Errorf("optimal A->C route should transit B (20<30); got %v", first)
	}
	// 叶子 L 不应作为基准路由的源或目的。
	for pair := range r.Baseline {
		if pair.Src == "L" || pair.Dst == "L" {
			t.Errorf("baseline should not involve leaf L; got pair %v", pair)
		}
	}
}

func TestOptimize_ProbeSetsFull(t *testing.T) {
	r := Optimize(fixtureInput())
	// 全集模式（M-3）：探测目标只含有 Reachable 入口的节点（A/B/C）；叶子 L 无入口
	// 不作任何人的目标，但 L 仍是发起方（探 A/B/C）。
	wantCount := map[string]int{
		"A": 2, // B,C
		"B": 2, // A,C
		"C": 2, // A,B
		"L": 3, // A,B,C
	}
	for _, n := range []string{"A", "B", "C", "L"} {
		ps := r.ProbeSets[n]
		if len(ps) != wantCount[n] {
			t.Errorf("node %s probe count = %d, want %d; got %v", n, len(ps), wantCount[n], ps)
		}
		for _, pt := range ps {
			if pt.NodeID == n {
				t.Errorf("node %s should not probe itself", n)
			}
			if pt.NodeID == "L" {
				t.Errorf("node %s should not probe leaf L (no reachable ingress)", n)
			}
		}
	}
	// 探测 A 的目标应带 A 的 Reachable 入口 a-i。
	for _, pt := range r.ProbeSets["B"] {
		if pt.NodeID == "A" && !reflect.DeepEqual(pt.IngressIDs, []string{"a-i"}) {
			t.Errorf("B probing A ingresses = %v, want [a-i]", pt.IngressIDs)
		}
	}
}

func TestOptimize_ProbeSymmetricClosure(t *testing.T) {
	in := fixtureInput()
	in.ProbeFull = false
	in.ProbeLimit = 1
	r := Optimize(in)
	// 入口集合：A/B/C 有入口，L 无入口（不可作探测目标）。
	hasIngress := func(id string) bool { return id == "A" || id == "B" || id == "C" }
	// 对称闭包：若 A 探 B，则 B 也探 A——前提双方都有入口（互为合法目标）。
	// L 无入口：L 可作发起方探有入口目标，但目标无法对称探回 L（M-3）。
	for src, targets := range r.ProbeSets {
		for _, pt := range targets {
			// 目标必有入口（M-3）。
			if !hasIngress(pt.NodeID) {
				t.Errorf("%s probes %s which has no reachable ingress (should not be a target)", src, pt.NodeID)
			}
			// 仅当发起方也有入口时才要求对称（否则无法对称探回无入口的 src）。
			if hasIngress(src) && !probes(r.ProbeSets, pt.NodeID, src) {
				t.Errorf("asymmetric probe: %s probes %s but %s does not probe %s",
					src, pt.NodeID, pt.NodeID, src)
			}
		}
	}
	// M-3：叶子 L（无入口）不应成为任何节点的探测目标，即便采样 + 对称闭包。
	for src := range r.ProbeSets {
		if probes(r.ProbeSets, src, "L") {
			t.Errorf("%s should not probe leaf L (no reachable ingress)", src)
		}
	}
}

func TestOptimize_LeafUplinksParam(t *testing.T) {
	// I-2：LeafUplinks 字段生效。给 L→C 也配质量，3 个可达中转下 LeafUplinks=1
	// 应只选最优 1 个（L→A 权 5）。
	in := fixtureInput()
	in.Quality[QKey{SrcNode: "L", DstNode: "C", DstIngress: "c-i"}] = 50
	in.LeafUplinks = 1
	r := Optimize(in)
	got := neighborMap(r.Neighbors["L"])
	if len(got) != 1 {
		t.Fatalf("LeafUplinks=1: L uplinks = %v, want exactly 1", r.Neighbors["L"])
	}
	if _, ok := got["A"]; !ok {
		t.Errorf("LeafUplinks=1: L should select best uplink A; got %v", r.Neighbors["L"])
	}
	// LeafUplinks<=0 回落默认 2。
	in.LeafUplinks = 0
	r2 := Optimize(in)
	if got2 := neighborMap(r2.Neighbors["L"]); len(got2) != 2 {
		t.Errorf("LeafUplinks=0 (fallback 2): L uplinks = %v, want 2", r2.Neighbors["L"])
	}
}

func TestOptimize_LeafSymmetricNoStableIngress(t *testing.T) {
	// 仅 SYMMETRIC + 无 Reachable 入口 → Leaf；有 Reachable 入口 → Transit。
	in := OptimizerInput{
		Version: 1,
		Nodes: []NodeEligibilityInput{
			{NodeID: "sym-leaf", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{unreachIng("x")}},
			{NodeID: "sym-pub", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{reachIng("y")}},
		},
		MaxPeers: 8, IngressK: 2, RouteK: 3, ProbeFull: true,
	}
	r := Optimize(in)
	if r.Roles["sym-leaf"] != RoleLeaf {
		t.Errorf("sym-leaf should be Leaf, got %v", r.Roles["sym-leaf"])
	}
	if r.Roles["sym-pub"] != RoleTransit {
		t.Errorf("sym-pub (has reachable ingress) should be Transit, got %v", r.Roles["sym-pub"])
	}
}

// --- helpers ---

// neighborMap 把 []NeighborSpec 转成 NodeID→Ingresses 映射，便于断言。
func neighborMap(specs []NeighborSpec) map[string][]string {
	out := map[string][]string{}
	for _, s := range specs {
		out[s.NodeID] = s.Ingresses
	}
	return out
}

// probes 判断 src 的探测集中是否含目标 dst。
func probes(sets map[string][]ProbeTarget, src, dst string) bool {
	for _, pt := range sets[src] {
		if pt.NodeID == dst {
			return true
		}
	}
	return false
}
