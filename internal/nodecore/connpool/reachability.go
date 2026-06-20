package connpool

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	backoffInit    = 2 * time.Second
	backoffMax     = 5 * time.Minute
	tcpPingTimeout = 1 * time.Second
)

// reachState 单个地址的可达性状态。
type reachState struct {
	reachable bool
	lastCheck time.Time
	backoff   time.Duration // 当前退避间隔
}

// reachability 管理所有 hop 地址的可达性状态（TCPing 预探 + 指数退避）。
type reachability struct {
	mu     sync.Mutex
	states map[string]*reachState
	pingFn func(addr string) bool // 可注入的 TCPing 函数
}

func newReachability(pingFn func(addr string) bool) *reachability {
	if pingFn == nil {
		pingFn = defaultTCPPing
	}
	return &reachability{
		states: make(map[string]*reachState),
		pingFn: pingFn,
	}
}

// defaultTCPPing 默认 TCPing 实现：纯 TCP connect 检测端口开放。
func defaultTCPPing(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, tcpPingTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ShouldDial 判断是否应该尝试拨号到指定地址。
// 返回 true 表示可以尝试（可达或退避时间已到需要重试）。
// 返回 false 表示仍在退避期内，不应拨号。
func (r *reachability) ShouldDial(addr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, ok := r.states[addr]
	if !ok {
		// 首次见到此地址，默认允许
		return true
	}
	if st.reachable {
		return true
	}
	// 不可达——检查退避是否已过期
	return time.Since(st.lastCheck) >= st.backoff
}

// CheckAndDial 执行 TCPing 预探，返回是否可达。
// 可达时重置退避；不可达时指数增长退避。
func (r *reachability) CheckAndDial(addr string) bool {
	ok := r.pingFn(addr)

	r.mu.Lock()
	defer r.mu.Unlock()

	st, exists := r.states[addr]
	if !exists {
		st = &reachState{backoff: backoffInit}
		r.states[addr] = st
	}

	st.lastCheck = time.Now()
	if ok {
		st.reachable = true
		st.backoff = backoffInit
		return true
	}

	// 不可达——指数退避
	st.reachable = false
	st.backoff *= 2
	if st.backoff > backoffMax {
		st.backoff = backoffMax
	}
	slog.Info("connpool: TCPing 不可达，退避",
		"addr", addr, "backoff", st.backoff.String())
	return false
}

// MarkReachable 标记地址为可达（连接成功后调用）。
func (r *reachability) MarkReachable(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[addr]
	if !ok {
		st = &reachState{backoff: backoffInit}
		r.states[addr] = st
	}
	st.reachable = true
	st.backoff = backoffInit
	st.lastCheck = time.Now()
}

// MarkUnreachable 标记地址为不可达（连接失败后调用）。
func (r *reachability) MarkUnreachable(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[addr]
	if !ok {
		st = &reachState{backoff: backoffInit}
		r.states[addr] = st
	}
	st.reachable = false
	st.lastCheck = time.Now()
	st.backoff *= 2
	if st.backoff < backoffInit {
		st.backoff = backoffInit
	}
	if st.backoff > backoffMax {
		st.backoff = backoffMax
	}
}

// IsReachable 查询地址是否可达。
func (r *reachability) IsReachable(addr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[addr]
	if !ok {
		return true // 未知地址默认可达
	}
	return st.reachable
}

// Remove 移除地址的可达性记录。
func (r *reachability) Remove(addr string) {
	r.mu.Lock()
	delete(r.states, addr)
	r.mu.Unlock()
}
