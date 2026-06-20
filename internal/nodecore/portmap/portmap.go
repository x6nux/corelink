// Package portmap 实现端口映射相关的平台无关辅助逻辑（NAT-PMP/PCP/UPnP 等）。
//
// 本文件定义端口映射子系统的核心类型与 Mapper 接口骨架：协议枚举、映射结果
// 结构体 Mapping、以及统一的 Map/Refresh/Unmap 抽象。各协议（NAT-PMP/PCP/UPnP）
// 提供各自的报文实现，聚合为 DefaultMapper（在后续 task 实现）对外暴露。
package portmap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/x6nux/corelink/pkg/tunnel"
)

// Protocol 标识端口映射所用的协议。
type Protocol int

const (
	// ProtocolNATPMP 为 NAT-PMP（RFC 6886），定长二进制 UDP 报文，最简单。
	ProtocolNATPMP Protocol = iota
	// ProtocolPCP 为 PCP（RFC 6887），NAT-PMP 的后继，兼容 IPv6。
	ProtocolPCP
	// ProtocolUPnP 为 UPnP IGD，基于 SOAP/HTTP 的端口映射。
	ProtocolUPnP
)

// String 返回协议的可读名称，便于日志与测试。
func (p Protocol) String() string {
	switch p {
	case ProtocolNATPMP:
		return "NAT-PMP"
	case ProtocolPCP:
		return "PCP"
	case ProtocolUPnP:
		return "UPnP"
	default:
		return "unknown"
	}
}

// Mapping 描述一次成功的端口映射结果，含续期/删除所需的全部信息。
type Mapping struct {
	Protocol     Protocol      // 创建该映射所用的协议。
	ExternalIP   string        // 路由器 WAN 口对外 IP（IPv4 字符串）。
	ExternalPort uint16        // 对外开放的外部端口。
	InternalPort uint16        // 映射到的本机内部端口。
	TransportUDP bool          // true=UDP(WireGuard)，false=TCP(隧道)。
	TTL          time.Duration // 租约时长（路由器实际授予的）。
	Gateway      string        // 网关地址(NAT-PMP/PCP) 或 control URL(UPnP)，续期/删除用。
}

// Mapper 是端口映射的统一抽象：在网关上建立、续期、删除一条映射。
//
// 具体实现 DefaultMapper（聚合 NAT-PMP/PCP/UPnP）在后续 task 提供，本接口只
// 定义契约。
type Mapper interface {
	// Map 在网关上建立一条 internalPort → 外部端口 的映射，udp 区分 UDP/TCP，
	// ttl 为期望租约时长。成功返回 Mapping，失败返回 error。
	Map(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error)
	// Refresh 对已有映射 m 续期（保活），不改变端口分配。
	Refresh(ctx context.Context, m *Mapping) error
	// Unmap 删除已有映射 m。
	Unmap(ctx context.Context, m *Mapping) error
}

// Config 为 DefaultMapper 的可选配置。零值均有合理默认值。
type Config struct {
	GatewayFn     func() []string  // 候选网关地址列表（默认 DefaultGateways(nil)）。
	SSDPTransport ssdpTransport    // SSDP transport 注入（nil 用默认 UDP 组播）。
	HTTPClient    *http.Client     // UPnP HTTP 注入（nil 用 http.DefaultClient）。
	DialTimeout   time.Duration    // 总竞速超时（默认 2s）。
	Clock         func() time.Time // 注入 clock（测试用，默认 time.Now）。
}

// DefaultMapper 聚合 NAT-PMP/PCP/UPnP-IGD 三协议，实现 Mapper 接口。
//
// Map 并发竞速三协议，第一个成功者获胜；Refresh/Unmap 按 Mapping.Protocol 分派。
type DefaultMapper struct {
	gatewayFn     func() []string
	ssdpTransport ssdpTransport
	httpClient    *http.Client
	dialTimeout   time.Duration
	clock         func() time.Time

	// 协议映射函数注入点（测试可覆盖以精确编排竞速时序）。默认指向包级实现。
	natpmpMapFn func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error)
	pcpMapFn    func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error)
	igdMapFn    func(ctx context.Context, controlURL, serviceType string, internal, ext uint16, ic string, udp bool, ttlSec uint32, hc *http.Client) (*Mapping, error)
	// unmapFn 注入点：竞速中后到的成功映射（loser）需清理，默认指向 m.Unmap。
	unmapFn func(ctx context.Context, m *Mapping) error
}

// 编译期断言 DefaultMapper 实现 Mapper。
var _ Mapper = (*DefaultMapper)(nil)

// New 创建 DefaultMapper，应用 Config 中的可选字段并填充默认值。
func New(cfg Config) *DefaultMapper {
	m := &DefaultMapper{
		gatewayFn:     cfg.GatewayFn,
		ssdpTransport: cfg.SSDPTransport,
		httpClient:    cfg.HTTPClient,
		dialTimeout:   cfg.DialTimeout,
		clock:         cfg.Clock,
	}
	if m.gatewayFn == nil {
		m.gatewayFn = func() []string { return DefaultGateways(nil) }
	}
	if m.httpClient == nil {
		m.httpClient = http.DefaultClient
	}
	if m.dialTimeout <= 0 {
		m.dialTimeout = 2 * time.Second
	}
	if m.clock == nil {
		m.clock = time.Now
	}
	if m.natpmpMapFn == nil {
		m.natpmpMapFn = natpmpMap
	}
	if m.pcpMapFn == nil {
		m.pcpMapFn = pcpMap
	}
	if m.igdMapFn == nil {
		m.igdMapFn = igdMap
	}
	if m.unmapFn == nil {
		m.unmapFn = m.Unmap
	}
	return m
}

// localOutboundIP 返回到达第一个候选网关的本机出口 IP（字符串），供 UPnP 的
// internalClient 参数使用。无候选网关或 dial 失败返回 error。
func localOutboundIP(gateways []string) (string, error) {
	// 注入 SO_BINDTODEVICE 绑定物理网卡，绕过 TUN 路由。
	d := net.Dialer{Control: tunnel.BindControl}
	for _, gw := range gateways {
		addr := natpmpGatewayAddr(gw)
		conn, err := d.DialContext(context.Background(), "udp", addr)
		if err != nil {
			continue
		}
		la, ok := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if ok && la.IP != nil {
			return la.IP.String(), nil
		}
	}
	return "", errors.New("portmap: cannot determine local outbound IP")
}

// Map 在网关上建立一条端口映射，三协议并发竞速，第一个成功者获胜。
//
// 两轮策略：先试 suggestedExtPort = internalPort（保持端口一致），全失败后
// 再试 suggestedExtPort = 0（让路由器随机分配），最大化成功率。
func (dm *DefaultMapper) Map(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error) {
	// 第一轮：尝试外部端口 = 内部端口。
	m, err := dm.mapRace(ctx, internalPort, internalPort, udp, ttl)
	if err == nil {
		return m, nil
	}
	// 第二轮：外部端口 = 0（路由器随机分配）。
	m2, err2 := dm.mapRace(ctx, internalPort, 0, udp, ttl)
	if err2 == nil {
		return m2, nil
	}
	return nil, fmt.Errorf("portmap: all protocols failed (round1: %v; round2: %w)", err, err2)
}

// mapRace 三协议并发竞速一轮。suggestedExtPort = 0 表示让路由器选择外部端口。
func (dm *DefaultMapper) mapRace(ctx context.Context, internalPort, suggestedExtPort uint16, udp bool, ttl time.Duration) (*Mapping, error) {
	rctx, cancel := context.WithTimeout(ctx, dm.dialTimeout)
	defer cancel()

	ttlSec := uint32(ttl / time.Second)
	if ttlSec == 0 {
		ttlSec = 1
	}

	gateways := dm.gatewayFn()
	internalClient, _ := localOutboundIP(gateways)

	type result struct {
		m   *Mapping
		err error
	}

	// channel 容量须覆盖所有发送者：每网关 natpmp+pcp 各一 + 一个 upnp，
	// 避免成功结果被非阻塞 default 丢弃（bug #9）。
	ch := make(chan result, len(gateways)*2+1)
	var wg sync.WaitGroup

	launch := func(fn func() (*Mapping, error)) {
		wg.Go(func() {
			m, err := fn()
			r := result{m: m, err: err}
			select {
			case ch <- r:
			default:
			}
		})
	}

	for _, gw := range gateways {
		launch(func() (*Mapping, error) {
			return dm.natpmpMapFn(rctx, gw, internalPort, suggestedExtPort, udp, ttlSec)
		})
	}

	for _, gw := range gateways {
		launch(func() (*Mapping, error) {
			return dm.pcpMapFn(rctx, gw, internalPort, suggestedExtPort, udp, ttlSec)
		})
	}

	wg.Go(func() {
		ssdpTimeout := max(dm.dialTimeout/2, 500*time.Millisecond)
		var locations []string
		var err error
		if dm.ssdpTransport != nil {
			locations, err = ssdpDiscoverWith(rctx, dm.ssdpTransport, ssdpTimeout)
		} else {
			locations, err = ssdpDiscover(rctx, ssdpTimeout)
		}
		if err != nil || len(locations) == 0 {
			errMsg := "upnp: no IGD discovered"
			if err != nil {
				errMsg = fmt.Sprintf("upnp ssdp: %v", err)
			}
			select {
			case ch <- result{err: errors.New(errMsg)}:
			default:
			}
			return
		}

		ic := internalClient
		for _, loc := range locations {
			if rctx.Err() != nil {
				break
			}
			controlURL, serviceType, err := fetchControlURL(rctx, loc, dm.httpClient)
			if err != nil {
				continue
			}
			m, err := dm.igdMapFn(rctx, controlURL, serviceType, internalPort, suggestedExtPort, ic, udp, ttlSec, dm.httpClient)
			if err != nil {
				continue
			}
			select {
			case ch <- result{m: m}:
			default:
			}
			return
		}
		select {
		case ch <- result{err: errors.New("upnp: all locations failed")}:
		default:
		}
	})

	go func() {
		wg.Wait()
		close(ch)
	}()

	var (
		errs   []error
		winner *Mapping
	)
	for r := range ch {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		if winner == nil {
			// 第一个成功者获胜；cancel() 取消 rctx 令在途 natpmp/pcp/igd 尽快返回。
			// drain-all：消费循环不在首个成功即 return，而是继续 range ch 直到 close，
			// 把后到的 loser 成功映射 Unmap、把失败原因收进 errs（cancel 仅停在途，不提前退出循环）。
			winner = r.m
			cancel()
			continue
		}
		// 后到的成功映射（loser）：胜者已选定，须清理以免泄漏（bug #9）。
		lm := r.m
		go func() {
			uctx, ucancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer ucancel()
			_ = dm.unmapFn(uctx, lm)
		}()
	}
	if winner != nil {
		return winner, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("portmap: no protocols attempted (no gateways)")
	}
	// 聚合全部失败原因（bug #34）。
	return nil, fmt.Errorf("portmap: all protocols failed: %w", errors.Join(errs...))
}

// validateGrantedTTL 校验网关授予的租约时长 grantedSec（秒）。
//
// 修复 bug #20：当我们请求了非 0 租约（requestedSec>0）、网关却授予 0 时，
// 视为异常网关响应并返回 error——绝不组装出 TTL=0 的 Mapping，否则上层
// Lifecycle 的续期间隔将为 0（ttl/2），导致每个 Tick 立刻"续期"，而 NAT-PMP/PCP
// 续期复用 Map 报文、lifetime=0 即删除语义，等价于每 Tick 删除自己建立的映射。
// requestedSec 本就为 0（删除/查询语义）时授予 0 属正常，原样放行。
func validateGrantedTTL(requestedSec, grantedSec uint32) (uint32, error) {
	if requestedSec > 0 && grantedSec == 0 {
		return 0, fmt.Errorf("portmap: gateway granted lifetime 0 while %d requested (abnormal)", requestedSec)
	}
	return grantedSec, nil
}

// Refresh 按 Mapping.Protocol 分派续期操作。
func (dm *DefaultMapper) Refresh(ctx context.Context, m *Mapping) error {
	if m == nil {
		return errors.New("portmap refresh: nil mapping")
	}
	switch m.Protocol {
	case ProtocolNATPMP:
		return natpmpRefresh(ctx, m.Gateway, m)
	case ProtocolPCP:
		return pcpRefresh(ctx, m)
	case ProtocolUPnP:
		// igdRefresh 需要 internalClient；从 Mapping.Gateway 无法恢复原始值，
		// 因此运行时重新获取本机 IP——开销极低（仅 DialUDP 无发包）。
		gateways := dm.gatewayFn()
		ic, err := localOutboundIP(gateways)
		if err != nil {
			return fmt.Errorf("portmap refresh: %w", err)
		}
		return igdRefresh(ctx, m, ic, dm.httpClient)
	default:
		return fmt.Errorf("portmap refresh: unknown protocol %v", m.Protocol)
	}
}

// Unmap 按 Mapping.Protocol 分派删除操作。
func (dm *DefaultMapper) Unmap(ctx context.Context, m *Mapping) error {
	if m == nil {
		return errors.New("portmap unmap: nil mapping")
	}
	switch m.Protocol {
	case ProtocolNATPMP:
		return natpmpUnmap(ctx, m.Gateway, m)
	case ProtocolPCP:
		return pcpUnmap(ctx, m)
	case ProtocolUPnP:
		return igdUnmap(ctx, m, dm.httpClient)
	default:
		return fmt.Errorf("portmap unmap: unknown protocol %v", m.Protocol)
	}
}

// ────────────────────────────────────────────────────────────────
// Lifecycle：管理活跃映射的续期保活、失败回调、退出清理
// ────────────────────────────────────────────────────────────────

// LifecycleConfig 为 Lifecycle 的配置参数。
type LifecycleConfig struct {
	// RenewInterval 返回续期间隔（默认 ttl/2）。
	RenewInterval func(ttl time.Duration) time.Duration
	// Clock 注入当前时间（确定性测试用，默认 time.Now）。
	Clock func() time.Time
	// OnMappingLost 单映射粒度失效回调（Refresh 失败时触发）。
	OnMappingLost func(*Mapping)
	// BackoffBase 退避重建基数（默认 5s）。
	BackoffBase time.Duration
	// BackoffMax 退避重建上限（默认 60s）。
	BackoffMax time.Duration
}

// entry 是 Lifecycle 内部管理的单个映射条目。
type entry struct {
	mapping   *Mapping  // 当前活跃映射。
	renewAt   time.Time // 下次续期时刻。
	rebuildAt time.Time // 退避重建时刻（零值表示不在重建退避中）。
	backoff   time.Duration
}

// Lifecycle 管理活跃映射的续期保活、失败回调和退出清理。
//
// 采用 Tick-based 驱动模式：调用方（或生产环境的 goroutine）定期调用
// Tick(now) 推进时间，触发续期/重建。无内部 goroutine，完全确定性。
type Lifecycle struct {
	mapper Mapper
	cfg    LifecycleConfig

	mu      sync.Mutex
	entries []*entry
	closed  bool
}

// NewLifecycle 创建 Lifecycle。m 为底层 Mapper，cfg 为可选配置。
func NewLifecycle(m Mapper, cfg LifecycleConfig) *Lifecycle {
	if cfg.RenewInterval == nil {
		cfg.RenewInterval = func(ttl time.Duration) time.Duration { return ttl / 2 }
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 5 * time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 60 * time.Second
	}
	return &Lifecycle{
		mapper: m,
		cfg:    cfg,
	}
}

// Manage 接管一个 Mapping，启动续期定时。支持同时管理多个 Mapping。
func (lc *Lifecycle) Manage(mapping *Mapping) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.closed {
		return
	}
	now := lc.cfg.Clock()
	interval := lc.cfg.RenewInterval(mapping.TTL)
	if interval <= 0 {
		// TTL=0 等病态映射：续期间隔为 0 会导致每 Tick 立即"续期"，而续期复用
		// Map 报文、lifetime=0 即删除自己——拒绝接管，避免删自己循环（bug #20）。
		return
	}
	lc.entries = append(lc.entries, &entry{
		mapping: mapping,
		renewAt: now.Add(interval),
		backoff: lc.cfg.BackoffBase,
	})
}

// Managed 返回当前管理中的所有映射快照。
func (lc *Lifecycle) Managed() []*Mapping {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]*Mapping, 0, len(lc.entries))
	for _, e := range lc.entries {
		out = append(out, e.mapping)
	}
	return out
}

// Tick 推进时间到 now，触发到期映射的续期或退避重建。
// Close 后调用为 no-op，不 panic。
func (lc *Lifecycle) Tick(now time.Time) {
	lc.mu.Lock()
	if lc.closed {
		lc.mu.Unlock()
		return
	}
	// 复制 entries 快照，在锁外执行 I/O 操作。
	snapshot := make([]*entry, len(lc.entries))
	copy(snapshot, lc.entries)
	lc.mu.Unlock()

	for _, e := range snapshot {
		lc.mu.Lock()
		if lc.closed {
			lc.mu.Unlock()
			return
		}
		lc.mu.Unlock()

		lc.tickEntry(e, now)
	}
}

// tickEntry 处理单个 entry 的续期或重建。
//
// 锁策略：所有 lock/unlock 均为手动配对，不使用 defer——函数中间需要释放锁
// 执行 I/O（Refresh/Map/回调），完成后重新获取锁更新状态。
func (lc *Lifecycle) tickEntry(e *entry, now time.Time) {
	lc.mu.Lock()
	// 检查 entry 是否仍在 entries 中（可能已被 Close 移除）。
	if !slices.Contains(lc.entries, e) || lc.closed {
		lc.mu.Unlock()
		return
	}

	// 判断是在重建退避中还是正常续期。
	if !e.rebuildAt.IsZero() {
		lc.tickRebuild(e, now)
		return
	}

	lc.tickRenew(e, now)
}

// tickRebuild 处理退避重建模式。调用时已持有 lc.mu。
func (lc *Lifecycle) tickRebuild(e *entry, now time.Time) {
	if now.Before(e.rebuildAt) {
		lc.mu.Unlock()
		return
	}
	// 到达重建时刻，尝试 Map 重建。
	m := e.mapping
	lc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	newMapping, err := lc.mapper.Map(ctx, m.InternalPort, m.TransportUDP, m.TTL)
	cancel()

	lc.mu.Lock()
	if lc.closed {
		lc.mu.Unlock()
		return
	}
	if err != nil {
		// 重建失败，加倍退避。
		e.backoff *= 2
		if e.backoff > lc.cfg.BackoffMax {
			e.backoff = lc.cfg.BackoffMax
		}
		e.rebuildAt = now.Add(e.backoff)
		lc.mu.Unlock()
		return
	}
	// 重建成功：替换映射，恢复正常续期模式。
	e.mapping = newMapping
	e.rebuildAt = time.Time{} // 清零，退出重建模式。
	interval := lc.cfg.RenewInterval(newMapping.TTL)
	e.renewAt = now.Add(interval)
	e.backoff = lc.cfg.BackoffBase
	lc.mu.Unlock()
}

// tickRenew 处理正常续期模式。调用时已持有 lc.mu。
func (lc *Lifecycle) tickRenew(e *entry, now time.Time) {
	if now.Before(e.renewAt) {
		lc.mu.Unlock()
		return
	}
	m := e.mapping
	lc.mu.Unlock()

	// 在锁外执行 Refresh I/O。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := lc.mapper.Refresh(ctx, m)
	cancel()

	if err == nil {
		// 续期成功，更新下次续期时刻。
		lc.mu.Lock()
		if !lc.closed {
			interval := lc.cfg.RenewInterval(m.TTL)
			e.renewAt = now.Add(interval)
		}
		lc.mu.Unlock()
		return
	}

	// 续期失败：触发回调（锁外）+ 进入退避重建。
	lc.mu.Lock()
	cb := lc.cfg.OnMappingLost
	lc.mu.Unlock()

	if cb != nil {
		cb(m)
	}

	lc.mu.Lock()
	if !lc.closed {
		e.rebuildAt = now.Add(e.backoff)
	}
	lc.mu.Unlock()
}

// Close 对所有活跃 Mapping 调 Unmap（best-effort，短超时不阻塞），
// 标记 closed 使后续 Tick/Manage 为 no-op。
func (lc *Lifecycle) Close() {
	lc.mu.Lock()
	if lc.closed {
		lc.mu.Unlock()
		return
	}
	lc.closed = true
	entries := lc.entries
	lc.entries = nil
	lc.mu.Unlock()

	// best-effort Unmap：每个映射给 2s 超时，失败忽略。
	for _, e := range entries {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = lc.mapper.Unmap(ctx, e.mapping)
		cancel()
	}
}
