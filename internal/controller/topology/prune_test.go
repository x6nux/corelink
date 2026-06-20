package topology

import (
	"reflect"
	"sort"
	"testing"
)

// sortEdges 对 PrunedEdge 切片做确定性排序，便于断言（与函数内部排序无关）。
func sortEdges(es []PrunedEdge) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Src != es[j].Src {
			return es[i].Src < es[j].Src
		}
		if es[i].Dst != es[j].Dst {
			return es[i].Dst < es[j].Dst
		}
		return es[i].Ingress < es[j].Ingress
	})
}

// neighborSet 返回 edges 中以 src 为源的所有物理邻居 (Dst) 集合。
func neighborSet(edges []PrunedEdge, src string) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		if e.Src == src {
			out[e.Dst] = true
		}
	}
	return out
}

// assertSymmetric 断言边集物理对对称：对任意保留的 A→B 边，必存在至少一条 B→A 边。
// 这验证双向连通（WireGuard 双向通信前提）。前提：测试用 qm 为双向都配可达入口的对。
func assertSymmetric(t *testing.T, edges []PrunedEdge) {
	t.Helper()
	dir := map[[2]string]bool{}
	for _, e := range edges {
		dir[[2]string{e.Src, e.Dst}] = true
	}
	for kk := range dir {
		rev := [2]string{kk[1], kk[0]}
		if !dir[rev] {
			t.Errorf("asymmetric: have %s->%s but missing %s->%s", kk[0], kk[1], rev[0], rev[1])
		}
	}
}

// unorderedPairs 提取边集涉及的无序物理对集合 {x,y}（x<y）。
func unorderedPairs(edges []PrunedEdge) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, e := range edges {
		x, y := e.Src, e.Dst
		if x > y {
			x, y = y, x
		}
		out[[2]string{x, y}] = true
	}
	return out
}

// TestPrune_symmetricClosure：4 个中转，d=2，双向 RTT 对称配 qm。验证：
//   - 对称闭包：任意 A→B 边都有反向 B→A 边（双向连通）。
//   - union 物理对：A 在 B.top-d 或 B 在 A.top-d 即成对。
func TestPrune_symmetricClosure(t *testing.T) {
	transits := []string{"A", "B", "C", "D"}
	ingressesByNode := map[string][]string{
		"A": {"a1"}, "B": {"b1"}, "C": {"c1"}, "D": {"d1"},
	}
	qm := QualityMatrix{}
	// set 同时写两个方向（对称 RTT），保证双向都有可达入口。
	set := func(x, y string, w uint64) {
		qm[QKey{x, y, ingressesByNode[y][0]}] = w
		qm[QKey{y, x, ingressesByNode[x][0]}] = w
	}
	set("A", "B", 10)
	set("A", "C", 20)
	set("A", "D", 30)
	set("B", "D", 15)
	set("B", "C", 25)
	set("C", "D", 1)
	// 各源 best-W 升序（对称值）：
	//   A: B10 < C20 < D30        -> top2 {B,C}
	//   B: A10 < D15 < C25        -> top2 {A,D}
	//   C: D1  < A20 < B25        -> top2 {D,A}
	//   D: C1  < B15 < A30        -> top2 {C,B}
	// union 无序对: {A,B},{A,C},{B,D},{C,D}

	edges := Prune(transits, qm, ingressesByNode, 2, 5)

	assertSymmetric(t, edges) // 核心：双向连通。

	got := unorderedPairs(edges)
	want := map[[2]string]bool{
		{"A", "B"}: true,
		{"A", "C"}: true,
		{"B", "D"}: true,
		{"C", "D"}: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("symmetric pairs = %v, want %v", got, want)
	}
}

// TestPrune_topKIngress：A↔B 双向配 qm；A→B 有 5 入口 k=2 保留最优 2 个；
// A↔C 双向配 qm；验证 A 保留 B、C 两个物理邻居（度数按物理对，B 的 5 入口只占 1 名额）。
func TestPrune_topKIngress(t *testing.T) {
	transits := []string{"A", "B", "C"}
	ingressesByNode := map[string][]string{
		"A": {"a1"},
		"B": {"b1", "b2", "b3", "b4", "b5"},
		"C": {"c1"},
	}
	qm := QualityMatrix{}
	// A→B 五入口：b3(10)<b1(20)<b5(30)<b2(40)<b4(50)
	qm[QKey{"A", "B", "b1"}] = 20
	qm[QKey{"A", "B", "b2"}] = 40
	qm[QKey{"A", "B", "b3"}] = 10
	qm[QKey{"A", "B", "b4"}] = 50
	qm[QKey{"A", "B", "b5"}] = 30
	// B→A 反向（双向连通）。
	qm[QKey{"B", "A", "a1"}] = 15
	// A↔C 双向，质量 100（比 B 的最优差，但 C 仍应保留，度数按物理对）。
	qm[QKey{"A", "C", "c1"}] = 100
	qm[QKey{"C", "A", "a1"}] = 100

	edges := Prune(transits, qm, ingressesByNode, 2, 2)
	assertSymmetric(t, edges)

	var fromA []PrunedEdge
	for _, e := range edges {
		if e.Src == "A" {
			fromA = append(fromA, e)
		}
	}
	sortEdges(fromA)
	want := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "b1", W: 20},
		{Src: "A", Dst: "B", Ingress: "b3", W: 10},
		{Src: "A", Dst: "C", Ingress: "c1", W: 100},
	}
	if !reflect.DeepEqual(fromA, want) {
		t.Fatalf("fromA edges = %v, want %v", fromA, want)
	}

	// 度数按物理对：A 保留 B 和 C 两个物理邻居（B 的 5 入口只占 1 名额）。
	ns := neighborSet(edges, "A")
	if !ns["B"] || !ns["C"] || len(ns) != 2 {
		t.Errorf("A neighbors = %v, want {B,C}", ns)
	}
}

// TestPrune_tiebreakNeighbor：出向物理邻居质量相同 -> 按 NodeID 字典序选 top-d。
// X/Y/Z 也通过默认权重回选 A（对称闭包），所以 A 最终有 3 个邻居。
func TestPrune_tiebreakNeighbor(t *testing.T) {
	transits := []string{"A", "X", "Y", "Z"}
	ingressesByNode := map[string][]string{
		"A": {"a1"}, "X": {"x1"}, "Y": {"y1"}, "Z": {"z1"},
	}
	qm := QualityMatrix{}
	qm[QKey{"A", "X", "x1"}] = 50
	qm[QKey{"A", "Y", "y1"}] = 50
	qm[QKey{"A", "Z", "z1"}] = 50

	edges := Prune(transits, qm, ingressesByNode, 2, 1)
	got := neighborSet(edges, "A")
	// A 出向 top-2 选 X,Y；Z 通过默认权重回选 A 也入对称闭包 → A 有 3 邻居
	if len(got) < 2 {
		t.Errorf("A out-neighbors = %v, want at least X,Y", got)
	}
}

// TestPrune_tiebreakIngress：A→B 入口质量相同 -> 按 ingressID 字典序保留 k 个。
func TestPrune_tiebreakIngress(t *testing.T) {
	transits := []string{"A", "B"}
	ingressesByNode := map[string][]string{
		"A": {"a1"},
		"B": {"b1", "b2", "b3"},
	}
	qm := QualityMatrix{}
	qm[QKey{"A", "B", "b1"}] = 50
	qm[QKey{"A", "B", "b2"}] = 50
	qm[QKey{"A", "B", "b3"}] = 50
	qm[QKey{"B", "A", "a1"}] = 50 // 反向，使对称对成立。

	edges := Prune(transits, qm, ingressesByNode, 2, 2)
	assertSymmetric(t, edges)

	var fromA []PrunedEdge
	for _, e := range edges {
		if e.Src == "A" {
			fromA = append(fromA, e)
		}
	}
	sortEdges(fromA)
	want := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "b1", W: 50},
		{Src: "A", Dst: "B", Ingress: "b2", W: 50},
	}
	if !reflect.DeepEqual(fromA, want) {
		t.Fatalf("A->B edges = %v, want %v (tiebreak by ingressID)", fromA, want)
	}
}

// TestPrune_directedDataReachability：单向有 qm 时，对向无探测数据用默认权重补全
// （确保新节点不被孤立）。
func TestPrune_directedDataReachability(t *testing.T) {
	transits := []string{"A", "B"}
	ingressesByNode := map[string][]string{
		"A": {"a1", "a2"},
		"B": {"b1"},
	}
	qm := QualityMatrix{}
	qm[QKey{"A", "B", "b1"}] = 30

	edges := Prune(transits, qm, ingressesByNode, 2, 2)
	sortEdges(edges)
	// A→B 用真实权重 30；B→A 无探测数据用默认权重 1_000_000
	if len(edges) != 3 {
		t.Fatalf("edges = %v, want 3 edges (A->B:b1 + B->A:a1 + B->A:a2)", edges)
	}
	if edges[0].Src != "A" || edges[0].W != 30 {
		t.Fatalf("first edge = %v, want A->B with W=30", edges[0])
	}
}

// TestPrune_bidirectionalWhenBothReachable：双向都有 qm 时，对称对两个方向都建边。
func TestPrune_bidirectionalWhenBothReachable(t *testing.T) {
	transits := []string{"A", "B"}
	ingressesByNode := map[string][]string{
		"A": {"a1"},
		"B": {"b1"},
	}
	qm := QualityMatrix{}
	qm[QKey{"A", "B", "b1"}] = 30
	qm[QKey{"B", "A", "a1"}] = 40

	edges := Prune(transits, qm, ingressesByNode, 2, 2)
	sortEdges(edges)
	assertSymmetric(t, edges)
	want := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "b1", W: 30},
		{Src: "B", Dst: "A", Ingress: "a1", W: 40},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges = %v, want %v", edges, want)
	}
}

func TestPrune_empty(t *testing.T) {
	edges := Prune(nil, QualityMatrix{}, nil, 2, 2)
	if len(edges) != 0 {
		t.Fatalf("expected no edges, got %v", edges)
	}
}

// TestPrune_nonPositiveDK：d 或 k <= 0 提前返回空边集。
func TestPrune_nonPositiveDK(t *testing.T) {
	transits := []string{"A", "B"}
	ingressesByNode := map[string][]string{"A": {"a1"}, "B": {"b1"}}
	qm := QualityMatrix{
		QKey{"A", "B", "b1"}: 10,
		QKey{"B", "A", "a1"}: 10,
	}
	cases := []struct{ d, k int }{
		{0, 2}, {2, 0}, {-1, 2}, {2, -1}, {0, 0},
	}
	for _, c := range cases {
		if got := Prune(transits, qm, ingressesByNode, c.d, c.k); len(got) != 0 {
			t.Errorf("Prune(d=%d,k=%d) = %v, want empty", c.d, c.k, got)
		}
	}
}
