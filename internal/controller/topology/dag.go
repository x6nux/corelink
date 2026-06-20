package topology

import (
	"fmt"
	"slices"
	"strings"
)

// dag.go: FIB 转发图 DAG 验证 + 环路消除。
//
// VIP 路由架构下，controller 计算出 per-source FIB 后需保证转发图无环：
//   - validateDAG: 三色 DFS 检测环路
//   - findOneCycle: 返回一条环路路径（调试 / 日志用）
//   - pruneGraphCycles: 迭代删除环路边，修正 FIB
//
// 所有类型和函数均为 package-internal（小写），供 computeFIB 使用。

// ---------- FIB 中间类型 ----------

// fibRouteEntry 是 FIB 中一条前缀的路由条目（对应一个目标 VIP）。
type fibRouteEntry struct {
	prefix   string       // 目标 VIP 前缀（如 "10.99.0.5/32"）
	nextHops []fibNextHop // ECMP 下一跳列表
}

// fibNextHop 是一条 next-hop 记录。
type fibNextHop struct {
	peerID    string // 下一跳节点 ID
	weight    uint32 // ECMP 权重
	ingressID string // 入口 ID（选用哪个入口接入该 next-hop）
}

// ---------- DAG 验证 ----------

// 三色标记（DFS 状态）。
const (
	colorWhite = 0 // 未访问
	colorGray  = 1 // 访问中（在当前 DFS 栈上）
	colorBlack = 2 // 已完成
)

// validateDAG 检查有向图是否为 DAG（无环）。
// graph: node → []successor。若存在环路返回包含环路描述的 error。
func validateDAG(graph map[string][]string) error {
	color := make(map[string]int, len(graph))
	for node := range graph {
		color[node] = colorWhite
	}
	// 确保所有被引用的后继也在 color map 中（处理只作为 successor 出现的叶子节点）。
	for _, succs := range graph {
		for _, s := range succs {
			if _, ok := color[s]; !ok {
				color[s] = colorWhite
			}
		}
	}

	for node := range graph {
		if color[node] == colorWhite {
			if hasCycle := dfsVisit(graph, node, color); hasCycle {
				// 找到环路，返回具体路径信息。
				cycle := findOneCycle(graph)
				if cycle != nil {
					return fmt.Errorf("转发图存在环路: %s", strings.Join(cycle, " → "))
				}
				return fmt.Errorf("转发图存在环路")
			}
		}
	}
	return nil
}

// dfsVisit 执行三色 DFS，返回是否检测到 back edge（即环路）。
func dfsVisit(graph map[string][]string, node string, color map[string]int) bool {
	color[node] = colorGray
	for _, succ := range graph[node] {
		switch color[succ] {
		case colorGray:
			// back edge → 环路
			return true
		case colorWhite:
			if dfsVisit(graph, succ, color) {
				return true
			}
		}
		// colorBlack: 已完成，跳过
	}
	color[node] = colorBlack
	return false
}

// findOneCycle 在有向图中找到一条环路并返回路径节点序列（首尾相同）。
// 若无环路返回 nil。
func findOneCycle(graph map[string][]string) []string {
	color := make(map[string]int, len(graph))
	parent := make(map[string]string, len(graph))

	// 收集所有节点。
	allNodes := make(map[string]struct{})
	for node, succs := range graph {
		allNodes[node] = struct{}{}
		for _, s := range succs {
			allNodes[s] = struct{}{}
		}
	}
	for n := range allNodes {
		color[n] = colorWhite
	}

	for node := range graph {
		if color[node] != colorWhite {
			continue
		}
		if cycle := dfsFindCycle(graph, node, color, parent); cycle != nil {
			return cycle
		}
	}
	return nil
}

// dfsFindCycle 是带路径回溯的 DFS，返回环路节点序列或 nil。
func dfsFindCycle(graph map[string][]string, node string, color map[string]int, parent map[string]string) []string {
	color[node] = colorGray
	for _, succ := range graph[node] {
		if color[succ] == colorGray {
			// 找到 back edge: node → succ，回溯构造环路。
			cycle := []string{succ, node}
			cur := node
			for cur != succ {
				cur = parent[cur]
				cycle = append(cycle, cur)
			}
			// cycle 目前是 [succ, node, ..., succ]，反转得到正序。
			slices.Reverse(cycle)
			return cycle
		}
		if color[succ] == colorWhite {
			parent[succ] = node
			if c := dfsFindCycle(graph, succ, color, parent); c != nil {
				return c
			}
		}
	}
	color[node] = colorBlack
	return nil
}

// ---------- FIB → 转发图 ----------

// buildForwardingGraph 将 per-source FIB 转换为有向图 (node → []successor)。
// perSrc key 是源节点 ID，值是该源的 FIB 条目列表。
func buildForwardingGraph(perSrc map[string][]fibRouteEntry) map[string][]string {
	graph := make(map[string][]string)
	for src, entries := range perSrc {
		if _, ok := graph[src]; !ok {
			graph[src] = nil
		}
		for _, entry := range entries {
			for _, nh := range entry.nextHops {
				// 去重：同一 src→nh.peerID 只记一次。
				if !slices.Contains(graph[src], nh.peerID) {
					graph[src] = append(graph[src], nh.peerID)
				}
			}
		}
	}
	return graph
}

// ---------- 环路修剪 ----------

// pruneGraphCycles 迭代检测并删除环路边，直到转发图变为 DAG。
// 每次检测到环路，删除环路中最后一条 back edge（权重最低的 next-hop）。
// 同时修改 perSrc 中对应的 FIB 条目以保持一致。
func pruneGraphCycles(perSrc map[string][]fibRouteEntry, graph map[string][]string) {
	for {
		cycle := findOneCycle(graph)
		if cycle == nil {
			return // 无环路，完成
		}
		// cycle 形如 [A, B, ..., A]（首尾相同）。
		// 删除环路中的最后一条 back edge（cycle[len-2] → cycle[len-1]）。
		n := len(cycle)
		from := cycle[n-2]
		to := cycle[n-1]

		// 从图中删除边。
		graph[from] = slices.DeleteFunc(graph[from], func(s string) bool {
			return s == to
		})

		// 从 FIB 中删除对应 next-hop。
		removeNextHop(perSrc, from, to)
	}
}

// removeNextHop 从 perSrc[from] 的所有条目中删除 peerID==to 的 next-hop。
// 若某条目的 nextHops 变空则移除该条目。
func removeNextHop(perSrc map[string][]fibRouteEntry, from, to string) {
	entries := perSrc[from]
	var kept []fibRouteEntry
	for _, entry := range entries {
		entry.nextHops = slices.DeleteFunc(entry.nextHops, func(nh fibNextHop) bool {
			return nh.peerID == to
		})
		if len(entry.nextHops) > 0 {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		delete(perSrc, from)
	} else {
		perSrc[from] = kept
	}
}
