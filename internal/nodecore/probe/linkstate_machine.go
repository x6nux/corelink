package probe

import "time"

// LinkState 是一条链路的离散状态（四档中的"档位"）。
//
// 四档逻辑（§4.5）由 LinkState 的转换 + 持续时间共同表达：
//   - Healthy：链路健康；
//   - Degraded：链路持续劣化（已跨 THold），已上报；
//   - Down：链路断连（连续 DownConfirm 次失败），已上报。
//
// "瞬时劣化"不是一个独立 LinkState：它是 Healthy 状态下"劣化计时进行中但尚未跨
// THold"的过渡阶段，本地消化、不转换状态、不上报。
type LinkState int

const (
	// Healthy 链路健康（默认初始状态）。
	Healthy LinkState = iota
	// Degraded 链路持续劣化（已上报 Degraded 事件）。
	Degraded
	// Down 链路断连（已上报 Down 事件）。
	Down
)

// String 返回状态的可读名称。
func (s LinkState) String() string {
	switch s {
	case Healthy:
		return "Healthy"
	case Degraded:
		return "Degraded"
	case Down:
		return "Down"
	default:
		return "Unknown"
	}
}

// FSMEventKind 是 FSM 转换事件的类型。
type FSMEventKind int

const (
	// EventDown 链路断连，需立即上报。
	EventDown FSMEventKind = iota
	// EventDegraded 链路持续劣化（跨 THold），需上报。
	EventDegraded
	// EventRecovered 链路从 Down/Degraded 恢复到 Healthy，需上报。
	EventRecovered
)

// String 返回事件类型的可读名称。
func (k FSMEventKind) String() string {
	switch k {
	case EventDown:
		return "Down"
	case EventDegraded:
		return "Degraded"
	case EventRecovered:
		return "Recovered"
	default:
		return "Unknown"
	}
}

// FSMEvent 是 LinkFSM 在状态转换时产生的、应上报的事件。
//
// 仅在状态发生**转换**时产生（不重复刷同一状态）。RTT / Loss 携带触发该事件
// 时的探测样本（Down 事件无有意义样本，置 0）。
type FSMEvent struct {
	Kind         FSMEventKind
	RTTMs        uint32
	LossPermille uint32
}

// LinkFSMConfig 是单条链路状态机的参数（§4.5）。
type LinkFSMConfig struct {
	// ProbeInterval 探测周期（默认 5s）。仅用于文档 / 默认值，状态机本身
	// 以注入 clock 的真实间隔为准。
	ProbeInterval time.Duration

	// DownConfirm 连续失败多少次确认断连（默认 3）。
	DownConfirm int

	// THold 劣化需持续多久才上报（默认 45s）。瞬时劣化（< THold）本地消化。
	THold time.Duration

	// TRecover 恢复需保持多久才回 Healthy（默认 30s）。
	TRecover time.Duration

	// 滞回阈值：劣化触发与恢复触发分离，防 flapping。

	// DegradeLossPermille 劣化触发：loss > 此值（默认 50 = 5%）。
	DegradeLossPermille uint32
	// DegradeRTTFactor 劣化触发：RTT > 基线 * 此倍（默认 1.5）。
	DegradeRTTFactor float64
	// RecoverLossPermille 恢复触发：loss < 此值（默认 20 = 2%）。
	RecoverLossPermille uint32
	// RecoverRTTFactor 恢复触发：RTT <= 基线 * 此倍（默认 1.2）。
	RecoverRTTFactor float64

	// BaselineEWMAAlpha 健康期基线 RTT 的 EWMA 平滑系数（0~1，默认 0.2）。
	// 越大越跟随新样本，越小越平滑。0 时退化为"仅用首个健康 RTT"。
	BaselineEWMAAlpha float64
}

// DefaultLinkFSMConfig 是 §4.5 推荐默认配置。
func DefaultLinkFSMConfig() LinkFSMConfig {
	return LinkFSMConfig{
		ProbeInterval:       5 * time.Second,
		DownConfirm:         3,
		THold:               45 * time.Second,
		TRecover:            30 * time.Second,
		DegradeLossPermille: 50,
		DegradeRTTFactor:    1.5,
		RecoverLossPermille: 20,
		RecoverRTTFactor:    1.2,
		BaselineEWMAAlpha:   0.2,
	}
}

// LinkFSM 是单条链路（一个 ProbeTarget）的状态机。
//
// 非并发安全：每条链路一个实例，由 Reporter 串行驱动（Reporter 自身加锁）。
type LinkFSM struct {
	cfg   LinkFSMConfig
	clock func() time.Time

	state LinkState

	// consecFails 连续探测失败计数（ok=false）。任意一次 ok=true 清零。
	consecFails int

	// baseline 基线 RTT（毫秒）。健康期以 EWMA 维护；haveBaseline 为 false 时未初始化。
	baseline     float64
	haveBaseline bool

	// degradeSince 劣化（样本越过劣化阈值）的起始时间；零值表示当前未处于劣化计时中。
	degradeSince time.Time

	// recoverSince 恢复候选（样本回到恢复阈值内、且当前非 Healthy）的起始时间；
	// 零值表示当前未处于恢复计时中。
	recoverSince time.Time
}

// NewLinkFSM 创建一条链路状态机。clock 注入时钟（测试用 fake，生产用 time.Now）。
func NewLinkFSM(cfg LinkFSMConfig, clock func() time.Time) *LinkFSM {
	return &LinkFSM{
		cfg:   cfg,
		clock: clock,
		state: Healthy,
	}
}

// State 返回当前离散状态（供观测 / 测试）。
func (f *LinkFSM) State() LinkState { return f.state }

// Baseline 返回当前基线 RTT（毫秒，四舍五入）；未初始化时返回 0。
func (f *LinkFSM) Baseline() uint32 {
	if !f.haveBaseline {
		return 0
	}
	return uint32(f.baseline + 0.5)
}

// Observe 喂入一次探测结果，返回应上报的事件（可能为空 nil）。
//
// 四档逻辑 + 滞回 + 事件去重（仅转换时发一次）全部在此实现：
//   - ok=false：累计连续失败，达 DownConfirm 且当前非 Down → 转 Down，发 EventDown；
//     未达阈值或已 Down 时不发事件（本地消化 / 不重复刷）。
//   - ok=true：先更新连续失败计数与基线，再按滞回阈值判定劣化 / 恢复：
//   - 劣化（loss>DegradeLoss 或 RTT>baseline*DegradeRTTFactor）：
//     若当前 Healthy，启动 / 维持劣化计时；持续 ≥ THold → 转 Degraded，发 EventDegraded；
//     未跨 THold 即"瞬时劣化"，本地消化不发事件。
//   - 恢复（loss<RecoverLoss 且 RTT<=baseline*RecoverRTTFactor）：
//     若当前非 Healthy，启动 / 维持恢复计时；持续 ≥ TRecover → 转 Healthy，发 EventRecovered。
//   - 介于劣化与恢复阈值之间（滞回带）：不改变计时方向、不转换状态、不发事件。
func (f *LinkFSM) Observe(rttMs, lossPermille uint32, ok bool) []FSMEvent {
	now := f.clock()

	if !ok {
		return f.observeFail()
	}
	return f.observeSuccess(now, rttMs, lossPermille)
}

// observeFail 处理一次失败探测（ok=false）。失败路径不涉及计时窗口判定，无需 now。
func (f *LinkFSM) observeFail() []FSMEvent {
	f.consecFails++
	// 失败样本不更新基线，也不参与劣化/恢复阈值判定。
	// 失败中断恢复计时（链路又不可达了）。
	f.recoverSince = time.Time{}

	if f.state == Down {
		// 已 Down，不重复发事件。
		return nil
	}

	if f.consecFails >= f.cfg.DownConfirm {
		// 连续失败达阈值 → 断连。
		f.state = Down
		f.degradeSince = time.Time{}
		f.recoverSince = time.Time{}
		return []FSMEvent{{Kind: EventDown}}
	}

	// 未达 DownConfirm：本地消化（瞬时丢包 / 单次超时），不发事件、不转换。
	return nil
}

// observeSuccess 处理一次成功探测（ok=true）。
func (f *LinkFSM) observeSuccess(now time.Time, rttMs, lossPermille uint32) []FSMEvent {
	f.consecFails = 0

	// 初始化基线：首个成功样本作为基线起点。
	if !f.haveBaseline {
		f.baseline = float64(rttMs)
		f.haveBaseline = true
	}

	degrade := f.isDegradeTrigger(rttMs, lossPermille)
	canRecover := f.isRecoverTrigger(rttMs, lossPermille)

	switch f.state {
	case Healthy:
		// 健康期才更新基线 EWMA（且仅在样本未触发劣化时，避免劣化样本污染基线）。
		if !degrade {
			f.updateBaseline(rttMs)
		}
		return f.observeHealthy(now, rttMs, lossPermille, degrade)
	case Degraded, Down:
		return f.observeUnhealthy(now, rttMs, lossPermille, canRecover)
	default:
		return nil
	}
}

// observeHealthy 在 Healthy 状态下处理成功样本：判定是否进入劣化计时 / 转 Degraded。
func (f *LinkFSM) observeHealthy(now time.Time, rttMs, lossPermille uint32, degrade bool) []FSMEvent {
	if !degrade {
		// 样本健康：清除劣化计时（之前的瞬时劣化结束，本地消化）。
		f.degradeSince = time.Time{}
		return nil
	}

	// 样本劣化：启动 / 维持劣化计时。
	if f.degradeSince.IsZero() {
		f.degradeSince = now
	}

	if now.Sub(f.degradeSince) >= f.cfg.THold {
		// 持续劣化跨 THold → 上报 Degraded。
		f.state = Degraded
		f.degradeSince = time.Time{}
		f.recoverSince = time.Time{}
		return []FSMEvent{{Kind: EventDegraded, RTTMs: rttMs, LossPermille: lossPermille}}
	}

	// 瞬时劣化（< THold）：本地消化，不发事件。
	return nil
}

// observeUnhealthy 在 Degraded / Down 状态下处理成功样本：判定是否进入恢复计时 / 转 Healthy。
func (f *LinkFSM) observeUnhealthy(now time.Time, rttMs, lossPermille uint32, canRecover bool) []FSMEvent {
	if !canRecover {
		// 样本仍未达恢复阈值（含落在滞回带内）：清除恢复计时，保持当前状态。
		f.recoverSince = time.Time{}
		return nil
	}

	// 样本达恢复阈值：启动 / 维持恢复计时。
	if f.recoverSince.IsZero() {
		f.recoverSince = now
	}

	if now.Sub(f.recoverSince) >= f.cfg.TRecover {
		// 恢复保持跨 TRecover → 回 Healthy，重置基线为当前样本。
		f.state = Healthy
		f.recoverSince = time.Time{}
		f.degradeSince = time.Time{}
		f.baseline = float64(rttMs)
		f.haveBaseline = true
		return []FSMEvent{{Kind: EventRecovered, RTTMs: rttMs, LossPermille: lossPermille}}
	}

	// 恢复中但未满 TRecover：保持当前状态，不发事件。
	return nil
}

// isDegradeTrigger 判定样本是否触发劣化（loss 或 RTT 任一越过劣化阈值）。
func (f *LinkFSM) isDegradeTrigger(rttMs, lossPermille uint32) bool {
	if lossPermille > f.cfg.DegradeLossPermille {
		return true
	}
	if f.haveBaseline && f.cfg.DegradeRTTFactor > 0 {
		if float64(rttMs) > f.baseline*f.cfg.DegradeRTTFactor {
			return true
		}
	}
	return false
}

// isRecoverTrigger 判定样本是否满足恢复（loss 与 RTT 都回到恢复阈值内）。
func (f *LinkFSM) isRecoverTrigger(rttMs, lossPermille uint32) bool {
	if lossPermille >= f.cfg.RecoverLossPermille {
		return false
	}
	if f.haveBaseline && f.cfg.RecoverRTTFactor > 0 {
		if float64(rttMs) > f.baseline*f.cfg.RecoverRTTFactor {
			return false
		}
	}
	return true
}

// updateBaseline 用 EWMA 更新健康期基线 RTT。
func (f *LinkFSM) updateBaseline(rttMs uint32) {
	a := f.cfg.BaselineEWMAAlpha
	if a <= 0 {
		// alpha<=0：保持首个健康 RTT 作为固定基线。
		return
	}
	if a > 1 {
		a = 1
	}
	f.baseline = a*float64(rttMs) + (1-a)*f.baseline
}
