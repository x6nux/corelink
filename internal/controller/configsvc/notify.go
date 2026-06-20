// Package configsvc 实现配置下发通知中心 + gRPC/WS/HTTP 三个服务端。
// 规格参考 spec §5.3（配置下发：混合通知 + HTTP 拉、per-node generation、
// 在线状态主备任一在线即在线）。
package configsvc

import (
	"sync"
	"sync/atomic"
)

// subID 订阅 ID 类型。
type subID uint64

// subIDCounter 全局自增订阅 ID。
var subIDCounter atomic.Uint64

func nextSubID() subID {
	return subID(subIDCounter.Add(1))
}

// ChangeSignalMsg 是推给订阅者的变更信号。
type ChangeSignalMsg struct {
	Changed    bool
	Generation uint64
}

// storeBumper 是 store 的最小接口：只需要 BumpGeneration 和 GetNode。
type storeBumper interface {
	BumpGeneration(nodeID string) (uint64, error)
}

// Notify 管理每节点订阅者集合，并通过 per-node 串行化 worker 推送变更信号。
//
// 并发安全：
//   - mu 保护 subs（订阅表）。
//   - triggers 中每个 chan struct{} 已在 startWorker 时创建，worker 写 subs
//     均发生在读 subs 之前（由 mu 保护），不需要额外锁。
//   - 优雅关闭：Close 持 mu 置 closed=true、关闭全部订阅 channel 并清空 subs，
//     再关 done channel；worker 收到 done 退出，消费者据订阅 channel 关闭退出。
//     ensureWorker 持 mu 检查 closed 后再 wg.Add，与 Close 经 mu 串行，杜绝
//     WaitGroup Add-after-Wait。closed 一律由 mu 保护（不用 trigMu）。
type Notify struct {
	st storeBumper

	mu     sync.RWMutex
	subs   map[string]map[subID]chan *ChangeSignalMsg // nodeID → {subID → ch}
	closed bool                                       // 受 mu 保护：Close 后置 true

	trigMu   sync.Mutex
	triggers map[string]chan struct{} // nodeID → 带缓冲(1)触发 channel

	done chan struct{}
	wg   sync.WaitGroup
}

// NewNotify 构造 Notify；st 用于 BumpGeneration。
func NewNotify(st storeBumper) *Notify {
	return &Notify{
		st:       st,
		subs:     make(map[string]map[subID]chan *ChangeSignalMsg),
		triggers: make(map[string]chan struct{}),
		done:     make(chan struct{}),
	}
}

// Subscribe 为 nodeID 添加一个订阅者，返回 (subID, channel)。
// channel 缓冲大小为 4，防止慢消费者阻塞 worker。
// 同时确保 per-node worker 已启动。
func (n *Notify) Subscribe(nodeID string) (subID, <-chan *ChangeSignalMsg) {
	ch := make(chan *ChangeSignalMsg, 4)
	id := nextSubID()

	n.mu.Lock()
	if n.closed {
		// 已 Close：不登记订阅者，直接返回（避免泄漏；worker 不会向其推送）。
		n.mu.Unlock()
		return id, ch
	}
	if n.subs[nodeID] == nil {
		n.subs[nodeID] = make(map[subID]chan *ChangeSignalMsg)
	}
	n.subs[nodeID][id] = ch
	n.mu.Unlock()

	// 释放 mu 后再调 ensureWorker（其内部按锁序重新获取 mu → trigMu）。
	n.ensureWorker(nodeID)
	return id, ch
}

// Unsubscribe 移除订阅并关闭 channel。
// 若 Notify 已 Close（channel 已被 Close 统一关闭并清空 subs），则跳过不再 close。
func (n *Notify) Unsubscribe(nodeID string, id subID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return // Close 已统一关闭并清空 subs，避免 double-close
	}
	if m, ok := n.subs[nodeID]; ok {
		if ch, ok := m[id]; ok {
			delete(m, id)
			close(ch)
		}
		if len(m) == 0 {
			delete(n.subs, nodeID)
		}
	}
}

// OnlineCount 返回 nodeID 当前订阅者数量。主备任一有订阅者即在线。
func (n *Notify) OnlineCount(nodeID string) int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.subs[nodeID])
}

// IsOnline 返回 nodeID 是否在线（有任意订阅者）。
func (n *Notify) IsOnline(nodeID string) bool {
	return n.OnlineCount(nodeID) > 0
}

// RecomputeAndNotify 触发对指定节点的重算 + 通知。
// 每节点 channel 缓冲为 1（如果已有触发在等待，可合并），
// 保证 per-node 串行化且 generation 不回退。
func (n *Notify) RecomputeAndNotify(nodeIDs ...string) {
	for _, id := range nodeIDs {
		n.ensureWorker(id)
		n.trigMu.Lock()
		ch := n.triggers[id]
		n.trigMu.Unlock()
		// 非阻塞投递：若 channel 已满说明已有一次待处理的触发，合并即可
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Close 停止所有 worker goroutine，关闭全部订阅 channel，等待 worker 退出。
// 持 mu 置 closed=true + close(done) + 关闭并清空所有订阅 channel，使：
//   - ensureWorker 不会在 close(done) 之后再 wg.Add（消除 Add-after-Wait）；
//   - 消费者（grpc/ws stream）的 `!ok` 分支触发并优雅退出，不必再等各自 ctx；
//   - runWorker/Unsubscribe 持 mu 检查 closed，不再向/重复关闭这些 channel。
//
// 幂等：重复 Close 直接返回，不再 close 已关闭的 done。
func (n *Notify) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	close(n.done)
	// 遍历关闭所有订阅 channel，并清空订阅表（防止 Unsubscribe 二次 close）。
	for nodeID, m := range n.subs {
		for id, ch := range m {
			close(ch)
			delete(m, id)
		}
		delete(n.subs, nodeID)
	}
	n.mu.Unlock()

	n.wg.Wait()
}

// ensureWorker 确保 nodeID 的 worker 已启动（幂等）。
// 锁序约定：先 mu 后 trigMu。先持 mu 检查 closed（已关闭则不新建），再持 trigMu
// 检查/登记 trigger，最后 wg.Add(1)+起 goroutine。closed 检查与 wg.Add 同处
// mu 临界区，使 close(done)（Close 持 mu）与 wg.Add 经 mu 串行，杜绝 Add-after-Wait。
func (n *Notify) ensureWorker(nodeID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return // 已关闭：不再新建 worker
	}

	n.trigMu.Lock()
	defer n.trigMu.Unlock()
	if _, ok := n.triggers[nodeID]; ok {
		return // 已存在
	}
	// 缓冲为 1：保证非阻塞投递且合并多次触发
	trigCh := make(chan struct{}, 1)
	n.triggers[nodeID] = trigCh

	n.wg.Add(1)
	go n.runWorker(nodeID, trigCh)
}

// runWorker 是 per-node 串行化 worker：
// 收到触发 → BumpGeneration → 向该节点所有订阅者推 ChangeSignalMsg。
// generation 单调性由 store.BumpGeneration（DB 原子自增）保证。
func (n *Notify) runWorker(nodeID string, trigCh <-chan struct{}) {
	defer n.wg.Done()
	for {
		select {
		case <-n.done:
			return
		case _, ok := <-trigCh:
			if !ok {
				return
			}
			gen, err := n.st.BumpGeneration(nodeID)
			if err != nil {
				// store 错误：跳过本次通知（不崩溃，等下次触发重试）
				continue
			}
			msg := &ChangeSignalMsg{Changed: true, Generation: gen}
			n.mu.RLock()
			// Close 已置 closed 并清空 subs；此处不向已关闭的 channel 推送。
			if n.closed {
				n.mu.RUnlock()
				return
			}
			subs := n.subs[nodeID]
			// 向所有订阅者推送（非阻塞：channel 有缓冲，满则丢弃本次信号）
			for _, ch := range subs {
				select {
				case ch <- msg:
				default:
				}
			}
			n.mu.RUnlock()
		}
	}
}
