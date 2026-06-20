// Package topoadapter 把 P1 Receiver（入口/质量/边事件接收）与 P2 topostore（拓扑
// 结果持久化）适配为 topology.TopoService 所需的注入接口（IngressSource /
// ResultStore），并提供 EdgeEvent→EdgeDelta 的转换（供装配时把 Receiver 收到的边
// 事件回调进 TopoService.OnEvent）。
//
// 解耦定位：topology 包刻意只依赖窄接口（不 import ingress/topostore，规避循环
// import）。本适配器是 P4 装配的"接缝"，承载所有转换语义（资格判定、代表 NAT、
// 质量矩阵聚合、Result↔blob 序列化），让 topology 与 ingress/topostore 都保持纯净。
package topoadapter

import (
	"sort"

	"github.com/x6nux/corelink/internal/controller/ingress"
	"github.com/x6nux/corelink/internal/controller/topology"
	"github.com/x6nux/corelink/internal/controller/topostore"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// lossPenalty 是丢包对边权的惩罚系数：W = rtt_ms + loss_permille * lossPenalty。
//
// 取值取舍：质量矩阵的权值越小越优（rtt 单位 ms）。丢包千分比直接加权会让 1‰ 丢包
// 等价 1ms 延迟，惩罚过轻；这里给 10 倍系数，使 10‰（1%）丢包≈100ms 延迟惩罚，
// 让明显丢包的边在剪枝/K 路由中被合理降权。与 node-side probe 的权值口径保持温和
// 一致（同 ms 量纲，可调）。
const lossPenalty uint64 = 10

// reachableConfidence 是"仅凭置信度即视为可达"的阈值。与 topology.minConfidence
// （未导出）保持一致：置信度≥60 的入口即使尚无质量样本覆盖，也按稳定可达计入资格。
//
// 取该常量为本包私有副本而非 import topology 的未导出常量（Go 无法跨包引用未导出
// 标识符）：两处都引用规格 §3.3 的"稳定可达入口"阈值 60；若规格调整需同步两处。
const reachableConfidence uint32 = 60

// IngressSourceAdapter 包装 *ingress.Receiver，实现 topology.IngressSource。
//
// 它把 Receiver 在内存中保存的"每节点最新 IngressSet + 每源最新 QualityReport"
// 转换成优化器输入：节点资格输入（NodeEligibilityInput）+ 入口级质量矩阵
// （QualityMatrix），并支持按 (nodeID,ingressID) 反查完整 genv1.Ingress 明细。
//
// getFP 是可选的证书指纹查询函数（A4），由 main 装配后通过 SetFingerprintFn 注入，
// 避免改构造函数签名破坏既有调用点。nil 时 NodeFingerprint 始终返回无指纹。
//
// getVIPs 是可选的 VIP 查询函数，由 main 装配后通过 SetNodeVIPsFn 注入。
// nil 时 NodeVIPs 返回空 map（FIB 不填充，向后兼容）。
type IngressSourceAdapter struct {
	recv    *ingress.Receiver
	getFP   func(string) (string, bool, error) // 注入：store.GetCertFingerprint；nil 表示无来源。
	getVIPs func() (map[string]string, error)  // 注入：VIP 查询；nil 表示无来源。
}

// NewIngressSourceAdapter 构造适配器。
func NewIngressSourceAdapter(recv *ingress.Receiver) *IngressSourceAdapter {
	return &IngressSourceAdapter{recv: recv}
}

// SetFingerprintFn 注入证书指纹查询函数（A4 装配调用，运行期可选）。
// 函数签名与 store.GetCertFingerprint 一致：返回 (fp, ok, err)。
// nil 时 NodeFingerprint 始终返回无指纹（保持既有测试零改动）。
func (a *IngressSourceAdapter) SetFingerprintFn(fn func(string) (string, bool, error)) {
	a.getFP = fn
}

// NodeFingerprint 返回邻居节点的证书指纹（实现 topology.IngressSource 接口，A4）。
// 未注入 getFP / 查询失败 / 无指纹时均返回 ("", false)，不影响下发流程。
func (a *IngressSourceAdapter) NodeFingerprint(nodeID string) (string, bool) {
	if a.getFP == nil {
		return "", false
	}
	fp, ok, err := a.getFP(nodeID)
	if err != nil || !ok {
		return "", false
	}
	return fp, true
}

// SetNodeVIPsFn 注入节点 VIP 查询函数（P5 FIB 装配调用，运行期可选）。
// 函数签名：返回 (map[nodeID]vip, err)，VIP 不含 /32 后缀（纯 IP 如 "100.64.0.2"）。
// nil 时 NodeVIPs 返回空 map（FIB 不填充，保持既有测试零改动）。
func (a *IngressSourceAdapter) SetNodeVIPsFn(fn func() (map[string]string, error)) {
	a.getVIPs = fn
}

// NodeVIPs 返回所有节点的 VIP 映射（实现 topology.IngressSource 接口）。
// 未注入 getVIPs / 查询失败时返回空 map，不影响下发流程（FIB 字段留空）。
func (a *IngressSourceAdapter) NodeVIPs() map[string]string {
	if a.getVIPs == nil {
		return nil
	}
	vips, err := a.getVIPs()
	if err != nil {
		return nil
	}
	return vips
}

var _ topology.IngressSource = (*IngressSourceAdapter)(nil)

// Snapshot 返回当前入口资格输入集 + 入口级质量矩阵。
//
// 资格转换（每 IngressSet → NodeEligibilityInput）：
//   - NodeID：set.NodeId。
//   - Nat：代表 NAT，取入口列表中首个入口的 NatType（节点的入口通常同源同 NAT；
//     无入口时为 NAT_TYPE_UNKNOWN）。判定主要看"有无稳定可达入口"（见 eligibility.go），
//     NAT 仅作信息透传，不单独否决。
//   - Ingresses：每入口 → IngressMeta{ID, Confidence, Reachable}。
//     Reachable 判定（任一成立即可达）：
//     a) 被质量上报覆盖：存在某 src 的 QualityReport 含一条 (dst=本节点, ingress=本入口)
//     样本——说明已有节点成功拨到该入口并探到质量。
//     b) confidence ≥ reachableConfidence：高置信入口即使尚无质量覆盖也视为稳定可达。
//
// 质量矩阵聚合：遍历所有 QualityReport 的所有样本，按 QKey{src,dst,ingress} 聚合，
// 同 key 多样本取最小 W（最优观测）。W = rtt_ms + loss_permille*lossPenalty。
//
// 返回的 nodes 按 NodeID 升序（确定性，便于上层 golden）。
func (a *IngressSourceAdapter) Snapshot() (nodes []topology.NodeEligibilityInput, qm topology.QualityMatrix) {
	qm = a.buildQualityMatrix()

	// "被质量上报覆盖的入口"集合：key (dstNode,ingressID)。
	covered := make(map[[2]string]bool)
	for k := range qm {
		covered[[2]string{k.DstNode, k.DstIngress}] = true
	}

	sets := a.recv.AllIngressSets()
	nodes = make([]topology.NodeEligibilityInput, 0, len(sets))
	for _, set := range sets {
		if set == nil {
			continue
		}
		in := topology.NodeEligibilityInput{NodeID: set.NodeId}
		if len(set.Ingresses) > 0 {
			in.Nat = set.Ingresses[0].GetNatType()
		}
		for _, ing := range set.Ingresses {
			reachable := ing.GetConfidence() >= reachableConfidence ||
				covered[[2]string{set.NodeId, ing.GetId()}]
			in.Ingresses = append(in.Ingresses, topology.IngressMeta{
				ID:         ing.GetId(),
				Confidence: ing.GetConfidence(),
				Reachable:  reachable,
			})
		}
		nodes = append(nodes, in)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	return nodes, qm
}

// buildQualityMatrix 从所有 QualityReport 聚合入口级质量矩阵（同 key 取最小 W）。
func (a *IngressSourceAdapter) buildQualityMatrix() topology.QualityMatrix {
	qm := make(topology.QualityMatrix)
	for _, q := range a.recv.AllQuality() {
		if q == nil {
			continue
		}
		for _, s := range q.GetSamples() {
			key := topology.QKey{
				SrcNode:    q.GetSrcNode(),
				DstNode:    s.GetDstNode(),
				DstIngress: s.GetIngressId(),
			}
			w := sampleWeight(s.GetRttMs(), s.GetLossPermille())
			if cur, ok := qm[key]; !ok || w < cur {
				qm[key] = w
			}
		}
	}
	return qm
}

// IngressDetail 查某节点某入口的完整 genv1.Ingress（用于填充 NeighborRef.Ingresses）。
//
// 精确匹配 ingressID → 回退：若精确匹配失败且 ingressID 为空（旧 optimizer 结果），
// 返回第一个有 host 的入口（防止 topology result 与 IngressSet 的 Id 格式不一致导致
// neighbors 下发无地址）。
func (a *IngressSourceAdapter) IngressDetail(nodeID, ingressID string) (*genv1.Ingress, bool) {
	set, ok := a.recv.GetIngressSet(nodeID)
	if !ok || set == nil {
		return nil, false
	}
	// 精确匹配。
	for _, ing := range set.Ingresses {
		if ing.GetId() == ingressID {
			return ing, true
		}
	}
	// 回退：仅当查询的 ingressID 为空时（旧 optimizer 结果无 Id 的情况），
	// 返回首个有 host 的入口。非空 ingressID 精确匹配失败则确实不存在。
	if ingressID == "" {
		for _, ing := range set.Ingresses {
			if ing.GetHost() != "" {
				return ing, true
			}
		}
	}
	return nil, false
}

// sampleWeight 把 (rtt_ms, loss_permille) 折算成边权（越小越优）。
func sampleWeight(rttMs, lossPermille uint32) uint64 {
	return uint64(rttMs) + uint64(lossPermille)*lossPenalty
}

// EdgeEventToDelta 把 genv1.EdgeEvent 转成 topology.EdgeDelta（供装配时把 Receiver
// 收到的边事件回调进 TopoService.OnEvent，驱动增量重算）。
//
// 映射：
//   - EDGE_EVENT_KIND_DOWN      → EdgeDown（删边，W 忽略）。
//   - EDGE_EVENT_KIND_DEGRADED  → EdgeDegraded（增权，W=新权重）。
//   - EDGE_EVENT_KIND_RECOVERED → EdgeRecovered（减权/恢复，W=新权重）。
//   - 其他/未指定               → EdgeRecovered（保守：按权重更新，不删边）。
//
// W 用与质量矩阵一致的折算口径（rtt + loss*penalty），保证增量边权与全量快照同口径。
func EdgeEventToDelta(ev *genv1.EdgeEvent) topology.EdgeDelta {
	d := topology.EdgeDelta{
		Src:     ev.GetSrcNode(),
		Dst:     ev.GetDstNode(),
		Ingress: ev.GetIngressId(),
		W:       sampleWeight(ev.GetRttMs(), ev.GetLossPermille()),
	}
	switch ev.GetKind() {
	case genv1.EdgeEventKind_EDGE_EVENT_KIND_DOWN:
		d.Kind = topology.EdgeDown
	case genv1.EdgeEventKind_EDGE_EVENT_KIND_DEGRADED:
		d.Kind = topology.EdgeDegraded
	default: // RECOVERED 或未指定：按权重更新（不删边）。
		d.Kind = topology.EdgeRecovered
	}
	return d
}

// ResultStoreAdapter 包装 *topostore.TopoStore，实现 topology.ResultStore。
//
// Result↔blob 的序列化由 topostore.MarshalResult / UnmarshalResult 负责（处理
// RoutePair 作 JSON map key 的限制）；本适配器只做对象↔字节的桥接 + 版本透传。
type ResultStoreAdapter struct {
	ts *topostore.TopoStore
}

// NewResultStoreAdapter 构造适配器。
func NewResultStoreAdapter(ts *topostore.TopoStore) *ResultStoreAdapter {
	return &ResultStoreAdapter{ts: ts}
}

var _ topology.ResultStore = (*ResultStoreAdapter)(nil)

// SaveResultObj 序列化并按 r.Version 持久化拓扑结果。
func (a *ResultStoreAdapter) SaveResultObj(r topology.Result) error {
	blob, err := topostore.MarshalResult(r)
	if err != nil {
		return err
	}
	return a.ts.SaveResult(r.Version, blob)
}

// LoadLatestResultObj 读取最新版本拓扑结果并反序列化。无结果时 ok=false。
func (a *ResultStoreAdapter) LoadLatestResultObj() (topology.Result, bool, error) {
	_, blob, ok, err := a.ts.LoadLatestResult()
	if err != nil {
		return topology.Result{}, false, err
	}
	if !ok {
		return topology.Result{}, false, nil
	}
	r, err := topostore.UnmarshalResult(blob)
	if err != nil {
		return topology.Result{}, false, err
	}
	return r, true, nil
}
