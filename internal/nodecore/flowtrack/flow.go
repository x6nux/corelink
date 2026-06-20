package flowtrack

import (
	"sync"
	"sync/atomic"
	"time"
)

// FlowState 表示流的生命周期状态。
type FlowState int

const (
	// FlowNew 表示刚创建的新流（尚未完成握手）。
	FlowNew FlowState = iota
	// FlowEstablished 表示已建立的活跃流。
	FlowEstablished
	// FlowClosing 表示正在关闭的流（收到 FIN/RST）。
	FlowClosing
	// FlowClosed 表示已完全关闭的流。
	FlowClosed
)

// TCPFlags 表示 TCP 包头中的标志位。
type TCPFlags struct {
	SYN bool
	FIN bool
	RST bool
	ACK bool
}

// Flow 表示一条被追踪的网络流。
type Flow struct {
	Key       FlowKey
	State     FlowState
	NextHop   string
	ConnID    uint64
	CreatedAt time.Time
	LastSeen  time.Time
	Bytes     uint64
	Packets   uint64
	DPIBuf    []byte
	DPIDone   bool
	mu        sync.Mutex // 保护状态转换

	// bytesTotal 独立的原子累计字节数（供大流判断，processOutbound 无锁读取）。
	// 与 Bytes 分离：Bytes 在 shard 锁内更新，bytesTotal 用 atomic 操作。
	bytesTotal atomic.Int64
}

// AddBytes 原子累加流量字节数（processOutbound 每包调用）。
func (f *Flow) AddBytes(n int) { f.bytesTotal.Add(int64(n)) }

// BytesTotal 原子读取累计字节数（大流判断用，~1ns on x86/arm64）。
func (f *Flow) BytesTotal() int64 { return f.bytesTotal.Load() }

// SetState 线程安全地设置流状态。
func (f *Flow) SetState(s FlowState) {
	f.mu.Lock()
	f.State = s
	f.mu.Unlock()
}

// GetState 线程安全地获取流状态。
func (f *Flow) GetState() FlowState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.State
}

// SetNextHop 线程安全地设置下一跳。
func (f *Flow) SetNextHop(h string) {
	f.mu.Lock()
	f.NextHop = h
	f.mu.Unlock()
}

// GetNextHop 线程安全地获取下一跳。
func (f *Flow) GetNextHop() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.NextHop
}

// SetDPIDone 线程安全地设置 DPI 完成标志。
func (f *Flow) SetDPIDone(done bool) {
	f.mu.Lock()
	f.DPIDone = done
	f.mu.Unlock()
}

// GetDPIDone 线程安全地获取 DPI 完成标志。
func (f *Flow) GetDPIDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.DPIDone
}
