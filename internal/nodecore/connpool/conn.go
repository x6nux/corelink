// Package connpool 实现弹性连接池：管理到所有下一跳的多协议连接，
// 基于质量指标选择最优连接，支持弹性扩缩容。
//
// 设计要点：
//   - 每个 nextHop 维护一组连接（hopGroup），Acquire 按最少流数选择。
//   - 流数接近 MaxFlowsPerConn * ScaleThreshold 时异步扩容（预分配新连接）。
//   - 后台 shrinkLoop 回收空闲超时连接，保留 MinConnsPerHop 保底。
//   - DialFunc 注入拨号器；nil 时创建无底层连接的 Conn（测试模式）。
package connpool

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x6nux/corelink/internal/transport"
)

// TransportType 传输协议类型。
type TransportType string

const (
	TransportUDP  TransportType = "udp"
	TransportTCP  TransportType = "tcp"
	TransportTLS  TransportType = "tls"
	TransportWS   TransportType = "ws"
	TransportWSS  TransportType = "wss"
	TransportGRPC TransportType = "grpc"
)

const rttBufSize = 10 // RTT 环形缓冲大小

// Conn 是池管理的单条连接。
type Conn struct {
	ID        uint64
	NextHop   string
	Addr      string
	Transport TransportType
	NetConn   net.Conn          // 底层网络连接（测试模式可为 nil）
	Framer    *transport.Framer // 帧读写器（建连后创建，测试模式可为 nil）
	FlowCount atomic.Int32
	Quality   QualityMetrics
	CreatedAt time.Time
	LastUsed  atomic.Int64 // UnixNano

	// RTT 环形缓冲（实时质量检测）
	rttBuf   [rttBufSize]time.Duration
	rttIdx   int
	rttCount int

	// keepalive 连续失败计数
	keepaliveFails int
	// deadline 宽限期截止时间：非零时表示连接已被标记替换，到期后强制关闭。
	deadline time.Time

	mu     sync.Mutex
	closed bool
}

// RecordRTT 记录一次 RTT 样本并更新滑动平均。
func (c *Conn) RecordRTT(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rttBuf[c.rttIdx] = d
	c.rttIdx = (c.rttIdx + 1) % rttBufSize
	if c.rttCount < rttBufSize {
		c.rttCount++
	}
	// 更新滑动平均 RTT
	var sum time.Duration
	for i := range c.rttCount {
		sum += c.rttBuf[i]
	}
	c.Quality.RTT = sum / time.Duration(c.rttCount)
	c.Quality.Score = c.computeScore()
	c.keepaliveFails = 0
}

// RecordKeepaliveFail 记录一次 keepalive 失败。
func (c *Conn) RecordKeepaliveFail() {
	c.mu.Lock()
	c.keepaliveFails++
	c.mu.Unlock()
}

// KeepaliveFails 返回连续 keepalive 失败次数。
func (c *Conn) KeepaliveFails() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keepaliveFails
}

// computeScore 计算综合质量分（越高越好）。持锁调用。
func (c *Conn) computeScore() float64 {
	rttMs := float64(c.Quality.RTT.Milliseconds())
	if rttMs <= 0 {
		return 1000 // 无 RTT 数据时给高分
	}
	// 分数 = 1000/rtt — RTT 越低分越高
	return 1000 / rttMs
}

// QualityScore 返回当前质量评分。
func (c *Conn) QualityScore() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rttCount == 0 {
		return 1000
	}
	return c.Quality.Score
}

// AvgRTTMs 返回连接的平均 RTT（毫秒）。无数据返回 -1。
func (c *Conn) AvgRTTMs() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rttCount == 0 {
		return -1
	}
	return float64(c.Quality.RTT.Milliseconds())
}

// Close 关闭连接（幂等）。
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.NetConn != nil {
		return c.NetConn.Close()
	}
	return nil
}

// isClosed 返回连接是否已关闭。
func (c *Conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// QualityMetrics 连接质量指标。
type QualityMetrics struct {
	RTT          time.Duration // 往返延迟
	LossPermille int           // 千分比丢包率（0-1000）
	Jitter       time.Duration // 抖动
	Score        float64       // 综合质量分（越高越好）
}

// HopInfo 下一跳的拨号信息。
// Addrs 包含该 hop 的所有候选地址（按置信度降序），dialConn 逐个尝试直到成功。
type HopInfo struct {
	Addrs     []string
	Transport TransportType
}

// Addr 返回首选地址（兼容旧代码）。
func (h HopInfo) Addr() string {
	if len(h.Addrs) > 0 {
		return h.Addrs[0]
	}
	return ""
}

// HopState 下一跳的可达性状态。
type HopState struct {
	Reachable bool   // 是否可达（最近一次拨号成功）
	Addr      string // 拨号地址
}

// Config 连接池配置。
type Config struct {
	MaxFlowsPerConn int           // 每连接最大流数，默认 100
	ScaleThreshold  float64       // 扩容阈值（流数占比），默认 0.8
	MinConnsPerHop  int           // 每跳最少连接数，默认 1
	MaxConnsPerHop  int           // 每跳最多连接数，默认 16
	IdleTimeout     time.Duration // 空闲回收超时，默认 5min
	DialTimeout     time.Duration // 拨号超时，默认 1s
	MaxTotalConns   int           // 全局出站连接上限，0=不限，默认 200
	MaxWANPeers     int           // 最大 WAN peer 数（LAN peer 不计入），0=不限，默认 20
	RotateInterval  time.Duration // WAN peer 轮换探测周期，默认 5min
	SelfAddrs       []string      // 本机地址列表（用于 LAN 判定），可为空
	// ProbeRTT 回调：拨号建连 → 通过连接发 Probe 帧测直连 RTT。
	// duration 指定持续测量时间（快速筛选传 0 表示单次，验证阶段传 1min）。
	// 返回平均 rttMs 和是否成功。由上层（main.go）注入实现。nil 时跳过轮换。
	ProbeRTT func(hop string, info HopInfo, duration time.Duration) (rttMs float64, ok bool)
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		MaxFlowsPerConn: 100,
		ScaleThreshold:  0.8,
		MinConnsPerHop:  1,
		MaxConnsPerHop:  16,
		IdleTimeout:     5 * time.Minute,
		DialTimeout:     1 * time.Second,
		MaxTotalConns:   200,
		MaxWANPeers:     20,
		RotateInterval:  5 * time.Minute,
	}
}

// IsLANAddr 判断地址是否为 LAN（与 SelfAddrs 同 /16 子网）。
func (c *Config) IsLANAddr(addr string) bool {
	if len(c.SelfAddrs) == 0 {
		return false
	}
	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = addr
	}
	target := net.ParseIP(host)
	if target == nil {
		return false
	}
	for _, selfAddr := range c.SelfAddrs {
		selfHost, _, _ := net.SplitHostPort(selfAddr)
		if selfHost == "" {
			selfHost = selfAddr
		}
		selfIP := net.ParseIP(selfHost)
		if selfIP == nil {
			continue
		}
		// 同 /16 子网
		if target.To4() != nil && selfIP.To4() != nil {
			if target.To4()[0] == selfIP.To4()[0] && target.To4()[1] == selfIP.To4()[1] {
				return true
			}
		}
	}
	return false
}
