package topology

import (
	"reflect"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// incremental_test.go: 增量优化器一致性测试（规格 §3.5 / Task2.5）。
//
// 铁律：无论走增量还是全量路径，ApplyEdgeEvents 的结果必须与
// Optimize(手工构造的新输入, newVersion) 逐字段 reflect.DeepEqual。
// 增量只是性能优化，结果不能偏离全量基线。
//
// 这些测试是增量正确性的唯一守护：脏源对识别若漏算，DeepEqual 必失败。

// incInput 构造一个用于增量测试的较丰富确定性拓扑：
//
//   - A,B,C,D,E：OPEN 中转，各 1 个 Reachable 入口（命名 节点小写+"-i"）。
//   - 一条"链"拓扑 A-B-C-D-E，使多跳路由经过中间边，方便制造脏源对：
//     A↔B(10) B↔C(10) C↔D(10) D↔E(10)，以及一些"长边"使绕行可选：
//     A↔C(30) B↔D(30) C↔E(30) A↔E(80)。
//   - 一个叶子 L：SYMMETRIC 无 Reachable 入口；L→A(5) L→B(20)。
//
// MaxPeers 给足够大（8）以避免剪枝抖动干扰一致性测试的边权变化语义。
func incInput() OptimizerInput {
	nodes := []NodeEligibilityInput{
		{NodeID: "A", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("a-i")}},
		{NodeID: "B", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("b-i")}},
		{NodeID: "C", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("c-i")}},
		{NodeID: "D", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("d-i")}},
		{NodeID: "E", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{reachIng("e-i")}},
		{NodeID: "L", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{unreachIng("l-i")}},
	}
	qm := QualityMatrix{}
	// 双向短边（相邻链）。
	addBi(qm, "A", "B", 10)
	addBi(qm, "B", "C", 10)
	addBi(qm, "C", "D", 10)
	addBi(qm, "D", "E", 10)
	// 双向长边（绕行可选）。
	addBi(qm, "A", "C", 30)
	addBi(qm, "B", "D", 30)
	addBi(qm, "C", "E", 30)
	addBi(qm, "A", "E", 80)
	// 叶子接入。
	qm[QKey{SrcNode: "L", DstNode: "A", DstIngress: "a-i"}] = 5
	qm[QKey{SrcNode: "L", DstNode: "B", DstIngress: "b-i"}] = 20
	return OptimizerInput{
		Version:    100,
		Nodes:      nodes,
		Quality:    qm,
		MaxPeers:   8,
		IngressK:   2,
		RouteK:     3,
		ProbeFull:  true,
		ProbeLimit: 0,
	}
}

// ing 返回节点的约定入口 ID（节点小写 + "-i"）。
func ing(node string) string {
	switch node {
	case "A":
		return "a-i"
	case "B":
		return "b-i"
	case "C":
		return "c-i"
	case "D":
		return "d-i"
	case "E":
		return "e-i"
	case "L":
		return "l-i"
	}
	return ""
}

// addBi 在 qm 双向加边（src→dst 用 dst 的约定入口，反向同理），权 w。
func addBi(qm QualityMatrix, x, y string, w uint64) {
	qm[QKey{SrcNode: x, DstNode: y, DstIngress: ing(y)}] = w
	qm[QKey{SrcNode: y, DstNode: x, DstIngress: ing(x)}] = w
}

// deltaBi 构造一对双向 EdgeDelta（x→y 与 y→x），同 Kind/W。
// 增量优化器内部只对单向 qm key 操作；为保持 qm 双向对称（剪枝/路由依赖），
// 调用方典型成对下发，测试也成对构造。
func deltaBi(x, y string, kind EdgeKind, w uint64) []EdgeDelta {
	return []EdgeDelta{
		{Src: x, Dst: y, Ingress: ing(y), Kind: kind, W: w},
		{Src: y, Dst: x, Ingress: ing(x), Kind: kind, W: w},
	}
}

// applyDeltasToInput 把 EdgeDelta 应用到 in 的 qm 副本，返回新输入（version=newVer）。
// 这是"手工构造的新输入"——与增量结果对照的全量基线来源。
//
// 语义与 ApplyEdgeEvents 内部一致：
//   - DOWN：删除 qm[{Src,Dst,Ingress}]。
//   - DEGRADED / RECOVERED：把 qm[{Src,Dst,Ingress}] 设为 W。
func applyDeltasToInput(in OptimizerInput, deltas []EdgeDelta, newVer uint64) OptimizerInput {
	out := in
	out.Version = newVer
	nq := make(QualityMatrix, len(in.Quality))
	for k, v := range in.Quality {
		nq[k] = v
	}
	for _, d := range deltas {
		key := QKey{SrcNode: d.Src, DstNode: d.Dst, DstIngress: d.Ingress}
		switch d.Kind {
		case EdgeDown:
			delete(nq, key)
		case EdgeDegraded, EdgeRecovered:
			nq[key] = d.W
		}
	}
	out.Quality = nq
	return out
}

// TestIncremental_MatchesFullRecompute 是核心一致性测试：多组确定性场景，
// 每组都断言 ApplyEdgeEvents 结果 == Optimize(手工新输入, newVer)。
func TestIncremental_MatchesFullRecompute(t *testing.T) {
	cases := []struct {
		name   string
		deltas []EdgeDelta
	}{
		{
			name:   "degrade-short-edge-BC",
			deltas: deltaBi("B", "C", EdgeDegraded, 15),
		},
		{
			name:   "recover-long-edge-AC-improves",
			deltas: deltaBi("A", "C", EdgeRecovered, 5),
		},
		{
			name:   "degrade-CD-mild",
			deltas: deltaBi("C", "D", EdgeDegraded, 12),
		},
		{
			name: "mixed-degrade-recover",
			deltas: append(
				deltaBi("B", "C", EdgeDegraded, 14),
				deltaBi("C", "E", EdgeRecovered, 11)...,
			),
		},
		{
			name:   "recover-AE-shortcut",
			deltas: deltaBi("A", "E", EdgeRecovered, 12),
		},
		{
			name: "multi-mixed",
			deltas: concat(
				deltaBi("A", "B", EdgeDegraded, 11),
				deltaBi("D", "E", EdgeDegraded, 11),
				deltaBi("B", "D", EdgeRecovered, 9),
			),
		},
		{
			// 叶子上行边变化（单向 L→B 升权）：不改中转结构，但改 L 的接入选择 / 入口质量。
			// 增量路径会按新 qm 重算 leafUplinks，必须与全量一致。
			name:   "leaf-uplink-degrade",
			deltas: []EdgeDelta{{Src: "L", Dst: "B", Ingress: "b-i", Kind: EdgeDegraded, W: 50}},
		},
		{
			// 叶子上行边降权 L→B（20→3，比 L→A 的 5 更优）：改变 L 接入 top-2 排序。
			name:   "leaf-uplink-recover",
			deltas: []EdgeDelta{{Src: "L", Dst: "B", Ingress: "b-i", Kind: EdgeRecovered, W: 3}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const newVer = uint64(101)
			base := incInput()
			inc := NewIncremental(base)
			got := inc.ApplyEdgeEvents(tc.deltas, newVer)

			wantInput := applyDeltasToInput(base, tc.deltas, newVer)
			want := Optimize(wantInput)

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("incremental != full recompute\n got=%#v\nwant=%#v", got, want)
			}
			if got.Version != newVer {
				t.Errorf("version = %d, want %d", got.Version, newVer)
			}
		})
	}
}

// TestIncremental_ThresholdSwitch 验证脏对比例跨阈值时走增量 vs 全量两条路径，
// 但两条路径结果都 == 全量基线。用 lastPathFull 标志验证走了哪条。
func TestIncremental_ThresholdSwitch(t *testing.T) {
	// 小幅单边变化 → 脏对比例小 → 应走增量。
	t.Run("below-threshold-takes-incremental", func(t *testing.T) {
		base := incInput()
		inc := NewIncremental(base)
		inc.threshold = 0.95 // 阈值很高，几乎一定走增量。
		// 升权一条几乎无人使用的最差长边 A-E(80→85)：受影响（旧 baseline 经过它）的
		// 对极少 → 脏对比例远低于阈值 → 走增量。用升权（非降权）以触发规则①而非规则②。
		deltas := deltaBi("A", "E", EdgeDegraded, 85)
		got := inc.ApplyEdgeEvents(deltas, 101)
		if inc.lastPathFull {
			t.Errorf("expected incremental path (below threshold), but took full")
		}
		want := Optimize(applyDeltasToInput(base, deltas, 101))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("incremental path result != full baseline")
		}
	})

	// 阈值设为 0 → 任何非空脏对都跨阈值 → 走全量。
	t.Run("above-threshold-takes-full", func(t *testing.T) {
		base := incInput()
		inc := NewIncremental(base)
		inc.threshold = 0.0 // 阈值为 0，任何脏对都 >= 阈值 → 全量。
		deltas := deltaBi("C", "D", EdgeDegraded, 12)
		got := inc.ApplyEdgeEvents(deltas, 101)
		if !inc.lastPathFull {
			t.Errorf("expected full path (>= threshold 0), but took incremental")
		}
		want := Optimize(applyDeltasToInput(base, deltas, 101))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("full path result != full baseline")
		}
	})
}

// TestIncremental_StructureChangeFallsBackToFull 验证边权变化大到改变剪枝邻居时
// 退全量且结果正确。
//
// 把 MaxPeers 降到 2 使剪枝有取舍：原本某中转的 top-2 邻居在 RECOVERED 减权后被
// 一个原本较差的邻居取代 → 剪枝邻居集变化 → 退全量。
func TestIncremental_StructureChangeFallsBackToFull(t *testing.T) {
	base := incInput()
	base.MaxPeers = 2 // 收紧度数，使边权变化能改变 top-d 邻居集。
	inc := NewIncremental(base)
	inc.threshold = 0.99 // 阈值高，若非结构变化本会走增量；此处应被结构检测拦截走全量。

	// 把一条原本较差的长边 A-E(80) 大幅 RECOVERED 到极优(1)，
	// 足以挤入 A 的 top-2 邻居，改变剪枝结构。
	deltas := deltaBi("A", "E", EdgeRecovered, 1)
	got := inc.ApplyEdgeEvents(deltas, 101)

	if !inc.lastPathFull {
		t.Errorf("structure change should fall back to full recompute")
	}
	want := Optimize(applyDeltasToInput(base, deltas, 101))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("structure-change full result != baseline\n got=%#v\nwant=%#v", got, want)
	}
}

// TestIncremental_RecoveredImprovesPath 验证 RECOVERED 减权使某对最优路径变更时
// 增量正确采纳（== 全量）。
//
// 原 A→C 最优经 B（10+10=20）优于直连 30；把 A-C 直连 RECOVERED 到 5 后，
// A→C 最优应变成直连。增量必须采纳这个更优新路径。
func TestIncremental_RecoveredImprovesPath(t *testing.T) {
	base := incInput()
	inc := NewIncremental(base)
	inc.threshold = 0.95
	deltas := deltaBi("A", "C", EdgeRecovered, 5)
	got := inc.ApplyEdgeEvents(deltas, 101)

	// 注：RECOVERED 是降权——可能让任意对采纳穿过该边的新更优路径，故脏对识别保守
	// 标记全部对（ratio=1.0 ≥ threshold → 实际切全量）。这与"降权全局影响"的语义一致。
	// 本测试只守护一致性铁律（结果 == 全量），不约束走了哪条路径。
	want := Optimize(applyDeltasToInput(base, deltas, 101))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered-improves-path: incremental != full\n got=%#v\nwant=%#v", got, want)
	}
	// 额外语义断言：A→C 第一条最优路由现在应是直连（单跳到 C）。
	paths := got.Baseline[RoutePair{Src: "A", Dst: "C"}]
	if len(paths) == 0 {
		t.Fatalf("missing A->C baseline")
	}
	first := paths[0]
	if len(first) != 1 || first[0].Node != "C" {
		t.Errorf("A->C optimal should now be direct single hop to C; got %v", first)
	}
}

// TestIncremental_DownRemovesEdge 验证 DOWN 删边后受影响对 baseline 重算（== 全量）。
//
// 把 B-C 短边 DOWN 删除：原本经 B-C 的路由（如 A→C 的备选、B→D 等）必须重算。
func TestIncremental_DownRemovesEdge(t *testing.T) {
	base := incInput()
	inc := NewIncremental(base)
	inc.threshold = 0.95 // 走增量（除非结构变化）。
	deltas := deltaBi("B", "C", EdgeDown, 0)
	got := inc.ApplyEdgeEvents(deltas, 101)

	want := Optimize(applyDeltasToInput(base, deltas, 101))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("down-removes-edge: incremental/full != baseline\n got=%#v\nwant=%#v", got, want)
	}
}

// TestIncremental_ResultPassthrough 验证 NewIncremental 建的基线 Result()
// 与 Optimize(原输入) 一致。
func TestIncremental_ResultBaseline(t *testing.T) {
	base := incInput()
	inc := NewIncremental(base)
	if !reflect.DeepEqual(inc.Result(), Optimize(base)) {
		t.Fatalf("NewIncremental baseline != Optimize(input)")
	}
}

// TestIncremental_NoAlias 验证增量复用缓存 baseline 不产生别名污染：
// 连续两次 ApplyEdgeEvents，第二次结果仍与对应全量一致（缓存未被前次别名破坏）。
func TestIncremental_NoAlias(t *testing.T) {
	base := incInput()
	inc := NewIncremental(base)
	inc.threshold = 0.95

	d1 := deltaBi("C", "D", EdgeDegraded, 12)
	got1 := inc.ApplyEdgeEvents(d1, 101)
	want1 := Optimize(applyDeltasToInput(base, d1, 101))
	if !reflect.DeepEqual(got1, want1) {
		t.Fatalf("first apply != baseline")
	}

	// 第二次在第一次基础上再变一条边。累积输入 = base + d1 + d2。
	d2 := deltaBi("D", "E", EdgeDegraded, 13)
	got2 := inc.ApplyEdgeEvents(d2, 102)
	cumInput := applyDeltasToInput(applyDeltasToInput(base, d1, 101), d2, 102)
	want2 := Optimize(cumInput)
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("second apply (cumulative) != baseline\n got=%#v\nwant=%#v", got2, want2)
	}

	// 再次确认 got1 未被第二次调用别名修改（深拷贝保证）。
	if !reflect.DeepEqual(got1, want1) {
		t.Fatalf("got1 mutated by second apply (aliasing bug)")
	}
}

// sparseInput 构造一个剪枝结构稳定的稀疏拓扑，专用于守护 DOWN 删边的增量路径。
//
// 关键设计：X-Y 物理对配置 2 个入口（y-i1 最优 10、y-i2 次优 20），但 IngressK=1
// 使剪枝每方向只保留最优入口（y-i1）。次优入口 y-i2 存在于 qm 但**不进剪枝边集**。
// 这样 DOWN 删 X->Y/y-i2（被剪掉的冗余入口）后：
//   - ClassifyNodes + Prune 的 (Src,Dst,Ingress) 集合完全不变（结构稳定）→ 走增量。
//   - 该边不在任何 baseline 路径里（baseline 只走剪枝边）→ 脏对识别确认不经过 →
//     脏对空、Baseline 全复用缓存。
//   - 增量路径仍按新 qm 重算 Neighbors / ProbeSets，必须与全量逐字段一致。
//
// 拓扑：X,Y,Z,W 四中转链 + 一叶子 Lf；采样探测模式（ProbeFull=false）使删边能影响
// 探测集，从而让"DOWN 在增量下 ProbeSets 重算"也被真实覆盖。
func sparseInput() OptimizerInput {
	r := func(id string) IngressMeta { return IngressMeta{ID: id, Confidence: 90, Reachable: true} }
	nodes := []NodeEligibilityInput{
		{NodeID: "X", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{r("x-i")}},
		{NodeID: "Y", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{r("y-i1"), r("y-i2")}},
		{NodeID: "Z", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{r("z-i")}},
		{NodeID: "W", Nat: genv1.NatType_NAT_TYPE_OPEN, Ingresses: []IngressMeta{r("w-i")}},
		{NodeID: "Lf", Nat: genv1.NatType_NAT_TYPE_SYMMETRIC, Ingresses: []IngressMeta{{ID: "lf-i", Reachable: false}}},
	}
	qm := QualityMatrix{
		// X-Y：双入口（y-i1 最优、y-i2 冗余次优）。
		{SrcNode: "X", DstNode: "Y", DstIngress: "y-i1"}: 10,
		{SrcNode: "X", DstNode: "Y", DstIngress: "y-i2"}: 20,
		{SrcNode: "Y", DstNode: "X", DstIngress: "x-i"}:  10,
		// Y-Z、Z-W 链。
		{SrcNode: "Y", DstNode: "Z", DstIngress: "z-i"}:  10,
		{SrcNode: "Z", DstNode: "Y", DstIngress: "y-i1"}: 10,
		{SrcNode: "Z", DstNode: "Y", DstIngress: "y-i2"}: 20,
		{SrcNode: "Z", DstNode: "W", DstIngress: "w-i"}:  10,
		{SrcNode: "W", DstNode: "Z", DstIngress: "z-i"}:  10,
		// X-Z、Y-W 长边（绕行可选）。
		{SrcNode: "X", DstNode: "Z", DstIngress: "z-i"}:  30,
		{SrcNode: "Z", DstNode: "X", DstIngress: "x-i"}:  30,
		{SrcNode: "Y", DstNode: "W", DstIngress: "w-i"}:  30,
		{SrcNode: "W", DstNode: "Y", DstIngress: "y-i1"}: 30,
		{SrcNode: "W", DstNode: "Y", DstIngress: "y-i2"}: 40,
		// 叶子接入。
		{SrcNode: "Lf", DstNode: "X", DstIngress: "x-i"}:  5,
		{SrcNode: "Lf", DstNode: "Y", DstIngress: "y-i1"}: 8,
	}
	return OptimizerInput{
		Version:    200,
		Nodes:      nodes,
		Quality:    qm,
		MaxPeers:   8,
		IngressK:   1, // 每方向只保留最优入口，使次优入口成为可删的冗余边。
		RouteK:     3,
		ProbeFull:  false, // 采样模式，使删边可影响探测集。
		ProbeLimit: 2,
	}
}

// TestIncremental_DownRedundantIngressTakesIncremental 守护 DOWN 删边的增量路径
// （reviewer M1）：删一条被剪枝剪掉的冗余入口边 → 剪枝结构不变 → 走增量
// （lastPathFull==false）且结果 reflect.DeepEqual == 全量 Optimize(新输入)。
//
// 这直接守护"DOWN 走增量时脏对识别（扫描确认不经过该边）+ Neighbors/ProbeSets
// 按新 qm 重算"的正确性——补上穷举测试里 DOWN 总退全量的盲区。
//
// 设计不变量（见 dirtyPairs / changedEdgeSet 注释）：能被 pathSetTouchesChangedEdge
// 匹配到的 DOWN 边必在剪枝边集里，删它必改结构而退全量；故"DOWN 走增量"必然对应
// "删的是剪枝边集外的冗余边、脏对为空"。本测试覆盖该唯一可达的增量 DOWN 形态。
func TestIncremental_DownRedundantIngressTakesIncremental(t *testing.T) {
	base := sparseInput()
	inc := NewIncremental(base)
	inc.threshold = 0.95

	// 删 X->Y/y-i2（IngressK=1 下被剪掉的冗余次优入口；剪枝只保留 y-i1）。
	deltas := []EdgeDelta{{Src: "X", Dst: "Y", Ingress: "y-i2", Kind: EdgeDown}}
	got := inc.ApplyEdgeEvents(deltas, 201)

	if inc.lastPathFull {
		t.Fatalf("DOWN of a pruned-out redundant ingress edge should take incremental path")
	}
	want := Optimize(applyDeltasToInput(base, deltas, 201))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("down-redundant-ingress: incremental != full\n got=%#v\nwant=%#v", got, want)
	}
}

// TestIncremental_SparseExhaustiveDown 在稀疏拓扑上对每条**冗余入口边**施加 DOWN，
// 断言走增量且 == 全量。冗余入口边 = qm 中存在但被 IngressK=1 剪掉的次优入口
// （y-i2 系列）。强化 DOWN 增量路径的脏对识别 + 重算覆盖。
func TestIncremental_SparseExhaustiveDown(t *testing.T) {
	redundant := []EdgeDelta{
		{Src: "X", Dst: "Y", Ingress: "y-i2", Kind: EdgeDown},
		{Src: "Z", Dst: "Y", Ingress: "y-i2", Kind: EdgeDown},
		{Src: "W", Dst: "Y", Ingress: "y-i2", Kind: EdgeDown},
	}
	incRuns := 0
	for _, d := range redundant {
		name := d.Src + "-" + d.Dst + "-" + d.Ingress
		t.Run(name, func(t *testing.T) {
			base := sparseInput()
			inc := NewIncremental(base)
			inc.threshold = 0.95
			deltas := []EdgeDelta{d}
			got := inc.ApplyEdgeEvents(deltas, 201)
			want := Optimize(applyDeltasToInput(base, deltas, 201))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: incremental/full != baseline\n got=%#v\nwant=%#v", name, got, want)
			}
			if !inc.lastPathFull {
				incRuns++
			}
		})
	}
	if incRuns == 0 {
		t.Errorf("no DOWN incremental-path runs exercised in sparse topology")
	}
}

// TestIncremental_ExhaustiveSingleEdge 穷举式守护：对拓扑里每条双向边，分别施加
// 升权 / 降权 / DOWN 三种确定性变化，每种都断言 ApplyEdgeEvents == 全量基线。
//
// 这是脏源对识别完整性的强守护：任一边的任一类变化若导致脏对漏算，DeepEqual 必失败。
// 同时统计实际走增量的次数（升权类应大量走增量），确保增量复用路径被真实覆盖。
func TestIncremental_ExhaustiveSingleEdge(t *testing.T) {
	edges := []struct{ x, y string }{
		{"A", "B"}, {"B", "C"}, {"C", "D"}, {"D", "E"},
		{"A", "C"}, {"B", "D"}, {"C", "E"}, {"A", "E"},
	}
	type variant struct {
		name string
		kind EdgeKind
		w    uint64
	}
	variants := []variant{
		{"increase", EdgeDegraded, 100}, // 大幅升权（不改结构，MaxPeers=8 足够宽松）。
		{"decrease", EdgeRecovered, 1},  // 大幅降权。
		{"down", EdgeDown, 0},           // 删边。
	}

	incrementalRuns := 0
	for _, e := range edges {
		for _, v := range variants {
			name := e.x + e.y + "-" + v.name
			t.Run(name, func(t *testing.T) {
				base := incInput()
				inc := NewIncremental(base)
				inc.threshold = 0.95 // 高阈值：升权类（局部脏）会走增量，降权类（全脏）走全量。
				deltas := deltaBi(e.x, e.y, v.kind, v.w)
				got := inc.ApplyEdgeEvents(deltas, 101)
				want := Optimize(applyDeltasToInput(base, deltas, 101))
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: incremental != full\n got=%#v\nwant=%#v", name, got, want)
				}
				if !inc.lastPathFull {
					incrementalRuns++
				}
			})
		}
	}
	// 至少有若干次真实走了增量复用路径（升权类局部脏对），否则增量逻辑未被覆盖。
	if incrementalRuns == 0 {
		t.Errorf("no incremental-path runs exercised; reuse logic uncovered")
	}
}

// concat 拼接多组 EdgeDelta。
func concat(groups ...[]EdgeDelta) []EdgeDelta {
	var out []EdgeDelta
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
