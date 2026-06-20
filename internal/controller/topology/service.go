package topology

import (
	"reflect"
	"sort"
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/protobuf/proto"
)

// service.go: 优化器下发集成 + damping 触发外壳（规格 §3.5 / §3.7）。
//
// TopoService 把 P1 Receiver（入口/质量/边事件接收）+ P2 IncrementalOptimizer（增量
// 优化）+ topostore（持久化）串成拓扑大脑：
//
//   - 周期触发（Tick）+ 事件触发（OnEvent 攒边到间隔批量 ApplyEdgeEvents），带 damping
//     （最小重算间隔 MinInterval）。
//   - 把 topology.Result 转成 per-node genv1.TopologyAssignment 供 configsvc 下发。
//   - 启动时从持久化加载即服务（Load）。
//
// 解耦（避免循环 import）：本文件不直接 import topostore / configsvc / P1 Receiver，
// 全部依赖通过注入接口（IngressSource / ResultStore / Notifier）+ 注入 clock 提供，
// 真实实现由 P4 装配时传入。这样 topology 包保持无外部 controller 依赖，
// 且测试可注入 mock + 确定性时钟（无真实 sleep）。

// IngressSource 提供当前入口集 / 质量矩阵 / 入口明细（真实由 P1 Receiver 实现）。
type IngressSource interface {
	// Snapshot 返回当前入口资格输入集 + 入口级质量矩阵。
	Snapshot() (nodes []NodeEligibilityInput, qm QualityMatrix)
	// IngressDetail 查某节点某入口的完整 genv1.Ingress（用于填充 NeighborRef.Ingresses）。
	IngressDetail(nodeID, ingressID string) (*genv1.Ingress, bool)
	// NodeFingerprint 返回节点的证书指纹（A4：mesh pin 注入前置）。
	// 无指纹时 ok=false（新节点 / 尚未签发 / 适配器未注入）。
	NodeFingerprint(nodeID string) (fp string, ok bool)
	// NodeVIPs 返回所有节点的 VIP 映射（nodeID → VIP，不含 /32 后缀）。
	// 用于 computeFIB 生成 per-node 转发信息库。
	// 未注入查询源时返回空 map（FIB 不填充，向后兼容）。
	NodeVIPs() map[string]string
}

// ResultStore 持久化拓扑结果（真实由 topostore 实现）。
//
// 边界取舍：接口直接存取 topology.Result 对象（而非 []byte blob），Result↔blob 的
// 序列化由实现方负责（P4 用 topostore.MarshalResult / UnmarshalResult）。
// 这样 service.go 无需 import topostore，彻底规避 topostore→topology 的循环 import。
type ResultStore interface {
	SaveResultObj(r Result) error
	LoadLatestResultObj() (r Result, ok bool, err error)
}

// Notifier 触发对受影响节点的下发（真实由 configsvc.Notify 实现）。
type Notifier interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// OptimizerParams 是优化器编排参数（透传给 Optimize / IncrementalOptimizer）。
type OptimizerParams struct {
	MaxPeers    int
	IngressK    int
	RouteK      int
	LeafUplinks int
	ProbeFull   bool
	ProbeLimit  int
}

// DampingParams 控制重算节流：MinInterval 是两次重算的最小间隔。
//
// 间隔内的触发（Tick / OnEvent）被记为 pending，不立即重算；到达间隔后的下一次触发
// 合并执行所有积压的变化。MinInterval<=0 表示不节流（每次触发即重算）。
type DampingParams struct {
	MinInterval time.Duration
}

// TopoServiceDeps 是 NewTopoService 的注入依赖集。
type TopoServiceDeps struct {
	Recv    IngressSource
	Store   ResultStore
	Notify  Notifier
	Clock   func() time.Time // 注入时钟（确定性测试）；nil 回落 time.Now。
	Params  OptimizerParams
	Damping DampingParams
	// OnError 是可选错误回调：Tick / OnEvent 这类 fire-and-forget 路径下重算 / 持久化
	// 失败时调用，供 P4 观测（记日志 / 上报）。nil 时静默忽略（但 pending 仍保留重试）。
	OnError func(error)
}

// TopoService 是拓扑大脑：触发重算（带 damping）→ 转 assignments → 持久化 + 下发。
type TopoService struct {
	recv    IngressSource
	store   ResultStore
	notify  Notifier
	clock   func() time.Time
	params  OptimizerParams
	damping DampingParams
	onError func(error)

	mu             sync.Mutex
	inc            *IncrementalOptimizer                // 增量优化器（首次重算后建立）。
	assignments    map[string]*genv1.TopologyAssignment // 最新 per-node 分配。
	version        uint64                               // 单调版本计数。
	lastRecompute  time.Time                            // 上次重算时间（damping 判定）。
	hasRecomputed  bool                                 // 是否已重算过（区分初始零值时间）。
	pendingTrigger bool                                 // 间隔内被抑制的周期/全量触发标记。
	pendingEvents  []EdgeDelta                          // 攒下的边事件，到间隔批量应用。
	lastResult     *Result                              // 上次已应用的拓扑结果（变更检测用）。
}

// NewTopoService 构造拓扑服务（不自动加载，调用方按需 Load）。
func NewTopoService(deps TopoServiceDeps) *TopoService {
	clk := deps.Clock
	if clk == nil {
		clk = time.Now
	}
	return &TopoService{
		recv:        deps.Recv,
		store:       deps.Store,
		notify:      deps.Notify,
		clock:       clk,
		params:      deps.Params,
		damping:     deps.Damping,
		onError:     deps.OnError,
		assignments: make(map[string]*genv1.TopologyAssignment),
	}
}

// reportError 调用注入的 OnError 回调（nil 时静默）。
func (s *TopoService) reportError(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

// tentativeVersion 返回下一个候选版本号（调用方持锁）。
// 仅用于 flushLocked 计算新 Result；只在变更检测通过后才 commit 到 s.version。
func (s *TopoService) tentativeVersion() uint64 {
	return s.version + 1
}

// canRecomputeNow 判定 damping：距上次重算是否已达 MinInterval（调用方持锁）。
func (s *TopoService) canRecomputeNow() bool {
	if s.damping.MinInterval <= 0 || !s.hasRecomputed {
		return true
	}
	return s.clock().Sub(s.lastRecompute) >= s.damping.MinInterval
}

// Recompute 强制执行一次全量重算（无视 damping），version 由调用方指定。
//
// 流程：Snapshot → Optimize → buildAssignments → SaveResultObj（持久化）→
// RecomputeAndNotify（受影响节点）。建立 / 重置增量优化器基线。
func (s *TopoService) Recompute(version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recomputeLocked(version)
}

// recomputeLocked 执行全量重算（调用方持锁）。
func (s *TopoService) recomputeLocked(version uint64) error {
	nodes, qm := s.recv.Snapshot()
	in := s.optimizerInput(version, nodes, qm)

	// 建立 / 重置增量优化器基线（后续 OnEvent 走增量）。
	s.inc = NewIncremental(in)
	res := s.inc.Result()

	return s.applyResultLocked(res)
}

// applyResultLocked 把一个 Result 落地：转 assignments → 持久化 → 下发（调用方持锁）。
func (s *TopoService) applyResultLocked(res Result) error {
	assigns := s.buildAssignments(res)

	if err := s.store.SaveResultObj(res); err != nil {
		return err
	}

	s.assignments = assigns
	s.lastRecompute = s.clock()
	s.hasRecomputed = true
	if res.Version > s.version {
		s.version = res.Version
	}

	// 保存已应用的结果（深拷贝），供 flushLocked 做变更检测。
	cp := copyResult(res)
	s.lastResult = &cp

	// 受影响节点：本实现为全量重算 / 批量增量，受影响节点取全集（确定性排序）。
	affected := make([]string, 0, len(assigns))
	for id := range assigns {
		affected = append(affected, id)
	}
	sort.Strings(affected)
	if len(affected) > 0 {
		s.notify.RecomputeAndNotify(affected...)
	}
	return nil
}

// optimizerInput 用注入参数 + 快照构造 OptimizerInput。
func (s *TopoService) optimizerInput(version uint64, nodes []NodeEligibilityInput, qm QualityMatrix) OptimizerInput {
	return OptimizerInput{
		Version:     version,
		Nodes:       nodes,
		Quality:     qm,
		MaxPeers:    s.params.MaxPeers,
		IngressK:    s.params.IngressK,
		RouteK:      s.params.RouteK,
		LeafUplinks: s.params.LeafUplinks,
		ProbeFull:   s.params.ProbeFull,
		ProbeLimit:  s.params.ProbeLimit,
	}
}

// Tick 周期触发：受 damping 节流。
//
//   - 未达 MinInterval：记 pendingTrigger，不重算。
//   - 达 MinInterval：执行重算；若有积压边事件则批量增量应用，否则全量重算。
func (s *TopoService) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.canRecomputeNow() {
		s.pendingTrigger = true
		return
	}
	s.flushLocked()
}

// OnEvent 接收一个边事件：攒入队列，到间隔批量 ApplyEdgeEvents（受 damping 节流）。
//
//   - 未达 MinInterval：仅入队（pendingEvents），不重算。
//   - 达 MinInterval：立即批量应用积压事件（含本次）。
func (s *TopoService) OnEvent(e EdgeDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pendingEvents = append(s.pendingEvents, e)
	if !s.canRecomputeNow() {
		return
	}
	s.flushLocked()
}

// resultContentEqual 比较两个 Result 的拓扑内容（忽略 Version），
// 用于变更检测：仅在拓扑实际变化时递增版本号。
func resultContentEqual(a, b *Result) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(a.Roles, b.Roles) &&
		reflect.DeepEqual(a.Neighbors, b.Neighbors) &&
		reflect.DeepEqual(a.Baseline, b.Baseline) &&
		reflect.DeepEqual(a.ProbeSets, b.ProbeSets)
}

// flushLocked 执行积压触发（调用方持锁，且已判定可重算）。
//
//   - 有积压边事件且增量基线存在 → 批量 ApplyEdgeEvents（增量优化）。
//   - 否则 → 全量重算。
//
// 变更检测：计算新 Result 后与上次已应用的 lastResult 做内容比较（忽略 Version）。
// 若拓扑未变化，跳过版本递增 / 持久化 / 节点通知（避免无变更时空转）。
//
// 数据安全：仅在重算 + 持久化成功（无 error）时才 drain pending；失败时保留 pending
// （下次 Tick/OnEvent 重试），并经 reportError 上报，避免静默丢数据。
// 注意增量路径失败时 inc 内部缓存已前进（ApplyEdgeEvents 已 mutate）：为避免下次对已
// 前进的 inc 重复 apply 同批事件导致语义偏移，失败时丢弃增量基线（inc=nil）并保留
// pendingTrigger，使下次走全量——全量用最新 Snapshot（已含这些边变化）重算，不丢数据。
func (s *TopoService) flushLocked() {
	if len(s.pendingEvents) > 0 && s.inc != nil {
		events := s.pendingEvents
		tentativeVer := s.tentativeVersion()
		res := s.inc.ApplyEdgeEvents(events, tentativeVer)

		// 变更检测：拓扑内容未变则跳过，仅 drain 事件队列。
		if s.lastResult != nil && resultContentEqual(s.lastResult, &res) {
			s.pendingTrigger = false
			s.pendingEvents = nil
			return
		}

		s.version = tentativeVer
		if err := s.applyResultLocked(res); err != nil {
			s.reportError(err)
			s.inc = nil
			s.pendingTrigger = true
			return
		}
		s.pendingTrigger = false
		s.pendingEvents = nil
		return
	}

	// 无积压边事件（纯周期触发）或无增量基线 → 全量重算。
	tentativeVer := s.tentativeVersion()
	nodes, qm := s.recv.Snapshot()
	in := s.optimizerInput(tentativeVer, nodes, qm)
	newInc := NewIncremental(in)
	res := newInc.Result()

	// 变更检测：拓扑内容未变则跳过版本递增和下发，仅更新增量基线。
	if s.lastResult != nil && resultContentEqual(s.lastResult, &res) {
		s.inc = newInc // 更新增量基线（后续 OnEvent 仍可走增量路径）。
		s.pendingTrigger = false
		s.pendingEvents = nil
		return
	}

	s.version = tentativeVer
	s.inc = newInc
	if err := s.applyResultLocked(res); err != nil {
		s.reportError(err)
		s.pendingTrigger = true
		return
	}
	s.pendingTrigger = false
	s.pendingEvents = nil
}

// Load 启动加载即服务：从 store 取最新 Result → 重建 assignments。
//
//   - 有持久化：重建 assignments + version，直接可服务（AssignmentForNode 即返回）。
//     注意：不重建增量优化器基线（缺持久化快照的 OptimizerInput）；首次 Tick/Recompute
//     会用当前 Snapshot 建立基线。因此 Load 后的首个 OnEvent 因无增量基线（inc==nil）
//     会在 flushLocked 中退化为全量重算（而非 ApplyEdgeEvents），符合预期。
//   - 无持久化：返回 ok=false，assignments 空，等首次 Recompute。
func (s *TopoService) Load() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, ok, err := s.store.LoadLatestResultObj()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	s.assignments = s.buildAssignments(res)
	if res.Version > s.version {
		s.version = res.Version
	}
	cp := copyResult(res)
	s.lastResult = &cp
	return true, nil
}

// AssignmentForNode 返回某节点的最新拓扑分配（供 configsvc 注入 NodeConfig.Topology）。
//
// 返回的是 proto.Clone 出的**独立副本**：service 内部 assignment 对象在重算时会被整体
// 替换，且可能被并发读。跨包边界（P4 configsvc 拿去塞 NodeConfig）不应依赖调用方守约，
// 返回独立副本彻底消除别名 / 并发竞争风险，调用方可自由读取乃至 mutate。
func (s *TopoService) AssignmentForNode(nodeID string) (*genv1.TopologyAssignment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.assignments[nodeID]
	if !ok {
		return nil, false
	}
	return proto.Clone(a).(*genv1.TopologyAssignment), true
}

// ServiceStatus 拓扑服务运行时状态快照。
type ServiceStatus struct {
	Version       uint64
	TransitCount  int
	LeafCount     int
	LastRecompute time.Time
}

// Status 返回拓扑服务运行时状态快照。
func (s *TopoService) Status() ServiceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var transit, leaf int
	for _, a := range s.assignments {
		switch a.GetRole() {
		case genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT:
			transit++
		case genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF:
			leaf++
		}
	}
	return ServiceStatus{
		Version:       s.version,
		TransitCount:  transit,
		LeafCount:     leaf,
		LastRecompute: s.lastRecompute,
	}
}

// snapshotAssignments 返回 assignments 的浅拷贝（测试 / 确定性对照用）。
func (s *TopoService) snapshotAssignments() map[string]*genv1.TopologyAssignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*genv1.TopologyAssignment, len(s.assignments))
	for k, v := range s.assignments {
		out[k] = v
	}
	return out
}

// pendingEventCount 返回当前积压边事件数（测试用）。
func (s *TopoService) pendingEventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pendingEvents)
}

// buildAssignments 把 topology.Result 转成 per-node genv1.TopologyAssignment。
//
// 字段映射（确定性，所有 slice 按 NodeID / DstNode 字典序）：
//   - Version：透传 r.Version。
//   - Role：Roles[A] → NodeTopoRole（RoleTransit→TRANSIT, RoleLeaf→LEAF）。
//   - Neighbors：Neighbors[A] 每 NeighborSpec{NodeID,Ingresses[]} →
//     NeighborRef{NodeId, Ingresses:[]*genv1.Ingress（用 recv.IngressDetail 查完整）}。
//   - BaselineRoutes：Baseline 中所有 {A,dst} 对的全部 K 条路由 → Route2{DstNode, Hops}，
//     topology.Hop{Node,Ingress} → genv1.Hop{NodeId,IngressId}；
//     供节点侧 SessionRouter 一致性哈希选路 + Degrade 降级。
//   - ProbeTargets：ProbeSets[A] 每 ProbeTarget{NodeID,IngressIDs} → genv1.ProbeTarget。
func (s *TopoService) buildAssignments(r Result) map[string]*genv1.TopologyAssignment {
	// 收集全部节点 ID（来自 Roles，覆盖中转 + 叶子）。
	out := make(map[string]*genv1.TopologyAssignment, len(r.Roles))

	// 预聚合每节点的 baseline 路由：Baseline key 是 RoutePair{Src,Dst}。
	// 下发全部 K 条路由供节点侧 SessionRouter 一致性哈希选路 + Degrade 降级。
	baselineBySrc := make(map[string][]*genv1.Route2)
	for pair, routes := range r.Baseline {
		for _, route := range routes {
			hops := make([]*genv1.Hop, 0, len(route))
			for _, h := range route {
				hops = append(hops, &genv1.Hop{NodeId: h.Node, IngressId: h.Ingress})
			}
			baselineBySrc[pair.Src] = append(baselineBySrc[pair.Src], &genv1.Route2{
				DstNode: pair.Dst,
				Hops:    hops,
			})
		}
	}
	// 每节点 baseline 路由按 DstNode 分组，同 DstNode 内按 Hop 序列字典序排列（确定性）。
	for src := range baselineBySrc {
		routes := baselineBySrc[src]
		sort.SliceStable(routes, func(i, j int) bool {
			if routes[i].DstNode != routes[j].DstNode {
				return routes[i].DstNode < routes[j].DstNode
			}
			// 同 DstNode：按逐跳 NodeId+IngressId 字典序（确定性排序）。
			hi, hj := routes[i].Hops, routes[j].Hops
			for k := 0; k < len(hi) && k < len(hj); k++ {
				ki := hi[k].NodeId + "/" + hi[k].IngressId
				kj := hj[k].NodeId + "/" + hj[k].IngressId
				if ki != kj {
					return ki < kj
				}
			}
			return len(hi) < len(hj)
		})
	}

	for nodeID, role := range r.Roles {
		a := &genv1.TopologyAssignment{
			Version: r.Version,
			Role:    mapRole(role),
		}

		// Neighbors（按 NodeID 升序；Ingresses 查完整明细）。
		specs := r.Neighbors[nodeID]
		neighbors := make([]*genv1.NeighborRef, 0, len(specs))
		for _, sp := range specs {
			ref := &genv1.NeighborRef{NodeId: sp.NodeID}
			for _, ingID := range sp.Ingresses {
				if det, ok := s.recv.IngressDetail(sp.NodeID, ingID); ok && det != nil {
					ref.Ingresses = append(ref.Ingresses, det)
				} else {
					// 明细缺失时退化为仅含 Id 的占位（不丢入口信息）。
					ref.Ingresses = append(ref.Ingresses, &genv1.Ingress{Id: ingID})
				}
			}
			// 填充邻居证书指纹（A4）：由 IngressSource.NodeFingerprint 提供，
			// 无指纹时（适配器未注入 / 节点尚未签发）跳过，保持字段零值。
			if fp, ok := s.recv.NodeFingerprint(sp.NodeID); ok {
				ref.Fingerprint = fp
			}
			neighbors = append(neighbors, ref)
		}
		sort.Slice(neighbors, func(i, j int) bool { return neighbors[i].NodeId < neighbors[j].NodeId })
		a.Neighbors = neighbors

		// BaselineRoutes（已按 DstNode 排序）。
		a.BaselineRoutes = baselineBySrc[nodeID]

		// ProbeTargets（按 NodeID 升序）。
		probes := r.ProbeSets[nodeID]
		targets := make([]*genv1.ProbeTarget, 0, len(probes))
		for _, p := range probes {
			ids := append([]string(nil), p.IngressIDs...)
			targets = append(targets, &genv1.ProbeTarget{NodeId: p.NodeID, IngressIds: ids})
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].NodeId < targets[j].NodeId })
		a.ProbeTargets = targets

		out[nodeID] = a
	}

	// FIB: 计算并填充 VIP 转发信息库。
	nodeVIPs := s.recv.NodeVIPs()
	if len(nodeVIPs) > 0 {
		fibs := computeFIB(&r, nodeVIPs, r.Version)
		for nodeID, asg := range out {
			if fib, ok := fibs[nodeID]; ok {
				asg.Fib = fib
			}
		}
	}

	return out
}

// mapRole 把 topology.Role 映射到 genv1.NodeTopoRole。
func mapRole(r Role) genv1.NodeTopoRole {
	switch r {
	case RoleTransit:
		return genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT
	case RoleLeaf:
		return genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF
	default:
		return genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED
	}
}
