package topology

import (
	"slices"
	"strings"
)

// assign.go: 角色分配与邻居构建 helper（规格 §3.3 编排步骤）。
//
// 把 optimizer.go 的编排细节下沉到独立 helper：
//   - buildNeighborsFromEdges: 从对称剪枝边集提取每个中转的互联邻居（含选用入口）。
//   - assignLeafUplinks:       为每个叶子选 top-N 就近优质中转作为接入点（含入口）。
//   - reachableIngressIDs:     从节点资格输入提取 Reachable 入口 ID（与资格判定一致）。
//   - buildProbeSets:          按全集 / 就近采样（对称闭包）构建每节点探测集。
//
// 所有输出 slice 均按字典序确定排序（NodeID / IngressID），保证幂等 + golden。

// 默认叶子接入中转数（top-N 就近优质中转）。规格允许小常数；取 2 兼顾冗余与开销。
// 当 OptimizerInput.LeafUplinks<=0 时回落此默认（与其他容量参数 <=0 回落语义一致）。
const defaultLeafUplinks = 2

// bestQuality 返回 src→dst 方向的最优（最小）入口质量。
//
// 遍历 dst 的入口 ID 列表 ingressIDs（应为该 dst 的 Reachable 入口），取其中
// qm[{src,dst,e}] 存在项的最小值。ok=false 表示该方向无任何可达入口（不可达）。
//
// 统一三处同构逻辑（叶子接入选优 / 探测就近采样 / 与 Prune 的 bestW 同义），
// 使 tiebreak 与「最优入口质量」定义只有一处实现，降低不一致风险。
func bestQuality(src, dst string, ingressIDs []string, qm QualityMatrix) (uint64, bool) {
	var best uint64
	have := false
	for _, e := range ingressIDs {
		if w, ok := qm[QKey{SrcNode: src, DstNode: dst, DstIngress: e}]; ok {
			if !have || w < best {
				best = w
				have = true
			}
		}
	}
	return best, have
}

// reachableIngressIDs 提取一个节点的 Reachable 入口 ID 列表（升序）。
//
// 仅取 Reachable==true 的入口，与 Eligible 的资格判定一致：不可达入口不应进入
// 剪枝 / 探测 / 接入选择。返回按 ID 字典序排序（确定性）。
func reachableIngressIDs(in NodeEligibilityInput) []string {
	var out []string
	for _, ing := range in.Ingresses {
		if ing.Reachable {
			out = append(out, ing.ID)
		}
	}
	slices.Sort(out)
	return out
}

// buildIngressIndex 构建 NodeID → Reachable 入口 ID 列表（升序）的索引。
func buildIngressIndex(nodes []NodeEligibilityInput) map[string][]string {
	idx := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		idx[n.NodeID] = reachableIngressIDs(n)
	}
	return idx
}

// buildNeighborsFromEdges 从对称剪枝边集提取每个中转的互联邻居（含选用入口）。
//
// PrunedEdge{Src,Dst,Ingress} 表示 Src 经物理邻居 Dst 的入口 Ingress 建链。
// 对每个 Src，按物理邻居 Dst 聚合其选用的入口 ID 集合，产出 NeighborSpec。
//
// 确定性：
//   - 每个 NeighborSpec.Ingresses 按 ID 字典序排序、去重。
//   - 每个 Src 的 []NeighborSpec 按邻居 NodeID 字典序排序。
//
// 返回 map：中转 NodeID → 互联邻居（含入口）。无边的中转不出现在 map 中（调用方
// 可据需补空 entry）。
func buildNeighborsFromEdges(edges []PrunedEdge) map[string][]NeighborSpec {
	// src → dst → ingress 集合
	agg := make(map[string]map[string]map[string]bool)
	for _, e := range edges {
		byDst, ok := agg[e.Src]
		if !ok {
			byDst = make(map[string]map[string]bool)
			agg[e.Src] = byDst
		}
		ings, ok := byDst[e.Dst]
		if !ok {
			ings = make(map[string]bool)
			byDst[e.Dst] = ings
		}
		ings[e.Ingress] = true
	}

	out := make(map[string][]NeighborSpec, len(agg))
	for src, byDst := range agg {
		specs := make([]NeighborSpec, 0, len(byDst))
		for dst, ings := range byDst {
			specs = append(specs, NeighborSpec{NodeID: dst, Ingresses: sortedKeys(ings)})
		}
		slices.SortFunc(specs, func(a, b NeighborSpec) int { return strings.Compare(a.NodeID, b.NodeID) })
		out[src] = specs
	}
	return out
}

// assignLeafUplinks 为每个叶子选 top-N 就近优质中转作为接入点（含入口）。
//
// 选择逻辑（规格 §3.3 叶子接入）：
//   - 对每个 leaf，遍历所有 transit B：取 leaf→B 的最优（最小）入口质量
//     min over e∈ingressesByNode[B] of qm[{leaf,B,e}]；无可达入口的 B 跳过。
//   - 按最优质量升序选 top-N 个中转（质量相同按 NodeID 字典序 tiebreak）。
//   - 对选中的中转 B，保留 leaf→B 所有有 qm 的入口（升序去重）作为 NeighborSpec.Ingresses。
//
// 叶子接入边独立、不占中转度数（规格），故此处只读 qm，不改动剪枝边集。
//
// 确定性：每个 leaf 的 []NeighborSpec 按选中中转 NodeID 字典序排序；入口 ID 升序。
// 无任何可达中转的 leaf 不出现在返回 map 中。
//
// n 是接入中转数上限（top-N）；n<=0 时回落 defaultLeafUplinks（与其他容量参数
// <=0 回落语义一致）。
func assignLeafUplinks(leaves, transits []string, qm QualityMatrix, ingressesByNode map[string][]string, n int) map[string][]NeighborSpec {
	out := make(map[string][]NeighborSpec)
	if n <= 0 {
		n = defaultLeafUplinks
	}
	for _, leaf := range leaves {
		type cand struct {
			dst  string
			best uint64
			ings []string // 该方向所有有 qm 的入口（升序）
		}
		var cands []cand
		for _, b := range transits {
			bestW, have := bestQuality(leaf, b, ingressesByNode[b], qm)
			if !have {
				continue
			}
			// 收集该方向所有有 qm 的入口（升序）作为接入选用入口。
			var ings []string
			for _, e := range ingressesByNode[b] {
				if _, ok := qm[QKey{SrcNode: leaf, DstNode: b, DstIngress: e}]; ok {
					ings = append(ings, e)
				}
			}
			slices.Sort(ings)
			cands = append(cands, cand{dst: b, best: bestW, ings: ings})
		}
		if len(cands) == 0 {
			continue
		}
		slices.SortFunc(cands, func(x, y cand) int {
			if x.best != y.best {
				if x.best < y.best {
					return -1
				}
				return 1
			}
			return strings.Compare(x.dst, y.dst)
		})
		if len(cands) > n {
			cands = cands[:n]
		}
		specs := make([]NeighborSpec, 0, len(cands))
		for _, c := range cands {
			specs = append(specs, NeighborSpec{NodeID: c.dst, Ingresses: c.ings})
		}
		// 选中集合按 NodeID 字典序排序（top-N 已按质量选定，此处稳定化输出顺序）。
		slices.SortFunc(specs, func(a, b NeighborSpec) int { return strings.Compare(a.NodeID, b.NodeID) })
		out[leaf] = specs
	}
	return out
}

// buildProbeSets 构建每节点的探测目标集。
//
// 探测目标只含**有 Reachable 入口的节点**：探一个无可达入口的节点无意义（它不被
// 接入、没入口可探）。无入口节点（典型叶子）仍是探测的**发起方**（src），但不会成为
// 任何人的探测目标（M-3）。
//
//   - probeFull=true：每个节点探测所有「有入口」的其他节点的全部 Reachable 入口。
//   - probeFull=false：每个节点按 src→target 最优入口质量就近采样 limit 个目标；
//     采样后做**对称闭包**（规格 §2 探测对称性）：若 A 采到 B，则也保证 B 探 A。
//     对称性仅在**双方都有入口**时成立——B 必有入口（它是合法目标），但 A 若无入口
//     （典型叶子发起方）则不成为 B 的目标（目标须有入口可探）。即无入口节点可作探测
//     发起方（为自身选上行接入质量），但永不被反向探测。
//
// 确定性：每个节点的 []ProbeTarget 按目标 NodeID 字典序排序；IngressIDs 升序。
//
// 参数：
//   - allNodeIDs:        所有节点 ID（升序，调用方保证）。
//   - ingressesByNode:   NodeID → Reachable 入口 ID（升序）。
//   - qm:                质量矩阵（就近采样依据）。
func buildProbeSets(allNodeIDs []string, ingressesByNode map[string][]string, qm QualityMatrix, probeFull bool, limit int) map[string][]ProbeTarget {
	// hasIngress 判定节点是否有 Reachable 入口（可作探测目标）。
	hasIngress := func(id string) bool { return len(ingressesByNode[id]) > 0 }

	// targets[src] = set of dst NodeID（dst 必为有入口节点）。
	targets := make(map[string]map[string]bool, len(allNodeIDs))
	ensure := func(src string) map[string]bool {
		s, ok := targets[src]
		if !ok {
			s = make(map[string]bool)
			targets[src] = s
		}
		return s
	}
	// 每个节点都作为发起方建一个（可能为空的）集合，保证返回 map 含全部节点。
	for _, src := range allNodeIDs {
		ensure(src)
	}

	if probeFull {
		for _, src := range allNodeIDs {
			s := targets[src]
			for _, dst := range allNodeIDs {
				if dst != src && hasIngress(dst) {
					s[dst] = true
				}
			}
		}
	} else {
		// 就近采样：对每个 src，按 src→dst 最优入口质量升序取 limit 个目标。
		for _, src := range allNodeIDs {
			s := targets[src]
			if limit <= 0 {
				continue
			}
			type cand struct {
				dst  string
				best uint64
			}
			var cands []cand
			for _, dst := range allNodeIDs {
				if dst == src || !hasIngress(dst) {
					continue
				}
				bestW, have := bestQuality(src, dst, ingressesByNode[dst], qm)
				if !have {
					continue
				}
				cands = append(cands, cand{dst: dst, best: bestW})
			}
			slices.SortFunc(cands, func(x, y cand) int {
				if x.best != y.best {
					if x.best < y.best {
						return -1
					}
					return 1
				}
				return strings.Compare(x.dst, y.dst)
			})
			if len(cands) > limit {
				cands = cands[:limit]
			}
			for _, c := range cands {
				s[c.dst] = true
			}
		}
		// 对称闭包：若 A 探 B，则也让 B 探 A。
		// B 必有入口（它本就是合法目标）；A 仅在有入口时才能成为 B 的目标。
		for src, s := range targets {
			if !hasIngress(src) {
				continue // 无入口的 src 不能成为任何人的目标。
			}
			for dst := range s {
				ensure(dst)[src] = true
			}
		}
	}

	// 组装 ProbeTarget：IngressIDs 取被探目标节点的全部 Reachable 入口（升序）。
	out := make(map[string][]ProbeTarget, len(targets))
	for src, s := range targets {
		specs := make([]ProbeTarget, 0, len(s))
		for dst := range s {
			ids := append([]string(nil), ingressesByNode[dst]...)
			slices.Sort(ids)
			specs = append(specs, ProbeTarget{NodeID: dst, IngressIDs: ids})
		}
		slices.SortFunc(specs, func(a, b ProbeTarget) int { return strings.Compare(a.NodeID, b.NodeID) })
		out[src] = specs
	}
	return out
}

// sortedKeys 返回 map 的 key 升序切片（去重天然由 map 保证）。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
