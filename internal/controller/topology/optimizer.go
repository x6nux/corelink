package topology

import "slices"

// optimizer.go: 拓扑优化器顶层编排（规格 §3.3）。
//
// Optimize 是纯函数：把 Task2.2（资格/剪枝）+ Task2.3（K 基准路由）串成完整拓扑
// 结果——角色、邻居（中转互联含入口 + 叶子接入含入口）、基准路由、探测集、版本。
//
// 纯函数性质：
//   - 不取时钟 / 不取随机：版本号 in.Version 由调用方传入透传。
//   - 输出完全由输入决定，所有 slice 按字典序确定排序 → 同输入两次调用
//     reflect.DeepEqual 相等（幂等，golden 可复现）。

// Role 是节点在拓扑中的角色。
type Role int

const (
	// RoleLeaf 叶子：无稳定可达入口，只能经中转接入（不参与互联与基准路由）。
	RoleLeaf Role = iota
	// RoleTransit 中转：有稳定可达入口，参与互联骨干与 K 基准路由。
	RoleTransit
)

// NeighborSpec 描述一个邻居及选用的入口 ID（升序）。
//   - 中转的 NeighborSpec：互联的对端中转 + 选定入口（来自剪枝边集）。
//   - 叶子的 NeighborSpec：接入的中转 + 选定入口（来自就近优质接入）。
type NeighborSpec struct {
	NodeID    string
	Ingresses []string
}

// ProbeTarget 描述一个探测目标及其入口 ID 列表（升序）。
type ProbeTarget struct {
	NodeID     string
	IngressIDs []string
}

// OptimizerInput 是 Optimize 的全部输入（规格 §3.3）。
type OptimizerInput struct {
	Version     uint64                 // 调用方传入的版本号（纯函数不取时钟）。
	Nodes       []NodeEligibilityInput // 各节点 {NodeID, Nat, Ingresses}（Task2.2 类型）。
	Quality     QualityMatrix          // 入口级质量矩阵（值越小越优）。
	MaxPeers    int                    // 中转度数上限 d（调用方典型给 8）。
	IngressK    int                    // 每对邻居入口边数 k（典型 2）。
	RouteK      int                    // 基准路由条数 K（典型 3）。
	LeafUplinks int                    // 叶子接入中转数 top-N；<=0 回落默认 2。
	ProbeFull   bool                   // true=探测集全集；false=就近采样。
	ProbeLimit  int                    // 采样模式下每节点探测目标数上限。
}

// Result 是 Optimize 的完整产出（规格 §3.3）。
type Result struct {
	Version   uint64                    // 透传 in.Version。
	Roles     map[string]Role           // NodeID → RoleTransit / RoleLeaf。
	Neighbors map[string][]NeighborSpec // 中转→互联邻居（含入口）；叶子→接入中转（含入口）。
	Baseline  map[RoutePair][][]Hop     // 中转对 → K 基准路由（Task2.3）。
	ProbeSets map[string][]ProbeTarget  // 节点 → 探测目标集。
}

// Optimize 编排完整拓扑优化（纯函数）。
//
// 步骤（规格 §3.3）：
//  1. ClassifyNodes → transits + leaves（按资格判定，SYMMETRIC 无稳定入口→叶子）。
//  2. Prune(transits, qm, ingressesByNode, MaxPeers, IngressK) → 对称剪枝边集；
//     从边集提取每中转的互联邻居（含入口）。
//  3. 叶子接入：每 leaf 选 top-N 就近优质中转（含入口），接入边独立不占中转度数。
//  4. BaselineRoutes(prunedEdges, transits, RouteK) → 中转对 K 基准路由。
//  5. 角色：transits→RoleTransit，leaves→RoleLeaf。
//  6. 探测集：ProbeFull→全集；否则就近采样 ProbeLimit + 对称闭包。
//  7. 版本透传 in.Version。
//
// ingressesByNode 仅取各节点的 Reachable 入口（与资格判定一致）。
func Optimize(in OptimizerInput) Result {
	// 0. 入口索引（仅 Reachable，与 Eligible 一致）。
	ingressesByNode := buildIngressIndex(in.Nodes)

	// 1. 角色分类。
	transits, leaves := ClassifyNodes(in.Nodes)

	// 2. 中转剪枝（对称边集）→ 中转互联邻居。
	prunedEdges := Prune(transits, in.Quality, ingressesByNode, in.MaxPeers, in.IngressK)
	neighbors := buildNeighborsFromEdges(prunedEdges)
	// 保证每个中转都有 entry（即使被剪枝后无边，便于调用方与 golden 稳定）。
	for _, t := range transits {
		if _, ok := neighbors[t]; !ok {
			neighbors[t] = []NeighborSpec{}
		}
	}

	// 3. 叶子接入（top-N 就近优质中转，含入口；不占中转度数）。
	leafUplinks := assignLeafUplinks(leaves, transits, in.Quality, ingressesByNode, in.LeafUplinks)
	for _, l := range leaves {
		if specs, ok := leafUplinks[l]; ok {
			neighbors[l] = specs
		} else {
			neighbors[l] = []NeighborSpec{} // 无可达中转的孤立叶子。
		}
	}

	// 4. 基准路由（中转对 K 路由）。
	baseline := BaselineRoutes(prunedEdges, transits, in.RouteK)

	// 5. 角色 map。
	roles := make(map[string]Role, len(in.Nodes))
	for _, t := range transits {
		roles[t] = RoleTransit
	}
	for _, l := range leaves {
		roles[l] = RoleLeaf
	}

	// 6. 探测集。
	allNodeIDs := make([]string, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		allNodeIDs = append(allNodeIDs, n.NodeID)
	}
	slices.Sort(allNodeIDs)
	probeSets := buildProbeSets(allNodeIDs, ingressesByNode, in.Quality, in.ProbeFull, in.ProbeLimit)

	// 7. 组装结果（版本透传）。
	return Result{
		Version:   in.Version,
		Roles:     roles,
		Neighbors: neighbors,
		Baseline:  baseline,
		ProbeSets: probeSets,
	}
}
