package probe

import (
	"context"
	"sync"
	"time"
)

// RelayProber 经指定 Relay 探测到目标节点的链路质量。
//
// 入参：relayID（经由哪个 Relay）、targetNodeID（目标节点）。
// 返回：rttMs（RTT 毫秒）、lossPermille（丢包率千分比）、ok（探测是否成功）。
//
// 生产实现由 setupNodeCore 注入（经指定 Relay Bind 发 UDP 探测包）；
// 测试注入 fake RelayProber 返回确定性结果。
type RelayProber func(relayID, targetNodeID string) (rttMs, lossPermille uint32, ok bool)

// MultiRelayProber 管理多 Relay 三维探测矩阵。
//
// 维度：(srcNode=本节点, dstNode=peer, viaRelay=relay)。
// 定时调 FullProbe 经每个 Relay 对每个 peer 探测，结果喂给 Reporter.OnProbe。
// 上报时 target.IngressID = relayID，从而在 Reporter 内区分经不同 Relay 的路径质量。
//
// 并发安全：SetRelays/SetPeers/FullProbe 可并发调用；内部 RWMutex 保护共享状态。
type MultiRelayProber struct {
	probe    RelayProber
	reporter *Reporter // 可为 nil，为 nil 时仅探测不上报

	mu     sync.RWMutex
	relays []string      // 当前已连 Relay ID 列表（拓扑变更时更新）
	peers  []ProbeTarget // 当前探测目标列表（NodeConfig 变更时更新）
}

// NewMultiRelayProber 创建 MultiRelayProber。
//
// probe 不可为 nil；reporter 可为 nil（此时 FullProbe 仅执行探测，不上报）。
func NewMultiRelayProber(probe RelayProber, reporter *Reporter) *MultiRelayProber {
	return &MultiRelayProber{
		probe:    probe,
		reporter: reporter,
	}
}

// SetRelays 更新已连 Relay 列表（拓扑变更时调用）。
//
// 以 relayIDs 的副本替换内部列表，线程安全。
func (m *MultiRelayProber) SetRelays(relayIDs []string) {
	cp := make([]string, len(relayIDs))
	copy(cp, relayIDs)

	m.mu.Lock()
	m.relays = cp
	m.mu.Unlock()
}

// SetPeers 更新探测目标列表（NodeConfig 变更时调用）。
//
// 以 peers 的副本替换内部列表，线程安全。
func (m *MultiRelayProber) SetPeers(peers []ProbeTarget) {
	cp := make([]ProbeTarget, len(peers))
	copy(cp, peers)

	m.mu.Lock()
	m.peers = cp
	m.mu.Unlock()
}

// FullProbe 执行一次全量探测：经每个 Relay 对每个 peer 探测，结果喂 Reporter.OnProbe。
//
// 上报 target：
//   - NodeID   = peer.NodeID（目标节点）
//   - IngressID = relayID（经由的 Relay，区分多路径质量）
//
// relays 或 peers 为空时立即返回（不 panic、不调用 probe）。
// reporter 为 nil 时仅执行探测，跳过上报。
func (m *MultiRelayProber) FullProbe() {
	// 在锁内取快照，避免迭代过程中列表被并发修改。
	m.mu.RLock()
	relays := m.relays
	peers := m.peers
	m.mu.RUnlock()

	if len(relays) == 0 || len(peers) == 0 {
		return
	}

	for _, relayID := range relays {
		for _, peer := range peers {
			rttMs, lossPermille, ok := m.probe(relayID, peer.NodeID)

			if m.reporter != nil {
				// IngressID = relayID，使 Reporter 内部按 (dstNode, viaRelay) 建立独立 FSM。
				target := ProbeTarget{
					NodeID:    peer.NodeID,
					IngressID: relayID,
				}
				m.reporter.OnProbe(target, rttMs, lossPermille, ok)
			}
		}
	}
}

// Run 周期执行 FullProbe，直到 ctx 取消。
//
// interval 为每次 FullProbe 之间的间隔。ctx 取消后 Run 立即返回（不等待进行中的 FullProbe）。
// 调用者通常在独立 goroutine 中运行 Run。
func (m *MultiRelayProber) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.FullProbe()
		}
	}
}
