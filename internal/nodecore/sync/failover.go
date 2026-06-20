// Package sync 实现配置同步客户端（S3-P2）。
//
// 通知通道多端点优先级切换策略（spec §5.3 扩展）：
//   - 端点列表按 Priority 排序（0=最高优先级）。
//   - 从最高优先级端点开始尝试连接；连接失败或断开 → 尝试下一优先级。
//   - 所有端点均不可用 → 等待 retryInterval 后从头重试。
//   - 当前活跃端点非最高优先级时，后台轮询更高优先级端点恢复；
//     稳定 switchBackAfter 后切回（滞回/迟滞，防抖动）。
//   - 支持运行期 AddEndpoint（动态注入新端点）。
package sync

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Signal 是通知通道输出的统一信号体（对齐 proto ChangeSignal）。
type Signal struct {
	Changed    bool
	Generation uint64
	Epoch      uint64 // 全网控制平面纪元（阶段1恒0）
}

// notifChannel 抽象通知通道（gRPC 或 WS 实现）。
type notifChannel interface {
	// Recv 阻塞接收一个 Signal；ctx 取消或通道关闭返回 error。
	Recv(ctx context.Context) (Signal, error)
	// Close 关闭通道。
	Close()
}

// channelFactory 是通道工厂函数类型（方便测试注入）。
type channelFactory func(ctx context.Context) (notifChannel, error)

// EndpointConfig 描述一个通知通道端点。
type EndpointConfig struct {
	ID       string         // 端点标识（如 "controller-grpc"）
	Priority int            // 优先级（0=最高，越小越优先）
	Factory  channelFactory // 连接工厂
}

// failoverConfig 是多端点切换配置参数。
type failoverConfig struct {
	// retryInterval 是端点重试间隔（默认 5 秒）。
	retryInterval time.Duration
	// switchBackAfter 是更高优先级端点恢复后等待多久才切回（滞回，默认 10 秒）。
	switchBackAfter time.Duration
}

func defaultFailoverConfig() failoverConfig {
	return failoverConfig{
		retryInterval:   5 * time.Second,
		switchBackAfter: 10 * time.Second,
	}
}

// failoverManager 管理多端点通知通道，对外输出统一信号流。
// 端点按 Priority 排序（0=最高），连接断开时向低优先级递降；
// 非最高优先级活跃时，后台轮询更高优先级端点恢复（迟滞切回）。
type failoverManager struct {
	mu        sync.Mutex
	endpoints []EndpointConfig // 按 Priority 升序排列
	cfg       failoverConfig
	sigCh     chan Signal // 外部读取
	activeIdx int         // 当前活跃端点索引（-1=无）
}

// newFailoverManager 构造 failoverManager（向后兼容旧二态签名）。
// primaryFn 映射为 Priority=0，secondaryFn 映射为 Priority=1。
func newFailoverManager(primaryFn, secondaryFn channelFactory, cfg failoverConfig) *failoverManager {
	m := &failoverManager{
		endpoints: []EndpointConfig{
			{ID: "primary", Priority: 0, Factory: primaryFn},
			{ID: "secondary", Priority: 1, Factory: secondaryFn},
		},
		cfg:       cfg,
		sigCh:     make(chan Signal, 16),
		activeIdx: -1,
	}
	return m
}

// AddEndpoint 运行期添加端点。按 Priority 排序插入。
// 线程安全（持锁）。
func (m *failoverManager) AddEndpoint(ep EndpointConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = append(m.endpoints, ep)
	sort.Slice(m.endpoints, func(i, j int) bool {
		return m.endpoints[i].Priority < m.endpoints[j].Priority
	})
}

// Signals 返回统一信号 channel（只读）。
func (m *failoverManager) Signals() <-chan Signal {
	return m.sigCh
}

// Run 启动多端点切换逻辑，阻塞直到 ctx 取消。
//
// 核心循环：
//  1. 从最高优先级端点开始，逐个尝试连接。
//  2. 连接成功 → 持续接收信号；连接断 → 继续尝试下一优先级。
//  3. 若活跃端点非最高优先级（activeIdx > 0），后台轮询更高优先级端点恢复，
//     稳定 switchBackAfter 后切回（回到外层重新从最高开始）。
//  4. 所有端点均不可用 → 等待 retryInterval 后重新从头扫描。
func (m *failoverManager) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		// 取当前端点快照
		m.mu.Lock()
		eps := make([]EndpointConfig, len(m.endpoints))
		copy(eps, m.endpoints)
		m.mu.Unlock()

		connected := false
		switchedBack := false
		for i, ep := range eps {
			if ctx.Err() != nil {
				return
			}

			ch, err := ep.Factory(ctx)
			if err != nil {
				continue // 此端点不可用，尝试下一个
			}

			// 连接成功
			m.mu.Lock()
			m.activeIdx = i
			m.mu.Unlock()
			connected = true

			if i == 0 {
				// 最高优先级：简单接收循环
				ctxDone := m.recvLoop(ctx, ch)
				ch.Close()
				if ctxDone {
					return
				}
				// 端点 0 断了 → 继续 for 尝试端点 1, 2, ...
			} else {
				// 非最高优先级：接收 + 后台轮询更高优先级恢复
				sb := m.recvWithRecovery(ctx, ch)
				ch.Close()
				if ctx.Err() != nil {
					return
				}
				if sb {
					// 更高优先级恢复 → 跳出内层，外层重新从 0 开始
					switchedBack = true
					break
				}
				// 当前端点断了 → 继续 for 尝试下一个
			}
		}

		if switchedBack {
			continue // 回到外层从最高优先级重新开始
		}

		if !connected {
			// 所有端点均不可用，等一段时间后重试
			select {
			case <-time.After(m.cfg.retryInterval):
			case <-ctx.Done():
				return
			}
		}
		// connected 但全部断了（所有端点走完 for），也回到外层重试
	}
}

// recvLoop 从通道循环接收信号，直到出错或 ctx 取消。
// 返回 true 表示 ctx 取消退出。
func (m *failoverManager) recvLoop(ctx context.Context, ch notifChannel) (ctxDone bool) {
	for {
		sig, err := ch.Recv(ctx)
		if err != nil {
			return ctx.Err() != nil
		}
		select {
		case m.sigCh <- sig:
		case <-ctx.Done():
			return true
		}
	}
}

// recvWithRecovery 从活跃通道接收信号，同时后台轮询更高优先级端点恢复。
// 返回 true 表示更高优先级端点恢复导致切回；false 表示当前通道断开或 ctx 取消。
func (m *failoverManager) recvWithRecovery(ctx context.Context, ch notifChannel) (switchedBack bool) {
	// 后台轮询更高优先级端点恢复（动态从 m.endpoints + m.activeIdx 读取）
	switchBackCh := make(chan struct{}, 1)
	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()
	go m.pollHigherPriorityRecovery(pollCtx, switchBackCh)

	for {
		type recvResult struct {
			sig Signal
			err error
		}
		recvCh := make(chan recvResult, 1)
		recvCtx, cancelRecv := context.WithCancel(ctx)

		go func() {
			sig, err := ch.Recv(recvCtx)
			recvCh <- recvResult{sig, err}
		}()

		select {
		case <-ctx.Done():
			cancelRecv()
			return false
		case <-switchBackCh:
			cancelRecv()
			return true // 切回更高优先级端点（外层重新扫描）
		case res := <-recvCh:
			cancelRecv()
			if res.err != nil {
				return false // 当前通道断，外层继续尝试下一端点
			}
			select {
			case m.sigCh <- res.sig:
			case <-ctx.Done():
				return false
			}
		}
	}
}

// pollHigherPriorityRecovery 后台轮询更高优先级端点列表，任一稳定 switchBackAfter 后发切回信号。
func (m *failoverManager) pollHigherPriorityRecovery(ctx context.Context, switchBackCh chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.cfg.retryInterval):
		}

		// 动态获取最新的 endpoints 列表中更高优先级的端点
		m.mu.Lock()
		currentActiveIdx := m.activeIdx
		var epsToCheck []EndpointConfig
		if currentActiveIdx > 0 {
			epsToCheck = make([]EndpointConfig, currentActiveIdx)
			copy(epsToCheck, m.endpoints[:currentActiveIdx])
		}
		m.mu.Unlock()

		if len(epsToCheck) == 0 {
			continue
		}

		// 尝试连接任一更高优先级端点
		for _, ep := range epsToCheck {
			ch, err := ep.Factory(ctx)
			if err != nil {
				continue
			}

			// 连通，等待 switchBackAfter 稳定窗口
			stable := m.waitStable(ctx, ch, m.cfg.switchBackAfter)
			ch.Close()

			if stable {
				select {
				case switchBackCh <- struct{}{}:
				default:
				}
				return
			}
			break // 只测试第一个能连上的更高优先级端点
		}
	}
}

// waitStable 在 duration 内持续接收通道信号；若 duration 到期未出错则认为稳定。
// 返回 true=稳定，false=通道出错或 ctx 取消。
func (m *failoverManager) waitStable(ctx context.Context, ch notifChannel, duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true // 稳定窗口到期，认为稳定
		}

		recvCtx, cancel := context.WithTimeout(ctx, remaining)
		_, err := ch.Recv(recvCtx)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return false // ctx 取消
			}
			// DeadlineExceeded 表示等待期间无错误（超时到期）
			if recvCtx.Err() == context.DeadlineExceeded {
				return true // 稳定
			}
			return false // 通道出错
		}
		// 收到信号但未超时，继续等待
	}
}
