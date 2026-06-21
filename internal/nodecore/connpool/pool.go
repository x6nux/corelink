package connpool

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x6nux/corelink/internal/transport"
)

// DialFunc 拨号器：为指定地址和传输协议建立底层网络连接。
// 生产环境注入 pkg/tunnel 的 mTLS 拨号器；测试模式可注入返回 nil 的 mock。
type DialFunc func(ctx context.Context, addr string, transport TransportType) (net.Conn, error)

// OnConnEstablished 连接建立后的回调：用于启动 recv 循环等。
// 参数: (conn *Conn)
type OnConnEstablished func(c *Conn)

// Option 连接池可选配置。
type Option func(*Pool)

// WithDialer 注入自定义拨号器。
func WithDialer(d DialFunc) Option {
	return func(p *Pool) {
		p.dialFn = d
	}
}

// WithOnConnEstablished 注入连接建立回调（用于启动出站连接的 recv 循环）。
func WithOnConnEstablished(fn OnConnEstablished) Option {
	return func(p *Pool) {
		p.onConnEstablished = fn
	}
}

// WithTCPPing 注入自定义 TCPing 函数（测试用）。
func WithTCPPing(fn func(addr string) bool) Option {
	return func(p *Pool) {
		p.reach = newReachability(fn)
	}
}

// Pool 弹性连接池：为每个下一跳维护一组连接，按质量优先+流数平衡选择，
// 支持自动扩缩容、TCPing 预探、实时质量检测。
type Pool struct {
	cfg               Config
	dialFn            DialFunc
	onConnEstablished OnConnEstablished
	reach             *reachability // TCPing 可达性状态机

	mu         sync.RWMutex
	groups     map[string]*hopGroup // nextHop → group
	nextID     atomic.Uint64
	totalConns atomic.Int32 // 全局活跃出站连接数

	// reachableCache 缓存 ReachableHops 结果，连接变化时原子递增 version 触发刷新。
	reachableVersion atomic.Uint64
	reachableMu      sync.Mutex
	reachableCached  []string
	reachableCacheV  uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// hopGroup 是单个下一跳的连接组。
type hopGroup struct {
	mu         sync.Mutex
	conns      []*Conn
	info       HopInfo
	isLAN      bool   // true=LAN peer（不计入 MaxWANPeers）
	lazy       bool   // true=不主动建连（超出 peer 上限，按需拨号或走中继）
	standby    bool   // true=后备候选（优于 5% 但不足 10%，等对应 WAN peer 空闲时替换）
	standbyFor string // 后备对应的 WAN peer hop（空闲回收时替换它）
}

// NewPool 创建连接池并启动后台缩容循环。
func NewPool(cfg Config, opts ...Option) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		cfg:    cfg,
		groups: make(map[string]*hopGroup),
		ctx:    ctx,
		cancel: cancel,
	}
	for _, o := range opts {
		o(p)
	}
	if p.reach == nil {
		p.reach = newReachability(nil) // 默认 TCPing
	}
	// 启动后台缩容循环
	p.wg.Add(1)
	go p.shrinkLoop()
	return p
}

// Acquire 为指定下一跳获取一条流数最少的连接。
//
// 逻辑：
//  1. 在 hopGroup 中查找流数最少且未关闭的连接
//  2. 若无可用连接则创建新连接
//  3. 递增 FlowCount，更新 LastUsed
//  4. 若流数超过 ScaleThreshold 则异步触发扩容
func (p *Pool) Acquire(nextHop string) (*Conn, error) {
	p.mu.RLock()
	g, ok := p.groups[nextHop]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("connpool: 获取连接: 未知下一跳 %q", nextHop)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// 质量优先 + 流数平衡选连接：按 score - flowPenalty 选最优
	var best *Conn
	var bestScore float64 = -1
	for _, c := range g.conns {
		if c.isClosed() {
			continue
		}
		score := c.QualityScore() - float64(c.FlowCount.Load())*0.1
		if best == nil || score > bestScore {
			best = c
			bestScore = score
		}
	}

	// 无可用连接 → 触发后台拨号，立刻返回错误（不阻塞热路径）
	if best == nil {
		hopCopy := nextHop
		infoCopy := g.info
		p.wg.Go(func() {
			c, err := p.dialConn(infoCopy, hopCopy)
			if err != nil {
				return
			}
			p.mu.RLock()
			g2, ok := p.groups[hopCopy]
			p.mu.RUnlock()
			if !ok {
				c.Close()
				return
			}
			g2.mu.Lock()
			g2.conns = append(g2.conns, c)
			g2.mu.Unlock()
		})
		return nil, fmt.Errorf("connpool: 无可用连接（后台拨号中）")
	}

	// 递增流数，更新 LastUsed
	best.FlowCount.Add(1)
	best.LastUsed.Store(time.Now().UnixNano())

	// 检查是否需要扩容
	threshold := int32(float64(p.cfg.MaxFlowsPerConn) * p.cfg.ScaleThreshold)
	if best.FlowCount.Load() >= threshold {
		p.tryScaleUp(nextHop, g)
	}

	return best, nil
}

// IsReachable 查询指定 hop 是否可达（有至少一条活跃连接）。
func (p *Pool) IsReachable(hop string) bool {
	p.mu.RLock()
	g, ok := p.groups[hop]
	p.mu.RUnlock()
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conns {
		if !c.isClosed() && c.Framer != nil {
			return true
		}
	}
	return false
}

// ReachableHops 返回所有可达 hop 的列表。
func (p *Pool) ReachableHops() []string {
	v := p.reachableVersion.Load()
	p.reachableMu.Lock()
	if p.reachableCacheV == v && p.reachableCached != nil {
		cached := p.reachableCached
		p.reachableMu.Unlock()
		return cached
	}
	p.reachableMu.Unlock()

	// 缓存失效，重新扫描
	p.mu.RLock()
	var hops []string
	for hop, g := range p.groups {
		g.mu.Lock()
		for _, c := range g.conns {
			if !c.isClosed() && c.Framer != nil {
				hops = append(hops, hop)
				break
			}
		}
		g.mu.Unlock()
	}
	p.mu.RUnlock()

	p.reachableMu.Lock()
	p.reachableCached = hops
	p.reachableCacheV = v
	p.reachableMu.Unlock()
	return hops
}

// InvalidateReachableCache 连接变化时调用，标记缓存需刷新。
func (p *Pool) InvalidateReachableCache() {
	p.reachableVersion.Add(1)
}

// Release 释放连接上的一条流（递减 FlowCount）。
func (p *Pool) Release(conn *Conn) {
	conn.FlowCount.Add(-1)
}

// Update 更新下一跳集合：新增跳创建组并预拨号，移除跳关闭所有连接。
func (p *Pool) Update(hops map[string]HopInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()

	slog.Info("connpool: Update", "newHops", len(hops), "existingGroups", len(p.groups))

	// 移除已不存在的跳
	for hop, g := range p.groups {
		if _, exists := hops[hop]; !exists {
			slog.Info("connpool: 移除 hop", "hop", hop[:min(8, len(hop))])
			g.mu.Lock()
			for _, c := range g.conns {
				if !c.isClosed() {
					p.totalConns.Add(-1)
				}
				c.Close()
			}
			g.conns = nil
			g.mu.Unlock()
			delete(p.groups, hop)
		}
	}

	// LAN/WAN 分类
	type hopEntry struct {
		hop   string
		info  HopInfo
		isLAN bool
	}
	var lanHops, wanHops []hopEntry
	for hop, info := range hops {
		isLAN := false
		for _, addr := range info.Addrs {
			if p.cfg.IsLANAddr(addr) {
				isLAN = true
				break
			}
		}
		entry := hopEntry{hop: hop, info: info, isLAN: isLAN}
		if isLAN {
			lanHops = append(lanHops, entry)
		} else {
			wanHops = append(wanHops, entry)
		}
	}

	// WAN peer 上限：超出的标记 lazy
	maxWAN := p.cfg.MaxWANPeers
	if maxWAN <= 0 {
		maxWAN = len(wanHops) // 不限
	}

	// 按已有连接优先（保持稳定），其余 TODO 后续按 RTT 排序
	activeWAN := 0
	for i := range wanHops {
		if _, exists := p.groups[wanHops[i].hop]; exists {
			activeWAN++
		}
	}

	// 新增或更新跳
	wanCount := 0
	allEntries := append(lanHops, wanHops...)
	for _, entry := range allEntries {
		isLazy := false
		if !entry.isLAN {
			wanCount++
			if wanCount > maxWAN {
				isLazy = true
			}
		}

		// 新 hop 或地址变更：重置退避状态（节点上线通知）
		if _, exists := p.groups[entry.hop]; !exists {
			for _, addr := range entry.info.Addrs {
				p.reach.MarkReachable(addr)
			}
			slog.Info("connpool: 新 peer 上线，重置退避", "hop", entry.hop[:min(8, len(entry.hop))])
		}

		if g, exists := p.groups[entry.hop]; exists {
			g.mu.Lock()
			// 地址变更：重置退避
			if g.info.Addr() != entry.info.Addr() {
				for _, addr := range entry.info.Addrs {
					p.reach.MarkReachable(addr)
				}
			}
			g.info = entry.info
			g.isLAN = entry.isLAN
			g.lazy = isLazy
			// 探活：尝试 keepalive 写，失败则标记 closed
			for _, c := range g.conns {
				if c.isClosed() || c.Framer == nil {
					continue
				}
				if err := c.Framer.WriteKeepalive(0); err != nil {
					slog.Info("connpool: 连接探活失败，关闭", "addr", c.Addr, "err", err)
					p.totalConns.Add(-1)
					c.Close()
				}
			}
			g.mu.Unlock()
		} else if !isLazy {
			// 新增非 lazy hop：创建 hopGroup 并预拨号
			g := &hopGroup{info: entry.info, isLAN: entry.isLAN, lazy: false}
			// 全局连接上限检查
			for range p.cfg.MinConnsPerHop {
				if p.cfg.MaxTotalConns > 0 && int(p.totalConns.Load()) >= p.cfg.MaxTotalConns {
					slog.Warn("connpool: 全局连接上限，跳过预拨号", "limit", p.cfg.MaxTotalConns)
					break
				}
				c, err := p.dialConn(entry.info, entry.hop)
				if err != nil {
					continue
				}
				g.conns = append(g.conns, c)
			}
			p.groups[entry.hop] = g
		} else {
			// lazy hop：只创建空 hopGroup，不拨号
			p.groups[entry.hop] = &hopGroup{info: entry.info, isLAN: entry.isLAN, lazy: true}
		}
	}

	slog.Info("connpool: Update 完成", "lan", len(lanHops), "wan", len(wanHops),
		"wanActive", min(wanCount, maxWAN), "lazy", max(0, len(wanHops)-maxWAN),
		"totalConns", p.totalConns.Load())
	p.InvalidateReachableCache()
}

// ConnCount 返回指定下一跳的当前连接数。
func (p *Pool) ConnCount(nextHop string) int {
	p.mu.RLock()
	g, ok := p.groups[nextHop]
	p.mu.RUnlock()
	if !ok {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, c := range g.conns {
		if !c.isClosed() {
			count++
		}
	}
	return count
}

// TotalConns 返回当前全局活跃出站连接数。
func (p *Pool) TotalConns() int32 {
	return p.totalConns.Load()
}

// Close 关闭连接池：停止后台循环，关闭所有连接。
func (p *Pool) Close() error {
	p.cancel()
	p.wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	for hop, g := range p.groups {
		g.mu.Lock()
		for _, c := range g.conns {
			if !c.isClosed() {
				p.totalConns.Add(-1)
			}
			c.Close()
		}
		g.conns = nil
		g.mu.Unlock()
		delete(p.groups, hop)
	}
	return nil
}

// GetConn 按 ID 查找连接（用于 DataPlane 从 flow.ConnID 取 Conn 发包）。
// 遍历所有组查找，返回 nil 表示连接已关闭或不存在。
func (p *Pool) GetConn(id uint64) *Conn {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, g := range p.groups {
		g.mu.Lock()
		for _, c := range g.conns {
			if c.ID == id && !c.closed {
				g.mu.Unlock()
				return c
			}
		}
		g.mu.Unlock()
	}
	return nil
}

// dialConn 创建一条新连接。遍历 HopInfo.Addrs 逐个尝试，
// LAN 地址优先（同内网延迟更低），每个地址先 TCPing 预探。
func (p *Pool) dialConn(info HopInfo, nextHop string) (*Conn, error) {
	var nc net.Conn
	var dialAddr string
	if p.dialFn != nil {
		// LAN 地址排前面
		addrs := make([]string, 0, len(info.Addrs))
		var wanAddrs []string
		for _, addr := range info.Addrs {
			if p.cfg.IsLANAddr(addr) {
				addrs = append(addrs, addr)
			} else {
				wanAddrs = append(wanAddrs, addr)
			}
		}
		addrs = append(addrs, wanAddrs...)

		var lastErr error
		for _, addr := range addrs {
			if !p.reach.ShouldDial(addr) {
				continue
			}
			if !p.reach.CheckAndDial(addr) {
				continue
			}
			ctx, cancel := context.WithTimeout(p.ctx, p.cfg.DialTimeout)
			nc, lastErr = p.dialFn(ctx, addr, info.Transport)
			cancel()
			if lastErr != nil {
				p.reach.MarkUnreachable(addr)
				slog.Warn("connpool: 拨号失败", "addr", addr, "err", lastErr)
				continue
			}
			p.reach.MarkReachable(addr)
			dialAddr = addr
			slog.Info("connpool: 拨号成功", "addr", addr, "hop", nextHop[:min(8, len(nextHop))])
			p.InvalidateReachableCache()
			break
		}
		if nc == nil && lastErr != nil {
			return nil, lastErr
		}
		if nc == nil && dialAddr == "" {
			return nil, fmt.Errorf("connpool: %v 所有地址均在退避中", info.Addrs)
		}
	}
	now := time.Now()
	c := &Conn{
		ID:        p.nextID.Add(1),
		NextHop:   nextHop,
		Addr:      dialAddr,
		Transport: info.Transport,
		NetConn:   nc,
		CreatedAt: now,
	}
	// TCP keepalive + 关闭 Nagle（快速检测死连接 + 低延迟）
	if tc, ok := nc.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)                  //nolint:errcheck
		tc.SetKeepAlivePeriod(5 * time.Second) //nolint:errcheck
		tc.SetNoDelay(true)                    //nolint:errcheck
	}
	// 建连成功后创建 Framer
	if nc != nil {
		c.Framer = transport.NewStreamFramer(nc)
	}
	c.LastUsed.Store(now.UnixNano())
	if nc != nil {
		p.totalConns.Add(1)
		// 连接关闭时递减计数（包装 Close 不方便，这里靠 shrinkOnce/maintainMinConns 中的清理路径）
	}
	// 触发回调（启动 recv 循环等）
	if p.onConnEstablished != nil && nc != nil {
		p.onConnEstablished(c)
	}
	return c, nil
}

// tryScaleUp 异步扩容：若连接数未达上限则在后台拨号一条新连接加入组。
// 全局连接上限为软限制：高流量突发时允许临时超额，shrinkOnce 会在流量结束后回收空闲连接。
// 调用方须已持有 g.mu。
func (p *Pool) tryScaleUp(nextHop string, g *hopGroup) {
	// 统计未关闭连接数
	active := 0
	for _, c := range g.conns {
		if !c.isClosed() {
			active++
		}
	}
	if active >= p.cfg.MaxConnsPerHop {
		return
	}
	info := g.info
	p.wg.Go(func() {
		c, err := p.dialConn(info, nextHop)
		if err != nil {
			return
		}
		// 拿到连接后加入组
		p.mu.RLock()
		g2, ok := p.groups[nextHop]
		p.mu.RUnlock()
		if !ok {
			// 组已被移除
			c.Close()
			return
		}
		g2.mu.Lock()
		g2.conns = append(g2.conns, c)
		g2.mu.Unlock()
	})
}

// shrinkLoop 后台循环：每 2s 扫描所有组——
// keepalive RTT 测量 + 死连接清理 + 最小连接维护 + 空闲回收 + 定期 WAN peer 轮换。
func (p *Pool) shrinkLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	rotateInterval := p.cfg.RotateInterval
	if rotateInterval <= 0 {
		rotateInterval = 5 * time.Minute
	}
	rotateTicker := time.NewTicker(rotateInterval)
	defer rotateTicker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.probeConns()
			p.maintainMinConns()
			p.shrinkOnce()
		case <-rotateTicker.C:
			p.rotateWANPeers()
		}
	}
}

// probeConns 对所有活跃连接发 keepalive 探测。
// RTT 由 Framer 内部的 OnRTT 回调异步记录；此处只负责发探测和检测失败。
func (p *Pool) probeConns() {
	p.mu.RLock()
	hops := make([]string, 0, len(p.groups))
	for h := range p.groups {
		hops = append(hops, h)
	}
	p.mu.RUnlock()

	for _, hop := range hops {
		p.mu.RLock()
		g, ok := p.groups[hop]
		p.mu.RUnlock()
		if !ok {
			continue
		}

		g.mu.Lock()
		for _, c := range g.conns {
			if c.isClosed() || c.Framer == nil {
				continue
			}
			// 挂载 RTT 回调（幂等）
			if c.Framer.OnRTT == nil {
				connRef := c
				c.Framer.OnRTT = func(rtt time.Duration) {
					connRef.RecordRTT(rtt)
				}
			}
			if err := c.Framer.WriteKeepalive(0); err != nil {
				c.RecordKeepaliveFail()
				if c.KeepaliveFails() >= 3 {
					slog.Info("connpool: 连接连续探测失败，关闭", "addr", c.Addr)
					p.totalConns.Add(-1)
					c.Close()
				}
			}
		}
		g.mu.Unlock()
	}
}

// maintainMinConns 维护每个 hop 的最小连接数——死连接清理 + 自动补充。
func (p *Pool) maintainMinConns() {
	p.mu.RLock()
	hops := make([]string, 0, len(p.groups))
	for h := range p.groups {
		hops = append(hops, h)
	}
	p.mu.RUnlock()

	for _, hop := range hops {
		p.mu.RLock()
		g, ok := p.groups[hop]
		p.mu.RUnlock()
		if !ok {
			continue
		}

		g.mu.Lock()
		// 清理死连接
		alive := 0
		for i := len(g.conns) - 1; i >= 0; i-- {
			if g.conns[i].isClosed() {
				g.conns = append(g.conns[:i], g.conns[i+1:]...)
			} else {
				alive++
			}
		}
		// LAN 升级检查：如果现有连接全走 WAN 但有 LAN 地址可用，替换为 LAN 连接
		hasLANAddr := false
		allWAN := true
		for _, addr := range g.info.Addrs {
			if p.cfg.IsLANAddr(addr) {
				hasLANAddr = true
				break
			}
		}
		for _, c := range g.conns {
			if !c.isClosed() && p.cfg.IsLANAddr(c.Addr) {
				allWAN = false
				break
			}
		}
		if hasLANAddr && allWAN && alive > 0 {
			// 标记旧 WAN 连接 30s 宽限期（允许活跃流量完成），到期后 shrinkOnce 强制关闭
			deadline := time.Now().Add(30 * time.Second)
			for _, c := range g.conns {
				if !c.isClosed() && c.deadline.IsZero() {
					c.deadline = deadline
					slog.Info("connpool: 标记 WAN 连接待替换(30s)", "addr", c.Addr, "hop", hop[:min(8, len(hop))])
				}
			}
		}

		// 补充到最小连接数——不可达的地址跳过
		need := p.cfg.MinConnsPerHop - alive
		isLazy := g.lazy
		info := g.info
		g.mu.Unlock()

		if need <= 0 || isLazy {
			continue // lazy hop 不主动补连接
		}
		if p.cfg.MaxTotalConns > 0 && int(p.totalConns.Load()) >= p.cfg.MaxTotalConns {
			continue // 全局满，不补
		}
		if !p.reach.ShouldDial(info.Addr()) {
			continue
		}
		// 异步拨号补充
		hopCopy := hop
		for range need {
			p.wg.Go(func() {
				c, err := p.dialConn(info, hopCopy)
				if err != nil {
					return
				}
				p.mu.RLock()
				g2, ok := p.groups[hopCopy]
				p.mu.RUnlock()
				if !ok {
					c.Close()
					return
				}
				g2.mu.Lock()
				g2.conns = append(g2.conns, c)
				g2.mu.Unlock()
			})
		}
	}
}

// shrinkOnce 执行一轮缩容扫描。
func (p *Pool) shrinkOnce() {
	p.mu.RLock()
	hops := make([]string, 0, len(p.groups))
	for h := range p.groups {
		hops = append(hops, h)
	}
	p.mu.RUnlock()

	// 预收集 standby 映射（避免遍历 groups 时持有 g.mu 死锁）
	standbyTargets := make(map[string]bool) // hop → 是否有后备等着替换
	p.mu.RLock()
	for _, sg := range p.groups {
		sg.mu.Lock()
		if sg.standby && sg.standbyFor != "" {
			standbyTargets[sg.standbyFor] = true
		}
		sg.mu.Unlock()
	}
	p.mu.RUnlock()

	now := time.Now()
	for _, hop := range hops {
		p.mu.RLock()
		g, ok := p.groups[hop]
		p.mu.RUnlock()
		if !ok {
			continue
		}

		g.mu.Lock()
		alive := 0
		for _, c := range g.conns {
			if !c.isClosed() {
				alive++
			}
		}

		hasStandby := standbyTargets[hop]
		overBudget := p.cfg.MaxTotalConns > 0 && int(p.totalConns.Load()) > p.cfg.MaxTotalConns
		// 有后备时允许回收到 0（后备会立即接替），否则保留 MinConnsPerHop
		minKeep := p.cfg.MinConnsPerHop
		if hasStandby {
			minKeep = 0
		}

		for i := len(g.conns) - 1; i >= 0; i-- {
			c := g.conns[i]
			if c.isClosed() {
				g.conns = append(g.conns[:i], g.conns[i+1:]...)
				continue
			}
			// deadline 到期强制关闭（LAN 升级替换旧 WAN 连接）
			if !c.deadline.IsZero() && now.After(c.deadline) {
				slog.Info("connpool: 宽限期到期，关闭旧连接", "addr", c.Addr)
				p.totalConns.Add(-1)
				c.Close()
				g.conns = append(g.conns[:i], g.conns[i+1:]...)
				alive--
				continue
			}
			if alive <= minKeep {
				break
			}
			lastUsed := time.Unix(0, c.LastUsed.Load())
			idle := c.FlowCount.Load() == 0
			if idle && (hasStandby || overBudget || now.Sub(lastUsed) > p.cfg.IdleTimeout) {
				p.totalConns.Add(-1)
				c.Close()
				g.conns = append(g.conns[:i], g.conns[i+1:]...)
				alive--
			}
		}
		if alive == 0 && hasStandby && !g.isLAN {
			hopCopy := hop
			g.mu.Unlock()
			p.tryStandbyReplace(hopCopy)
		} else {
			g.mu.Unlock()
		}
	}
}

// tryStandbyReplace 检查是否有后备候选等着替换指定 hop。
func (p *Pool) tryStandbyReplace(demotedHop string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, g := range p.groups {
		g.mu.Lock()
		if g.standby && g.standbyFor == demotedHop {
			hop := g.standbyFor
			g.standby = false
			g.standbyFor = ""
			g.lazy = false
			info := g.info
			g.mu.Unlock()

			// 降级原 hop
			if wg, ok := p.groups[demotedHop]; ok {
				wg.mu.Lock()
				wg.lazy = true
				wg.mu.Unlock()
			}

			slog.Info("connpool: 后备替换激活", "promote", hop[:min(8, len(hop))], "demote", demotedHop[:min(8, len(demotedHop))])
			p.wg.Go(func() {
				c, err := p.dialConn(info, hop)
				if err != nil {
					return
				}
				p.mu.RLock()
				g2, ok := p.groups[hop]
				p.mu.RUnlock()
				if !ok {
					c.Close()
					return
				}
				g2.mu.Lock()
				g2.conns = append(g2.conns, c)
				g2.mu.Unlock()
			})
			return
		}
		g.mu.Unlock()
	}
}

// rotateWANPeers 定期对 lazy hop 做 Probe 直连 RTT 测量。
// 如果某个 lazy hop 的直连 RTT 优于当前最差 WAN peer，就升级替换。
func (p *Pool) rotateWANPeers() {
	if p.cfg.ProbeRTT == nil {
		return
	}

	p.mu.RLock()
	type hopRTT struct {
		hop  string
		info HopInfo
		rtt  float64
	}
	var lazyHops []hopRTT
	var activeWAN []hopRTT

	for hop, g := range p.groups {
		g.mu.Lock()
		isLazy, isLAN, info := g.lazy, g.isLAN, g.info
		bestRTT := -1.0
		for _, c := range g.conns {
			if c.isClosed() {
				continue
			}
			if r := c.AvgRTTMs(); r > 0 && (bestRTT < 0 || r < bestRTT) {
				bestRTT = r
			}
		}
		g.mu.Unlock()

		if isLAN {
			continue
		}
		if isLazy {
			lazyHops = append(lazyHops, hopRTT{hop: hop, info: info})
		} else {
			activeWAN = append(activeWAN, hopRTT{hop: hop, info: info, rtt: bestRTT})
		}
	}
	p.mu.RUnlock()

	if len(lazyHops) == 0 || len(activeWAN) == 0 {
		return
	}

	// 找当前最差的 active WAN peer
	worstIdx := 0
	for i, h := range activeWAN {
		if h.rtt > activeWAN[worstIdx].rtt {
			worstIdx = i
		}
	}
	worstRTT := activeWAN[worstIdx].rtt
	worstHop := activeWAN[worstIdx].hop
	if worstRTT <= 0 {
		return
	}

	slog.Info("connpool: WAN peer 轮换扫描", "lazy", len(lazyHops), "worstRTT", worstRTT, "worstHop", worstHop[:min(8, len(worstHop))])

	// 批量 TCPing 预筛：端口不通的直接跳过，不浪费建连+Probe 开销
	var reachable []hopRTT
	for _, candidate := range lazyHops {
		addr := candidate.info.Addr()
		if addr == "" {
			continue
		}
		if p.reach.CheckAndDial(addr) {
			reachable = append(reachable, candidate)
		}
	}
	if len(reachable) == 0 {
		slog.Info("connpool: 所有 lazy hop TCPing 不通，跳过轮换")
		return
	}
	slog.Info("connpool: TCPing 预筛完成", "reachable", len(reachable), "total", len(lazyHops))

	for _, candidate := range reachable {
		// 快速筛选：单次 Probe 直连 RTT
		rtt, ok := p.cfg.ProbeRTT(candidate.hop, candidate.info, 0)
		if !ok {
			continue
		}
		improvement := (worstRTT - rtt) / worstRTT

		if improvement < 0.05 {
			continue
		}
		// ≥5%：启动 1min 持续 Probe 验证
		slog.Info("connpool: 候选优于5%，启动持续验证",
			"hop", candidate.hop[:min(8, len(candidate.hop))], "quickRTT", rtt, "worstRTT", worstRTT)
		avgRTT, ok2 := p.cfg.ProbeRTT(candidate.hop, candidate.info, 1*time.Minute)
		if !ok2 {
			continue
		}
		confirmed := (worstRTT - avgRTT) / worstRTT
		if confirmed >= 0.10 {
			// 验证后 ≥10%：立即替换
			slog.Info("connpool: WAN peer 轮换！验证≥10%",
				"promote", candidate.hop[:min(8, len(candidate.hop))], "avgRTT", avgRTT,
				"demote", worstHop[:min(8, len(worstHop))], "worstRTT", worstRTT)
			p.promoteAndDemote(candidate.hop, candidate.info, worstHop)
			break
		} else if confirmed >= 0.05 {
			// 验证后 5%~10%：标为后备
			slog.Info("connpool: 验证后5%~10%，标为后备",
				"hop", candidate.hop[:min(8, len(candidate.hop))], "avgRTT", avgRTT)
			p.markStandby(candidate.hop, worstHop)
		}
	}
}

// ProbeNewPeer 新 peer 加入时主动探测直连 RTT，如果优于最差 WAN peer 则替换。
func (p *Pool) ProbeNewPeer(hop string) {
	if p.cfg.ProbeRTT == nil {
		return
	}
	p.mu.RLock()
	g, ok := p.groups[hop]
	if !ok {
		p.mu.RUnlock()
		return
	}
	g.mu.Lock()
	if !g.lazy || g.isLAN {
		g.mu.Unlock()
		p.mu.RUnlock()
		return
	}
	info := g.info
	g.mu.Unlock()

	// TCPing 预检：端口不通直接返回
	addr := info.Addr()
	if addr != "" && !p.reach.CheckAndDial(addr) {
		p.mu.RUnlock()
		return
	}

	var worstHop string
	var worstRTT float64
	for h, wg := range p.groups {
		wg.mu.Lock()
		if wg.lazy || wg.isLAN {
			wg.mu.Unlock()
			continue
		}
		for _, c := range wg.conns {
			if !c.isClosed() {
				if r := c.AvgRTTMs(); r > worstRTT {
					worstRTT = r
					worstHop = h
				}
			}
		}
		wg.mu.Unlock()
	}
	p.mu.RUnlock()

	if worstHop == "" || worstRTT <= 0 {
		return
	}

	hopCopy, infoCopy := hop, info
	p.wg.Go(func() {
		rtt, ok := p.cfg.ProbeRTT(hopCopy, infoCopy, 0)
		if !ok {
			return
		}
		if (worstRTT-rtt)/worstRTT < 0.05 {
			return
		}
		// ≥5%：持续验证 1min
		avgRTT, ok2 := p.cfg.ProbeRTT(hopCopy, infoCopy, 1*time.Minute)
		if !ok2 {
			return
		}
		confirmed := (worstRTT - avgRTT) / worstRTT
		if confirmed >= 0.10 {
			slog.Info("connpool: 新 peer 优于最差 WAN！替换",
				"new", hopCopy[:min(8, len(hopCopy))], "avgRTT", avgRTT,
				"old", worstHop[:min(8, len(worstHop))], "worstRTT", worstRTT)
			p.promoteAndDemote(hopCopy, infoCopy, worstHop)
		} else if confirmed >= 0.05 {
			p.markStandby(hopCopy, worstHop)
		}
	})
}

// markStandby 标记 lazy hop 为后备候选。
func (p *Pool) markStandby(hop, forHop string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if g, ok := p.groups[hop]; ok {
		g.mu.Lock()
		g.standby = true
		g.standbyFor = forHop
		g.mu.Unlock()
	}
}

// promoteAndDemote 升级候选 hop + 降级最差 hop。
func (p *Pool) promoteAndDemote(promoteHop string, promoteInfo HopInfo, demoteHop string) {
	p.mu.RLock()
	if cg, ok := p.groups[promoteHop]; ok {
		cg.mu.Lock()
		cg.lazy = false
		cg.mu.Unlock()
	}
	if wg, ok := p.groups[demoteHop]; ok {
		wg.mu.Lock()
		wg.lazy = true
		for _, c := range wg.conns {
			if !c.isClosed() {
				p.totalConns.Add(-1)
				c.Close()
			}
		}
		wg.conns = nil
		wg.mu.Unlock()
	}
	p.mu.RUnlock()

	p.wg.Go(func() {
		c, err := p.dialConn(promoteInfo, promoteHop)
		if err != nil {
			return
		}
		p.mu.RLock()
		g2, ok := p.groups[promoteHop]
		p.mu.RUnlock()
		if !ok {
			c.Close()
			return
		}
		g2.mu.Lock()
		g2.conns = append(g2.conns, c)
		g2.mu.Unlock()
	})
}
