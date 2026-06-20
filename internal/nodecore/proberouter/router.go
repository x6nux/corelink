// Package proberouter 实现节点自治路由：周期/事件驱动 Probe 探测，自动选择最优 NextHop。
package proberouter

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/dataplane"
	"github.com/x6nux/corelink/internal/nodecore/route"
	"github.com/x6nux/corelink/internal/transport"
)

// PeerInfo 描述一个 peer 节点。
type PeerInfo struct {
	NodeID string
	VIP    netip.Addr
}

// BestRoute 描述到某目标的最优路由。
type BestRoute struct {
	NextHopVIP netip.Addr // 第一跳 VIP（直连时=目标本身）
	NextHopID  string     // 第一跳 nodeID
	RTTMs      float64
	Label      string // "direct" / "via xxx(.y)"
	Stale      bool   // 本轮探测失败，使用上一轮结果
}

// targetResult 存储单个目标的穷举探测结果。
type targetResult struct {
	target PeerInfo
	ranked []rankedRoute
}

// Config 是 ProbeRouter 的配置。
type Config struct {
	DataPlane       *dataplane.DataPlane
	Router          *route.Engine
	SelfVIP         netip.Addr
	ProbeTimeout    time.Duration                                         // 单次 Probe 超时（默认 3s）
	TickInterval    time.Duration                                         // 兜底探测周期（默认 60s）
	Debounce        time.Duration                                         // 事件合并窗口（默认 1s）
	MaxViaDepth     int                                                   // 最大 via 跳数（默认 2）
	MaxConcurrency  int                                                   // 最大并行探测数（默认 10）
	OnMaxHopsUpdate func(int)                                             // 最长路由跳数变更回调（供 TTL 环路检测用）
	OnRouteUpdate   func(map[netip.Addr]BestRoute, map[netip.Addr]string) // 路由更新回调（best, vipToNodeID → 外部持久化）
}

// ProbeRouter 是节点自治路由引擎。
type ProbeRouter struct {
	dp     *dataplane.DataPlane
	router *route.Engine
	cfg    Config

	mu          sync.Mutex
	peers       []PeerInfo
	vipToNodeID map[netip.Addr]string
	best        map[netip.Addr]BestRoute
	maxHops     int // 当前路由表中最长路由的实际跳数
	probeCount  int // 已完成的探测轮数（首次=0）

	// peerRoutes 存储 peer 广播来的路由表（RouteSync 接收）。
	peerRoutesMu sync.Mutex
	peerRoutes   map[netip.Addr][]transport.RouteSyncEntry // senderVIP → 对方路由表

	triggerCh chan struct{} // 事件触发信号
}

// New 创建 ProbeRouter。
func New(cfg Config) *ProbeRouter {
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = 3 * time.Second
	}
	if cfg.TickInterval == 0 {
		cfg.TickInterval = 60 * time.Second
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 1 * time.Second
	}
	if cfg.MaxViaDepth == 0 {
		cfg.MaxViaDepth = 2
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 10
	}
	return &ProbeRouter{
		dp:          cfg.DataPlane,
		router:      cfg.Router,
		cfg:         cfg,
		vipToNodeID: make(map[netip.Addr]string),
		best:        make(map[netip.Addr]BestRoute),
		peerRoutes:  make(map[netip.Addr][]transport.RouteSyncEntry),
		triggerCh:   make(chan struct{}, 1),
	}
}

// MaxHops 返回当前路由表中最长路由的实际跳数（含 relay 透明转发）。
// 通过 Probe 探测的 ranked 结果直接统计路由 VIP 数量（= 实际经过的节点数）。
func (pr *ProbeRouter) MaxHops() int {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	return pr.maxHops
}

// TriggerReprobe 触发重新探测（环路检测等场景）。
func (pr *ProbeRouter) TriggerReprobe() {
	select {
	case pr.triggerCh <- struct{}{}:
	default:
	}
}

// ReceiveRouteSync 处理收到的单条 peer 路由广播（累积存储，覆盖同 dst 旧条目）。
func (pr *ProbeRouter) ReceiveRouteSync(senderVIP netip.Addr, entry transport.RouteSyncEntry) {
	pr.peerRoutesMu.Lock()
	existing := pr.peerRoutes[senderVIP]
	found := false
	for i, old := range existing {
		if old.DstVIP == entry.DstVIP {
			existing[i] = entry
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, entry)
	}
	pr.peerRoutes[senderVIP] = existing
	pr.peerRoutesMu.Unlock()
	slog.Debug("proberouter: 收到路由广播", "from", senderVIP, "dst", entry.DstVIP, "via", entry.NextHopVIP)
}

// SetPeers 更新 peer 列表并触发探测（debounce 合并）。
func (pr *ProbeRouter) SetPeers(peers []PeerInfo) {
	pr.mu.Lock()
	pr.peers = peers
	pr.vipToNodeID = make(map[netip.Addr]string, len(peers))
	for _, p := range peers {
		pr.vipToNodeID[p.VIP] = p.NodeID
	}
	pr.mu.Unlock()

	// 非阻塞触发
	select {
	case pr.triggerCh <- struct{}{}:
	default:
	}
}

// Run 启动主循环：兜底定时器 + 事件触发（带 debounce）。
func (pr *ProbeRouter) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(pr.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pr.probeAndApply()
		case <-pr.triggerCh:
			// debounce：等合并窗口后执行
			select {
			case <-time.After(pr.cfg.Debounce):
			case <-ctx.Done():
				return
			}
			// 排空窗口内的额外触发
			for {
				select {
				case <-pr.triggerCh:
				default:
					goto doProbe
				}
			}
		doProbe:
			pr.probeAndApply()
		}
	}
}

// probeAndApply 执行一轮全量探测并应用最优路由。
func (pr *ProbeRouter) probeAndApply() {
	pr.mu.Lock()
	peers := make([]PeerInfo, len(pr.peers))
	copy(peers, pr.peers)
	vipMap := make(map[netip.Addr]string, len(pr.vipToNodeID))
	for k, v := range pr.vipToNodeID {
		vipMap[k] = v
	}
	pr.mu.Unlock()

	if len(peers) == 0 {
		return
	}

	pr.mu.Lock()
	isFirstProbe := pr.probeCount == 0
	pr.mu.Unlock()

	slog.Info("proberouter: 开始全量探测", "peers", len(peers), "first", isFirstProbe)

	// 第 1 阶段：穷举探测每个目标的全部候选路由（按 RTT 排序）
	var allResults []targetResult
	for _, target := range peers {
		if target.VIP == pr.cfg.SelfVIP {
			continue
		}
		var others []PeerInfo
		for _, p := range peers {
			if p.VIP != pr.cfg.SelfVIP && p.VIP != target.VIP {
				others = append(others, p)
			}
		}
		ranked := pr.probeTargetRanked(target, others, isFirstProbe)
		allResults = append(allResults, targetResult{target: target, ranked: ranked})
	}

	// 第 2 阶段：选最优路由（不改 FIB，只设 preferredRelay）
	// FIB NextHop 始终保持目标自身 nodeID，processOutbound 优先走 peerFramer 直连，
	// 不通时走 preferredRelay（mtr enum 选出的最优中继）。无环路风险。
	results := make(map[netip.Addr]BestRoute)

	for _, tr := range allResults {
		targetNodeID := vipMap[tr.target.VIP]
		if targetNodeID == "" {
			continue
		}

		if len(tr.ranked) == 0 {
			pr.mu.Lock()
			if old, ok := pr.best[tr.target.VIP]; ok {
				old.Stale = true
				results[tr.target.VIP] = old
				slog.Warn("proberouter: 探测全失败，保留上一轮", "target", tr.target.VIP)
			}
			pr.mu.Unlock()
			continue
		}

		// 滞回检查：小波动（<5%）保持当前路由，大波动（>25%）直接切换
		best := tr.ranked[0]
		pr.mu.Lock()
		current, hasCurrent := pr.best[tr.target.VIP]
		pr.mu.Unlock()
		if hasCurrent && !current.Stale && current.NextHopID != "" && current.RTTMs > 0 {
			for _, r := range tr.ranked {
				if r.nextHopID == current.NextHopID {
					// 延迟变动超过 25% → 直接选绝对最优，不滞回
					delta := (r.rttMs - current.RTTMs) / current.RTTMs
					if delta > 0.25 || delta < -0.25 {
						slog.Info("proberouter: 延迟波动>25%，直接切换", "target", tr.target.VIP, "old", current.RTTMs, "new", r.rttMs)
						break
					}
					// 小波动：新最优必须比当前好 5% 才切换
					if best.rttMs < r.rttMs*0.95 {
						break
					}
					best = r
					slog.Debug("proberouter: 滞回保持当前路由", "target", tr.target.VIP, "keep", current.Label, "rtt", r.rttMs)
					break
				}
			}
		}

		selected := BestRoute{
			NextHopVIP: best.nextHopVIP, NextHopID: best.nextHopID,
			RTTMs: best.rttMs, Label: best.route.label,
		}

		// ── 逐条广播 + 环路校验 ──
		// 广播这条路由给所有 peer（单条 10B，即时发送）
		if !selected.Stale {
			entry := transport.RouteSyncEntry{
				DstVIP: tr.target.VIP, NextHopVIP: selected.NextHopVIP, RTTMs: selected.RTTMs,
			}
			pr.broadcastOneRoute(entry, tr, peers)
		}

		// 用已存的 peer 路由交叉校验环路
		if selected.NextHopVIP != tr.target.VIP {
			if alt, ok := pr.checkLoopAndYield(tr.target.VIP, selected, tr.ranked); ok {
				selected = alt
			}
		}

		results[tr.target.VIP] = selected
	}

	// 构建 rankedHops 表：目标 nodeID → 按 MTR 质量排序的候选 hop 列表
	ranked := make(map[string][]string)
	for _, tr := range allResults {
		targetNodeID := vipMap[tr.target.VIP]
		if targetNodeID == "" || len(tr.ranked) == 0 {
			continue
		}
		hops := make([]string, 0, len(tr.ranked))
		seen := make(map[string]bool)
		for _, r := range tr.ranked {
			if r.nextHopID != "" && !seen[r.nextHopID] {
				hops = append(hops, r.nextHopID)
				seen[r.nextHopID] = true
			}
		}
		if len(hops) > 0 {
			ranked[targetNodeID] = hops
		}
	}
	pr.dp.SetRankedHops(ranked)

	// 统计最长路由实际跳数（从 ranked route 的 VIP 数量）
	computedMaxHops := 1
	for _, tr := range allResults {
		best, ok := results[tr.target.VIP]
		if !ok {
			continue
		}
		for _, r := range tr.ranked {
			if r.route.label == best.Label {
				if len(r.route.vips) > computedMaxHops {
					computedMaxHops = len(r.route.vips)
				}
				break
			}
		}
	}

	// 更新 best 表
	pr.mu.Lock()
	pr.best = results
	pr.maxHops = computedMaxHops
	pr.probeCount++
	pr.mu.Unlock()

	// 通知最长路由跳数（TTL 环路检测用）
	if pr.cfg.OnMaxHopsUpdate != nil {
		pr.cfg.OnMaxHopsUpdate(pr.MaxHops())
	}

	slog.Info("proberouter: 路由已更新", "targets", len(results))
	for dst, best := range results {
		slog.Info("proberouter: 最优路由",
			"dst", dst,
			"nextHop", best.NextHopVIP,
			"rtt", best.RTTMs,
			"label", best.Label,
			"stale", best.Stale,
		)
	}

	// 通知外部持久化
	if pr.cfg.OnRouteUpdate != nil {
		pr.cfg.OnRouteUpdate(results, vipMap)
	}
}

// broadcastOneRoute 向所有 peer 广播单条路由（探测完一条即时广播）。
// 使用 Probe 帧源路由投递，保证即使存在环路也能送达。
func (pr *ProbeRouter) broadcastOneRoute(entry transport.RouteSyncEntry, tr targetResult, peers []PeerInfo) {
	pr.mu.Lock()
	count := pr.probeCount
	pr.mu.Unlock()

	for _, peer := range peers {
		if peer.VIP == pr.cfg.SelfVIP {
			continue
		}
		// 用当前探测结果中到该 peer 的最优路径作为投递路由
		deliveryRoute := []netip.Addr{peer.VIP}
		if tr.target.VIP == peer.VIP && len(tr.ranked) > 0 {
			deliveryRoute = tr.ranked[0].route.vips
		}

		probe := &transport.ProbeFrame{
			IsRouteSync: true,
			Nonce:       uint64(count),
			TimestampNs: time.Now().UnixNano(),
			SourceVIP:   pr.cfg.SelfVIP,
			HopIndex:    0,
			Route:       deliveryRoute,
			SyncEntry:   entry,
		}
		target := deliveryRoute[0]
		pr.dp.SendProbe(probe, target)
	}
}

// checkLoopAndYield 检查当前选路是否与已存的 peer 路由形成环路。
// 如果环路且自己应让步，返回次优路由和 true；否则返回零值和 false。
func (pr *ProbeRouter) checkLoopAndYield(dstVIP netip.Addr, selected BestRoute, ranked []rankedRoute) (BestRoute, bool) {
	pr.peerRoutesMu.Lock()
	peerEntries := pr.peerRoutes[selected.NextHopVIP]
	pr.peerRoutesMu.Unlock()

	for _, pe := range peerEntries {
		if pe.DstVIP != dstVIP || pe.NextHopVIP != pr.cfg.SelfVIP {
			continue
		}
		// 环路！我到 D via P，P 到 D via 我
		iYield := selected.RTTMs > pe.RTTMs ||
			(selected.RTTMs == pe.RTTMs && pr.cfg.SelfVIP.Compare(selected.NextHopVIP) > 0)
		if !iYield {
			return BestRoute{}, false // 对方让步
		}
		slog.Warn("proberouter: 环路检测！让步切换次优",
			"dst", dstVIP, "conflict", selected.NextHopVIP,
			"myRTT", selected.RTTMs, "peerRTT", pe.RTTMs)
		// 选次优（跳过冲突 hop）
		for _, alt := range ranked[1:] {
			if alt.nextHopVIP == selected.NextHopVIP {
				continue
			}
			return BestRoute{
				NextHopVIP: alt.nextHopVIP, NextHopID: alt.nextHopID,
				RTTMs: alt.rttMs, Label: alt.route.label + "(防环让步)",
			}, true
		}
		break
	}
	return BestRoute{}, false
}
