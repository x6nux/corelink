package topology

import (
	"slices"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// minConfidence 是入口被视为"稳定可达"的最小置信度阈值。
// 规格 §3.3：仅经探测验证可达 (Reachable) 且置信度达阈值的入口才计入中转资格。
const minConfidence uint32 = 60

// IngressMeta 描述单个入口的资格信息。
//   - ID:         入口标识。
//   - Confidence: 探测置信度（0-100），越高越可信。
//   - Reachable:  是否经探测验证可达（仅可达入口计入资格）。
type IngressMeta struct {
	ID         string
	Confidence uint32
	Reachable  bool
}

// NodeEligibilityInput 是 ClassifyNodes 的单节点输入。
type NodeEligibilityInput struct {
	NodeID    string
	Nat       genv1.NatType
	Ingresses []IngressMeta
}

// Eligible 判定一个节点是否可作为中转候选。
//
// 合格条件：至少存在一个 Reachable==true 且 Confidence>=minConfidence 的入口。
//
// 关于 NAT：判定看"有无稳定可达入口"，而非单看 NAT 类型。
//   - SYMMETRIC 且无稳定可达入口 → 只能叶子（false）。
//   - SYMMETRIC 但有 reachable 高置信入口（如 CDN / 网卡公网）→ 仍合格（true）。
//
// 因此 nat 参数当前不直接改变判定结果（稳定入口的存在已隐含可中转性），
// 保留该参数是为了显式表达规格语义并便于未来按 NAT 细化策略。
func Eligible(nat genv1.NatType, ingresses []IngressMeta) bool {
	_ = nat // 见 doc：判定以"稳定可达入口"为准，NAT 不单独否决。
	for _, ing := range ingresses {
		if ing.Reachable && ing.Confidence >= minConfidence {
			return true
		}
	}
	return false
}

// ClassifyNodes 批量将节点分为中转候选与叶子。
//
// 前置条件（调用方保证，纯函数不做运行时校验，与 graph.go BuildGraph 风格一致）：
//   - nodes 内 NodeID 唯一（重复会在两列表中各出现一次，语义未定义）。
//
// 返回的 transits / leaves 均按 NodeID 字典序排序（确定性）。
func ClassifyNodes(nodes []NodeEligibilityInput) (transits, leaves []string) {
	for _, n := range nodes {
		if Eligible(n.Nat, n.Ingresses) {
			transits = append(transits, n.NodeID)
		} else {
			leaves = append(leaves, n.NodeID)
		}
	}
	slices.Sort(transits)
	slices.Sort(leaves)
	return transits, leaves
}
