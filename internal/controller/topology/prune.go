package topology

import (
	"slices"
	"strings"
)

// PrunedEdge 是 controller 侧的"逻辑"中转边：表示从中转 Src 经物理邻居 Dst
// 的入口 Ingress 建链，权重 W（越小越优）。
//
// 注意：这与 graph.go 的拆点图 Edge（顶点级 To/W）是不同概念，故用独立类型避免
// 混淆。PrunedEdge 是"物理节点对 + 选定入口"粒度的边，供 Task2.3 K 最短路 /
// Task2.4 转 SetAdjacency 使用。
type PrunedEdge struct {
	Src     string
	Dst     string
	Ingress string
	W       uint64
}

// Prune 在中转候选之间构建带度数上限的邻接边集（物理节点对粒度，双向连通）。
//
// 前置条件（调用方保证，纯函数不做运行时校验，与 graph.go BuildGraph 风格一致）：
//   - transits 内 NodeID 唯一。
//   - ingressesByNode 的 key 与 transits 中的 NodeID 对齐（缺失视为该节点无入口）。
//   - 单节点内 ingress ID 唯一。
//   - qm 值越小越优。
//
// 算法：
//  1. 出向 top-d 候选：对每个中转 A，按 A 到 B 的"最优入口边质量"（B 所有入口里
//     qm[{A,B,e}] 的最小值，即最优）升序，取质量最好的 d 个物理邻居作为 A 的出向
//     候选；质量相同按 NodeID 字典序 tiebreak。无任何可达入口的 B 不参与。
//  2. 对称物理邻居对（union 闭包）：A-B 成为物理邻居 iff A 在 B 的 top-d **或**
//     B 在 A 的 top-d。这保证任意保留的物理对**双向都建边**——WireGuard 双向通信
//     要求边集对称（A→B 存在 ⟺ B→A 存在，前提该方向有可达入口），否则反向流量的
//     源中转在剪枝边集上算不出基准路由（Task2.3 K 最短路），导致 L2→L1 不可达。
//  3. 每个对称物理对、每个方向独立取 top-k 入口：A→B 方向用 B 的入口
//     （qm[{A,B,e}] 选最优 k 个），B→A 方向用 A 的入口（qm[{B,A,e}] 选最优 k 个）；
//     质量相同按 ingressID 字典序 tiebreak。其余入口剪除。
//
// 度数语义：top-d 是**软上限**。因对称闭包（union），A 的实际物理邻居数可能略超
// d（当 B 在 A 的 top-d 之外、却因 A 在 B 的 top-d 而被引入时）。这是保证双向连通
// 的必要代价；RTT 往返通常对称，A/B 多半互选，实际很少超出。度数仍按物理节点对算
// （一个 B 不管保留几个入口只占一个邻居名额）。
//
// 边方向性：返回的是有向边 []PrunedEdge，但边集对称——只要某物理对的某方向有 qm
// 可达入口就建该方向边。若某方向完全无 qm 项（数据上不可达），该方向确实建不了边，
// 属数据问题而非算法问题（对称闭包只决定"哪些物理对入选"，不能凭空制造可达入口）。
//
// 返回保留边集，按 (Src, Dst, Ingress) 字典序排序（确定性）。
func Prune(transits []string, qm QualityMatrix, ingressesByNode map[string][]string, d, k int) []PrunedEdge {
	var out []PrunedEdge
	if d <= 0 || k <= 0 {
		return out
	}

	// 无探测数据时的默认权重（较大值，优先使用有真实探测数据的链路）。
	const defaultWeight uint64 = 1_000_000

	// topKIngressEdges 返回 src→dst 方向按 qm 最优的 top-k 入口边（用 dst 的入口）。
	// 无可达入口时返回 nil。质量相同按 ingressID 字典序 tiebreak。
	topKIngressEdges := func(src, dst string) []PrunedEdge {
		var edges []PrunedEdge
		for _, e := range ingressesByNode[dst] {
			if w, ok := qm[QKey{SrcNode: src, DstNode: dst, DstIngress: e}]; ok {
				edges = append(edges, PrunedEdge{Src: src, Dst: dst, Ingress: e, W: w})
			} else {
				// 无探测数据时用默认权重（不丢弃，确保互联）
				edges = append(edges, PrunedEdge{Src: src, Dst: dst, Ingress: e, W: defaultWeight})
			}
		}
		slices.SortFunc(edges, func(x, y PrunedEdge) int {
			if x.W != y.W {
				if x.W < y.W {
					return -1
				}
				return 1
			}
			return strings.Compare(x.Ingress, y.Ingress)
		})
		if len(edges) > k {
			edges = edges[:k]
		}
		return edges
	}

	// bestW 返回 src→dst 的最优（最小）入口质量；ok=false 表示无可达入口。
	// 无探测数据但 dst 有入口时返回默认权重（假设可达），确保新节点不被孤立。
	bestW := func(src, dst string) (uint64, bool) {
		var best uint64
		have := false
		for _, e := range ingressesByNode[dst] {
			if w, ok := qm[QKey{SrcNode: src, DstNode: dst, DstIngress: e}]; ok {
				if !have || w < best {
					best = w
					have = true
				}
			}
		}
		if have {
			return best, true
		}
		// 无探测数据但 dst 有入口 → 默认可达
		if len(ingressesByNode[dst]) > 0 {
			return defaultWeight, true
		}
		return 0, false
	}

	// 1. 各中转算出向 top-d 候选邻居集合。
	topD := make(map[string]map[string]bool, len(transits))
	for _, a := range transits {
		type cand struct {
			dst string
			w   uint64
		}
		var cands []cand
		for _, b := range transits {
			if b == a {
				continue
			}
			if w, ok := bestW(a, b); ok {
				cands = append(cands, cand{dst: b, w: w})
			}
		}
		slices.SortFunc(cands, func(x, y cand) int {
			if x.w != y.w {
				if x.w < y.w {
					return -1
				}
				return 1
			}
			return strings.Compare(x.dst, y.dst)
		})
		if len(cands) > d {
			cands = cands[:d]
		}
		set := make(map[string]bool, len(cands))
		for _, c := range cands {
			set[c.dst] = true
		}
		topD[a] = set
	}

	// 2. 对称闭包：收集所有无序物理对 {x,y}（x<y），A 在 B.topD 或 B 在 A.topD。
	type pair struct{ x, y string }
	pairSet := make(map[pair]bool)
	for _, a := range transits {
		for b := range topD[a] {
			x, y := a, b
			if x > y {
				x, y = y, x
			}
			pairSet[pair{x, y}] = true
		}
	}

	// 3. 对每个对称物理对，两个方向各自独立保留 top-k 入口边。
	//    某方向若无 qm 可达入口则该方向无边（数据不可达，非算法剪除）。
	for p := range pairSet {
		out = append(out, topKIngressEdges(p.x, p.y)...) // x→y 方向
		out = append(out, topKIngressEdges(p.y, p.x)...) // y→x 方向
	}

	// 4. 全局确定性排序：(Src, Dst, Ingress)。
	slices.SortFunc(out, func(x, y PrunedEdge) int {
		if c := strings.Compare(x.Src, y.Src); c != 0 {
			return c
		}
		if c := strings.Compare(x.Dst, y.Dst); c != 0 {
			return c
		}
		return strings.Compare(x.Ingress, y.Ingress)
	})

	return out
}
