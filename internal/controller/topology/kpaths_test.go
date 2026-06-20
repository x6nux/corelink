package topology

import (
	"reflect"
	"testing"
)

// 菱形拓扑（拆点后，物理节点对 + 入口）：
//
//	A →(e_ab)→ B →(e_bd)→ D
//	A →(e_ac)→ C →(e_cd)→ D
//	B →(e_bc)→ C            （提供次优路径 A→B→C→D）
//
// 边权设计（W 越小越优）：
//
//	A→B : 1   A→C : 1
//	B→D : 1   C→D : 1
//	B→C : 1
//
// 两条等价最优路径 A→B→D / A→C→D 总权重相同 = 2（不含 0 权内部边）。
// 次优 A→B→C→D 总权重 = 3。
func diamondEdges() []PrunedEdge {
	return []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "e_ab", W: 1},
		{Src: "A", Dst: "C", Ingress: "e_ac", W: 1},
		{Src: "B", Dst: "D", Ingress: "e_bd", W: 1},
		{Src: "C", Dst: "D", Ingress: "e_cd", W: 1},
		{Src: "B", Dst: "C", Ingress: "e_bc", W: 1},
	}
}

func TestBuildSplitGraph_CrossAndInternalEdges(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())

	// 跨节点边 coreV(A) → ingV(B,e_ab) 权 1。
	found := false
	for _, e := range g.Neighbors(coreV("A")) {
		if e.To == ingV("B", "e_ab") {
			found = true
			if e.W != 1 {
				t.Fatalf("coreV(A)->ingV(B,e_ab) W=%d, want 1", e.W)
			}
		}
	}
	if !found {
		t.Fatalf("missing cross edge coreV(A)->ingV(B,e_ab)")
	}

	// 内部边 ingV(B,e_ab) → coreV(B) 权 0。
	ints := g.Neighbors(ingV("B", "e_ab"))
	if len(ints) != 1 || ints[0].To != coreV("B") || ints[0].W != 0 {
		t.Fatalf("internal edge ingV(B,e_ab) wrong: %+v", ints)
	}
}

func TestBuildSplitGraph_DedupInternalEdge(t *testing.T) {
	// 两条 PrunedEdge 指向同一 ingV → 内部边只加一次。
	edges := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "e1", W: 1},
		{Src: "C", Dst: "B", Ingress: "e1", W: 2}, // 同 ingV(B,e1)
	}
	g := BuildSplitGraph(edges)
	ints := g.Neighbors(ingV("B", "e1"))
	if len(ints) != 1 {
		t.Fatalf("internal edge ingV(B,e1)->coreV(B) deduped wrong: %+v", ints)
	}
}

func TestKShortest_Diamond_K2(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	paths := KShortest(g, coreV("A"), coreV("D"), 2)

	if len(paths) != 2 {
		t.Fatalf("want 2 paths, got %d: %v", len(paths), paths)
	}

	// 两条等价最优路径，tiebreak 按顶点序列字典序。
	// 路径1 经 B：A∶core→B∶e_ab→B∶core→D∶e_bd→D∶core
	want0 := []Vertex{coreV("A"), ingV("B", "e_ab"), coreV("B"), ingV("D", "e_bd"), coreV("D")}
	// 路径2 经 C：A∶core→C∶e_ac→C∶core→D∶e_cd→D∶core
	want1 := []Vertex{coreV("A"), ingV("C", "e_ac"), coreV("C"), ingV("D", "e_cd"), coreV("D")}

	if !reflect.DeepEqual(paths[0], want0) {
		t.Errorf("path0:\n got %v\nwant %v", paths[0], want0)
	}
	if !reflect.DeepEqual(paths[1], want1) {
		t.Errorf("path1:\n got %v\nwant %v", paths[1], want1)
	}
}

func TestKShortest_Diamond_K3_IncludesSuboptimal(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	paths := KShortest(g, coreV("A"), coreV("D"), 3)

	if len(paths) != 3 {
		t.Fatalf("want 3 paths, got %d: %v", len(paths), paths)
	}
	// 第3条是次优 A→B→C→D，权重 3。
	want2 := []Vertex{
		coreV("A"), ingV("B", "e_ab"), coreV("B"),
		ingV("C", "e_bc"), coreV("C"),
		ingV("D", "e_cd"), coreV("D"),
	}
	if !reflect.DeepEqual(paths[2], want2) {
		t.Errorf("path2:\n got %v\nwant %v", paths[2], want2)
	}
}

func TestKShortest_KExceedsAvailable(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	// 总共只有 3 条简单路径 A→D，K=10 → 返回全部 3 条不重复。
	paths := KShortest(g, coreV("A"), coreV("D"), 10)
	if len(paths) != 3 {
		t.Fatalf("want 3 paths (all distinct), got %d: %v", len(paths), paths)
	}
	// 校验不重复。
	seen := map[string]bool{}
	for _, p := range paths {
		key := ""
		for _, v := range p {
			key += string(v) + "|"
		}
		if seen[key] {
			t.Fatalf("duplicate path: %v", p)
		}
		seen[key] = true
	}
}

func TestKShortest_Unreachable(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	// X 不存在 → 不可达返回空。
	paths := KShortest(g, coreV("A"), coreV("X"), 2)
	if len(paths) != 0 {
		t.Fatalf("want 0 paths for unreachable, got %d: %v", len(paths), paths)
	}
}

func TestKShortest_SrcEqualsDst(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	paths := KShortest(g, coreV("A"), coreV("A"), 2)
	if len(paths) != 0 {
		t.Fatalf("src==dst want empty, got %v", paths)
	}
}

func TestKShortest_KZeroOrNegative(t *testing.T) {
	g := BuildSplitGraph(diamondEdges())
	if got := KShortest(g, coreV("A"), coreV("D"), 0); len(got) != 0 {
		t.Fatalf("K=0 want empty, got %v", got)
	}
	if got := KShortest(g, coreV("A"), coreV("D"), -1); len(got) != 0 {
		t.Fatalf("K<0 want empty, got %v", got)
	}
}

func TestBaselineRoutes_Compression(t *testing.T) {
	routes := BaselineRoutes(diamondEdges(), []string{"A", "B", "C", "D"}, 2)

	got := routes[RoutePair{Src: "A", Dst: "D"}]
	if len(got) != 2 {
		t.Fatalf("(A,D) want 2 hop-seqs, got %d: %v", len(got), got)
	}
	// 压缩：A∶core→B∶e_ab→B∶core→D∶e_bd→D∶core → [{B,e_ab},{D,e_bd}]
	want0 := []Hop{{Node: "B", Ingress: "e_ab"}, {Node: "D", Ingress: "e_bd"}}
	want1 := []Hop{{Node: "C", Ingress: "e_ac"}, {Node: "D", Ingress: "e_cd"}}
	if !reflect.DeepEqual(got[0], want0) {
		t.Errorf("hops0:\n got %v\nwant %v", got[0], want0)
	}
	if !reflect.DeepEqual(got[1], want1) {
		t.Errorf("hops1:\n got %v\nwant %v", got[1], want1)
	}
}

func TestBaselineRoutes_SingleHopDirect(t *testing.T) {
	// 单跳直连 A→B：A∶core→B∶e→B∶core 压缩成 [{B,e}]。
	edges := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "e", W: 5},
		{Src: "B", Dst: "A", Ingress: "f", W: 5},
	}
	routes := BaselineRoutes(edges, []string{"A", "B"}, 2)
	got := routes[RoutePair{Src: "A", Dst: "B"}]
	if len(got) != 1 {
		t.Fatalf("(A,B) want 1 hop-seq, got %d: %v", len(got), got)
	}
	want := []Hop{{Node: "B", Ingress: "e"}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("hops:\n got %v\nwant %v", got[0], want)
	}
}

func TestBaselineRoutes_UnreachablePairNotInMap(t *testing.T) {
	// D 只有入站边，没有从 D 出发的边 → (D, anything) 不可达，不入 map。
	routes := BaselineRoutes(diamondEdges(), []string{"A", "B", "C", "D"}, 2)
	if _, ok := routes[RoutePair{Src: "D", Dst: "A"}]; ok {
		t.Fatalf("(D,A) unreachable should not be in map")
	}
	// 自反对不入 map。
	if _, ok := routes[RoutePair{Src: "A", Dst: "A"}]; ok {
		t.Fatalf("(A,A) self-pair should not be in map")
	}
}

func TestBaselineRoutes_Determinism(t *testing.T) {
	// 同输入多次运行结果完全一致。
	for i := 0; i < 5; i++ {
		r1 := BaselineRoutes(diamondEdges(), []string{"A", "B", "C", "D"}, 3)
		r2 := BaselineRoutes(diamondEdges(), []string{"D", "C", "B", "A"}, 3)
		if !reflect.DeepEqual(r1, r2) {
			t.Fatalf("non-deterministic: transit order changed result")
		}
	}
}
