package route

import (
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/x6nux/corelink/internal/nodecore/dpi"
	"github.com/x6nux/corelink/internal/nodecore/flowtrack"
	"github.com/x6nux/corelink/internal/nodecore/metadata"
)

// routeSnapshot 是路由引擎的不可变快照，通过 atomic.Pointer 实现无锁读取。
type routeSnapshot struct {
	fib     []FIBEntry // 按前缀长度降序排列（最长匹配优先）
	l4Rules []L4Rule
	l5Rules []L5Rule
}

// Engine 是多层路由引擎，支持并发无锁查询和原子配置热更新。
type Engine struct {
	snap atomic.Pointer[routeSnapshot]
}

// NewEngine 创建一个空的路由引擎。
func NewEngine() *Engine {
	e := &Engine{}
	e.snap.Store(&routeSnapshot{})
	return e
}

// Update 原子替换路由快照。FIB 条目按前缀长度降序排列以实现最长前缀匹配。
// cfg 为 nil 时等价于清空所有路由规则。
func (e *Engine) Update(cfg *RouteConfig) {
	if cfg == nil {
		e.snap.Store(&routeSnapshot{})
		return
	}
	s := &routeSnapshot{}

	// 复制 FIB 并按前缀长度降序排序（最长匹配优先）
	if len(cfg.FIB) > 0 {
		s.fib = make([]FIBEntry, len(cfg.FIB))
		copy(s.fib, cfg.FIB)
		sort.Slice(s.fib, func(i, j int) bool {
			return s.fib[i].Prefix.Bits() > s.fib[j].Prefix.Bits()
		})
	}

	// 复制 L4/L5 规则（保持用户指定的顺序，first-match）
	if len(cfg.L4Rules) > 0 {
		s.l4Rules = make([]L4Rule, len(cfg.L4Rules))
		copy(s.l4Rules, cfg.L4Rules)
	}
	if len(cfg.L5Rules) > 0 {
		s.l5Rules = make([]L5Rule, len(cfg.L5Rules))
		copy(s.l5Rules, cfg.L5Rules)
	}

	e.snap.Store(s)
}

// Route 对给定流执行多层路由查询。优先级：L5 > L4 > L3。
// 返回的 Decision.NextHop 为空表示无匹配路由。
func (e *Engine) Route(key flowtrack.FlowKey, dpiResult dpi.Result) Decision {
	s := e.snap.Load()

	// L5：SNI / Host 应用层匹配
	if dpiResult.Domain != "" {
		for i := range s.l5Rules {
			r := &s.l5Rules[i]
			if matchL5(r, dpiResult) {
				return Decision{
					NextHop:  r.NextHop,
					Via:      r.Via,
					RelayID:  r.RelayID,
					Priority: 5,
					Rule:     r.Name,
				}
			}
		}
	}

	// L4：端口 / 协议策略匹配
	for i := range s.l4Rules {
		r := &s.l4Rules[i]
		if matchL4(r, key) {
			return Decision{
				NextHop:  r.NextHop,
				Via:      r.Via,
				RelayID:  r.RelayID,
				Priority: 4,
				Rule:     r.Name,
			}
		}
	}

	// L3：FIB 最长前缀匹配（已按 prefix bits 降序排列）
	for i := range s.fib {
		entry := &s.fib[i]
		if entry.Prefix.Contains(key.DstIP) {
			return Decision{
				NextHop:  entry.NextHop,
				Via:      entry.Via,
				RelayID:  entry.RelayID,
				Priority: 3,
				Rule:     entry.Prefix.String(),
			}
		}
	}

	// 无匹配
	return Decision{}
}

// RouteCtx 对 InboundContext 执行多层路由查询，结果直接写入 ctx。
// 优先级：L5（域名）> L4（端口/协议）> L3（FIB 最长前缀）。
// ctx.NextHop 为空表示无匹配路由。
func (e *Engine) RouteCtx(ctx *metadata.InboundContext) {
	s := e.snap.Load()

	key := flowtrack.FlowKey{
		SrcIP:   ctx.Source.Addr(),
		DstIP:   ctx.Destination.Addr(),
		Proto:   networkToProto(ctx.Network),
		SrcPort: ctx.Source.Port(),
		DstPort: ctx.Destination.Port(),
	}

	// L5：域名匹配（通过 ctx.Domain 中的 SNI 或 HTTP Host）
	if ctx.Domain != "" {
		for i := range s.l5Rules {
			r := &s.l5Rules[i]
			if matchL5Domain(r, ctx.Domain) {
				ctx.NextHop = r.NextHop
				ctx.Via = hopTypeToString(r.Via)
				ctx.RelayID = r.RelayID
				ctx.Priority = 5
				ctx.Rule = r.Name
				return
			}
		}
	}

	// L4：端口/协议策略匹配
	for i := range s.l4Rules {
		r := &s.l4Rules[i]
		if matchL4(r, key) {
			ctx.NextHop = r.NextHop
			ctx.Via = hopTypeToString(r.Via)
			ctx.RelayID = r.RelayID
			ctx.Priority = 4
			ctx.Rule = r.Name
			return
		}
	}

	// L3：FIB 最长前缀匹配（已按 prefix bits 降序排列）
	for i := range s.fib {
		entry := &s.fib[i]
		if entry.Prefix.Contains(key.DstIP) {
			ctx.NextHop = entry.NextHop
			ctx.Via = hopTypeToString(entry.Via)
			ctx.RelayID = entry.RelayID
			ctx.Priority = 3
			ctx.Rule = entry.Prefix.String()
			return
		}
	}
}

// networkToProto 将 metadata.Network* 字符串转换为 IP 协议号。
func networkToProto(network string) uint8 {
	switch network {
	case metadata.NetworkTCP:
		return 6
	case metadata.NetworkUDP:
		return 17
	case metadata.NetworkICMP:
		return 1
	default:
		return 0
	}
}

// hopTypeToString 将 HopType 转换为 InboundContext.Via 使用的字符串表示。
func hopTypeToString(h HopType) string {
	switch h {
	case HopDirect:
		return "direct"
	case HopRelay:
		return "relay"
	default:
		return "unknown"
	}
}

// matchL5Domain 检查 L5 规则是否匹配给定域名（SNIPattern 或 HostPattern glob 匹配）。
// DNS 域名按 RFC 大小写不敏感，故匹配前对 pattern 和 domain 统一小写。
func matchL5Domain(r *L5Rule, domain string) bool {
	domain = strings.ToLower(domain)
	if r.SNIPattern != "" {
		matched, err := path.Match(strings.ToLower(r.SNIPattern), domain)
		if err != nil {
			slog.Warn("route: L5 SNI pattern 匹配错误", "pattern", r.SNIPattern, "domain", domain, "err", err)
		} else if matched {
			return true
		}
	}
	if r.HostPattern != "" {
		matched, err := path.Match(strings.ToLower(r.HostPattern), domain)
		if err != nil {
			slog.Warn("route: L5 Host pattern 匹配错误", "pattern", r.HostPattern, "domain", domain, "err", err)
		} else if matched {
			return true
		}
	}
	return false
}

// matchL5 检查 L5 规则是否匹配 DPI 结果（Domain glob 匹配）。
// DNS 域名按 RFC 大小写不敏感，故匹配前对 pattern 和 domain 统一小写。
func matchL5(r *L5Rule, res dpi.Result) bool {
	if res.Domain == "" {
		return false
	}
	domain := strings.ToLower(res.Domain)
	if r.SNIPattern != "" {
		matched, err := path.Match(strings.ToLower(r.SNIPattern), domain)
		if err != nil {
			slog.Warn("route: L5 SNI pattern 匹配错误", "pattern", r.SNIPattern, "domain", res.Domain, "err", err)
		} else if matched {
			return true
		}
	}
	if r.HostPattern != "" {
		matched, err := path.Match(strings.ToLower(r.HostPattern), domain)
		if err != nil {
			slog.Warn("route: L5 Host pattern 匹配错误", "pattern", r.HostPattern, "domain", res.Domain, "err", err)
		} else if matched {
			return true
		}
	}
	return false
}

// matchL4 检查 L4 规则是否匹配流的目的前缀、端口和协议。
func matchL4(r *L4Rule, key flowtrack.FlowKey) bool {
	if !r.DstPrefix.Contains(key.DstIP) {
		return false
	}
	if r.DstPort != 0 && r.DstPort != key.DstPort {
		return false
	}
	if r.Proto != 0 && r.Proto != key.Proto {
		return false
	}
	return true
}
