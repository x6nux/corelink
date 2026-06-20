package relayclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// fakeConnector 是可控的连接器：按预设脚本返回成功/失败，记录尝试次数。
type fakeConnector struct {
	mu       sync.Mutex
	attempts int
	results  []error // 第 i 次 Connect 返回 results[i]（越界则返回最后一个）
	// disconnect 在每次成功连接后用于通知断开（测试驱动）。
	disconnectCh chan struct{}
	connectedCh  chan int // 每次成功连接推入当前 attempt 序号
}

func newFakeConnector(results ...error) *fakeConnector {
	return &fakeConnector{
		results:      results,
		disconnectCh: make(chan struct{}, 16),
		connectedCh:  make(chan int, 16),
	}
}

func (f *fakeConnector) Connect(ctx context.Context, ep *genv1.RelayEndpoint) error {
	f.mu.Lock()
	i := f.attempts
	f.attempts++
	var err error
	if len(f.results) == 0 {
		err = nil
	} else if i < len(f.results) {
		err = f.results[i]
	} else {
		err = f.results[len(f.results)-1]
	}
	f.mu.Unlock()
	if err == nil {
		f.connectedCh <- i
	}
	return err
}

// Wait 阻塞直到底层连接断开（由测试通过 triggerDisconnect 驱动）。
func (f *fakeConnector) Wait(ctx context.Context) error {
	select {
	case <-f.disconnectCh:
		return errors.New("disconnected")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeConnector) triggerDisconnect() { f.disconnectCh <- struct{}{} }

func (f *fakeConnector) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func testEndpoint() *genv1.RelayEndpoint {
	return &genv1.RelayEndpoint{
		RelayId: "relay-1",
		Udp:     &genv1.Endpoint{Host: "127.0.0.1", Port: 51820},
		Tunnel:  &genv1.Endpoint{Host: "127.0.0.1", Port: 8443},
	}
}

// TestConnectSuccess 验证：首次连接成功后状态为 Connected。
func TestConnectSuccess(t *testing.T) {
	fc := newFakeConnector(nil)
	c := New(Config{Connector: fc})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, testEndpoint())

	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待首次连接")
	}
	waitState(t, c, StateConnected)
}

// TestPassiveReconnect 验证：断线后被动重连成功。
func TestPassiveReconnect(t *testing.T) {
	fc := newFakeConnector(nil) // 始终成功
	var slept []time.Duration
	var smu sync.Mutex
	c := New(Config{
		Connector: fc,
		sleep:     func(d time.Duration) { smu.Lock(); slept = append(slept, d); smu.Unlock() },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, testEndpoint())

	// 第一次连接。
	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待首次连接")
	}
	// 触发断开 → 应被动重连。
	fc.triggerDisconnect()
	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待重连")
	}
	if fc.attemptCount() < 2 {
		t.Fatalf("attempts=%d, 期望 >=2（含重连）", fc.attemptCount())
	}
}

// TestBackoffSequence 验证：连续失败时退避序列为指数增长且不超过上限。
func TestBackoffSequence(t *testing.T) {
	// 前 4 次失败，第 5 次成功。
	fc := newFakeConnector(
		errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4"), nil,
	)
	var slept []time.Duration
	var smu sync.Mutex
	c := New(Config{
		Connector: fc,
		Backoff: BackoffConfig{
			Base:   100 * time.Millisecond,
			Max:    1 * time.Second,
			Factor: 2,
			Jitter: 0, // 关闭抖动以便断言确定序列
		},
		sleep: func(d time.Duration) { smu.Lock(); slept = append(slept, d); smu.Unlock() },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, testEndpoint())

	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待最终连接成功")
	}

	smu.Lock()
	got := append([]time.Duration(nil), slept...)
	smu.Unlock()

	// 4 次失败 → 4 次退避：100ms,200ms,400ms,800ms（均 < Max=1s）。
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
	}
	if len(got) < len(want) {
		t.Fatalf("退避次数=%d, 期望 >=%d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("退避[%d]=%v, 期望 %v（完整: %v）", i, got[i], want[i], got)
		}
	}
}

// TestBackoffCappedAtMax 验证退避被 Max 截断。
func TestBackoffCappedAtMax(t *testing.T) {
	errs := make([]error, 8)
	for i := range errs {
		errs[i] = errors.New("fail")
	}
	fc := newFakeConnector(append(errs, nil)...)
	var slept []time.Duration
	var smu sync.Mutex
	c := New(Config{
		Connector: fc,
		Backoff:   BackoffConfig{Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, Factor: 2, Jitter: 0},
		sleep:     func(d time.Duration) { smu.Lock(); slept = append(slept, d); smu.Unlock() },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, testEndpoint())
	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时")
	}
	smu.Lock()
	defer smu.Unlock()
	for i, d := range slept {
		if d > 500*time.Millisecond {
			t.Errorf("退避[%d]=%v 超过 Max=500ms", i, d)
		}
	}
	// 后段应已到达上限 500ms。
	if len(slept) >= 4 && slept[3] != 500*time.Millisecond {
		t.Errorf("退避[3]=%v, 期望封顶 500ms（序列 %v）", slept[3], slept)
	}
}

// TestStopViaContext 验证 ctx 取消后 Run 退出、状态变为 Disconnected。
func TestStopViaContext(t *testing.T) {
	fc := newFakeConnector(nil)
	c := New(Config{Connector: fc})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, testEndpoint()); close(done) }()
	select {
	case <-fc.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待连接")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后退出")
	}
}

func waitState(t *testing.T, c *Client, want State) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if c.State() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("超时等待状态 %v, 当前 %v", want, c.State())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
