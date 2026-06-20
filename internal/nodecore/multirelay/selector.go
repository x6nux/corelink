// Package multirelay 实现 agent 的多 relay ingress 选择与自适应切换（spec §7.2.1）。
//
// 模型（§7.2.1）：
//   - agent 维护一个候选 relay 列表（来自 NodeConfig.Relays，按 Priority 升序，小=优先）。
//   - 任意时刻只保持 1 个主接入连接（Current）；其余候选做低频「无连接」探测
//     （轻量 UDP/RTT，不建 mTLS/隧道），用注入的 Probe 函数测 RTT/可用性。
//   - 切换决策（带滞回防抖）：
//   - 候选明显更优：某候选 RTT 比当前接入低至少 SwitchMargin 毫秒，且
//     连续 SwitchStreak 次（滞回）→ 切换到该候选。
//   - 主连接劣化：注入 ReportPrimaryDegraded 连续 DegradeStreak 次，或当前
//     接入探测连续失败 → 切到当前可用的最优候选。
//   - 切换 = OnSwitch(old,new) 回调（上层据此对新 relay 建接入 + 关旧接入）
//   - OnLocation 上报（detach 旧 relay、attach 新 relay）。
//
// 本包不直接建连/发包：探测器、时钟、切换/上报回调全部注入，便于确定性测试
// （注入假时钟避免真实 sleep flaky）。上层（agent 主循环）周期调用 Tick 驱动探测，
// 或自行起 goroutine 调 Run。
package multirelay

import (
	"context"
	"sort"
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// Probe 对一个候选 relay 做轻量无连接探测，返回 RTT（毫秒）与是否可达。
// 实现应轻量（一次 UDP 往返 + 超时），不建立 mTLS/隧道。
type Probe func(ep *genv1.RelayEndpoint) (rttMs int, ok bool)

// SwitchCallback 在接入 relay 切换时调用：from=旧 relayID，to=新 relayID。
// 上层据此对 to 建接入、关 from 接入。
type SwitchCallback func(from, to string)

// LocationCallback 在切换时上报位置变更：attached=true 为接入新 relay，
// false 为离开旧 relay。上层据此调用 controller.ReportNodeLocation。
type LocationCallback func(relayID string, attached bool)

// IngressEndpoint 是「按选定入口接入」所需的拨号参数（Task3.6）。
//
// S7 下发的接入中转含选定入口（genv1.NeighborRef.Ingresses / TopologyAssignment）——
// 入口选择已内生于下发路径，multirelay 不再做第二层选优，只按下发入口建立接入。
// 当入口为 CDN（IsCDN）时上层用 SNI 拨号（CDN 边缘据 SNI 路由），指纹仍验节点身份。
type IngressEndpoint struct {
	// IngressID 是选定入口的 ID（genv1.Ingress.Id）。
	IngressID string
	// Addr 是该入口的拨号地址（host:port）。
	Addr string
	// SNI 是 CDN 入口的 SNI hostname（IsCDN 时非空，普通入口留空）。
	SNI string
	// IsCDN 标记该入口经 CDN 边缘接入（拨号须用 SNI）。
	IsCDN bool
}

// IngressResolver 把目标 relayID 解析为其「下发的选定入口」拨号参数（Task3.6）。
//
// 返回 nil 表示该 relay 无选定入口信息（上层按默认 RelayEndpoint.Udp/Tunnel 接入，兼容）。
// 注入此回调即启用「按入口接入」；不注入则 Selector 行为与既有完全一致。
type IngressResolver func(relayID string) *IngressEndpoint

// SwitchIngressCallback 在切换接入 relay 时附带选定入口信息回调（Task3.6）。
//
// 与 OnSwitch 并列：OnSwitch 仅给 relayID，OnSwitchIngress 额外给新接入 relay 的
// 选定入口端点（含 CDN SNI 拨号参数）。上层据此对 to 按选定入口（含 CDN SNI）建立接入。
// ep 可能为 nil（无入口信息时退回默认接入）。
type SwitchIngressCallback func(from, to string, ep *IngressEndpoint)

// Config 构造 Selector 的参数。
type Config struct {
	// Candidates 是候选 relay 列表（来自 NodeConfig.Relays）。按 Priority 升序排序后，
	// 列表首项作为初始主接入。空列表时 Selector 无接入（Current 返回 ""）。
	Candidates []*genv1.RelayEndpoint

	// Probe 必填：对候选 relay 做无连接探测。
	Probe Probe

	// Clock 注入时钟（返回当前时间），nil 用 time.Now。
	Clock func() time.Time

	// ProbeInterval 是候选探测的最小周期；Tick 距上次探测不足此间隔则跳过。默认 5s。
	ProbeInterval time.Duration

	// SwitchMargin 是候选被判为「明显更优」所需比当前接入低的 RTT 毫秒数。默认 30。
	SwitchMargin int
	// SwitchStreak 是切换到更优候选所需的连续更优观测次数（滞回）。默认 3。
	SwitchStreak int
	// DegradeStreak 是主连接被判劣化所需的连续劣化信号次数。默认 2。
	DegradeStreak int

	// OnSwitch 可选：切换接入 relay 时回调（上层建新接入/关旧接入）。
	OnSwitch SwitchCallback
	// OnLocation 可选：切换时上报位置（detach 旧、attach 新）。
	OnLocation LocationCallback

	// IngressResolver 可选（Task3.6）：把 relayID 解析为下发的选定入口拨号参数。
	// 注入即启用「按入口接入」——切换/查询时据此带上 CDN SNI 等拨号信息。
	IngressResolver IngressResolver
	// OnSwitchIngress 可选（Task3.6）：切换时附带新接入 relay 的选定入口端点回调。
	// 与 OnSwitch 并列触发（OnSwitch 先、OnSwitchIngress 后）。
	OnSwitchIngress SwitchIngressCallback
}

func (c *Config) applyDefaults() {
	if c.ProbeInterval <= 0 {
		c.ProbeInterval = 5 * time.Second
	}
	if c.SwitchMargin <= 0 {
		c.SwitchMargin = 30
	}
	if c.SwitchStreak <= 0 {
		c.SwitchStreak = 3
	}
	if c.DegradeStreak <= 0 {
		c.DegradeStreak = 2
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
}

// Selector 维护候选 relay 列表与当前主接入，按探测与质量信号自适应切换。
//
// 并发安全：mu 保护 current/计数/lastProbe。
type Selector struct {
	cfg        Config
	candidates []*genv1.RelayEndpoint // 已按 Priority 升序

	mu           sync.Mutex
	current      string // 当前主接入 relayID
	lastProbe    time.Time
	betterStreak int    // 同一候选连续「明显更优」计数
	betterID     string // 当前正在累计的更优候选
	degradeCount int    // 主连接连续劣化计数
	primaryFail  int    // 主连接连续探测失败计数
}

// New 构造 Selector，并将候选列表首项（按 Priority）设为初始主接入。
func New(cfg Config) *Selector {
	cfg.applyDefaults()
	cands := make([]*genv1.RelayEndpoint, len(cfg.Candidates))
	copy(cands, cfg.Candidates)
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].GetPriority() < cands[j].GetPriority()
	})
	s := &Selector{cfg: cfg, candidates: cands}
	if len(cands) > 0 {
		s.current = cands[0].GetRelayId()
	}
	return s
}

// Current 返回当前主接入 relayID（无候选时为 ""）。
func (s *Selector) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// ReportPrimaryDegraded 注入一次主连接质量劣化信号（来自 bind 双通道质量监控
// 或上层观测）。累计达 DegradeStreak 后，下次 Tick 切到可用的最优候选。
func (s *Selector) ReportPrimaryDegraded() {
	s.mu.Lock()
	s.degradeCount++
	s.mu.Unlock()
}

// ReportPrimaryHealthy 注入一次主连接健康信号，清零劣化计数（防抖）。
func (s *Selector) ReportPrimaryHealthy() {
	s.mu.Lock()
	s.degradeCount = 0
	s.mu.Unlock()
}

// candidateByID 返回指定 relayID 的候选端点（找不到返回 nil）。
func (s *Selector) candidateByID(id string) *genv1.RelayEndpoint {
	for _, ep := range s.candidates {
		if ep.GetRelayId() == id {
			return ep
		}
	}
	return nil
}

// Tick 执行一轮探测 + 切换决策。上层周期调用（或经 Run 自动调度）。
//
// 距上次探测不足 ProbeInterval 则跳过本轮（节流），返回是否实际执行了探测。
func (s *Selector) Tick() bool {
	now := s.cfg.Clock()

	s.mu.Lock()
	if !s.lastProbe.IsZero() && now.Sub(s.lastProbe) < s.cfg.ProbeInterval {
		s.mu.Unlock()
		return false
	}
	s.lastProbe = now
	cur := s.current
	degraded := s.degradeCount >= s.cfg.DegradeStreak
	s.mu.Unlock()

	if cur == "" {
		return true
	}

	// 探测当前接入：失败累计，连续失败视为不可用。
	var curRTT int
	curOK := false
	if ep := s.candidateByID(cur); ep != nil {
		curRTT, curOK = s.cfg.Probe(ep)
	}

	// 探测其余候选，挑出可用且 RTT 最低者作为最优候选。
	bestID := ""
	bestRTT := 0
	for _, ep := range s.candidates {
		id := ep.GetRelayId()
		if id == cur {
			continue
		}
		rtt, ok := s.cfg.Probe(ep)
		if !ok {
			continue
		}
		if bestID == "" || rtt < bestRTT {
			bestID = id
			bestRTT = rtt
		}
	}

	s.mu.Lock()
	if !curOK {
		s.primaryFail++
	} else {
		s.primaryFail = 0
	}
	primaryUnusable := !curOK && s.primaryFail >= s.cfg.DegradeStreak
	degraded = degraded || s.degradeCount >= s.cfg.DegradeStreak

	// ── 决策 1：主连接劣化/不可用 → 切到当前可用最优候选（不要求 margin）。
	if (degraded || primaryUnusable) && bestID != "" {
		from := s.current
		s.switchLocked(bestID)
		s.mu.Unlock()
		s.fireSwitch(from, bestID)
		return true
	}

	// ── 决策 2：候选明显更优（带滞回）。
	// 仅当主连接可用且某候选 RTT 比当前低至少 SwitchMargin 时累计滞回。
	if curOK && bestID != "" && curRTT-bestRTT >= s.cfg.SwitchMargin {
		if s.betterID == bestID {
			s.betterStreak++
		} else {
			s.betterID = bestID
			s.betterStreak = 1
		}
		if s.betterStreak >= s.cfg.SwitchStreak {
			from := s.current
			s.switchLocked(bestID)
			s.mu.Unlock()
			s.fireSwitch(from, bestID)
			return true
		}
	} else {
		// 不再更优（含候选变差/不可用）：清零滞回计数，防抖动误切。
		s.betterStreak = 0
		s.betterID = ""
	}
	s.mu.Unlock()
	return true
}

// switchLocked 在持有 mu 时把当前接入切到 newID 并重置所有滞回/劣化计数。
func (s *Selector) switchLocked(newID string) {
	s.current = newID
	s.betterStreak = 0
	s.betterID = ""
	s.degradeCount = 0
	s.primaryFail = 0
}

// fireSwitch 在锁外触发 OnSwitch / OnLocation / OnSwitchIngress 回调
// （detach 旧、attach 新；如启用入口接入则附带新接入 relay 的选定入口）。
func (s *Selector) fireSwitch(from, to string) {
	if s.cfg.OnLocation != nil {
		if from != "" {
			s.cfg.OnLocation(from, false)
		}
		s.cfg.OnLocation(to, true)
	}
	if s.cfg.OnSwitch != nil {
		s.cfg.OnSwitch(from, to)
	}
	if s.cfg.OnSwitchIngress != nil {
		var ep *IngressEndpoint
		if s.cfg.IngressResolver != nil {
			ep = s.cfg.IngressResolver(to)
		}
		s.cfg.OnSwitchIngress(from, to, ep)
	}
}

// CurrentIngress 返回当前主接入 relay 的选定入口端点（Task3.6）。
//
// 未注入 IngressResolver 或当前无接入 / 无入口信息时返回 nil。
// 上层据此对当前接入按选定入口（含 CDN SNI）建立连接。
func (s *Selector) CurrentIngress() *IngressEndpoint {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur == "" || s.cfg.IngressResolver == nil {
		return nil
	}
	return s.cfg.IngressResolver(cur)
}

// Run 以 ProbeInterval 为周期持续调用 Tick，直到 ctx 取消（生产路径）。
//
// 测试应直接驱动 Tick + 注入假时钟，避免真实 sleep flaky；Run 仅供 agent
// 主循环在真实时钟下使用。
func (s *Selector) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.ProbeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Candidates 返回按 Priority 排序后的候选 relayID 列表（快照）。
func (s *Selector) Candidates() []string {
	ids := make([]string, len(s.candidates))
	for i, ep := range s.candidates {
		ids[i] = ep.GetRelayId()
	}
	return ids
}
