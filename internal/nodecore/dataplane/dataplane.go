// Package dataplane 实现数据面编排层：串联 TUN ↔ FlowTracker ↔ DPI ↔ RouteEngine ↔ ConnPool，
// 形成完整的出站/入站数据管线。
//
// 出站方向（TUN → 网络）：
//  1. 从 TUN 读取 IP 包（批量）
//  2. parsePacketToContext 构建 InboundContext（五元组 + 版本）
//  3. isDNSHijack 检测 DNS 劫持目标
//  4. FlowTracker 识别五元组流
//  5. 新流：DPI.InspectCtx 检测应用层协议 → RouteEngine.RouteCtx 路由决策 → ConnPool 获取连接
//  6. 通过连接发送到对端
//
// 入站方向（网络 → TUN）：
//  1. 从 Framer 读取帧
//  2. 提取 payload（IP 包）
//  3. 写入 TUN
package dataplane

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/connpool"
	"github.com/x6nux/corelink/internal/nodecore/dpi"
	"github.com/x6nux/corelink/internal/nodecore/flowtrack"
	"github.com/x6nux/corelink/internal/nodecore/metadata"
	"github.com/x6nux/corelink/internal/nodecore/route"
	"github.com/x6nux/corelink/internal/nodecore/tun"
	"github.com/x6nux/corelink/internal/transport"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// Config 是 DataPlane 的初始化配置。
type Config struct {
	TUN            tun.Device                                                 // TUN 设备（生产用真实设备，测试用 fakeTUN）
	Pool           *connpool.Pool                                             // 弹性连接池
	Router         *route.Engine                                              // 多层路由引擎
	FlowTracker    *flowtrack.Tracker                                         // 分段锁流追踪器
	DefaultTTL     uint8                                                      // 默认 TTL，0 时使用 64
	SelfRelayIdx   uint16                                                     // 本节点 relay 索引（LEAF 为 0）
	DNSHijackAddrs []netip.Addr                                               // 需要 DNS 劫持的目标 IP 列表（通常为虚拟 DNS 地址）
	UpstreamDNS    string                                                     // DNS 转发上游地址（默认 8.8.8.8:53）
	OnRouteSync    func(senderVIP netip.Addr, entry transport.RouteSyncEntry) // 收到 RouteSync 路由同步帧回调
}

// DataPlane 是数据面编排器，管理 TUN 读写循环和网络收发。
type DataPlane struct {
	cfg     Config
	tracker *flowtrack.Tracker
	router  *route.Engine
	pool    *connpool.Pool
	tunDev  tun.Device
	ttl     uint8

	// dnsHijackAddrs 存储需要拦截的 DNS 目标地址集合（来自 Config.DNSHijackAddrs）。
	dnsHijackAddrs map[netip.Addr]bool
	// upstreamDNS DNS 转发上游地址
	upstreamDNS string

	// peerFramers 存储 nodeID → Framer 映射：listener 接受的入站连接注册在此。
	// 当 processOutbound 或中继转发找不到 ConnPool 连接时，查此表用入站连接发帧。
	peerFramersMu sync.RWMutex
	peerFramers   map[string]*transport.Framer

	// weightedRoutes 大流加权路由：目标 nodeID → 加权最优 nodeID（throughput×0.6+latency×0.4）。
	weightedRoutesMu sync.RWMutex
	weightedRoutes   map[string]string

	// rankedHops ProbeRouter MTR 质量排序的候选 hop 列表：目标 nodeID → [最优hop, 次优hop, ...]。
	// processOutbound 按此顺序尝试发送，无 MTR 数据时 fallback 到任意可达 hop。
	rankedHopsMu sync.RWMutex
	rankedHops   map[string][]string

	// loopAvoid 环路回避表：目标 nodeID → { 需回避的 hop nodeID → 过期时间 }。
	// 检测到环路后标记，processOutbound/RelayForward 跳过已标记的 hop。60s 自动过期。
	loopAvoidMu sync.RWMutex
	loopAvoid   map[string]map[string]time.Time

	// blockedPeers 调试屏蔽集合：被屏蔽的 nodeID 不会通过 peer framer 或 connpool 直连发送，
	// 强制走中继路径以测试 failover。
	blockedPeersMu sync.RWMutex
	blockedPeers   map[string]bool

	// tunBufPool 复用入站 TUN 写缓冲区，避免每包分配。
	tunBufPool sync.Pool

	// closeMu 保护 Close() 与 HandleDNSFrame 等动态 wg.Add(1) 的竞态：
	// HandleDNSFrame 取 RLock 后检查 ctx.Err() 并 wg.Add(1)，
	// Close() 取 WLock 后 cancel+wg.Wait，确保二者互斥。
	closeMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New 创建一个新的数据面编排器。
func New(cfg Config) (*DataPlane, error) {
	// 校验必需的配置项，防止 nil 解引用
	if cfg.TUN == nil {
		return nil, fmt.Errorf("dataplane: Config.TUN 不能为 nil")
	}
	if cfg.Pool == nil {
		return nil, fmt.Errorf("dataplane: Config.Pool 不能为 nil")
	}
	if cfg.Router == nil {
		return nil, fmt.Errorf("dataplane: Config.Router 不能为 nil")
	}
	if cfg.FlowTracker == nil {
		return nil, fmt.Errorf("dataplane: Config.FlowTracker 不能为 nil")
	}

	ttl := cfg.DefaultTTL
	if ttl == 0 {
		ttl = 64
	}
	// 构建 DNS 劫持地址集合
	hijackAddrs := make(map[netip.Addr]bool, len(cfg.DNSHijackAddrs))
	for _, addr := range cfg.DNSHijackAddrs {
		hijackAddrs[addr] = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	dp := &DataPlane{
		cfg:            cfg,
		tracker:        cfg.FlowTracker,
		router:         cfg.Router,
		pool:           cfg.Pool,
		tunDev:         cfg.TUN,
		ttl:            ttl,
		dnsHijackAddrs: hijackAddrs,
		upstreamDNS: func() string {
			if cfg.UpstreamDNS != "" {
				return cfg.UpstreamDNS
			}
			return "8.8.8.8:53"
		}(),
		peerFramers:    make(map[string]*transport.Framer),
		weightedRoutes: make(map[string]string),
		rankedHops:     make(map[string][]string),
		loopAvoid:      make(map[string]map[string]time.Time),
		blockedPeers:   make(map[string]bool),
		ctx:            ctx,
		cancel:         cancel,
	}
	// 初始化 TUN 写缓冲池（入站路径复用，避免每包分配）
	dp.tunBufPool.New = func() any {
		// 65535 = 最大 IP 包 + tunWriteOffset 前导空间
		return make([]byte, tunWriteOffset+65535)
	}
	return dp, nil
}

// Run 启动 TUN 读取循环（出站方向）。阻塞直到 context 被取消。
// 取 closeMu.RLock 保证 ctx.Err() 检查与 wg.Add(1) 的原子性，
// 消除 Close() 在两步之间介入导致的 TOCTOU 竞态（与 HandleDNSFrame 一致）。
func (dp *DataPlane) Run() error {
	dp.closeMu.RLock()
	if dp.ctx.Err() != nil {
		dp.closeMu.RUnlock()
		return dp.ctx.Err()
	}
	dp.wg.Add(1)
	dp.closeMu.RUnlock()
	go dp.runTUNRead()
	// 阻塞直到关闭
	<-dp.ctx.Done()
	return nil
}

// ApplyConfig 热更新路由和连接池配置。
func (dp *DataPlane) ApplyConfig(cfg *genv1.NodeConfig) error {
	// 1. 更新路由引擎 FIB
	routeCfg := buildRouteConfig(cfg)
	dp.router.Update(routeCfg)

	// 2. 更新连接池下一跳
	hops := buildHopMap(cfg)
	dp.pool.Update(hops)

	// 3. 过期旧流（路由可能已变更）
	dp.tracker.Expire()

	slog.Info("dataplane: 配置已更新",
		"peers", len(cfg.GetPeers()),
		"fib_entries", len(routeCfg.FIB),
	)
	return nil
}

// Close 优雅关闭数据面：取 closeMu.WLock 后取消 context、关闭 TUN 设备（解除 Read 阻塞），
// 然后等待所有 goroutine 退出。closeMu 保证与 HandleDNSFrame 的 wg.Add(1) 互斥。
func (dp *DataPlane) Close() error {
	dp.closeMu.Lock()
	dp.cancel()
	dp.closeMu.Unlock()
	// 关闭 TUN 设备以解除 Read 的阻塞，捕获并返回错误
	tunErr := dp.tunDev.Close()
	// 带超时等待，避免入站 peer 连接阻塞导致 Close 永久挂起
	done := make(chan struct{})
	go func() { dp.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		slog.Warn("dataplane: Close 等待 goroutine 超时（可能有 peer 连接仍阻塞）")
	}
	return tunErr
}

// DefaultDataPlanePort 是数据面默认端口，当 Ingress 未指定端口时使用。
const DefaultDataPlanePort = 7447

// tunWriteOffset 是 TUN 读写时需要的前导偏移量。
// macOS utun 驱动要求写入时在包前保留 4 字节 AF 头；
// Linux TUN 在启用 virtio-net-hdr 时需要 10 字节前导空间。
// 统一使用 10 字节保守值，确保在所有平台下不会发生 "invalid offset" 错误。
const tunWriteOffset = 10

// RegisterPeerFramer 注册一个入站连接的 Framer（listener 接受连接后调用）。
// 用于反向发帧给该 peer（双向连接复用）。
func (dp *DataPlane) RegisterPeerFramer(nodeID string, f *transport.Framer) {
	dp.peerFramersMu.Lock()
	dp.peerFramers[nodeID] = f
	dp.peerFramersMu.Unlock()
	slog.Info("dataplane: 注册 peer framer", "nodeID", nodeID[:min(8, len(nodeID))])
}

// UnregisterPeerFramer 注销入站连接（连接断开时调用）。
// 仅当 map 中存储的 framer 与待注销的 framer 相同时才删除，
// 防止断线重连时旧连接的 defer 误删新连接的 framer。
func (dp *DataPlane) UnregisterPeerFramer(nodeID string, f *transport.Framer) {
	dp.peerFramersMu.Lock()
	if dp.peerFramers[nodeID] == f {
		delete(dp.peerFramers, nodeID)
	}
	dp.peerFramersMu.Unlock()
}

// GetPeerFramer 查找 peer 的入站 Framer。
func (dp *DataPlane) GetPeerFramer(nodeID string) *transport.Framer {
	dp.peerFramersMu.RLock()
	f := dp.peerFramers[nodeID]
	dp.peerFramersMu.RUnlock()
	return f
}

// SetRankedHops 设置 ProbeRouter MTR 质量排序的候选 hop（目标 nodeID → [最优, 次优, ...]）。
// processOutbound 按此顺序尝试发送。
func (dp *DataPlane) SetRankedHops(ranked map[string][]string) {
	dp.rankedHopsMu.Lock()
	dp.rankedHops = ranked
	dp.rankedHopsMu.Unlock()
}

// getRankedHops 查询 MTR 排序的候选 hop 列表。
func (dp *DataPlane) getRankedHops(targetNodeID string) []string {
	dp.rankedHopsMu.RLock()
	r := dp.rankedHops[targetNodeID]
	dp.rankedHopsMu.RUnlock()
	return r
}

// SetWeightedRoutes 设置大流加权路由表（ProbeRouter 带宽×0.6+延迟×0.4 评分后的最优 NextHop）。
func (dp *DataPlane) SetWeightedRoutes(routes map[string]string) {
	dp.weightedRoutesMu.Lock()
	dp.weightedRoutes = routes
	dp.weightedRoutesMu.Unlock()
}

// getWeightedRoute 查询大流加权路由。无则返回空串。
func (dp *DataPlane) getWeightedRoute(targetNodeID string) string {
	dp.weightedRoutesMu.RLock()
	r := dp.weightedRoutes[targetNodeID]
	dp.weightedRoutesMu.RUnlock()
	return r
}

// MarkLoopAvoid 标记环路回避：到达 dstNodeID 时避开 avoidHops 中的节点，60s 后过期。
func (dp *DataPlane) MarkLoopAvoid(dstNodeID string, avoidHops ...string) {
	dp.loopAvoidMu.Lock()
	defer dp.loopAvoidMu.Unlock()
	m := dp.loopAvoid[dstNodeID]
	if m == nil {
		m = make(map[string]time.Time)
		dp.loopAvoid[dstNodeID] = m
	}
	expiry := time.Now().Add(60 * time.Second)
	for _, h := range avoidHops {
		m[h] = expiry
		slog.Info("dataplane: 标记环路回避", "dst", dstNodeID[:min(8, len(dstNodeID))], "avoid", h[:min(8, len(h))])
	}
}

// isLoopAvoided 检查 hop 是否被环路回避（已过期自动清除）。
func (dp *DataPlane) isLoopAvoided(dstNodeID, hop string) bool {
	dp.loopAvoidMu.RLock()
	m := dp.loopAvoid[dstNodeID]
	if m == nil {
		dp.loopAvoidMu.RUnlock()
		return false
	}
	expiry, ok := m[hop]
	dp.loopAvoidMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		dp.loopAvoidMu.Lock()
		delete(dp.loopAvoid[dstNodeID], hop)
		dp.loopAvoidMu.Unlock()
		return false
	}
	return true
}

// RelayForward 中继转发帧：用 rankedHops 按质量排序尝试，跳过 srcNodeID（防 2-hop 环路）和环路回避表中的 hop。
// 返回 true 表示转发成功。
func (dp *DataPlane) RelayForward(srcNodeID string, dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte) bool {
	// 查路由找目标 nodeID
	key := flowtrack.FlowKey{DstIP: dstVIP}
	decision := dp.router.Route(key, dpi.Result{})
	targetNodeID := decision.NextHop
	if targetNodeID == "" {
		slog.Warn("dataplane: 中继无路由", "dst", dstVIP)
		return false
	}

	// 构建候选列表（rankedHops 排序 + 兜底）
	candidates := dp.getRankedHops(targetNodeID)
	if len(candidates) == 0 {
		candidates = []string{targetNodeID}
	}
	for _, h := range dp.pool.ReachableHops() {
		dup := false
		for _, c := range candidates {
			if c == h {
				dup = true
				break
			}
		}
		if !dup {
			candidates = append(candidates, h)
		}
	}

	// 按序尝试，跳过 srcNodeID（防回弹）和环路回避表
	for _, hop := range candidates {
		if hop == srcNodeID {
			continue // 不回弹给发送者
		}
		if dp.isBlocked(hop) || dp.isLoopAvoided(targetNodeID, hop) {
			continue
		}
		// 先试 peerFramer
		if pf := dp.GetPeerFramer(hop); pf != nil {
			if fErr := pf.WritePacket(dstVIP, dstRelay, ttl-1, payload); fErr == nil {
				slog.Debug("dataplane: 中继转发成功(peerFramer)", "dst", dstVIP, "hop", hop[:min(8, len(hop))])
				return true
			}
		}
		// 再试 ConnPool
		if !dp.pool.IsReachable(hop) {
			continue
		}
		conn, err := dp.pool.Acquire(hop)
		if err != nil {
			continue
		}
		if conn.Framer == nil {
			dp.pool.Release(conn)
			continue
		}
		if wErr := conn.Framer.WritePacket(dstVIP, dstRelay, ttl-1, payload); wErr != nil {
			conn.Close()
			dp.pool.Release(conn)
			continue
		}
		dp.pool.Release(conn)
		slog.Debug("dataplane: 中继转发成功(connpool)", "dst", dstVIP, "hop", hop[:min(8, len(hop))])
		return true
	}

	slog.Warn("dataplane: 中继转发失败（所有候选均不可用）", "dst", dstVIP)
	return false
}

// BlockPeer 屏蔽指定 peer 的直连调度（peer framer + connpool），流量强制走中继。
func (dp *DataPlane) BlockPeer(nodeID string) {
	dp.blockedPeersMu.Lock()
	dp.blockedPeers[nodeID] = true
	dp.blockedPeersMu.Unlock()
	slog.Info("dataplane: 屏蔽 peer 直连调度", "nodeID", nodeID[:min(8, len(nodeID))])
}

// UnblockPeer 恢复指定 peer 的直连调度。
func (dp *DataPlane) UnblockPeer(nodeID string) {
	dp.blockedPeersMu.Lock()
	delete(dp.blockedPeers, nodeID)
	dp.blockedPeersMu.Unlock()
	slog.Info("dataplane: 恢复 peer 直连调度", "nodeID", nodeID[:min(8, len(nodeID))])
}

// ListBlocked 返回所有被屏蔽的 peer ID 列表。
func (dp *DataPlane) ListBlocked() []string {
	dp.blockedPeersMu.RLock()
	defer dp.blockedPeersMu.RUnlock()
	out := make([]string, 0, len(dp.blockedPeers))
	for id := range dp.blockedPeers {
		out = append(out, id)
	}
	return out
}

// isBlocked 检查指定 peer 是否被屏蔽。
// Pool 返回底层连接池引用，供外部发送控制帧。
func (dp *DataPlane) Pool() *connpool.Pool { return dp.pool }

func (dp *DataPlane) isBlocked(nodeID string) bool {
	dp.blockedPeersMu.RLock()
	b := dp.blockedPeers[nodeID]
	dp.blockedPeersMu.RUnlock()
	return b
}

// InjectInbound 从外部注入一个 IP 包到 TUN（入站方向）。
// 用于接收其他节点发来的帧，注入本地网络栈。
func (dp *DataPlane) InjectInbound(pkt []byte) {
	// 给 pkt 前面加 offset 预留空间（TUN 驱动要求）
	buf := make([]byte, tunWriteOffset+len(pkt))
	copy(buf[tunWriteOffset:], pkt)
	bufs := [][]byte{buf}
	n, err := dp.tunDev.Write(bufs, tunWriteOffset)
	if err != nil {
		slog.Warn("dataplane: InjectInbound TUN 写入失败", "err", err, "pktLen", len(pkt))
	} else {
		slog.Debug("dataplane: InjectInbound 成功", "written", n, "pktLen", len(pkt))
	}
}

// SendDNSTo 通过 peer framer 或 connpool 向指定 VIP 发送 DNS 帧。
func (dp *DataPlane) SendDNSTo(nodeID string, dstVIP netip.Addr, dnsPayload []byte) error {
	// 优先 peer framer
	if pf := dp.GetPeerFramer(nodeID); pf != nil {
		return pf.WriteDNS(dstVIP, dnsPayload)
	}
	// 退而用 connpool
	conn, err := dp.pool.Acquire(nodeID)
	if err != nil {
		return err
	}
	defer dp.pool.Release(conn)
	if conn.Framer == nil {
		return fmt.Errorf("连接无 framer")
	}
	return conn.Framer.WriteDNS(dstVIP, dnsPayload)
}

// HandleDNSFrame 处理收到的 DNS 帧（出口节点角色）：
// 将 DNS 查询转发到上游 DNS（默认 8.8.8.8:53，可通过 Config.UpstreamDNS 配置）解析，响应通过 DNS 帧回传给请求方。
func (dp *DataPlane) HandleDNSFrame(srcVIP netip.Addr, dnsPayload []byte, replyFramer *transport.Framer) {
	slog.Info("dataplane: 收到 DNS 帧", "srcVIP", srcVIP, "len", len(dnsPayload))
	// 取 closeMu.RLock 保证 ctx.Err() 检查与 wg.Add(1) 的原子性，
	// 消除 Close() 在两步之间介入导致的 TOCTOU 竞态。
	dp.closeMu.RLock()
	if dp.ctx.Err() != nil {
		dp.closeMu.RUnlock()
		return
	}
	dp.wg.Add(1)
	dp.closeMu.RUnlock()
	go func() {
		defer dp.wg.Done()
		// 转发到公共 DNS
		conn, err := net.DialTimeout("udp", dp.upstreamDNS, 3*time.Second)
		if err != nil {
			slog.Warn("dataplane: DNS 转发拨号失败", "err", err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err := conn.Write(dnsPayload); err != nil {
			slog.Warn("dataplane: DNS 转发写入失败", "err", err)
			return
		}

		resp := make([]byte, 4096)
		n, err := conn.Read(resp)
		if err != nil {
			slog.Warn("dataplane: DNS 转发读取失败", "err", err)
			return
		}

		// 通过 DNS 帧回传给请求方
		if replyFramer != nil {
			if err := replyFramer.WriteDNS(srcVIP, resp[:n]); err != nil {
				slog.Warn("dataplane: DNS 响应帧发送失败", "err", err)
			}
		}
	}()
}

// StartRecvLoop 启动入站接收循环：从 Framer 读取帧并写入 TUN。
// 每个活跃连接调用一次，goroutine 在 framer 出错或 context 取消后退出。
// 取 closeMu.RLock 保证 ctx.Err() 检查与 wg.Go(内含 wg.Add(1)) 的原子性，
// 消除 Close() 在两步之间介入导致的 TOCTOU 竞态（与 HandleDNSFrame 一致）。
func (dp *DataPlane) StartRecvLoop(framer *transport.Framer) {
	dp.closeMu.RLock()
	if dp.ctx.Err() != nil {
		dp.closeMu.RUnlock()
		return
	}
	// wg.Add(1) 在 RLock 保护下完成，保证 Close() 的 WLock 不会在
	// ctx.Err() 检查与 Add(1) 之间介入（与 HandleDNSFrame 一致）。
	dp.wg.Add(1)
	dp.closeMu.RUnlock()
	go func() {
		defer dp.wg.Done()
		for {
			_, _, _, payload, err := framer.ReadPacket()
			if err != nil {
				if dp.ctx.Err() != nil {
					return
				}
				slog.Debug("dataplane: 入站读取失败", "err", err)
				return
			}
			// 写入 TUN（入站方向：网络 → TUN），必须带 tunWriteOffset 前导空间。
			// 使用 sync.Pool 复用缓冲区，避免每包分配。
			buf := dp.tunBufPool.Get().([]byte)
			n := tunWriteOffset + len(payload)
			copy(buf[tunWriteOffset:n], payload)
			_, werr := dp.tunDev.Write([][]byte{buf[:n]}, tunWriteOffset)
			dp.tunBufPool.Put(buf)
			if werr != nil {
				if dp.ctx.Err() != nil {
					return
				}
				slog.Error("dataplane: TUN 写入失败", "err", werr)
			}
		}
	}()
}

// runTUNRead 是 TUN 读取主循环（出站方向：TUN → 网络）。
func (dp *DataPlane) runTUNRead() {
	defer dp.wg.Done()

	batchSize := dp.tunDev.BatchSize()
	bufs := make([][]byte, batchSize)
	sizes := make([]int, batchSize)
	for i := range bufs {
		// 分配足够容纳最大 IP 包（65535）+ 前导偏移量的缓冲区，
		// 避免 macOS/Linux 驱动因偏移不够或缓冲区过小而崩溃。
		bufs[i] = make([]byte, tunWriteOffset+65535)
	}

	slog.Info("dataplane: TUN 读循环已启动", "batchSize", batchSize)
	for {
		// 使用 tunWriteOffset 作为 offset 参数，确保各平台驱动安全读取
		n, err := dp.tunDev.Read(bufs, sizes, tunWriteOffset)
		if err != nil {
			if dp.ctx.Err() != nil {
				return
			}
			// 持续错误时增加短暂背压，避免 CPU 空转
			select {
			case <-dp.ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		slog.Debug("dataplane: TUN 读到包", "n", n, "size0", sizes[0])

		for i := range n {
			if sizes[i] == 0 {
				continue
			}
			// 跳过前导 offset，提取真实 IP 包数据
			pkt := bufs[i][tunWriteOffset : tunWriteOffset+sizes[i]]
			dp.processOutbound(pkt)
		}
	}
}

// processOutbound 处理一个出站 IP 包：
// parsePacketToContext → isDNSHijack → FlowTracker → DPI.InspectCtx → RouteCtx → 发送。
func (dp *DataPlane) processOutbound(pkt []byte) {
	// 0. 解析包头，构建 InboundContext（五元组 + 协议版本）
	ctx := dp.parsePacketToContext(pkt)

	slog.Debug("dataplane: processOutbound", "dst", ctx.Destination, "src", ctx.Source, "net", ctx.Network, "len", len(pkt))

	// 0.5 DNS 劫持检测：命中则交由 handleDNSHijack 处理，不进入正常路由
	if dp.isDNSHijack(pkt, ctx) {
		dp.handleDNSHijack(pkt, ctx)
		return
	}

	// 1. FlowTracker：识别/创建流
	flow, isNew := dp.tracker.Track(pkt)
	if flow == nil {
		return // 解析失败或流表满，丢弃
	}

	// 1.5 累计字节数（原子操作，大流判断用）
	flow.AddBytes(len(pkt))

	// 2. 新流：DPI 检测（首包 TCP payload 识别应用层协议）
	if isNew {
		tcpPayload := extractTCPPayload(pkt)
		if len(tcpPayload) > 0 {
			done := dpi.InspectCtx(tcpPayload, ctx)
			flow.SetDPIDone(done)
		}
		flow.SetState(flowtrack.FlowEstablished)
	}

	// 3. 路由决策：已有流用缓存路由（路由切换只影响新流），新流查 FIB
	var targetHop string
	if !isNew {
		targetHop = flow.GetNextHop() // 已有流：保持当前路由，防切换丢包
	}
	if targetHop == "" {
		dp.router.RouteCtx(ctx)
		targetHop = ctx.NextHop
	}
	if targetHop == "" {
		slog.Warn("dataplane: 无路由", "dst", ctx.Destination.Addr())
		return
	}
	// 3.5 大流路由切换：累积 > 1MB 的流走加权最优路由（throughput×0.6+latency×0.4）
	if flow.BytesTotal() > 1<<20 {
		if alt := dp.getWeightedRoute(targetHop); alt != "" {
			targetHop = alt
		}
	}
	flow.SetNextHop(targetHop)

	slog.Debug("dataplane: 路由决策",
		"dst", ctx.Destination.Addr(),
		"port", ctx.Destination.Port(),
		"hop", targetHop[:min(8, len(targetHop))],
		"rule", ctx.Rule,
		"proto", ctx.Protocol,
	)

	dstVIP := ctx.Destination.Addr()
	if !dstVIP.IsValid() {
		// 尝试从包重新提取目的 IP（容错）
		if extracted, ok := extractDstIP(pkt); ok {
			dstVIP = extracted
		} else {
			return
		}
	}

	// 构建候选 hop 列表（MTR 质量排序优先，第一个能发通就返回）
	candidates := dp.getRankedHops(targetHop)
	if len(candidates) == 0 {
		// 无 MTR 数据：目标直连 + 所有可达 hop
		candidates = append(candidates, targetHop)
	}
	// 补充 MTR 列表未覆盖的可达 hop（兜底）
	for _, h := range dp.pool.ReachableHops() {
		dup := false
		for _, c := range candidates {
			if c == h {
				dup = true
				break
			}
		}
		if !dup {
			candidates = append(candidates, h)
		}
	}

	for _, hop := range candidates {
		if dp.isBlocked(hop) || dp.isLoopAvoided(targetHop, hop) {
			continue
		}
		// 先试 peerFramer（入站连接复用，零拷贝）
		if pf := dp.GetPeerFramer(hop); pf != nil {
			if fErr := pf.WritePacket(dstVIP, dp.cfg.SelfRelayIdx, dp.ttl, pkt); fErr == nil {
				return
			}
		}
		// 再试 ConnPool（出站连接）
		if !dp.pool.IsReachable(hop) {
			continue
		}
		conn, err := dp.pool.Acquire(hop)
		if err != nil {
			continue
		}
		if conn.Framer == nil {
			dp.pool.Release(conn)
			continue
		}
		slog.Debug("dataplane: 发送帧", "dst", dstVIP, "hop", hop, "addr", conn.Addr)
		if wErr := conn.Framer.WritePacket(dstVIP, dp.cfg.SelfRelayIdx, dp.ttl, pkt); wErr != nil {
			conn.Close()
			dp.pool.Release(conn)
			continue
		}
		dp.pool.Release(conn)
		return
	}
	slog.Debug("dataplane: 无可用 hop，丢弃", "dst", dstVIP)
}

// parsePacketToContext 解析 IPv4 包头，构造 InboundContext（五元组 + 网络层信息）。
// 支持 TCP（proto=6）、UDP（proto=17）、ICMP（proto=1）三种传输层协议。
func (dp *DataPlane) parsePacketToContext(pkt []byte) *metadata.InboundContext {
	ctx := &metadata.InboundContext{}
	if len(pkt) < 1 {
		return ctx
	}
	version := pkt[0] >> 4
	ctx.IPVersion = version
	if version == 4 && len(pkt) >= 20 {
		ctx.Network = protoToNetwork(pkt[9])
		var srcArr, dstArr [4]byte
		copy(srcArr[:], pkt[12:16])
		copy(dstArr[:], pkt[16:20])
		srcIP := netip.AddrFrom4(srcArr)
		dstIP := netip.AddrFrom4(dstArr)
		ihl := int(pkt[0]&0x0F) * 4
		if ihl < 20 {
			return ctx // IHL 最小 20 字节
		}
		srcPort, dstPort := uint16(0), uint16(0)
		// TCP 或 UDP 有四字节端口头
		if pkt[9] == 6 || pkt[9] == 17 {
			if len(pkt) >= ihl+4 {
				srcPort = uint16(pkt[ihl])<<8 | uint16(pkt[ihl+1])
				dstPort = uint16(pkt[ihl+2])<<8 | uint16(pkt[ihl+3])
			}
		}
		ctx.Source = netip.AddrPortFrom(srcIP, srcPort)
		ctx.Destination = netip.AddrPortFrom(dstIP, dstPort)
	}
	return ctx
}

// protoToNetwork 将 IP 协议号转换为 metadata.Network* 字符串常量。
func protoToNetwork(proto uint8) string {
	switch proto {
	case 6:
		return metadata.NetworkTCP
	case 17:
		return metadata.NetworkUDP
	case 1:
		return metadata.NetworkICMP
	default:
		return ""
	}
}

// extractUDPPayload 从 IPv4+UDP 包中提取 UDP payload（8 字节头之后的数据）。
// 仅处理 IPv4（version=4）且协议为 UDP（proto=17）的包。
func extractUDPPayload(pkt []byte) []byte {
	if len(pkt) < 20 {
		return nil
	}
	if pkt[0]>>4 != 4 {
		return nil // 非 IPv4，暂不支持 IPv6
	}
	if pkt[9] != 17 {
		return nil // 非 UDP
	}
	ihl := int(pkt[0]&0x0F) * 4
	udpStart := ihl
	if len(pkt) < udpStart+8 {
		return nil // UDP 头至少 8 字节
	}
	return pkt[udpStart+8:]
}

// isDNSHijack 检测该包是否命中 DNS 劫持规则：
// 目的地在 dnsHijackAddrs 集合中，且 UDP payload 为合法 DNS 查询。
// 命中时将 ctx.Protocol 设为 metadata.ProtocolDNS。
func (dp *DataPlane) isDNSHijack(pkt []byte, ctx *metadata.InboundContext) bool {
	if len(dp.dnsHijackAddrs) == 0 {
		return false
	}
	if !dp.dnsHijackAddrs[ctx.Destination.Addr()] {
		return false
	}
	payload := extractUDPPayload(pkt)
	if len(payload) > 0 && dpi.SniffDNS(payload) {
		ctx.Protocol = metadata.ProtocolDNS
		return true
	}
	return false
}

// handleDNSHijack 处理命中 DNS 劫持的包（当前为 stub，打印日志后丢弃）。
// TODO: 接入 dnsproxy 模块，实现本地 DNS 解析与响应注入。
func (dp *DataPlane) handleDNSHijack(pkt []byte, ctx *metadata.InboundContext) {
	slog.Info("dataplane: DNS 劫持命中", "src", ctx.Source, "dst", ctx.Destination)

	// 构造 DNS SERVFAIL 响应包（RCODE=2），让客户端快速收到明确错误
	// 比静默丢弃更好：客户端可以快速失败并尝试其它 DNS 服务器
	//
	// TODO: 未来可在此接入真正的 DNS 上游（dnsproxy），实现完整的解析功能
	servfail := buildDNS_SERVFAIL(pkt)
	if servfail != nil {
		dp.InjectInbound(servfail)
	}
}

// buildDNS_SERVFAIL 从原始 IP/UDP/DNS 包构造一个 SERVFAIL 响应包。
// 交换 src/dst IP+port，设置 DNS 响应标志（QR=1, RCODE=2）。
func buildDNS_SERVFAIL(pkt []byte) []byte {
	if len(pkt) < 1 || pkt[0]>>4 != 4 {
		return nil
	}
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 { // IHL 最小值校验
		return nil
	}
	if len(pkt) < ihl+8+12 { // IP + UDP(8) + DNS header(12)
		return nil
	}
	if pkt[9] != 17 { // 仅 UDP
		return nil
	}
	resp := make([]byte, len(pkt))
	copy(resp, pkt)
	// 交换 src/dst IP
	copy(resp[12:16], pkt[16:20])
	copy(resp[16:20], pkt[12:16])
	// 交换 src/dst port
	copy(resp[ihl:ihl+2], pkt[ihl+2:ihl+4])
	copy(resp[ihl+2:ihl+4], pkt[ihl:ihl+2])
	// 设置合理的 TTL（原始包 TTL 可能已递减为 0）
	resp[8] = 64
	// DNS 头：设置 QR=1（响应），保留 opcode，RCODE=2（SERVFAIL）
	dnsOff := ihl + 8
	resp[dnsOff+2] = pkt[dnsOff+2] | 0x80          // QR=1，保留 RD/TC/AA 等原始标志
	resp[dnsOff+3] = (pkt[dnsOff+3] & 0xF0) | 0x02 // RCODE=SERVFAIL
	// 计算 IP 校验和（macOS/Windows TUN 可能校验入站包）
	resp[10], resp[11] = 0, 0
	resp[ihl+6], resp[ihl+7] = 0, 0 // UDP 校验和清零（可选）
	var csum uint32
	for i := 0; i < ihl; i += 2 {
		csum += uint32(resp[i])<<8 | uint32(resp[i+1])
	}
	for csum > 0xFFFF {
		csum = (csum >> 16) + (csum & 0xFFFF)
	}
	resp[10] = byte(^csum >> 8)
	resp[11] = byte(^csum)
	return resp
}

// HandleProbeFrame 处理收到的路径探测帧。
//
// 每个中间节点同时做两件事（一包测全路径）：
//  1. 回复：发一个 isReply=true 帧回源端，携带当前 hopIndex（源端据此知道到此跳的累计延迟）
//  2. 转发：递增 hopIndex 并转发到 Route[hopIndex]（若还有下一跳）
//
// 回包路由有两种模式：
//   - 原路回包（AutoReply=false）：reply 沿正向路径反转逐跳返回，用于 --via 精确测量指定路径
//   - Auto 回包（AutoReply=true）：reply 走 FIB 自然路由直发源端，用于自然路由测量
//
// 终点节点只回复不转发。源端发一个 Probe 包即可收到 N 个 reply（每跳一个）。
func (dp *DataPlane) HandleProbeFrame(nodeID string, frameDstVIP netip.Addr, selfVIP netip.Addr, payload []byte) {
	probe, err := transport.DecodeProbePayload(payload)
	if err != nil {
		slog.Warn("dataplane: Probe 帧解码失败", "from", nodeID[:min(8, len(nodeID))], "err", err)
		return
	}

	slog.Info("dataplane: 收到 Probe 帧",
		"from", nodeID[:min(8, len(nodeID))],
		"frameDst", frameDstVIP,
		"self", selfVIP,
		"nonce", probe.Nonce,
		"hopIndex", probe.HopIndex,
		"isReply", probe.IsReply,
		"autoReply", probe.AutoReply,
	)

	// ─── Relay 透明转发：帧头 DstVIP 不是本机 → 转发到 DstVIP ───
	// 适用于所有帧类型（request/reply/auto/trace），统一判断
	if frameDstVIP != selfVIP && frameDstVIP.IsValid() {
		slog.Info("dataplane: Probe relay 转发", "target", frameDstVIP, "self", selfVIP)
		dp.sendProbeFrame(probe, frameDstVIP, false)
		return
	}

	// ─── RouteSync 帧处理（源路由投递，到达终点回调） ───
	if probe.IsRouteSync {
		nextVIP := probe.NextHop()
		if nextVIP.IsValid() {
			// 还有下一跳：沿源路由转发
			probe.Advance()
			dp.sendProbeFrame(probe, nextVIP, true) // 源路由禁止中继
		} else if dp.cfg.OnRouteSync != nil {
			// 终点：回调上层
			dp.cfg.OnRouteSync(probe.SourceVIP, probe.SyncEntry)
		}
		return
	}

	// ─── Reply 帧处理 ───
	if probe.IsReply {
		if probe.AutoReply {
			// Auto 模式 reply：无预存路由，直接投递或用 FIB 转发到 SourceVIP
			dp.deliverProbeReply(probe)
		} else {
			// 原路模式 reply：沿 reply 路由逐跳转发
			probe.Advance()
			nextVIP := probe.NextHop()
			if !nextVIP.IsValid() {
				dp.deliverProbeReply(probe)
			} else {
				dp.sendProbeFrame(probe, nextVIP, false)
			}
		}
		return
	}

	// ─── Request 帧处理 ───
	probe.Advance()

	// 1. 发 reply 回源端（OrigHop 保存原始跳索引，HopIndex 专用于 reply 路由从 0 开始）
	if probe.AutoReply {
		reply := &transport.ProbeFrame{
			IsReply:     true,
			AutoReply:   true,
			Nonce:       probe.Nonce,
			TimestampNs: probe.TimestampNs,
			SourceVIP:   probe.SourceVIP,
			HopIndex:    0,
			OrigHop:     probe.HopIndex,
		}
		dp.sendProbeFrame(reply, probe.SourceVIP, false) // reply 允许中继
	} else {
		// 原路模式：构造反转路由（排除自身，只包含之前经过的跳）
		prevHops := int(probe.HopIndex) - 1
		replyRoute := make([]netip.Addr, prevHops+1)
		for i := range prevHops {
			replyRoute[i] = probe.Route[prevHops-1-i]
		}
		replyRoute[len(replyRoute)-1] = probe.SourceVIP

		reply := &transport.ProbeFrame{
			IsReply:     true,
			AutoReply:   false,
			Nonce:       probe.Nonce,
			TimestampNs: probe.TimestampNs,
			SourceVIP:   probe.SourceVIP,
			HopIndex:    0,
			OrigHop:     probe.HopIndex,
			Route:       replyRoute,
		}
		dp.sendProbeFrame(reply, replyRoute[0], false) // reply 允许中继
	}

	// 2. 若还有下一跳则沿指定路由转发（禁止中继：不可直连就丢弃）
	nextVIP := probe.NextHop()
	if nextVIP.IsValid() {
		dp.sendProbeFrame(probe, nextVIP, true) // request 转发禁止中继
	}
}

// SendProbe 发送 Probe/RouteSync 帧到指定 VIP（允许中继 fallback）。
func (dp *DataPlane) SendProbe(probe *transport.ProbeFrame, targetVIP netip.Addr) {
	dp.sendProbeFrame(probe, targetVIP, false)
}

// sendProbeFrame 将 Probe 帧发送到指定 VIP 对应的节点。
// noRelay=true 时禁止中继 fallback——目标不可直连则丢弃（用于 --via 指定路由的请求转发）。
func (dp *DataPlane) sendProbeFrame(probe *transport.ProbeFrame, targetVIP netip.Addr, noRelay bool) {
	// 查找 VIP 对应的 nodeID
	ctx := &metadata.InboundContext{
		Network: metadata.NetworkICMP, IPVersion: 4,
		Destination: netip.AddrPortFrom(targetVIP, 0),
	}
	dp.router.RouteCtx(ctx)
	targetHop := ctx.NextHop
	if targetHop == "" {
		slog.Warn("dataplane: Probe 转发无路由", "targetVIP", targetVIP)
		return
	}

	slog.Info("dataplane: Probe 发送", "targetVIP", targetVIP, "hop", targetHop[:min(8, len(targetHop))], "isReply", probe.IsReply, "hopIdx", probe.HopIndex)

	// 优先 peer framer（直连入站连接）
	if pf := dp.GetPeerFramer(targetHop); pf != nil {
		if err := pf.WriteProbe(probe, targetVIP); err == nil {
			slog.Info("dataplane: Probe 通过 peerFramer 发送", "hop", targetHop[:min(8, len(targetHop))])
			return
		}
		slog.Warn("dataplane: Probe peerFramer 写失败", "hop", targetHop[:min(8, len(targetHop))])
	}

	// ConnPool
	sendHop := targetHop
	if !dp.pool.IsReachable(sendHop) {
		if noRelay {
			// 指定路由模式：目标不可直连 → 丢弃（源端超时，如实反映链路不通）
			slog.Warn("dataplane: Probe 目标不可直连（noRelay），丢弃", "targetVIP", targetVIP, "hop", targetHop[:min(8, len(targetHop))])
			return
		}
		// 允许中继 fallback
		reachable := dp.pool.ReachableHops()
		found := false
		for _, h := range reachable {
			sendHop = h
			found = true
			break
		}
		if !found {
			slog.Warn("dataplane: Probe 无可达 hop", "targetVIP", targetVIP)
			return
		}
		slog.Info("dataplane: Probe 经中继发送", "target", targetHop[:min(8, len(targetHop))], "relay", sendHop[:min(8, len(sendHop))])
	}

	conn, err := dp.pool.Acquire(sendHop)
	if err != nil {
		slog.Warn("dataplane: Probe Acquire 失败", "hop", sendHop[:min(8, len(sendHop))], "err", err)
		return
	}
	defer dp.pool.Release(conn)
	if conn.Framer != nil {
		if err := conn.Framer.WriteProbe(probe, targetVIP); err != nil {
			slog.Warn("dataplane: Probe 写入失败", "hop", sendHop[:min(8, len(sendHop))], "err", err)
		}
	}
}

// probeWaiters 存储等待 Probe 回复的 channel（nonce → chan *ProbeFrame）。
var probeWaitersMu sync.Mutex
var probeWaiters = make(map[uint64]chan *transport.ProbeFrame)

// RegisterProbeWaiter 注册一个 Probe 回复等待者。
// 缓冲区大小为 MaxProbeHops，支持一个 Probe 包收到路径上所有跳的 reply。
func RegisterProbeWaiter(nonce uint64) chan *transport.ProbeFrame {
	ch := make(chan *transport.ProbeFrame, transport.MaxProbeHops)
	probeWaitersMu.Lock()
	probeWaiters[nonce] = ch
	probeWaitersMu.Unlock()
	return ch
}

// UnregisterProbeWaiter 注销 Probe 回复等待者。
func UnregisterProbeWaiter(nonce uint64) {
	probeWaitersMu.Lock()
	delete(probeWaiters, nonce)
	probeWaitersMu.Unlock()
}

// deliverProbeReply 将 Probe 回复投递到对应的等待者。
func (dp *DataPlane) deliverProbeReply(probe *transport.ProbeFrame) {
	probeWaitersMu.Lock()
	ch, ok := probeWaiters[probe.Nonce]
	probeWaitersMu.Unlock()
	if ok {
		select {
		case ch <- probe:
		default:
		}
	} else {
		slog.Debug("dataplane: Probe 回复无等待者", "nonce", probe.Nonce)
	}
}

// ProbeResult 单跳探测结果。
type ProbeResult struct {
	HopIndex uint8         // 跳索引（从 1 开始，Advance 后的值）
	RTT      time.Duration // 从发送到收到该跳 reply 的累计延迟
}

// SendProbeAll 发起一次 Probe 探测，收集路径上所有跳的 reply。
// 一个 Probe 包 → 每个中间/终点节点各回一个 reply → 返回 per-hop RTT。
// route 为预存路由的 VIP 列表（不含自身），autoReply 控制回包路由模式，timeout 为等待超时。
func (dp *DataPlane) SendProbeAll(selfVIP netip.Addr, route []netip.Addr, autoReply bool, timeout time.Duration) ([]ProbeResult, error) {
	slog.Info("dataplane: SendProbeAll 开始", "selfVIP", selfVIP, "hops", len(route), "autoReply", autoReply, "firstHop", route[0])
	if len(route) == 0 {
		return nil, fmt.Errorf("dataplane: Probe 路由为空")
	}

	nonce := uint64(time.Now().UnixNano())
	sendTime := time.Now()

	probe := &transport.ProbeFrame{
		IsReply:     false,
		AutoReply:   autoReply,
		Nonce:       nonce,
		TimestampNs: sendTime.UnixNano(),
		SourceVIP:   selfVIP,
		HopIndex:    0,
		Route:       route,
	}

	ch := RegisterProbeWaiter(nonce)
	defer UnregisterProbeWaiter(nonce)

	dp.sendProbeFrame(probe, route[0], false) // 初始发送允许中继到达第一跳

	// 收集所有跳的 reply（最多 len(route) 个）
	var results []ProbeResult
	deadline := time.After(timeout)
	for range len(route) {
		select {
		case reply := <-ch:
			rtt := time.Since(sendTime)
			results = append(results, ProbeResult{HopIndex: reply.OrigHop, RTT: rtt})
		case <-deadline:
			return results, nil // 超时返回已收到的部分结果
		case <-dp.ctx.Done():
			return results, dp.ctx.Err()
		}
	}
	return results, nil
}

// SendBandwidthFrame 发送带宽测速帧（FlagBandwidth）到指定目标 VIP。
func (dp *DataPlane) SendBandwidthFrame(targetVIP netip.Addr, payload []byte) {
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkUDP,
		IPVersion:   4,
		Destination: netip.AddrPortFrom(targetVIP, 0),
	}
	dp.router.RouteCtx(ctx)
	hop := ctx.NextHop
	if hop == "" {
		return
	}
	// peer framer 优先
	if pf := dp.GetPeerFramer(hop); pf != nil {
		pf.WritePacket(targetVIP, 0, 64, payload) //nolint:errcheck
		return
	}
	conn, err := dp.pool.Acquire(hop)
	if err != nil {
		return
	}
	defer dp.pool.Release(conn)
	if conn.Framer != nil {
		conn.Framer.WritePacket(targetVIP, 0, 64, payload) //nolint:errcheck
	}
}

// PathInfo 描述到目标 VIP 的 overlay 路由路径。
type PathInfo struct {
	Hops []string // 经过的 nodeID 列表（不含自身）
	Via  string   // "direct" / "relay" / "blocked"
}

// QueryRoute 查询到目标 VIP 的 overlay 路由路径。
// 返回路径中经过的 nodeID 列表及路径类型。
func (dp *DataPlane) QueryRoute(dstVIP netip.Addr) PathInfo {
	ctx := &metadata.InboundContext{
		Network:     metadata.NetworkICMP,
		IPVersion:   4,
		Destination: netip.AddrPortFrom(dstVIP, 0),
	}
	dp.router.RouteCtx(ctx)
	targetHop := ctx.NextHop
	if targetHop == "" {
		return PathInfo{}
	}

	// 被屏蔽 → 经中继
	if dp.isBlocked(targetHop) {
		reachable := dp.pool.ReachableHops()
		for _, h := range reachable {
			if !dp.isBlocked(h) && h != targetHop {
				return PathInfo{Hops: []string{h, targetHop}, Via: "relay(blocked)"}
			}
		}
		return PathInfo{Hops: []string{targetHop}, Via: "blocked"}
	}

	// 直连可达
	if dp.GetPeerFramer(targetHop) != nil || dp.pool.IsReachable(targetHop) {
		return PathInfo{Hops: []string{targetHop}, Via: "direct"}
	}

	// 不可直连 → 选可达 hop 做中继
	reachable := dp.pool.ReachableHops()
	for _, h := range reachable {
		if h != targetHop {
			return PathInfo{Hops: []string{h, targetHop}, Via: "relay"}
		}
	}
	return PathInfo{Hops: []string{targetHop}, Via: "unknown"}
}

// extractTCPPayload 从 IPv4+TCP 包中提取 TCP payload。
// 仅处理 IPv4（version=4）且协议为 TCP（proto=6）的包。
func extractTCPPayload(pkt []byte) []byte {
	if len(pkt) < 20 {
		return nil
	}
	version := pkt[0] >> 4
	if version != 4 {
		return nil // 非 IPv4
	}
	ihl := int(pkt[0]&0x0F) * 4
	if len(pkt) < ihl {
		return nil
	}
	proto := pkt[9]
	if proto != 6 {
		return nil // 非 TCP
	}
	if len(pkt) < ihl+20 {
		return nil // TCP 头至少 20 字节
	}
	tcpOffset := ihl
	dataOffset := int(pkt[tcpOffset+12]>>4) * 4
	payloadStart := tcpOffset + dataOffset
	if payloadStart >= len(pkt) {
		return nil
	}
	return pkt[payloadStart:]
}

// extractDstIP 从 IPv4 包头提取目标 IP。
func extractDstIP(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 20 {
		return netip.Addr{}, false
	}
	if pkt[0]>>4 != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]}), true
}

// buildRouteConfig 从 NodeConfig 构建路由配置。
// 从 peers 的 AllowedIPs 构建 L3 FIB 条目，根据 relay 信息区分直连/中继。
func buildRouteConfig(cfg *genv1.NodeConfig) *route.RouteConfig {
	// 构建 relayID 集合，用于判断 peer 是否需要经 relay 中转。
	// 同时维护排序后的 relayID 列表，确保选择确定性（map 迭代顺序不保证）。
	relaySet := make(map[string]string) // relayID → addr
	var sortedRelayIDs []string
	for _, r := range cfg.GetRelays() {
		if t := r.GetTunnel(); t != nil && t.GetHost() != "" {
			rid := r.GetRelayId()
			relaySet[rid] = net.JoinHostPort(t.GetHost(), fmt.Sprintf("%d", t.GetPort()))
			sortedRelayIDs = append(sortedRelayIDs, rid)
		}
	}
	sort.Strings(sortedRelayIDs)

	rc := &route.RouteConfig{}

	for _, peer := range cfg.GetPeers() {
		// 判断此 peer 是直连还是经 relay
		via := route.HopDirect
		relayID := ""
		if len(sortedRelayIDs) > 0 {
			// 有 relay 配置时，默认经 relay 中转（选排序后的第一个，确保确定性）
			via = route.HopRelay
			relayID = sortedRelayIDs[0]
		}

		for _, cidr := range peer.GetAllowedIps() {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil {
				slog.Warn("dataplane: 解析 CIDR 失败", "cidr", cidr, "err", err)
				continue
			}
			rc.FIB = append(rc.FIB, route.FIBEntry{
				Prefix:  prefix,
				NextHop: peer.GetNodeId(),
				Via:     via,
				RelayID: relayID,
			})
		}
	}
	return rc
}

// buildHopMap 从 NodeConfig 构建下一跳映射。
// 从 Topology.Neighbors 的 Ingresses 获取每个 peer 的可达地址（数据面端口 7447）。
// 不可直连的 peer 使用可达 relay 的地址作为中继——所有流量都经 relay 转发，
// relay 的 listener OnFrame 按 DstVIP 做二次路由。
func buildHopMap(cfg *genv1.NodeConfig) map[string]connpool.HopInfo {
	hops := make(map[string]connpool.HopInfo)

	// 收集所有 neighbor 的全部候选地址（一个 peer 可能有多个公网 IP）
	peerAddrs := make(map[string][]string) // nodeID → []addr
	if topo := cfg.GetTopology(); topo != nil {
		for _, nb := range topo.GetNeighbors() {
			if nb == nil {
				continue
			}
			nid := nb.GetNodeId()
			for _, ing := range nb.GetIngresses() {
				if ing == nil || ing.GetHost() == "" {
					continue
				}
				port := ing.GetPort()
				if port == 0 {
					port = DefaultDataPlanePort
				}
				addr := net.JoinHostPort(ing.GetHost(), fmt.Sprintf("%d", port))
				peerAddrs[nid] = append(peerAddrs[nid], addr)
			}
		}
	}

	// 为每个 peer 设置连接地址：优先直连，否则用首个可达的 relay 作为中继
	var fallbackAddr string
	// 专门收集 relay 地址作为 fallback（只有 TRANSIT 节点才能中转）。
	// 强制使用 DefaultDataPlanePort：relay 上报的 tunnel port 是 AccessListener(7446)，
	// 但数据面 ConnPool 需要连接 DataPlane Listener(7447)。
	for _, r := range cfg.GetRelays() {
		if r == nil {
			continue
		}
		tun := r.GetTunnel()
		if tun == nil || tun.GetHost() == "" {
			continue
		}
		if fallbackAddr == "" {
			fallbackAddr = net.JoinHostPort(tun.GetHost(), fmt.Sprintf("%d", DefaultDataPlanePort))
		}
	}

	// 每个 peer 的所有候选地址 + fallback 中继地址
	for nid, addrs := range peerAddrs {
		if fallbackAddr != "" {
			addrs = append(addrs, fallbackAddr)
		}
		hops[nid] = connpool.HopInfo{Addrs: addrs, Transport: connpool.TransportTLS}
	}

	// 对于 peers 中没有 neighbor 信息的，用 relay fallback 地址
	for _, peer := range cfg.GetPeers() {
		nodeID := peer.GetNodeId()
		if _, ok := hops[nodeID]; !ok && fallbackAddr != "" {
			hops[nodeID] = connpool.HopInfo{Addrs: []string{fallbackAddr}, Transport: connpool.TransportTLS}
		}
	}

	slog.Info("dataplane: buildHopMap", "hops", len(hops))
	for id, hi := range hops {
		if len(id) > 8 {
			slog.Info("dataplane: hop", "peer", id[:8], "addr", hi.Addr)
		}
	}
	return hops
}
