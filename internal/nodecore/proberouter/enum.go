package proberouter

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// rankedRoute 带 RTT 排序的候选路由。
type rankedRoute struct {
	route enumRoute
	rttMs float64
	// 第一跳信息
	nextHopVIP netip.Addr
	nextHopID  string
}

// probeTarget 对单个目标穷举所有 via 路由，选最优。
func (pr *ProbeRouter) probeTarget(target PeerInfo, others []PeerInfo) BestRoute {
	ranked := pr.probeTargetRanked(target, others, true)
	if len(ranked) == 0 {
		return BestRoute{}
	}
	r := ranked[0]
	return BestRoute{
		NextHopVIP: r.nextHopVIP,
		NextHopID:  r.nextHopID,
		RTTMs:      r.rttMs,
		Label:      r.route.label,
	}
}

// probeTargetRanked 对单个目标穷举所有路由，返回按 RTT 升序排列的全部成功候选。
// firstProbe=true 时每条路由只采样 1 次（快速收敛），后续采样 10 次取平均（稳定）。
func (pr *ProbeRouter) probeTargetRanked(target PeerInfo, others []PeerInfo, firstProbe bool) []rankedRoute {
	routes := enumRoutes(others, target.VIP, pr.cfg.MaxViaDepth)
	if len(routes) == 0 {
		return nil
	}

	type probeResult struct {
		idx   int
		rttMs float64
		ok    bool
	}
	resultCh := make(chan probeResult, len(routes))
	sem := make(chan struct{}, pr.cfg.MaxConcurrency)
	var wg sync.WaitGroup

	for i, rt := range routes {
		wg.Add(1)
		go func(idx int, r enumRoute) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 多次采样取平均值（减少 RTT 波动导致路由翻转）
			// 首次启动 1 次快速收敛，后续 10 次稳定采样
			samples := 10
			if firstProbe {
				samples = 1
			}
			var totalMs float64
			okCount := 0
			for range samples {
				results, _ := pr.dp.SendProbeAll(pr.cfg.SelfVIP, r.vips, true, pr.cfg.ProbeTimeout)
				var maxHop uint8
				var rtt time.Duration
				for _, res := range results {
					if res.HopIndex >= maxHop {
						maxHop = res.HopIndex
						rtt = res.RTT
					}
				}
				if len(results) > 0 && rtt > 0 {
					totalMs += float64(rtt.Microseconds()) / 1000.0
					okCount++
				}
			}
			if okCount > 0 {
				resultCh <- probeResult{idx: idx, rttMs: totalMs / float64(okCount), ok: true}
			} else {
				resultCh <- probeResult{idx: idx, ok: false}
			}
		}(i, rt)
	}
	wg.Wait()
	close(resultCh)

	// 收集成功结果并按 RTT 排序
	var ranked []rankedRoute
	for r := range resultCh {
		if !r.ok {
			continue
		}
		rt := routes[r.idx]
		firstHopVIP := target.VIP
		firstHopID := target.NodeID
		if len(rt.viaIndices) > 0 {
			firstHopVIP = others[rt.viaIndices[0]].VIP
			firstHopID = others[rt.viaIndices[0]].NodeID
		}
		pr.mu.Lock()
		if nid := pr.vipToNodeID[firstHopVIP]; nid != "" {
			firstHopID = nid
		}
		pr.mu.Unlock()

		ranked = append(ranked, rankedRoute{
			route:      rt,
			rttMs:      r.rttMs,
			nextHopVIP: firstHopVIP,
			nextHopID:  firstHopID,
		})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].rttMs < ranked[j].rttMs })

	slog.Debug("proberouter: 探测完成", "target", target.VIP, "candidates", len(ranked))
	return ranked
}

// enumRoute 描述一条待探测路由。
type enumRoute struct {
	vips       []netip.Addr // 完整路由 VIP 列表（via... + target）
	viaIndices []int        // others 中的索引序列
	label      string
}

// enumRoutes 生成所有无环 via 排列（深度 <= maxDepth，总数上限 100）。
func enumRoutes(others []PeerInfo, targetVIP netip.Addr, maxDepth int) []enumRoute {
	var routes []enumRoute
	const maxRoutes = 100

	// direct
	routes = append(routes, enumRoute{
		vips:  []netip.Addr{targetVIP},
		label: "direct",
	})

	peerLabel := func(p PeerInfo) string {
		id := p.NodeID
		if len(id) > 6 {
			id = id[:6]
		}
		vipStr := p.VIP.String()
		return fmt.Sprintf("%s(.%s)", id, vipStr[strings.LastIndex(vipStr, ".")+1:])
	}

	// 递归生成排列
	var gen func(chosen []int, used []bool)
	gen = func(chosen []int, used []bool) {
		if len(routes) >= maxRoutes {
			return
		}
		for i := range others {
			if used[i] || len(routes) >= maxRoutes {
				continue
			}
			next := append(append([]int{}, chosen...), i)
			// 构建路由
			vips := make([]netip.Addr, 0, len(next)+1)
			var labels []string
			for _, idx := range next {
				vips = append(vips, others[idx].VIP)
				labels = append(labels, peerLabel(others[idx]))
			}
			vips = append(vips, targetVIP)
			routes = append(routes, enumRoute{
				vips:       vips,
				viaIndices: append([]int{}, next...),
				label:      "via " + strings.Join(labels, "→"),
			})
			if len(next) < maxDepth && len(next) < len(others) {
				used[i] = true
				gen(next, used)
				used[i] = false
			}
		}
	}
	gen(nil, make([]bool, len(others)))

	if len(routes) >= maxRoutes {
		slog.Warn("proberouter: 路由排列截断", "total", len(routes), "max", maxRoutes)
	}
	return routes
}
