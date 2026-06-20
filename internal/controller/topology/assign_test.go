package topology

import (
	"reflect"
	"testing"
)

// ─── sortedKeys 测试 ─────────────────────────────────────────────────────────

func TestSortedKeys_Empty(t *testing.T) {
	got := sortedKeys(nil)
	if len(got) != 0 {
		t.Errorf("期望空切片，实际 %v", got)
	}
}

func TestSortedKeys_Sorted(t *testing.T) {
	m := map[string]bool{"c": true, "a": true, "b": true}
	got := sortedKeys(m)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedKeys = %v, 期望 %v", got, want)
	}
}

func TestSortedKeys_SingleElement(t *testing.T) {
	m := map[string]bool{"only": true}
	got := sortedKeys(m)
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("sortedKeys = %v", got)
	}
}

// ─── bestQuality 测试 ────────────────────────────────────────────────────────

func TestBestQuality_NoEntries(t *testing.T) {
	// 无任何 qm 条目时返回 ok=false。
	qm := QualityMatrix{}
	_, ok := bestQuality("A", "B", []string{"e1", "e2"}, qm)
	if ok {
		t.Fatal("无 qm 条目时 ok 应为 false")
	}
}

func TestBestQuality_SingleEntry(t *testing.T) {
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "e1"}: 100,
	}
	best, ok := bestQuality("A", "B", []string{"e1"}, qm)
	if !ok {
		t.Fatal("应有结果")
	}
	if best != 100 {
		t.Errorf("best = %d, 期望 100", best)
	}
}

func TestBestQuality_SelectsMinimum(t *testing.T) {
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "e1"}: 200,
		{SrcNode: "A", DstNode: "B", DstIngress: "e2"}: 50,
		{SrcNode: "A", DstNode: "B", DstIngress: "e3"}: 150,
	}
	best, ok := bestQuality("A", "B", []string{"e1", "e2", "e3"}, qm)
	if !ok {
		t.Fatal("应有结果")
	}
	if best != 50 {
		t.Errorf("best = %d, 期望 50（最小值）", best)
	}
}

func TestBestQuality_PartialEntries(t *testing.T) {
	// 只有部分入口在 qm 中。
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "e2"}: 300,
	}
	best, ok := bestQuality("A", "B", []string{"e1", "e2", "e3"}, qm)
	if !ok {
		t.Fatal("e2 在 qm 中，应返回 ok=true")
	}
	if best != 300 {
		t.Errorf("best = %d, 期望 300", best)
	}
}

func TestBestQuality_EmptyIngresses(t *testing.T) {
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "e1"}: 100,
	}
	_, ok := bestQuality("A", "B", nil, qm)
	if ok {
		t.Fatal("空入口列表应返回 ok=false")
	}
}

// ─── reachableIngressIDs 测试 ────────────────────────────────────────────────

func TestReachableIngressIDs_AllReachable(t *testing.T) {
	in := NodeEligibilityInput{
		Ingresses: []IngressMeta{
			{ID: "c", Reachable: true},
			{ID: "a", Reachable: true},
			{ID: "b", Reachable: true},
		},
	}
	got := reachableIngressIDs(in)
	want := []string{"a", "b", "c"} // 升序排序
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, 期望 %v", got, want)
	}
}

func TestReachableIngressIDs_MixedReachability(t *testing.T) {
	in := NodeEligibilityInput{
		Ingresses: []IngressMeta{
			{ID: "e1", Reachable: true},
			{ID: "e2", Reachable: false},
			{ID: "e3", Reachable: true},
		},
	}
	got := reachableIngressIDs(in)
	want := []string{"e1", "e3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, 期望 %v", got, want)
	}
}

func TestReachableIngressIDs_NoneReachable(t *testing.T) {
	in := NodeEligibilityInput{
		Ingresses: []IngressMeta{
			{ID: "e1", Reachable: false},
		},
	}
	got := reachableIngressIDs(in)
	if len(got) != 0 {
		t.Errorf("全不可达时应返回空，实际 %v", got)
	}
}

func TestReachableIngressIDs_Empty(t *testing.T) {
	in := NodeEligibilityInput{}
	got := reachableIngressIDs(in)
	if len(got) != 0 {
		t.Errorf("无入口时应返回空，实际 %v", got)
	}
}

// ─── buildIngressIndex 测试 ──────────────────────────────────────────────────

func TestBuildIngressIndex(t *testing.T) {
	nodes := []NodeEligibilityInput{
		{NodeID: "A", Ingresses: []IngressMeta{
			{ID: "a1", Reachable: true}, {ID: "a2", Reachable: false},
		}},
		{NodeID: "B", Ingresses: []IngressMeta{
			{ID: "b1", Reachable: true}, {ID: "b2", Reachable: true},
		}},
		{NodeID: "C"}, // 无入口
	}
	idx := buildIngressIndex(nodes)
	if got := idx["A"]; !reflect.DeepEqual(got, []string{"a1"}) {
		t.Errorf("A 入口 = %v, 期望 [a1]", got)
	}
	if got := idx["B"]; !reflect.DeepEqual(got, []string{"b1", "b2"}) {
		t.Errorf("B 入口 = %v, 期望 [b1 b2]", got)
	}
	if got := idx["C"]; len(got) != 0 {
		t.Errorf("C 无入口时应为空，实际 %v", got)
	}
}

// ─── buildNeighborsFromEdges 测试 ────────────────────────────────────────────

func TestBuildNeighborsFromEdges_Empty(t *testing.T) {
	got := buildNeighborsFromEdges(nil)
	if len(got) != 0 {
		t.Errorf("空边集应返回空 map，实际 %v", got)
	}
}

func TestBuildNeighborsFromEdges_SingleEdge(t *testing.T) {
	edges := []PrunedEdge{{Src: "A", Dst: "B", Ingress: "e1"}}
	got := buildNeighborsFromEdges(edges)
	specs := got["A"]
	if len(specs) != 1 {
		t.Fatalf("A 邻居数 = %d, 期望 1", len(specs))
	}
	if specs[0].NodeID != "B" || !reflect.DeepEqual(specs[0].Ingresses, []string{"e1"}) {
		t.Errorf("A 邻居 = %+v", specs[0])
	}
}

func TestBuildNeighborsFromEdges_MultipleIngresses(t *testing.T) {
	// 同一对 Src→Dst 多条边（不同入口）应合并。
	edges := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "e2"},
		{Src: "A", Dst: "B", Ingress: "e1"},
		{Src: "A", Dst: "B", Ingress: "e3"},
	}
	got := buildNeighborsFromEdges(edges)
	specs := got["A"]
	if len(specs) != 1 {
		t.Fatalf("A 邻居数 = %d, 期望 1", len(specs))
	}
	want := []string{"e1", "e2", "e3"} // 升序
	if !reflect.DeepEqual(specs[0].Ingresses, want) {
		t.Errorf("入口 = %v, 期望 %v", specs[0].Ingresses, want)
	}
}

func TestBuildNeighborsFromEdges_DeterministicOrder(t *testing.T) {
	// 多个邻居按 NodeID 字典序。
	edges := []PrunedEdge{
		{Src: "A", Dst: "C", Ingress: "e1"},
		{Src: "A", Dst: "B", Ingress: "e1"},
	}
	got := buildNeighborsFromEdges(edges)
	specs := got["A"]
	if len(specs) != 2 {
		t.Fatalf("A 邻居数 = %d, 期望 2", len(specs))
	}
	if specs[0].NodeID != "B" || specs[1].NodeID != "C" {
		t.Errorf("邻居顺序错误: %s, %s", specs[0].NodeID, specs[1].NodeID)
	}
}

func TestBuildNeighborsFromEdges_Bidirectional(t *testing.T) {
	// 对称边集：A→B + B→A。
	edges := []PrunedEdge{
		{Src: "A", Dst: "B", Ingress: "e1"},
		{Src: "B", Dst: "A", Ingress: "e2"},
	}
	got := buildNeighborsFromEdges(edges)
	if len(got["A"]) != 1 || got["A"][0].NodeID != "B" {
		t.Errorf("A 邻居 = %+v", got["A"])
	}
	if len(got["B"]) != 1 || got["B"][0].NodeID != "A" {
		t.Errorf("B 邻居 = %+v", got["B"])
	}
}

// ─── assignLeafUplinks 测试 ──────────────────────────────────────────────────

func TestAssignLeafUplinks_BasicTopN(t *testing.T) {
	leaves := []string{"L1"}
	transits := []string{"T1", "T2", "T3"}
	qm := QualityMatrix{
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e1"}: 100,
		{SrcNode: "L1", DstNode: "T2", DstIngress: "e1"}: 50,
		{SrcNode: "L1", DstNode: "T3", DstIngress: "e1"}: 200,
	}
	idx := map[string][]string{"T1": {"e1"}, "T2": {"e1"}, "T3": {"e1"}}

	// top-2：应选 T2(50) 和 T1(100)。
	got := assignLeafUplinks(leaves, transits, qm, idx, 2)
	specs := got["L1"]
	if len(specs) != 2 {
		t.Fatalf("L1 接入中转数 = %d, 期望 2", len(specs))
	}
	// 输出按 NodeID 排序。
	if specs[0].NodeID != "T1" || specs[1].NodeID != "T2" {
		t.Errorf("接入中转 = [%s, %s], 期望 [T1, T2]", specs[0].NodeID, specs[1].NodeID)
	}
}

func TestAssignLeafUplinks_DefaultN(t *testing.T) {
	// n<=0 时回落 defaultLeafUplinks (2)。
	leaves := []string{"L1"}
	transits := []string{"T1", "T2", "T3"}
	qm := QualityMatrix{
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e1"}: 10,
		{SrcNode: "L1", DstNode: "T2", DstIngress: "e1"}: 20,
		{SrcNode: "L1", DstNode: "T3", DstIngress: "e1"}: 30,
	}
	idx := map[string][]string{"T1": {"e1"}, "T2": {"e1"}, "T3": {"e1"}}

	got := assignLeafUplinks(leaves, transits, qm, idx, 0)
	specs := got["L1"]
	if len(specs) != 2 {
		t.Fatalf("默认 n=2, L1 接入中转数 = %d", len(specs))
	}
}

func TestAssignLeafUplinks_NoReachableTransit(t *testing.T) {
	// 叶子无可达中转时不出现在结果中。
	leaves := []string{"L1"}
	transits := []string{"T1"}
	qm := QualityMatrix{} // 空 qm = 无可达
	idx := map[string][]string{"T1": {"e1"}}

	got := assignLeafUplinks(leaves, transits, qm, idx, 2)
	if _, ok := got["L1"]; ok {
		t.Error("无可达中转时 L1 不应出现在结果中")
	}
}

func TestAssignLeafUplinks_FewerTransitsThanN(t *testing.T) {
	// 可达中转数 < n 时返回全部可达中转。
	leaves := []string{"L1"}
	transits := []string{"T1"}
	qm := QualityMatrix{
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e1"}: 50,
	}
	idx := map[string][]string{"T1": {"e1"}}

	got := assignLeafUplinks(leaves, transits, qm, idx, 5)
	specs := got["L1"]
	if len(specs) != 1 || specs[0].NodeID != "T1" {
		t.Errorf("specs = %+v", specs)
	}
}

func TestAssignLeafUplinks_MultipleIngresses(t *testing.T) {
	// 中转有多个入口，选中后保留所有有 qm 的入口（升序）。
	leaves := []string{"L1"}
	transits := []string{"T1"}
	qm := QualityMatrix{
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e2"}: 100,
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e3"}: 200,
		// e1 不在 qm 中。
	}
	idx := map[string][]string{"T1": {"e1", "e2", "e3"}}

	got := assignLeafUplinks(leaves, transits, qm, idx, 2)
	specs := got["L1"]
	if len(specs) != 1 {
		t.Fatalf("specs 长度 = %d", len(specs))
	}
	want := []string{"e2", "e3"} // 仅 qm 中存在的入口，升序
	if !reflect.DeepEqual(specs[0].Ingresses, want) {
		t.Errorf("入口 = %v, 期望 %v", specs[0].Ingresses, want)
	}
}

func TestAssignLeafUplinks_TiebreakByNodeID(t *testing.T) {
	// 质量相同时按 NodeID 字典序选择。
	leaves := []string{"L1"}
	transits := []string{"T2", "T1", "T3"}
	qm := QualityMatrix{
		{SrcNode: "L1", DstNode: "T1", DstIngress: "e1"}: 100,
		{SrcNode: "L1", DstNode: "T2", DstIngress: "e1"}: 100,
		{SrcNode: "L1", DstNode: "T3", DstIngress: "e1"}: 100,
	}
	idx := map[string][]string{"T1": {"e1"}, "T2": {"e1"}, "T3": {"e1"}}

	got := assignLeafUplinks(leaves, transits, qm, idx, 2)
	specs := got["L1"]
	if len(specs) != 2 {
		t.Fatalf("specs 长度 = %d", len(specs))
	}
	// 质量相同，按 NodeID 字典序取前 2：T1, T2。
	// 输出再按 NodeID 排序。
	if specs[0].NodeID != "T1" || specs[1].NodeID != "T2" {
		t.Errorf("tiebreak 错误: [%s, %s]", specs[0].NodeID, specs[1].NodeID)
	}
}

// ─── buildProbeSets 测试 ─────────────────────────────────────────────────────

func TestBuildProbeSets_FullMode(t *testing.T) {
	// probeFull=true：每个节点探测所有有入口的其他节点。
	allNodes := []string{"A", "B", "C"}
	idx := map[string][]string{
		"A": {"a1"},
		"B": {"b1"},
		// C 无入口。
	}
	qm := QualityMatrix{}

	got := buildProbeSets(allNodes, idx, qm, true, 0)

	// A 应探测 B（有入口），不探测 C（无入口）。
	if len(got["A"]) != 1 || got["A"][0].NodeID != "B" {
		t.Errorf("A 探测目标 = %+v, 期望 [B]", got["A"])
	}
	// B 应探测 A。
	if len(got["B"]) != 1 || got["B"][0].NodeID != "A" {
		t.Errorf("B 探测目标 = %+v, 期望 [A]", got["B"])
	}
	// C 无入口不是探测目标，但仍可作发起方。
	// C 应探测 A 和 B。
	if len(got["C"]) != 2 {
		t.Errorf("C 探测目标数 = %d, 期望 2", len(got["C"]))
	}
}

func TestBuildProbeSets_SamplingMode(t *testing.T) {
	// probeFull=false：就近采样 + 对称闭包。
	allNodes := []string{"A", "B", "C"}
	idx := map[string][]string{
		"A": {"a1"},
		"B": {"b1"},
		"C": {"c1"},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		{SrcNode: "A", DstNode: "C", DstIngress: "c1"}: 100,
		{SrcNode: "B", DstNode: "A", DstIngress: "a1"}: 10,
		{SrcNode: "C", DstNode: "A", DstIngress: "a1"}: 100,
	}

	// limit=1：A 选 B（最近）；对称闭包：B 也探 A。
	got := buildProbeSets(allNodes, idx, qm, false, 1)

	// A 采样 1 个最近目标 = B。对称闭包可能加更多。
	found := false
	for _, pt := range got["A"] {
		if pt.NodeID == "B" {
			found = true
		}
	}
	if !found {
		t.Errorf("A 应探测 B（最近）: %+v", got["A"])
	}
}

func TestBuildProbeSets_NoIngressNodeNotTarget(t *testing.T) {
	// 无入口的节点不应成为任何人的探测目标。
	allNodes := []string{"A", "B"}
	idx := map[string][]string{
		"A": {"a1"},
		// B 无入口。
	}
	qm := QualityMatrix{}

	got := buildProbeSets(allNodes, idx, qm, true, 0)

	// A 的探测目标不应包含 B。
	for _, pt := range got["A"] {
		if pt.NodeID == "B" {
			t.Error("B 无入口，不应成为探测目标")
		}
	}
}

func TestBuildProbeSets_IncludesAllNodes(t *testing.T) {
	// 返回 map 应包含所有节点（含无入口的）。
	allNodes := []string{"A", "B", "C"}
	idx := map[string][]string{"A": {"a1"}}
	qm := QualityMatrix{}

	got := buildProbeSets(allNodes, idx, qm, true, 0)
	for _, id := range allNodes {
		if _, ok := got[id]; !ok {
			t.Errorf("返回 map 缺少节点 %s", id)
		}
	}
}

func TestBuildProbeSets_SymmetricClosure(t *testing.T) {
	// 对称闭包验证：A 采样到 B → B 也应探 A（若 A 有入口）。
	allNodes := []string{"A", "B"}
	idx := map[string][]string{
		"A": {"a1"},
		"B": {"b1"},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
		// B→A 方向无 qm 条目：B 自身采样可能不选 A，但对称闭包应补上。
	}

	got := buildProbeSets(allNodes, idx, qm, false, 1)

	// A→B 存在（采样结果）。
	foundAtoB := false
	for _, pt := range got["A"] {
		if pt.NodeID == "B" {
			foundAtoB = true
		}
	}
	if !foundAtoB {
		t.Error("A 应探 B")
	}

	// B→A 应由对称闭包补上（A 有入口）。
	foundBtoA := false
	for _, pt := range got["B"] {
		if pt.NodeID == "A" {
			foundBtoA = true
		}
	}
	if !foundBtoA {
		t.Error("对称闭包：B 应探 A（A 有入口）")
	}
}

func TestBuildProbeSets_SymmetricClosureNoIngressSrc(t *testing.T) {
	// A（无入口）采样到 B → 对称闭包不应让 B 反探 A（A 无入口）。
	allNodes := []string{"A", "B"}
	idx := map[string][]string{
		// A 无入口。
		"B": {"b1"},
	}
	qm := QualityMatrix{
		{SrcNode: "A", DstNode: "B", DstIngress: "b1"}: 10,
	}

	got := buildProbeSets(allNodes, idx, qm, false, 1)

	// B 不应反探 A（A 无入口）。
	for _, pt := range got["B"] {
		if pt.NodeID == "A" {
			t.Error("A 无入口，B 不应因对称闭包而探 A")
		}
	}
}

func TestBuildProbeSets_ProbeTargetIngressIDs(t *testing.T) {
	// 验证 ProbeTarget.IngressIDs 取目标节点的全部 Reachable 入口（升序）。
	allNodes := []string{"A", "B"}
	idx := map[string][]string{
		"A": {"a1"},
		"B": {"b2", "b1", "b3"}, // 已升序（buildIngressIndex 保证）。
	}
	qm := QualityMatrix{}

	got := buildProbeSets(allNodes, idx, qm, true, 0)

	for _, pt := range got["A"] {
		if pt.NodeID == "B" {
			want := []string{"b1", "b2", "b3"} // 升序
			if !reflect.DeepEqual(pt.IngressIDs, want) {
				t.Errorf("B 的 IngressIDs = %v, 期望 %v", pt.IngressIDs, want)
			}
			return
		}
	}
	t.Error("A 应探 B")
}

func TestBuildProbeSets_SamplingLimitZero(t *testing.T) {
	// limit<=0 时采样模式下无目标（但对称闭包仍可能补）。
	allNodes := []string{"A", "B"}
	idx := map[string][]string{"A": {"a1"}, "B": {"b1"}}
	qm := QualityMatrix{}

	got := buildProbeSets(allNodes, idx, qm, false, 0)
	// limit=0：无采样 → 无对称闭包触发 → 所有节点探测集为空。
	if len(got["A"]) != 0 {
		t.Errorf("limit=0 时 A 探测集应为空，实际 %+v", got["A"])
	}
}
