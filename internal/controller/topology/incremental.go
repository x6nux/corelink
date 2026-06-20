package topology

import "slices"

// incremental.go: 增量重算优化器（规格 §3.5 / Task2.5）。
//
// 拓扑变化（链路质量 / 边事件）不必每次全量重算。本优化器维护上次输入快照 + 上次
// Result，收到边事件后：
//
//  1. 应用边事件到 qm 副本（DOWN 删边 / DEGRADED 增权 / RECOVERED 减权或恢复）。
//  2. 结构变化检测：用新 qm 重跑 ClassifyNodes + Prune，与缓存对比。角色集或剪枝
//     边集变化 → 退全量 Optimize（结构变化不做增量）。
//  3. 结构不变（仅边权变）→ 脏源对识别 + 阈值切换：脏对比例 < threshold 走增量
//     （只对脏对重跑 K 路由），否则全量。
//
// 铁律（一致性）：无论增量还是全量路径，结果必须与 Optimize(新输入) 逐字段
// reflect.DeepEqual。增量只是性能优化，结果不能偏离全量基线。incremental_test.go
// 的 golden 对照测试是这一铁律的守护。
//
// 设计取舍（务实，避免完整动态 SSSP 复杂度）：
//   - 增量只复用"clean 脏对之外的 Baseline K 路由"——这是唯一昂贵部分（Yen's K 最短路）。
//   - Roles / Neighbors / ProbeSets 在增量路径里"按新 qm 重新计算"（均为 O(n²) 廉价
//     运算）。这样保证它们与全量逐字段一致，无需精细推导哪些 qm 变化影响它们。
//     真正省下的是脏对之外所有中转对的 K 最短路计算。
//   - 脏源对识别宁可保守多算（多算只影响性能不影响正确性；漏算会被 DeepEqual 抓）。

// EdgeKind 是边事件类型（本包内部等价于 genv1.EdgeEventKind，避免直接依赖 genv1）。
type EdgeKind int

const (
	// EdgeDown 边不可达：删除该 qm key。
	EdgeDown EdgeKind = iota
	// EdgeDegraded 边质量下降：把 qm 设为新（更大）W。
	EdgeDegraded
	// EdgeRecovered 边质量恢复 / 提升：把 qm 设为新（更小）W 或恢复该边。
	EdgeRecovered
)

// EdgeDelta 是本包内部的边变化描述（调用方从 genv1.EdgeEvent 转换而来）。
//
// 对应拆点边 coreV(Src) → ingV(Dst, Ingress)，即 qm[{SrcNode:Src, DstNode:Dst,
// DstIngress:Ingress}]。W 是新权重（EdgeDown 时忽略）。
type EdgeDelta struct {
	Src     string
	Dst     string
	Ingress string
	Kind    EdgeKind
	W       uint64
}

// defaultDirtyThreshold 是脏源对比例阈值默认值：脏对比例 < 阈值走增量，否则全量。
const defaultDirtyThreshold = 0.20

// IncrementalOptimizer 持有上次输入快照（含 qm 副本）+ 上次 Result，支持增量重算。
type IncrementalOptimizer struct {
	lastInput  OptimizerInput
	lastResult Result
	threshold  float64 // 脏源对比例阈值，默认 defaultDirtyThreshold。

	// lastPathFull 记录上次 ApplyEdgeEvents 是否走了全量路径（测试 / 观测用）。
	lastPathFull bool
}

// NewIncremental 用初始输入建立基线（内部跑一次全量 Optimize）。
//
// 输入的 qm 被深拷贝进快照，后续边事件应用在快照副本上，不影响调用方的 qm。
func NewIncremental(in OptimizerInput) *IncrementalOptimizer {
	snap := in
	snap.Quality = copyQM(in.Quality)
	return &IncrementalOptimizer{
		lastInput:  snap,
		lastResult: Optimize(snap),
		threshold:  defaultDirtyThreshold,
	}
}

// Result 返回当前缓存的 Result（深拷贝，避免调用方修改污染内部缓存）。
func (o *IncrementalOptimizer) Result() Result {
	return copyResult(o.lastResult)
}

// ApplyEdgeEvents 应用一批边事件并返回新 Result（version=newVersion）。
//
// 步骤见文件头注释。返回值是深拷贝，调用方可自由修改。
func (o *IncrementalOptimizer) ApplyEdgeEvents(events []EdgeDelta, newVersion uint64) Result {
	// 1. 应用边事件到 qm 副本。
	newQM := copyQM(o.lastInput.Quality)
	for _, e := range events {
		key := QKey{SrcNode: e.Src, DstNode: e.Dst, DstIngress: e.Ingress}
		switch e.Kind {
		case EdgeDown:
			delete(newQM, key)
		case EdgeDegraded, EdgeRecovered:
			newQM[key] = e.W
		}
	}

	// 新输入快照（version 透传 newVersion）。
	newInput := o.lastInput
	newInput.Quality = newQM
	newInput.Version = newVersion

	// 2. 结构变化检测：重跑 ClassifyNodes + Prune，与缓存对比。
	ingressesByNode := buildIngressIndex(newInput.Nodes)
	transits, leaves := ClassifyNodes(newInput.Nodes)
	newPruned := Prune(transits, newQM, ingressesByNode, newInput.MaxPeers, newInput.IngressK)

	oldTransits, oldLeaves := ClassifyNodes(o.lastInput.Nodes)
	oldPruned := Prune(oldTransits, o.lastInput.Quality, ingressesByNode, o.lastInput.MaxPeers, o.lastInput.IngressK)

	structureChanged := !sameStringSet(transits, oldTransits) ||
		!sameStringSet(leaves, oldLeaves) ||
		!samePrunedEdges(newPruned, oldPruned)

	if structureChanged {
		return o.fallbackFull(newInput)
	}

	// 3. 结构不变（仅边权变）→ 脏源对识别。
	changedEdges := changedEdgeSet(events, o.lastInput.Quality)
	dirty := o.dirtyPairs(transits, changedEdges)

	totalPairs := numPairs(transits)
	var ratio float64
	if totalPairs > 0 {
		ratio = float64(len(dirty)) / float64(totalPairs)
	}

	if ratio >= o.threshold {
		// 脏对比例达阈值 → 全量。
		return o.fallbackFull(newInput)
	}

	// 增量路径：脏对 K 路由重算，其余 Baseline 复用缓存；
	// Roles / Neighbors / ProbeSets 按新 qm 重算（廉价，保证一致）。
	o.lastPathFull = false
	res := o.incrementalRecompute(newInput, transits, leaves, newPruned, ingressesByNode, dirty)

	// 更新缓存（深拷贝快照，避免与返回值别名）。
	o.lastInput = newInput
	o.lastResult = copyResult(res)
	return res
}

// fallbackFull 走全量 Optimize，更新缓存并返回深拷贝。
func (o *IncrementalOptimizer) fallbackFull(newInput OptimizerInput) Result {
	o.lastPathFull = true
	res := Optimize(newInput)
	o.lastInput = newInput
	o.lastResult = copyResult(res)
	return copyResult(res)
}

// incrementalRecompute 构造增量 Result：
//   - Roles / Neighbors / ProbeSets：按新输入 / 新 qm 重算（与 Optimize 同逻辑）。
//   - Baseline：脏对用新 qm 重跑 KShortest；clean 对复用缓存（深拷贝）。
func (o *IncrementalOptimizer) incrementalRecompute(
	in OptimizerInput,
	transits, leaves []string,
	prunedEdges []PrunedEdge,
	ingressesByNode map[string][]string,
	dirty map[RoutePair]bool,
) Result {
	// --- Roles（与 Optimize 一致）。
	roles := make(map[string]Role, len(in.Nodes))
	for _, t := range transits {
		roles[t] = RoleTransit
	}
	for _, l := range leaves {
		roles[l] = RoleLeaf
	}

	// --- Neighbors（与 Optimize 一致：中转互联 + 叶子接入）。
	neighbors := buildNeighborsFromEdges(prunedEdges)
	for _, t := range transits {
		if _, ok := neighbors[t]; !ok {
			neighbors[t] = []NeighborSpec{}
		}
	}
	leafUplinks := assignLeafUplinks(leaves, transits, in.Quality, ingressesByNode, in.LeafUplinks)
	for _, l := range leaves {
		if specs, ok := leafUplinks[l]; ok {
			neighbors[l] = specs
		} else {
			neighbors[l] = []NeighborSpec{}
		}
	}

	// --- Baseline：clean 对复用缓存（深拷贝），脏对用新拆点图重算。
	g := BuildSplitGraph(prunedEdges)
	baseline := make(map[RoutePair][][]Hop, len(o.lastResult.Baseline))
	for _, a := range transits {
		for _, b := range transits {
			if a == b {
				continue
			}
			pair := RoutePair{Src: a, Dst: b}
			if dirty[pair] {
				paths := KShortest(g, coreV(a), coreV(b), in.RouteK)
				if len(paths) == 0 {
					continue // 不可达对不入 map（与 BaselineRoutes 一致）。
				}
				hopSeqs := make([][]Hop, 0, len(paths))
				for _, p := range paths {
					hopSeqs = append(hopSeqs, compressPath(p))
				}
				baseline[pair] = hopSeqs
			} else if cached, ok := o.lastResult.Baseline[pair]; ok {
				baseline[pair] = copyHopSeqs(cached) // 深拷贝避免别名。
			}
			// clean 且缓存里也没有 → 该对原本不可达，仍不可达，不入 map。
		}
	}

	// --- ProbeSets（与 Optimize 一致：按新 qm 重算）。
	allNodeIDs := make([]string, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		allNodeIDs = append(allNodeIDs, n.NodeID)
	}
	slices.Sort(allNodeIDs)
	probeSets := buildProbeSets(allNodeIDs, ingressesByNode, in.Quality, in.ProbeFull, in.ProbeLimit)

	return Result{
		Version:   in.Version,
		Roles:     roles,
		Neighbors: neighbors,
		Baseline:  baseline,
		ProbeSets: probeSets,
	}
}

// dirtyPairs 识别脏源对（保守多算，正确性优先）。
//
// 边权变化对 K 最短路集合的影响分两类，分别用不同（且**可证明充分**）的规则：
//
//	① 权重增大 / 删除（DEGRADED 升权、DOWN）：只有"旧 baseline 的某条 K 路径经过该
//	   变化边"的对才会受影响。
//	   证明：若某对的旧 K 条路径都不经过该边，则升权 / 删除该边不会让任何"原本更差、
//	   现在变优"的路径出现（没有更便宜的边出现），那 K 条路径仍存在且权重不变、相对
//	   次序不变 → 该对 K 集合不变。故"旧 baseline 经过该边"是受影响的充要近似（充分）。
//	   规则：缓存 baseline 中**路径经过任一升权 / 删除边**的 RoutePair（从 []Hop 反推：
//	   第一跳边 coreV(Src)→ingV(Hop[0].Node,Hop[0].Ingress)，后续
//	   coreV(Hop[i].Node)→ingV(Hop[i+1].Node,Hop[i+1].Ingress)）。
//
//	② 权重减小 / 新增（RECOVERED 降权、或新出现的边）：更便宜的边可能被**任意**对的新
//	   最优路径采纳——即便旧 baseline 不经过它、即便该对端点与变化边端点无关（路由可
//	   "穿过"该边）。无法廉价排除任何对。
//	   规则（保守正确）：只要存在任一降权 / 新增边 → 标记**所有**中转对为脏。多算只损
//	   性能不损正确性；漏算会被 golden 对照测试抓。
//	   （此时脏对比例 = 1.0 ≥ threshold，ApplyEdgeEvents 实际会切到全量——这与"降权可
//	   能全局影响"的语义一致，仍满足一致性铁律。）
//
// 取两类并集。changedEdges 的 key 是 splitEdgeKey(coreV(Src), ingV(Dst,Ingress))。
func (o *IncrementalOptimizer) dirtyPairs(transits []string, changedEdges changeSet) map[RoutePair]bool {
	dirty := make(map[RoutePair]bool)

	// ② 降权 / 新增边 → 全部中转对脏（保守正确）。
	if changedEdges.hasDecrease {
		for _, a := range transits {
			for _, b := range transits {
				if a != b {
					dirty[RoutePair{Src: a, Dst: b}] = true
				}
			}
		}
		return dirty
	}

	// ① 仅升权 / 删除：旧 baseline 经过该边的对。
	for pair, paths := range o.lastResult.Baseline {
		if pathSetTouchesChangedEdge(pair, paths, changedEdges) {
			dirty[pair] = true
		}
	}

	return dirty
}

// 不变量（DOWN 与结构检测的交互，重要）：
//
// 能被 pathSetTouchesChangedEdge 匹配的边必出现在某条缓存 baseline 路径里；而 baseline
// 路径只由剪枝边集（prunedEdges）的边构成。因此一条"会被匹配"的 DOWN 边必在剪枝边集
// 里——但 DOWN 删除 qm key 后，Prune 的 topKIngressEdges 不再产出该 (Src,Dst,Ingress)，
// 剪枝边集随之改变，结构检测（samePrunedEdges 比较 Src/Dst/Ingress）必判定结构变 →
// ApplyEdgeEvents 提前退全量，根本不进入本函数。
//
// 推论：进入增量路径的 DOWN 边必是"剪枝边集之外的冗余边"（如 IngressK 剪掉的次优入口），
// 它不在任何 baseline 路径里 → pathSetTouchesChangedEdge 对它恒为 false → 它贡献的脏对
// 为空，Baseline 全量复用缓存。此时增量仍按新 qm 重算 Neighbors/ProbeSets 保证一致。
// 故 pathSetTouchesChangedEdge 内对 DOWN 边的匹配分支是防御性的（实际由升权 DEGRADED
// 命中）；这一不变量由 TestIncremental_DownRedundantIngressTakesIncremental 等守护。

// pathSetTouchesChangedEdge 判断某对的任一缓存路径是否经过任一变化边。
func pathSetTouchesChangedEdge(pair RoutePair, paths [][]Hop, changed changeSet) bool {
	for _, hops := range paths {
		if len(hops) == 0 {
			continue
		}
		// 第一跳边：coreV(Src) → ingV(Hop[0].Node, Hop[0].Ingress)。
		if changed.edges[splitEdgeKey(coreV(pair.Src), ingV(hops[0].Node, hops[0].Ingress))] {
			return true
		}
		// 后续跳边：coreV(Hop[i].Node) → ingV(Hop[i+1].Node, Hop[i+1].Ingress)。
		for i := 0; i+1 < len(hops); i++ {
			if changed.edges[splitEdgeKey(coreV(hops[i].Node), ingV(hops[i+1].Node, hops[i+1].Ingress))] {
				return true
			}
		}
	}
	return false
}

// changeSet 是变化边集合：拆点边 key 集 + 是否含降权 / 新增边的标志。
//
//   - edges：升权 / 删除类变化边的拆点边 key 集（供规则①路径反推匹配）。降权 / 新增
//     边不需进此集（规则②走"全部对脏"，不依赖逐边匹配）。
//   - hasDecrease：是否存在任一降权（新 W < 旧 W）或新增（旧 qm 无此 key）边。
type changeSet struct {
	edges       map[string]bool // splitEdgeKey(coreV(Src), ingV(Dst,Ingress)) → true（仅升权/删除边）
	hasDecrease bool            // 存在降权 / 新增边 → 触发规则②（全部对脏）
}

// changedEdgeSet 从边事件 + 旧 qm 构造变化边集，区分升权/删除 vs 降权/新增。
//
//   - DOWN：删除边（升权类，进 edges 供规则①匹配旧 baseline）。
//   - DEGRADED / RECOVERED：比较新 W 与旧 qm 值：
//     新 W < 旧值 或 旧 qm 无此 key（新增）→ 降权/新增（置 hasDecrease）。
//     新 W >= 旧值 → 升权/不变（进 edges 供规则①匹配）。
//
// 注：按事件 Kind 命名虽是 DEGRADED/RECOVERED，但实际是升权还是降权以"新 W vs 旧 W"
// 客观判定（更稳健，避免调用方 Kind 与 W 不自洽时误判）。
func changedEdgeSet(events []EdgeDelta, oldQM QualityMatrix) changeSet {
	cs := changeSet{
		edges: make(map[string]bool, len(events)),
	}
	for _, e := range events {
		key := QKey{SrcNode: e.Src, DstNode: e.Dst, DstIngress: e.Ingress}
		switch e.Kind {
		case EdgeDown:
			// 删除边：旧 baseline 经过它的对受影响（升权类，进 edges）。
			cs.edges[splitEdgeKey(coreV(e.Src), ingV(e.Dst, e.Ingress))] = true
		default: // EdgeDegraded / EdgeRecovered
			old, had := oldQM[key]
			if !had || e.W < old {
				cs.hasDecrease = true // 新增或降权 → 规则②。
			} else {
				// 升权（或权重不变）：进 edges 供规则①匹配旧 baseline。
				cs.edges[splitEdgeKey(coreV(e.Src), ingV(e.Dst, e.Ingress))] = true
			}
		}
	}
	return cs
}

// splitEdgeKey 拼接拆点有向边 key（与 kpaths.go edgeKey 同语义，独立避免耦合）。
func splitEdgeKey(from, to Vertex) string {
	return string(from) + sep + string(to)
}

// numPairs 返回 n 个中转的有序对数 n*(n-1)。
func numPairs(transits []string) int {
	n := len(transits)
	return n * (n - 1)
}

// --- 集合 / 拷贝 helper ---

// sameStringSet 判断两个已排序字符串切片是否相等（ClassifyNodes 返回已排序）。
func sameStringSet(a, b []string) bool {
	return slices.Equal(a, b)
}

// samePrunedEdges 判断两个剪枝边集是否相等（Prune 返回已按 (Src,Dst,Ingress) 排序）。
// 边权 W 也参与比较——W 变化虽不改变结构连通，但为稳妥仍视为需关注；然而 W 变化
// 本就是边权变（增量路径处理），故这里**只比结构（Src,Dst,Ingress）忽略 W**，
// 以便边权变化走增量而非误判结构变化。
func samePrunedEdges(a, b []PrunedEdge) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Src != b[i].Src || a[i].Dst != b[i].Dst || a[i].Ingress != b[i].Ingress {
			return false
		}
	}
	return true
}

// copyQM 深拷贝质量矩阵。
func copyQM(qm QualityMatrix) QualityMatrix {
	out := make(QualityMatrix, len(qm))
	for k, v := range qm {
		out[k] = v
	}
	return out
}

// copyResult 深拷贝 Result（所有 map / slice 不与源共享，避免别名污染）。
func copyResult(r Result) Result {
	out := Result{Version: r.Version}

	if r.Roles != nil {
		out.Roles = make(map[string]Role, len(r.Roles))
		for k, v := range r.Roles {
			out.Roles[k] = v
		}
	}

	if r.Neighbors != nil {
		out.Neighbors = make(map[string][]NeighborSpec, len(r.Neighbors))
		for k, specs := range r.Neighbors {
			out.Neighbors[k] = copyNeighborSpecs(specs)
		}
	}

	if r.Baseline != nil {
		out.Baseline = make(map[RoutePair][][]Hop, len(r.Baseline))
		for k, seqs := range r.Baseline {
			out.Baseline[k] = copyHopSeqs(seqs)
		}
	}

	if r.ProbeSets != nil {
		out.ProbeSets = make(map[string][]ProbeTarget, len(r.ProbeSets))
		for k, targets := range r.ProbeSets {
			out.ProbeSets[k] = copyProbeTargets(targets)
		}
	}

	return out
}

// copyNeighborSpecs 深拷贝 []NeighborSpec（含 Ingresses slice）。
func copyNeighborSpecs(specs []NeighborSpec) []NeighborSpec {
	if specs == nil {
		return nil
	}
	out := make([]NeighborSpec, len(specs))
	for i, s := range specs {
		out[i] = NeighborSpec{NodeID: s.NodeID, Ingresses: copyStrings(s.Ingresses)}
	}
	return out
}

// copyProbeTargets 深拷贝 []ProbeTarget（含 IngressIDs slice）。
func copyProbeTargets(targets []ProbeTarget) []ProbeTarget {
	if targets == nil {
		return nil
	}
	out := make([]ProbeTarget, len(targets))
	for i, t := range targets {
		out[i] = ProbeTarget{NodeID: t.NodeID, IngressIDs: copyStrings(t.IngressIDs)}
	}
	return out
}

// copyHopSeqs 深拷贝 [][]Hop。
func copyHopSeqs(seqs [][]Hop) [][]Hop {
	if seqs == nil {
		return nil
	}
	out := make([][]Hop, len(seqs))
	for i, hs := range seqs {
		if hs == nil {
			out[i] = nil
			continue
		}
		cp := make([]Hop, len(hs))
		copy(cp, hs)
		out[i] = cp
	}
	return out
}

// copyStrings 深拷贝字符串切片（保持 nil/空区分以匹配 DeepEqual 语义）。
func copyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}
