// Package topology builds and operates on the relay overlay topology graph.
//
// 拆点展开图 (node-splitting graph): 每个中转节点 R 拆成 N 个"入口顶点"
// R∶e1..eN (入站属性) + 1 个"出站核心顶点" R∶core (出站能力)。这样在该图
// 上跑最短路得到的路径天然是 `A∶core → B∶e2 → B∶core → ...` 即
// `节点∶入口` 序列，统一表达"选哪个中转节点 + 用该中转哪个入口"。
package topology

import (
	"slices"
	"strings"
)

// sep 是顶点编码使用的不可见分隔符 (US, Unit Separator, \x1f)。
// 假设: node ID 与 ingress ID 均不包含 \x1f 字符 (普通标识符不会出现该控制符)，
// 因此编码无歧义。
const sep = "\x1f"

// coreToken 与 ingToken 是顶点类型标记。
const (
	coreToken = "core"
	ingToken  = "i"
)

// Vertex 是拆点展开图中的顶点编码。
// 形如 `node\x1fcore`（出站核心顶点）或 `node\x1fi\x1fingressID`（入口顶点）。
type Vertex string

// coreV 返回节点的出站核心顶点。
func coreV(node string) Vertex {
	return Vertex(node + sep + coreToken)
}

// ingV 返回节点某入口的入口顶点。
func ingV(node, ingressID string) Vertex {
	return Vertex(node + sep + ingToken + sep + ingressID)
}

// parseV 逆解析顶点编码。
//   - 对 coreV(n): 返回 (n, "", true)
//   - 对 ingV(n,e): 返回 (n, e, false)
//
// 非法编码返回 (node="", ingressID="", isCore=false)。调用方可据 node=="" 判断非法。
func parseV(v Vertex) (node, ingressID string, isCore bool) {
	s := string(v)
	parts := strings.Split(s, sep)
	switch {
	case len(parts) == 2 && parts[1] == coreToken && parts[0] != "":
		return parts[0], "", true
	case len(parts) == 3 && parts[1] == ingToken && parts[0] != "":
		return parts[0], parts[2], false
	default:
		return "", "", false
	}
}

// NodeIngresses 描述一个节点及其入口 ID 列表。
// 本包仅需 ingress ID（不需完整的 genv1.Ingress）。
type NodeIngresses struct {
	NodeID    string
	Ingresses []string
}

// QKey 是质量矩阵的键：从 SrcNode 拨向 DstNode 的入口 DstIngress。
type QKey struct {
	SrcNode    string
	DstNode    string
	DstIngress string
}

// QualityMatrix 是边权矩阵。缺失的 (src,dst,ingress) 视为不可达，不建边。
type QualityMatrix map[QKey]uint64

// Edge 是一条带权有向边。
type Edge struct {
	To Vertex
	W  uint64
}

// Graph 是拆点展开图，以邻接表表示。
type Graph struct {
	Adj map[Vertex][]Edge
}

// BuildGraph 构造拆点展开图。
//
//   - 每个 transit：建 N 个入口顶点 + 1 个核心顶点；节点内边 ingV(B,e)→coreV(B) 权 0。
//   - 跨节点边：对每对 transit A≠B、B 的每个入口 e：若 qm[{A,B,e}] 存在 →
//     建边 coreV(A)→ingV(B,e) 权该值。
//   - 每个 leaf：只建 coreV；对每个 transit B 的每个入口 e：若 qm[{leaf,B,e}]
//     存在 → 建边 coreV(leaf)→ingV(B,e) 权该值（叶子接入）。
//   - 邻接表内边按 To 顶点字典序排序，保证确定性。
//
// 前置条件（调用方保证，纯函数不做运行时校验，与既有风格一致）：
//   - transit 与 leaf 的 NodeID 集合不相交（同一节点不能既是中转又是叶子）。
//   - 节点列表内 NodeID 唯一（transits 内唯一、leaves 内唯一）。
//   - 单节点内 ingress ID 唯一。
//
// 若违反上述前置条件，会重复建顶点/边，语义未定义。
func BuildGraph(transits []NodeIngresses, leaves []NodeIngresses, qm QualityMatrix) *Graph {
	g := &Graph{Adj: make(map[Vertex][]Edge)}

	ensure := func(v Vertex) {
		if _, ok := g.Adj[v]; !ok {
			g.Adj[v] = nil
		}
	}

	// 1. 建 transit 顶点 + 节点内边。
	for _, t := range transits {
		ensure(coreV(t.NodeID))
		for _, e := range t.Ingresses {
			iv := ingV(t.NodeID, e)
			ensure(iv)
			// 节点内边 ingV(B,e) → coreV(B) 权 0（同进程零成本）。
			g.Adj[iv] = append(g.Adj[iv], Edge{To: coreV(t.NodeID), W: 0})
		}
	}

	// 2. 跨节点边（transit A → transit B，A≠B）。
	for _, a := range transits {
		for _, b := range transits {
			if a.NodeID == b.NodeID {
				continue
			}
			for _, e := range b.Ingresses {
				if w, ok := qm[QKey{SrcNode: a.NodeID, DstNode: b.NodeID, DstIngress: e}]; ok {
					src := coreV(a.NodeID)
					g.Adj[src] = append(g.Adj[src], Edge{To: ingV(b.NodeID, e), W: w})
				}
			}
		}
	}

	// 3. 叶子接入边（leaf core → transit ingress）。
	for _, l := range leaves {
		ensure(coreV(l.NodeID)) // 叶子只有 core，不建入口顶点。
		for _, b := range transits {
			for _, e := range b.Ingresses {
				if w, ok := qm[QKey{SrcNode: l.NodeID, DstNode: b.NodeID, DstIngress: e}]; ok {
					src := coreV(l.NodeID)
					g.Adj[src] = append(g.Adj[src], Edge{To: ingV(b.NodeID, e), W: w})
				}
			}
		}
	}

	// 4. 邻接表内边按 To 顶点字典序排序（确定性）。
	for v := range g.Adj {
		edges := g.Adj[v]
		slices.SortFunc(edges, func(a, b Edge) int { return strings.Compare(string(a.To), string(b.To)) })
	}

	return g
}

// Neighbors 返回顶点 v 的出边副本（调用方修改不影响内部状态）。
func (g *Graph) Neighbors(v Vertex) []Edge {
	src := g.Adj[v]
	if len(src) == 0 {
		return nil
	}
	out := make([]Edge, len(src))
	copy(out, src)
	return out
}

// Vertices 返回所有顶点，按字典序排序（确定性）。
func (g *Graph) Vertices() []Vertex {
	out := make([]Vertex, 0, len(g.Adj))
	for v := range g.Adj {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
