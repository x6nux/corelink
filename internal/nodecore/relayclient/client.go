// Package relayclient 实现 agent 对单个 relay 的接入与被动重连（S3-P4）。
//
// S3 范围（spec §4.1、§7.2.1）：agent 接入单个 relay 作为数据面会合点；
// 断线后被动重连（指数退避 + 抖动 + 上限）。多 relay 质量探测与主动切换/
// 迁移留待 S5。
//
// 数据面双通道（UDP 主路 + 隧道备路）由 bind 包在 Bind 内维护；本包负责
// 监督到 relay 的连通性、在断线时重连，并可选地向 controller 上报接入位置。
package relayclient

import (
	"context"
	"math/rand"
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// State 是 relay 接入状态。
type State int

const (
	// StateDisconnected 未连接（初始/已停止）。
	StateDisconnected State = iota
	// StateConnecting 正在尝试连接（含重连退避中）。
	StateConnecting
	// StateConnected 已接入 relay。
	StateConnected
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	default:
		return "disconnected"
	}
}

// Connector 抽象"连接到 relay 并保持，直到断开"的底层操作，便于测试注入。
//
// 真实实现（tunnelConnector）经 bind 双通道把 relay 设为接入点；
// 测试实现用回环 mock。
type Connector interface {
	// Connect 尝试建立到 relay 的接入。成功返回 nil。
	Connect(ctx context.Context, ep *genv1.RelayEndpoint) error
	// Wait 在 Connect 成功后阻塞，直到该连接断开（返回非 nil 错误）
	// 或 ctx 取消。返回即表示需要重连。
	Wait(ctx context.Context) error
}

// BackoffConfig 控制重连退避参数。
type BackoffConfig struct {
	Base   time.Duration // 初始退避，默认 500ms
	Max    time.Duration // 退避上限，默认 30s
	Factor float64       // 增长因子，默认 2
	Jitter float64       // 抖动比例 0..1，默认 0.2（在退避值上下浮动）
}

func (b *BackoffConfig) applyDefaults() {
	if b.Base <= 0 {
		b.Base = 500 * time.Millisecond
	}
	if b.Max <= 0 {
		b.Max = 30 * time.Second
	}
	if b.Factor <= 1 {
		b.Factor = 2
	}
	if b.Jitter < 0 {
		b.Jitter = 0
	}
	if b.Jitter == 0 {
		// 区分"未设置"与"显式 0"：用一个哨兵无意义，这里约定 0 即关闭抖动。
		// applyDefaults 不强加抖动，保留调用方意图。
	}
}

// Reporter 可选地向 controller 上报本节点的 relay 接入位置（mTLS gRPC）。
// S3 先打通连接，Reporter 为 nil 时不上报。
type Reporter interface {
	ReportNodeLocation(ctx context.Context, loc *genv1.NodeLocation) error
}

// Config 构造 Client 的参数。
type Config struct {
	// Connector 为必填：实际建立/保持到 relay 的连接。
	Connector Connector
	// Backoff 为重连退避参数，零值用默认。
	Backoff BackoffConfig
	// NodeID 为本节点 ID（用于 ReportNodeLocation）。
	NodeID string
	// Reporter 可选：连接成功/断开时上报接入位置。
	Reporter Reporter

	// sleep 注入睡眠（测试用）；nil 则用 time.Sleep。
	sleep func(time.Duration)
	// rng 注入随机源（测试抖动用）；nil 则用全局 rand。
	rng *rand.Rand
}

// Client 监督到单个 relay 的接入与被动重连。
type Client struct {
	cfg           Config
	backoff       BackoffConfig
	injectedSleep bool // sleep 是否为调用方注入（测试用，直接调用不走可中断定时器）

	mu    sync.Mutex
	state State
}

// New 构造 Client。
func New(cfg Config) *Client {
	bo := cfg.Backoff
	bo.applyDefaults()
	injected := cfg.sleep != nil
	return &Client{cfg: cfg, backoff: bo, injectedSleep: injected, state: StateDisconnected}
}

// State 返回当前接入状态。
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) setState(s State) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// Run 阻塞运行接入监督循环，直到 ctx 取消。
//
// 流程：连接（失败则按退避重试）→ 连接成功 → 上报接入 → Wait 阻塞至断开 →
// 上报离开 → 重连（退避从头计）。ctx 取消时优雅退出并置 Disconnected。
func (c *Client) Run(ctx context.Context, ep *genv1.RelayEndpoint) {
	defer c.setState(StateDisconnected)

	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		c.setState(StateConnecting)
		err := c.cfg.Connector.Connect(ctx, ep)
		if err != nil {
			attempt++
			delay := c.backoffFor(attempt)
			if !c.sleepCtx(ctx, delay) {
				return // ctx 取消
			}
			continue
		}

		// 连接成功：重置退避计数，置 Connected，上报接入。
		attempt = 0
		c.setState(StateConnected)
		c.report(ctx, ep, true)

		// 阻塞直到断开或 ctx 取消。
		waitErr := c.cfg.Connector.Wait(ctx)
		c.report(ctx, ep, false)
		if ctx.Err() != nil {
			return
		}
		_ = waitErr // 断开：进入下一轮重连（被动重连）。
	}
}

// backoffFor 计算第 attempt 次失败（attempt>=1）后的退避时长（含抖动、封顶）。
func (c *Client) backoffFor(attempt int) time.Duration {
	d := float64(c.backoff.Base)
	for i := 1; i < attempt; i++ {
		d *= c.backoff.Factor
		if d >= float64(c.backoff.Max) {
			d = float64(c.backoff.Max)
			break
		}
	}
	if d > float64(c.backoff.Max) {
		d = float64(c.backoff.Max)
	}
	// 抖动：在 d 上叠加 ±Jitter 比例的随机偏移。
	if c.backoff.Jitter > 0 {
		j := c.backoff.Jitter
		var r float64
		if c.cfg.rng != nil {
			r = c.cfg.rng.Float64()
		} else {
			r = rand.Float64()
		}
		// 偏移范围 [-j, +j]。
		offset := (r*2 - 1) * j
		d = d * (1 + offset)
		if d < 0 {
			d = 0
		}
		if d > float64(c.backoff.Max) {
			d = float64(c.backoff.Max)
		}
	}
	return time.Duration(d)
}

// sleepCtx 睡眠 d，期间 ctx 取消则提前返回 false。
func (c *Client) sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	// 调用方注入了 sleep（测试）：直接调用，记录退避时长。
	if c.injectedSleep {
		c.cfg.sleep(d)
		return ctx.Err() == nil
	}
	// 默认：可被 ctx 中断的定时器（生产路径）。
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// report 在 Reporter 非 nil 时上报接入/离开位置（best-effort，错误忽略）。
func (c *Client) report(ctx context.Context, ep *genv1.RelayEndpoint, attached bool) {
	if c.cfg.Reporter == nil {
		return
	}
	loc := &genv1.NodeLocation{
		NodeId:   c.cfg.NodeID,
		RelayId:  ep.GetRelayId(),
		Attached: attached,
	}
	_ = c.cfg.Reporter.ReportNodeLocation(ctx, loc)
}
