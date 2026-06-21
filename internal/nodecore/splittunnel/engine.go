package splittunnel

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/geoip"
	"github.com/x6nux/corelink/internal/nodecore/tun"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// Engine v3 分流引擎生命周期管理。
type Engine struct {
	mu       sync.Mutex // 保护 applied/gstack/wrapper.gstack 的并发访问
	wrapper  *SplitTunWrapper
	router   *Router
	cache    *connCache
	gstack   *GVisorStack
	tunName  string
	physIfce string
	applied  bool
}

// NewEngine 创建 v3 分流引擎。传入真实 TUN 设备，返回的 Engine.Wrapper() 传给 WireGuard。
// 立即设置 SO_BINDTODEVICE + SO_MARK，确保所有 corelink-node 出站绕过 TUN 策略路由。
func NewEngine(realTUN tun.Device, physIfce, tunName string) *Engine {
	tunnel.SetBindInterface(physIfce)
	tunnel.SetFwMark(FwMarkBypass)

	router := &Router{
		forceDirectIPs: make(map[netip.Addr]bool),
		defaultAct:     ActionDirect,
	}
	cache := newConnCache(5 * time.Minute)
	wrapper := newWrapper(realTUN)
	wrapper.router = router
	wrapper.cache = cache
	return &Engine{
		wrapper:  wrapper,
		router:   router,
		cache:    cache,
		tunName:  tunName,
		physIfce: physIfce,
	}
}

// SetDNSRelay 注入 TUN 层 DNS 中继。
func (e *Engine) SetDNSRelay(r *DNSRelay) {
	e.wrapper.SetDNSRelay(r)
}

// SetLocalVIP 设置本机 VIP（用于 IPIP 解封装匹配）。
// 应在首次 ApplyConfig 后调用，无论分流是否启用——所有节点都可能收到封装包。
// 使用 vipMu 保护 loadVIPs + storeVIPs 的 RMW 操作，消除与 Apply 并发时的竞态。
func (e *Engine) SetLocalVIP(vip string) {
	if addr, err := netip.ParseAddr(vip); err == nil {
		e.wrapper.vipMu.Lock()
		old := e.wrapper.loadVIPs()
		e.wrapper.storeVIPs(vipConfig{localVIP: addr, exitVIP: old.exitVIP})
		e.wrapper.vipMu.Unlock()
	}
}

// Wrapper 返回 TUN wrapper（实现 tun.Device），传给 WireGuard。
func (e *Engine) Wrapper() tun.Device {
	return e.wrapper
}

// ApplyOptions 传入 Apply 的额外参数。
type ApplyOptions struct {
	LocalVIP string        // 本机 VIP（如 "100.64.0.2"）
	Peers    []*genv1.Peer // 当前 NodeConfig 的 peers（用于查找出口节点 VIP）
}

// Apply 启用分流。
func (e *Engine) Apply(ctx context.Context, policy *genv1.SplitTunnelPolicy, matcher *geoip.Matcher, controllerAddrs, relayAddrs []string, opts *ApplyOptions) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if policy == nil || !policy.Enabled {
		if e.applied {
			e.cleanupLocked(ctx)
		}
		return nil
	}

	// 构建强制直连列表
	forceIPs := buildForceDirectIPs(controllerAddrs, relayAddrs)

	// 更新路由器
	defaultAct := ActionDirect
	if policy.DefaultAction == "proxy" {
		defaultAct = ActionProxy
	}
	slog.Info("splittunnel: 路由器更新", "defaultAction", policy.DefaultAction, "defaultAct", defaultAct, "rules", len(policy.Rules), "forceIPs", len(forceIPs))
	e.router.Update(policy.Rules, defaultAct, forceIPs, matcher)

	// 设置 IPIP 封装所需的 VIP 信息（使用 vipMu 保护 RMW 操作）
	if opts != nil {
		e.wrapper.vipMu.Lock()
		old := e.wrapper.loadVIPs()
		newCfg := old
		if addr, err := netip.ParseAddr(opts.LocalVIP); err == nil {
			newCfg.localVIP = addr
		}
		if exitVIP := resolveExitVIP(policy, opts.Peers); exitVIP.IsValid() {
			newCfg.exitVIP = exitVIP
			slog.Info("splittunnel: 出口节点已配置", "exit_vip", exitVIP)
		}
		e.wrapper.storeVIPs(newCfg)
		e.wrapper.vipMu.Unlock()
	}

	if !e.applied {
		// 首次：创建 gVisor 用户态协议栈（SO_BINDTODEVICE + SO_MARK 已在 NewEngine 中设置）
		gstack, err := NewGVisorStack(e.physIfce, e.wrapper.real)
		if err != nil {
			tunnel.ClearBindInterface()
			tunnel.ClearFwMark()
			return err
		}
		e.gstack = gstack
		e.wrapper.gstack.Store(gstack)

		// 安装策略路由（ip rule + 独立路由表，不污染主表）
		if err := InstallPolicyRoutes(e.tunName, e.physIfce); err != nil {
			// 回滚 gVisor 协议栈，避免泄漏
			e.wrapper.gstack.Store(nil)
			gstack.Close()
			e.gstack = nil
			tunnel.ClearBindInterface()
			tunnel.ClearFwMark()
			return err
		}

		// 激活 wrapper
		e.wrapper.active.Store(true)
		e.applied = true
		slog.Info("splittunnel: v3 分流引擎已启用", "physIfce", e.physIfce, "tunName", e.tunName)
	} else {
		// 热更新：清空缓存
		e.cache.mu.Lock()
		e.cache.table = make(map[connKey]connEntry)
		e.cache.mu.Unlock()
		slog.Info("splittunnel: 分流规则已热更新", "rules", len(policy.Rules))
	}
	return nil
}

// ResolveExitVIPFromPolicy 从 SplitTunnelPolicy + peers 解析默认出口节点的 VIP（导出版本）。
func ResolveExitVIPFromPolicy(policy *genv1.SplitTunnelPolicy, peers []*genv1.Peer) netip.Addr {
	return resolveExitVIP(policy, peers)
}

// resolveExitVIP 从 SplitTunnelPolicy + peers 解析默认出口节点的 VIP。
// 优先 default_exit_node_id，回退到第一条 proxy 规则的 exit_node_id。
func resolveExitVIP(policy *genv1.SplitTunnelPolicy, peers []*genv1.Peer) netip.Addr {
	exitID := policy.GetDefaultExitNodeId()
	if exitID == "" {
		for _, r := range policy.GetRules() {
			if r.GetAction() == "proxy" && r.GetExitNodeId() != "" {
				exitID = r.GetExitNodeId()
				break
			}
		}
	}
	if exitID == "" {
		return netip.Addr{}
	}
	for _, p := range peers {
		if p.GetNodeId() != exitID {
			continue
		}
		for _, cidr := range p.GetAllowedIps() {
			if addr, err := netip.ParsePrefix(cidr); err == nil && addr.Bits() == 32 {
				return addr.Addr()
			}
		}
	}
	return netip.Addr{}
}

// Cleanup 停止分流。
func (e *Engine) Cleanup(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cleanupLocked(ctx)
}

// cleanupLocked 是 Cleanup 的无锁内部实现，调用者必须已持有 e.mu。
func (e *Engine) cleanupLocked(ctx context.Context) {
	e.wrapper.active.Store(false)
	RemovePolicyRoutes()
	if e.gstack != nil {
		e.wrapper.gstack.Store(nil)
		e.gstack.Close()
		e.gstack = nil
	}
	e.cache.mu.Lock()
	e.cache.table = make(map[connKey]connEntry)
	e.cache.mu.Unlock()
	tunnel.ClearFwMark()
	tunnel.ClearBindInterface()
	e.applied = false
	slog.Info("splittunnel: v3 分流引擎已停止")
}

// UpdateMatcher 热更新 GeoIP 数据。
func (e *Engine) UpdateMatcher(m *geoip.Matcher) {
	e.router.mu.Lock()
	e.router.matcher = m
	e.router.mu.Unlock()
	e.cache.mu.Lock()
	e.cache.table = make(map[connKey]connEntry)
	e.cache.mu.Unlock()
}

// buildForceDirectIPs 收集强制直连 IP。
func buildForceDirectIPs(controllerAddrs, relayAddrs []string) map[netip.Addr]bool {
	m := make(map[netip.Addr]bool)
	addAddr := func(s string) {
		if addr, err := netip.ParseAddr(s); err == nil {
			m[addr] = true
			return
		}
		host, _, err := net.SplitHostPort(s)
		if err == nil {
			if addr, err := netip.ParseAddr(host); err == nil {
				m[addr] = true
			}
		}
	}
	for _, a := range controllerAddrs {
		addAddr(a)
	}
	for _, a := range relayAddrs {
		addAddr(a)
	}
	// 默认网关
	if gw := detectGatewayIP(); gw.IsValid() {
		m[gw] = true
	}
	// 系统 DNS 不再加入强制直连——分流引擎已在 TUN 层拦截 DNS，
	// resolv.conf 中的 DNS 地址（如 8.8.8.8）也应参与 GeoIP 分流。
	return m
}
