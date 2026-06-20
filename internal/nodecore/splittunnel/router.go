package splittunnel

import (
	"net/netip"
	"strings"
	"sync"

	"github.com/x6nux/corelink/internal/nodecore/geoip"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// vipPrefix 是 CoreLink VIP 网段，VIP 目标始终走 WireGuard 隧道。
var vipPrefix = netip.MustParsePrefix("100.64.0.0/10")

// Router 分流路由器：根据目标 IP 决定走直连还是代理。
// 优先级: 强制直连 IP > GeoIP 规则 > CIDR 规则 > 默认动作。
type Router struct {
	mu             sync.RWMutex
	forceDirectIPs map[netip.Addr]bool
	matcher        *geoip.Matcher
	rules          []*genv1.SplitRule
	defaultAct     Action
}

// Decide 对目标 IP 做分流决策,按优先级匹配规则。
func (r *Router) Decide(dstIP netip.Addr) Action {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 优先级 0: VIP 网段始终走 WireGuard（保护数据面）
	if vipPrefix.Contains(dstIP) {
		return ActionProxy
	}

	// 优先级 1: 强制直连 IP(网关、DNS 等控制面地址)
	if r.forceDirectIPs[dstIP] {
		return ActionDirect
	}

	// 优先级 2-3: 按规则顺序匹配(GeoIP / CIDR)
	for _, rule := range r.rules {
		match := rule.Match
		act := parseAction(rule.Action)

		switch {
		case strings.HasPrefix(match, "geoip:!"):
			// 取反: 不在指定国家 → 命中
			code := strings.ToLower(strings.TrimPrefix(match, "geoip:!"))
			if r.matcher != nil && !containsIP(r.matcher.LookupCIDRs(code), dstIP) {
				return act
			}
		case strings.HasPrefix(match, "geoip:"):
			// 正向: 在指定国家 → 命中
			code := strings.ToLower(strings.TrimPrefix(match, "geoip:"))
			if r.matcher != nil && containsIP(r.matcher.LookupCIDRs(code), dstIP) {
				return act
			}
		case strings.HasPrefix(match, "cidr:"):
			// CIDR 前缀匹配
			if p, err := netip.ParsePrefix(strings.TrimPrefix(match, "cidr:")); err == nil && p.Contains(dstIP) {
				return act
			}
		}
	}

	// 兜底: 默认动作
	return r.defaultAct
}

// Update 原子更新路由器配置。
func (r *Router) Update(rules []*genv1.SplitRule, defaultAct Action, forceIPs map[netip.Addr]bool, matcher *geoip.Matcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = rules
	r.defaultAct = defaultAct
	r.forceDirectIPs = forceIPs
	if matcher != nil {
		r.matcher = matcher
	}
}

// parseAction 将字符串动作转为 Action 枚举
func parseAction(s string) Action {
	if s == "direct" {
		return ActionDirect
	}
	return ActionProxy
}

// containsIP 检查 IP 是否在任一前缀中
func containsIP(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
