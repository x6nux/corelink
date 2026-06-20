package topology

import (
	"reflect"
	"slices"
	"testing"
)

// helper: collect all edges as (from, to, w) tuples for easy assertion.
type edgeTuple struct {
	From Vertex
	To   Vertex
	W    uint64
}

func allEdges(g *Graph) []edgeTuple {
	var out []edgeTuple
	for _, v := range g.Vertices() {
		for _, e := range g.Neighbors(v) {
			out = append(out, edgeTuple{From: v, To: e.To, W: e.W})
		}
	}
	return out
}

func TestVertexEncodeDecode(t *testing.T) {
	// coreV round-trip
	n, ing, isCore := parseV(coreV("nodeA"))
	if n != "nodeA" || ing != "" || !isCore {
		t.Fatalf("parseV(coreV) got (%q,%q,%v), want (nodeA, \"\", true)", n, ing, isCore)
	}

	// ingV round-trip
	n2, ing2, isCore2 := parseV(ingV("nodeB", "e5"))
	if n2 != "nodeB" || ing2 != "e5" || isCore2 {
		t.Fatalf("parseV(ingV) got (%q,%q,%v), want (nodeB, e5, false)", n2, ing2, isCore2)
	}
}

func TestParseVInvalid(t *testing.T) {
	// 所有畸形/非法编码必须返回 node=="" (约定的非法标志)。
	//   ""                  : 空串，段数 1 -> 非法
	//   "x"                 : 无分隔符 -> 非法
	//   "a\x1fcore\x1fextra": 段数 3 但 token 非 "i" -> 非法
	//   "a\x1fz"            : 段数 2 但 token 非 "core" -> 非法
	//   "\x1fcore"          : node 段为空 -> 非法
	//   "a\x1fi"            : 段数 2 但 token 是 "i" 非 "core" -> 非法
	for _, bad := range []Vertex{"", "x", "a\x1fcore\x1fextra", "a\x1fz", "\x1fcore", "a\x1fi"} {
		if n, _, _ := parseV(bad); n != "" {
			t.Fatalf("parseV(%q) should be invalid (node==\"\"), got node=%q", bad, n)
		}
	}
}

func TestBuildGraphDisjointTransitsAndLeaves(t *testing.T) {
	// 钉住前置条件满足（transit/leaf NodeID 不相交、各自唯一、ingress 唯一）时的正常行为：
	// leaf 与 transit 共存，各顶点不重复，跨节点边与叶子接入边各就各位。
	transits := []NodeIngresses{
		{NodeID: "A", Ingresses: []string{"a1"}},
		{NodeID: "B", Ingresses: []string{"b1"}},
	}
	leaves := []NodeIngresses{
		{NodeID: "L", Ingresses: nil},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "L", DstNode: "A", DstIngress: "a1"}: 7,
	}
	g := BuildGraph(transits, leaves, qm)

	wantVerts := []Vertex{
		coreV("A"), ingV("A", "a1"),
		coreV("B"), ingV("B", "b1"),
		coreV("L"),
	}
	slices.Sort(wantVerts)
	if got := g.Vertices(); !reflect.DeepEqual(got, wantVerts) {
		t.Fatalf("Vertices mismatch:\n got=%v\nwant=%v", got, wantVerts)
	}

	// 每个顶点的出边数应确定，无重复建边。
	if nb := g.Neighbors(coreV("A")); len(nb) != 1 || nb[0] != (Edge{To: ingV("B", "b1"), W: 10}) {
		t.Fatalf("A:core neighbors = %v, want [{B:i:b1 10}]", nb)
	}
	if nb := g.Neighbors(coreV("L")); len(nb) != 1 || nb[0] != (Edge{To: ingV("A", "a1"), W: 7}) {
		t.Fatalf("L:core neighbors = %v, want [{A:i:a1 7}]", nb)
	}
	if nb := g.Neighbors(ingV("A", "a1")); len(nb) != 1 || nb[0] != (Edge{To: coreV("A"), W: 0}) {
		t.Fatalf("A:i:a1 neighbors = %v, want [{A:core 0}]", nb)
	}
}

func TestBuildGraphTwoTransits(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "A", Ingresses: []string{"a1"}},
		{NodeID: "B", Ingresses: []string{"b1", "b2"}},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "A", DstNode: "B", DstIngress: "b2"}: 20,
		{SrcNode: "B", DstNode: "A", DstIngress: "a1"}: 15,
	}

	g := BuildGraph(transits, nil, qm)

	// Vertex set assertion.
	wantVerts := []Vertex{
		coreV("A"), ingV("A", "a1"),
		coreV("B"), ingV("B", "b1"), ingV("B", "b2"),
	}
	slices.Sort(wantVerts)
	gotVerts := g.Vertices()
	if !reflect.DeepEqual(gotVerts, wantVerts) {
		t.Fatalf("Vertices mismatch:\n got=%v\nwant=%v", gotVerts, wantVerts)
	}

	// Edge set assertion (per-edge weight).
	want := map[edgeTuple]bool{
		{From: ingV("A", "a1"), To: coreV("A"), W: 0}:  true,
		{From: ingV("B", "b1"), To: coreV("B"), W: 0}:  true,
		{From: ingV("B", "b2"), To: coreV("B"), W: 0}:  true,
		{From: coreV("A"), To: ingV("B", "b1"), W: 10}: true,
		{From: coreV("A"), To: ingV("B", "b2"), W: 20}: true,
		{From: coreV("B"), To: ingV("A", "a1"), W: 15}: true,
	}
	got := allEdges(g)
	if len(got) != len(want) {
		t.Fatalf("edge count = %d, want %d; got=%v", len(got), len(want), got)
	}
	for _, e := range got {
		if !want[e] {
			t.Fatalf("unexpected edge %v; full=%v", e, got)
		}
	}
}

func TestBuildGraphLeaf(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "B", Ingresses: []string{"b1"}},
	}
	leaves := []NodeIngresses{
		{NodeID: "L", Ingresses: []string{"ignored"}}, // ingresses ignored for leaves
	}
	qm := QualityMatrix{
		{SrcNode: "L", DstNode: "B", DstIngress: "b1"}: 5,
	}

	g := BuildGraph(transits, leaves, qm)

	// L must only have core, no ingress vertices.
	for _, v := range g.Vertices() {
		n, _, isCore := parseV(v)
		if n == "L" && !isCore {
			t.Fatalf("leaf L should only have core vertex, found %v", v)
		}
	}

	// leaf access edge L:core -> B:i:b1 (5)
	want := edgeTuple{From: coreV("L"), To: ingV("B", "b1"), W: 5}
	found := false
	for _, e := range allEdges(g) {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing leaf access edge %v; edges=%v", want, allEdges(g))
	}
}

func TestBuildGraphMissingQMNoEdge(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "A", Ingresses: []string{"a1"}},
		{NodeID: "B", Ingresses: []string{"b1"}},
	}
	// Only A->B:b1 present; B->A missing.
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
	}
	g := BuildGraph(transits, nil, qm)

	// No cross edge from B:core.
	if nb := g.Neighbors(coreV("B")); len(nb) != 0 {
		t.Fatalf("B:core should have no cross edges (missing qm), got %v", nb)
	}
}

func TestNeighborsDeterministicSort(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "A", Ingresses: []string{"a1"}},
		{NodeID: "B", Ingresses: []string{"b2", "b1"}}, // intentionally unsorted input
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "A", DstNode: "B", DstIngress: "b2"}: 20,
	}
	g := BuildGraph(transits, nil, qm)

	nb := g.Neighbors(coreV("A"))
	if len(nb) != 2 {
		t.Fatalf("expected 2 neighbors, got %v", nb)
	}
	// Must be sorted by To vertex lexicographically.
	if !(nb[0].To < nb[1].To) {
		t.Fatalf("neighbors not sorted: %v", nb)
	}
	if nb[0].To != ingV("B", "b1") || nb[1].To != ingV("B", "b2") {
		t.Fatalf("neighbor order wrong: %v", nb)
	}
}

func TestVerticesDeterministicSort(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "Z", Ingresses: []string{"z1"}},
		{NodeID: "A", Ingresses: []string{"a1"}},
	}
	g := BuildGraph(transits, nil, QualityMatrix{})
	vs := g.Vertices()
	if !slices.IsSorted(vs) {
		t.Fatalf("Vertices not sorted: %v", vs)
	}
}

func TestNeighborsReturnsCopy(t *testing.T) {
	transits := []NodeIngresses{
		{NodeID: "A", Ingresses: []string{"a1"}},
		{NodeID: "B", Ingresses: []string{"b1"}},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
	}
	g := BuildGraph(transits, nil, qm)
	nb := g.Neighbors(coreV("A"))
	if len(nb) == 0 {
		t.Fatal("expected neighbors")
	}
	nb[0].W = 999 // mutate caller copy
	nb2 := g.Neighbors(coreV("A"))
	if nb2[0].W != 10 {
		t.Fatalf("Neighbors should return a copy; internal mutated to %d", nb2[0].W)
	}
}
