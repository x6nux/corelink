package topology

import (
	"container/heap"
	"slices"
	"strings"
)

// kpaths.go: 拆点图上的 K-最短路基准路由（规格 §3.3 第3步）。
//
// 在 Task2.2 Prune 产出的剪枝边集上，用 BuildSplitGraph 构拆点图，再对每对中转
// 用 Yen's algorithm 算 K 条近优路径，压缩成 []Hop{node,ingress} 基准路由。
//
// 确定性：所有路径排序在权重相等时按顶点序列字典序 tiebreak，保证 golden 可复现。
// 这是 Task2.5 增量"K 路由 vs 全量逐字段一致"的前提。

// maxW 是路径权重的合理上界（用作 Dijkstra 的"无穷大"哨兵）。
// 单边权 uint64，路径长度受顶点数限制；用 1<<63 作不可达标记，避免相加溢出
// （正常权重远小于该值，求和不会越过 1<<63 再继续累加到溢出）。
const maxW = uint64(1) << 63

// BuildSplitGraph 从剪枝边集构拆点图。
//
// 每条 PrunedEdge{Src,Dst,Ingress,W} 产生：
//   - 跨节点边 coreV(Src) → ingV(Dst,Ingress) 权 W；
//   - 内部边 ingV(Dst,Ingress) → coreV(Dst) 权 0（同一 ingV→coreV 去重，只加一次）。
//
// 邻接表内边按 To 顶点字典序排序，保证确定性（与 graph.go BuildGraph 一致）。
func BuildSplitGraph(prunedEdges []PrunedEdge) *Graph {
	g := &Graph{Adj: make(map[Vertex][]Edge)}

	ensure := func(v Vertex) {
		if _, ok := g.Adj[v]; !ok {
			g.Adj[v] = nil
		}
	}

	// 已加内部边的 ingV 集合，用于去重。
	internalDone := make(map[Vertex]bool)

	for _, pe := range prunedEdges {
		src := coreV(pe.Src)
		iv := ingV(pe.Dst, pe.Ingress)
		ensure(src)
		ensure(iv)
		ensure(coreV(pe.Dst))

		// 跨节点边 coreV(Src) → ingV(Dst,Ingress) 权 W。
		g.Adj[src] = append(g.Adj[src], Edge{To: iv, W: pe.W})

		// 内部边 ingV(Dst,Ingress) → coreV(Dst) 权 0（去重）。
		if !internalDone[iv] {
			g.Adj[iv] = append(g.Adj[iv], Edge{To: coreV(pe.Dst), W: 0})
			internalDone[iv] = true
		}
	}

	// 邻接表内边按 To 顶点字典序排序（确定性）。
	for v := range g.Adj {
		edges := g.Adj[v]
		slices.SortFunc(edges, func(a, b Edge) int { return strings.Compare(string(a.To), string(b.To)) })
	}

	return g
}

// pqItem 是 Dijkstra 优先队列元素。
// 采用延迟删除（visited 跳过陈旧项），不需 heap.Fix/heap.Remove，故无 idx 字段。
type pqItem struct {
	v    Vertex
	dist uint64
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int { return len(pq) }
func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].dist != pq[j].dist {
		return pq[i].dist < pq[j].dist
	}
	// 距离相等按顶点字典序 tiebreak（确定性松弛顺序）。
	return pq[i].v < pq[j].v
}
func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}
func (pq *priorityQueue) Push(x any) {
	*pq = append(*pq, x.(*pqItem))
}
func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return it
}

// dijkstra 在 g 上算 src→dst 的单条最短路（确定性 tiebreak）。
// removedV / removedE 分别屏蔽顶点 / 边（Yen's spur 用）。
// 返回路径顶点序列（含 src 与 dst）与总权重；不可达返回 (nil, maxW)。
//
// removedE 的 key 是 "from\x1fto"（用 sep 拼接，避免歧义）。
func dijkstra(g *Graph, src, dst Vertex, removedV map[Vertex]bool, removedE map[string]bool) ([]Vertex, uint64) {
	if removedV[src] || removedV[dst] {
		return nil, maxW
	}

	dist := map[Vertex]uint64{src: 0}
	prev := map[Vertex]Vertex{}
	visited := map[Vertex]bool{}

	pq := &priorityQueue{}
	heap.Push(pq, &pqItem{v: src, dist: 0})

	for pq.Len() > 0 {
		cur := heap.Pop(pq).(*pqItem)
		u := cur.v
		if visited[u] {
			continue
		}
		visited[u] = true
		if u == dst {
			break
		}

		for _, e := range g.Adj[u] { // 邻接已按 To 字典序排序
			if removedV[e.To] {
				continue
			}
			if removedE[edgeKey(u, e.To)] {
				continue
			}
			nd := cur.dist + e.W
			if old, ok := dist[e.To]; !ok || nd < old {
				dist[e.To] = nd
				prev[e.To] = u
				heap.Push(pq, &pqItem{v: e.To, dist: nd})
			}
			// 注意：相等不更新 prev，保持先到（字典序更小的松弛源）稳定。
		}
	}

	if !visited[dst] {
		return nil, maxW
	}

	// 回溯路径。
	var rev []Vertex
	for v := dst; ; v = prev[v] {
		rev = append(rev, v)
		if v == src {
			break
		}
	}
	slices.Reverse(rev)
	return rev, dist[dst]
}

// edgeKey 拼接有向边 key（用 sep 分隔，与顶点编码同分隔符无歧义）。
func edgeKey(from, to Vertex) string {
	return string(from) + sep + string(to)
}

// edgeWeight 返回 from→to 边权（存在则 ok=true）。
func edgeWeight(g *Graph, from, to Vertex) (uint64, bool) {
	for _, e := range g.Adj[from] {
		if e.To == to {
			return e.W, true
		}
	}
	return 0, false
}

// prefixWeight 计算 path 前 i 段边的权重和（path[0]→path[1]→…→path[i]）。
// 调用方传入的 path 为已确认的最短路，各边必然存在。
func prefixWeight(g *Graph, path []Vertex, i int) uint64 {
	var total uint64
	for j := 0; j < i; j++ {
		w, _ := edgeWeight(g, path[j], path[j+1])
		total += w
	}
	return total
}

// candidate 是 Yen's 候选路径，携带预算好的总权重 w（避免选优时重算）。
type candidate struct {
	path []Vertex
	w    uint64
}

// lessCandidate 比较两候选：先按缓存权重，再按顶点序列字典序 tiebreak（确定性）。
func lessCandidate(a, b candidate) bool {
	if a.w != b.w {
		return a.w < b.w
	}
	return comparePathSeq(a.path, b.path) < 0
}

// comparePathSeq 按顶点序列字典序比较。
func comparePathSeq(a, b []Vertex) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if c := strings.Compare(string(a[i]), string(b[i])); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

// pathSeqKey 把路径转成可哈希 key（去重用）。
func pathSeqKey(p []Vertex) string {
	var b strings.Builder
	for _, v := range p {
		b.WriteString(string(v))
		b.WriteByte('\x00')
	}
	return b.String()
}

// KShortest 在拆点 Graph 上算 src→dst 的 K 条近优路径（按总权重升序，确定性 tiebreak）。
//
// 采用 Yen's algorithm（基于 Dijkstra 的 spur path）。
//   - 确定性：同权重路径按顶点序列字典序 tiebreak；Dijkstra 内部松弛顺序由邻接
//     字典序 + 距离相等顶点字典序保证。
//   - 返回每条路径 []Vertex（含 src 与 dst）。
//   - 不可达返回空 [][]Vertex{}；K 超过可用路径数 → 返回全部不重复路径。
//   - src==dst 约定返回空；K<=0 返回空。
func KShortest(g *Graph, src, dst Vertex, K int) [][]Vertex {
	result := [][]Vertex{}
	if K <= 0 || src == dst {
		return result
	}

	// A: 已确定的 K 最短路。
	first, w := dijkstra(g, src, dst, nil, nil)
	if first == nil || w >= maxW {
		return result
	}
	result = append(result, first)

	// 已选路径 key 集合（去重）。
	chosen := map[string]bool{pathSeqKey(first): true}

	// B: 线性扫描候选集（基准路由路径数小，简单优先，不必上 container/heap）。
	// 每个候选携带预算好的总权重 w，避免选优内循环里反复 pathWeight 重算。
	var candidates []candidate
	candSeen := map[string]bool{} // 候选去重（避免重复入候选集）

	for len(result) < K {
		prevPath := result[len(result)-1]

		// spur node 遍历 prevPath 上除终点外每个顶点。
		for i := 0; i+1 < len(prevPath); i++ {
			spurNode := prevPath[i]
			rootPath := prevPath[:i+1] // 含 spurNode

			removedE := map[string]bool{}
			removedV := map[Vertex]bool{}

			// 对所有已选 A 路径：若其 rootPath 与当前 rootPath 相同，则移除其
			// 第 i 段出边，强制 spur path 走不同分支。
			for _, p := range result {
				if len(p) > i && comparePathSeq(p[:i+1], rootPath) == 0 {
					removedE[edgeKey(p[i], p[i+1])] = true
				}
			}

			// 移除 rootPath 上除 spurNode 外的顶点（避免回环、保证简单路径）。
			for j := 0; j < i; j++ {
				removedV[rootPath[j]] = true
			}

			spurPath, sw := dijkstra(g, spurNode, dst, removedV, removedE)
			if spurPath == nil || sw >= maxW {
				continue
			}

			// total = rootPath[:i] + spurPath（spurPath[0]==spurNode==rootPath[i]）。
			total := make([]Vertex, 0, len(rootPath)+len(spurPath)-1)
			total = append(total, rootPath[:i]...)
			total = append(total, spurPath...)

			key := pathSeqKey(total)
			if chosen[key] || candSeen[key] {
				continue
			}
			candSeen[key] = true
			// 总权重 = rootPath 前缀边权（spurNode 之前的 i 段）+ spur 距离 sw。
			// 避免后续选优时对全路径重算 pathWeight。
			candidates = append(candidates, candidate{path: total, w: prefixWeight(g, prevPath, i) + sw})
		}

		if len(candidates) == 0 {
			break // 无更多候选。
		}

		// 选出候选中最优者（缓存权重升序 + 顶点序列字典序 tiebreak）。
		bestIdx := 0
		for j := 1; j < len(candidates); j++ {
			if lessCandidate(candidates[j], candidates[bestIdx]) {
				bestIdx = j
			}
		}
		best := candidates[bestIdx]
		// 从候选集移除。
		candidates = append(candidates[:bestIdx], candidates[bestIdx+1:]...)
		delete(candSeen, pathSeqKey(best.path))

		result = append(result, best.path)
		chosen[pathSeqKey(best.path)] = true
	}

	return result
}

// Hop 是压缩后的基准路由跳：到达 Node 节点经入口 Ingress。
type Hop struct {
	Node    string
	Ingress string
}

// RoutePair 是基准路由的源/目的中转对（物理节点 ID）。
type RoutePair struct {
	Src string
	Dst string
}

// BaselineRoutes 在剪枝边集上为每对不同中转算 K 条基准路由（压缩成 []Hop）。
//
//   - 内部 BuildSplitGraph(prunedEdges) 构拆点图。
//   - 对每对不同 transit (A,B)：KShortest(g, coreV(A), coreV(B), K)，每条 []Vertex
//     路径压缩为 []Hop：遍历顶点，遇 ingV 顶点（parseV isCore=false）记一个
//     Hop{node,ingress}，跳过所有 core 顶点（含起点 A∶core）。
//   - 结果 map：key=RoutePair{A,B}，value=K 条 Hop 序列（按总权重升序，确定性）。
//   - 无路径的对不放入 map；自反对 (A,A) 不处理。
//
// 确定性：transits 顺序不影响结果（KShortest 与压缩均确定，map 不依赖插入序）。
func BaselineRoutes(prunedEdges []PrunedEdge, transits []string, K int) map[RoutePair][][]Hop {
	g := BuildSplitGraph(prunedEdges)
	out := make(map[RoutePair][][]Hop)

	for _, a := range transits {
		for _, b := range transits {
			if a == b {
				continue
			}
			paths := KShortest(g, coreV(a), coreV(b), K)
			if len(paths) == 0 {
				continue // 不可达对不入 map。
			}
			hopSeqs := make([][]Hop, 0, len(paths))
			for _, p := range paths {
				hopSeqs = append(hopSeqs, compressPath(p))
			}
			out[RoutePair{Src: a, Dst: b}] = hopSeqs
		}
	}

	return out
}

// compressPath 把拆点路径压缩成 []Hop：每个 ingV 顶点记一跳，跳过 core 顶点。
func compressPath(path []Vertex) []Hop {
	var hops []Hop
	for _, v := range path {
		node, ingress, isCore := parseV(v)
		if node == "" || isCore {
			continue // 非法或 core 顶点跳过。
		}
		hops = append(hops, Hop{Node: node, Ingress: ingress})
	}
	return hops
}
