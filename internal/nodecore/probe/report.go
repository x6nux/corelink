package probe

import (
	"sort"
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// EmitEventFunc 是事件上报回调（高优先级，立即上报）。
//
// 生产实现由 gRPC IngressService.ReportEdgeEvent 完成；测试注入 fake 收集 EdgeEvent。
type EmitEventFunc func(*genv1.EdgeEvent)

// EmitQualityFunc 是周期质量上报回调（低频背景）。
//
// 生产实现由 gRPC 周期质量上报完成；测试注入 fake 收集 QualityReport。
type EmitQualityFunc func(*genv1.QualityReport)

// QualityDamping 控制周期质量上报的阻尼（防小变化刷屏），风格对齐 mesh.DampingConfig。
type QualityDamping struct {
	// MinInterval 两次质量上报之间的最小间隔；间隔不足即使有变化也不报。
	MinInterval time.Duration
	// RTTThreshold RTT 显著变化的最小幅度（毫秒）；小于此值视为微小变化。
	RTTThreshold uint32
	// LossThreshold 丢包率显著变化的最小幅度（千分比）。
	LossThreshold uint32
}

// DefaultQualityDamping 是合理的默认质量上报阻尼配置。
func DefaultQualityDamping() QualityDamping {
	return QualityDamping{
		MinInterval:   30 * time.Second,
		RTTThreshold:  10,
		LossThreshold: 10,
	}
}

// qualitySample 是一条链路的最新质量样本（用于周期上报与 damping 比较）。
type qualitySample struct {
	target       ProbeTarget
	rttMs        uint32
	lossPermille uint32
	ok           bool
	updated      bool // 自上次上报以来是否被刷新过
}

// Reporter 聚合各链路 LinkFSM 事件，驱动双通道上报。
//
//   - 事件上报（OnProbe → LinkFSM.Observe → FSMEvent → EmitEvent）：高优先级、立即；
//   - 周期质量上报（Tick → 带 damping 的 EmitQuality）：低频背景。
//
// 并发安全：内部加锁，可被探测调度协程与 Tick 协程并发调用。
type Reporter struct {
	mu sync.Mutex

	selfNode string // EdgeEvent.SrcNode / QualityReport.SrcNode

	cfg     LinkFSMConfig
	clock   func() time.Time
	damping QualityDamping

	emitEvent   EmitEventFunc
	emitQuality EmitQualityFunc

	fsms    map[string]*LinkFSM       // target.Key() → FSM
	samples map[string]*qualitySample // target.Key() → 最新样本

	lastReported  map[string]qualitySample // target.Key() → 上次上报时的样本（damping 比较基准）
	lastQualityAt time.Time                // 上次质量上报时间

	// targets 是当前活跃目标集（SetTargets/RemoveTarget 维护）。
	// 为空（nil）时退化为旧行为：以所有累积样本为准（不限制活跃集）。
	targets map[string]ProbeTarget
}

// ReporterConfig 配置 Reporter。
type ReporterConfig struct {
	SelfNode    string
	FSM         LinkFSMConfig
	Damping     QualityDamping
	Clock       func() time.Time
	EmitEvent   EmitEventFunc
	EmitQuality EmitQualityFunc
}

// NewReporter 创建一个 Reporter。Clock 必填（确定性测试 / 生产 time.Now）。
func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Reporter{
		selfNode:     cfg.SelfNode,
		cfg:          cfg.FSM,
		clock:        cfg.Clock,
		damping:      cfg.Damping,
		emitEvent:    cfg.EmitEvent,
		emitQuality:  cfg.EmitQuality,
		fsms:         make(map[string]*LinkFSM),
		samples:      make(map[string]*qualitySample),
		lastReported: make(map[string]qualitySample),
		targets:      make(map[string]ProbeTarget),
	}
}

// fsmFor 返回 target 对应的 LinkFSM（不存在则惰性创建）。调用方须持锁。
func (r *Reporter) fsmFor(t ProbeTarget) *LinkFSM {
	k := t.Key()
	fsm, ok := r.fsms[k]
	if !ok {
		fsm = NewLinkFSM(r.cfg, r.clock)
		r.fsms[k] = fsm
	}
	return fsm
}

// OnProbe 喂入一次对 target 的探测结果。
//
// 驱动对应 LinkFSM.Observe → 若产生事件则立即 EmitEvent（事件上报通道）；
// 同时累积质量样本供周期上报（Tick）使用。
func (r *Reporter) OnProbe(target ProbeTarget, rttMs, lossPermille uint32, ok bool) {
	r.mu.Lock()

	fsm := r.fsmFor(target)
	events := fsm.Observe(rttMs, lossPermille, ok)

	// 累积 / 更新质量样本。
	k := target.Key()
	r.samples[k] = &qualitySample{
		target:       target,
		rttMs:        rttMs,
		lossPermille: lossPermille,
		ok:           ok,
		updated:      true,
	}

	// 在锁内构造 EdgeEvent（读取 selfNode），但回调在锁外执行避免回调内重入死锁。
	var toEmit []*genv1.EdgeEvent
	for _, ev := range events {
		toEmit = append(toEmit, &genv1.EdgeEvent{
			SrcNode:      r.selfNode,
			DstNode:      target.NodeID,
			IngressId:    target.IngressID,
			Kind:         fsmKindToProto(ev.Kind),
			RttMs:        ev.RTTMs,
			LossPermille: ev.LossPermille,
		})
	}
	r.mu.Unlock()

	if r.emitEvent != nil {
		for _, e := range toEmit {
			r.emitEvent(e)
		}
	}
}

// Tick 周期触发质量上报（带 damping）。
//
// 收集自上次上报以来刷新过的质量样本，若整体满足 damping 条件（距上次上报间隔足够，
// 且至少一条样本发生显著变化），则构造 QualityReport 并 EmitQuality；否则抑制不报。
func (r *Reporter) Tick() {
	r.mu.Lock()

	now := r.clock()

	// damping：间隔不足直接抑制（首次上报不受间隔限制）。
	if !r.lastQualityAt.IsZero() && now.Sub(r.lastQualityAt) < r.damping.MinInterval {
		r.mu.Unlock()
		return
	}

	// 判断是否存在显著变化，并收集所有当前样本作为上报内容。
	significant := false
	samples := make([]*genv1.EdgeSample, 0, len(r.samples))
	tsUnix := now.Unix()

	// 确定性顺序：按 key 排序，避免 map 遍历乱序影响测试 / 上报稳定性。
	// 若已设置活跃目标集（targets 非空），仅上报仍活跃的样本；否则退化为全量样本。
	keys := make([]string, 0, len(r.samples))
	for k := range r.samples {
		if len(r.targets) > 0 {
			if _, active := r.targets[k]; !active {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		s := r.samples[k]
		prev, had := r.lastReported[k]
		if !had || isQualitySignificant(prev, *s, r.damping) {
			significant = true
		}
		samples = append(samples, &genv1.EdgeSample{
			DstNode:      s.target.NodeID,
			IngressId:    s.target.IngressID,
			RttMs:        s.rttMs,
			LossPermille: s.lossPermille,
			TsUnix:       tsUnix,
		})
	}

	if len(samples) == 0 || !significant {
		// 无样本或无显著变化：抑制（damping）。
		r.mu.Unlock()
		return
	}

	report := &genv1.QualityReport{
		SrcNode: r.selfNode,
		Samples: samples,
	}

	// 记录本次上报基准，重置 updated 标志。
	for _, k := range keys {
		s := r.samples[k]
		r.lastReported[k] = *s
		s.updated = false
	}
	r.lastQualityAt = now

	r.mu.Unlock()

	if r.emitQuality != nil {
		r.emitQuality(report)
	}
}

// State 返回 target 当前 FSM 状态（供观测 / 测试）；target 未知时返回 Healthy。
func (r *Reporter) State(target ProbeTarget) LinkState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fsm, ok := r.fsms[target.Key()]; ok {
		return fsm.State()
	}
	return Healthy
}

// SetTargets 用 targets 全量替换当前活跃目标集，并同步剪除不在新集合中的
// 历史 fsms/samples/lastReported，避免目标移除后 map 无界增长 + 陈旧样本上报。
func (r *Reporter) SetTargets(targets []ProbeTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]ProbeTarget, len(targets))
	for _, t := range targets {
		next[t.Key()] = t
	}
	// 剪除不再活跃的链路状态。
	for k := range r.fsms {
		if _, ok := next[k]; !ok {
			delete(r.fsms, k)
			delete(r.samples, k)
			delete(r.lastReported, k)
		}
	}
	r.targets = next
}

// RemoveTarget 移除单个目标并同步剪除其 fsms/samples/lastReported。
func (r *Reporter) RemoveTarget(target ProbeTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := target.Key()
	delete(r.targets, k)
	delete(r.fsms, k)
	delete(r.samples, k)
	delete(r.lastReported, k)
}

// trackedLen 返回当前被跟踪的链路数（fsms 槽数），供测试断言 map 不无界增长。
func (r *Reporter) trackedLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fsms)
}

// fsmKindToProto 把内部 FSMEventKind 映射到 proto EdgeEventKind。
func fsmKindToProto(k FSMEventKind) genv1.EdgeEventKind {
	switch k {
	case EventDown:
		return genv1.EdgeEventKind_EDGE_EVENT_KIND_DOWN
	case EventDegraded:
		return genv1.EdgeEventKind_EDGE_EVENT_KIND_DEGRADED
	case EventRecovered:
		return genv1.EdgeEventKind_EDGE_EVENT_KIND_RECOVERED
	default:
		return genv1.EdgeEventKind_EDGE_EVENT_KIND_UNSPECIFIED
	}
}

// isQualitySignificant 判断两次质量样本之间是否有显著变化（任一阈值越过，或 ok 状态翻转）。
func isQualitySignificant(prev, cur qualitySample, d QualityDamping) bool {
	if prev.ok != cur.ok {
		return true
	}
	if absDiffU32(prev.rttMs, cur.rttMs) >= d.RTTThreshold {
		return true
	}
	if absDiffU32(prev.lossPermille, cur.lossPermille) >= d.LossThreshold {
		return true
	}
	return false
}

func absDiffU32(a, b uint32) uint32 {
	if a >= b {
		return a - b
	}
	return b - a
}
