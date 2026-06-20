package sync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── TestMultiEndpoint_ThreeEndpoints ────────────────────────────────────────
// 3 端点 priority 0/1/10：
//   - 0 断 → 自动切 1 → 信号继续
//   - 0 恢复 → 迟滞切回 0
func TestMultiEndpoint_ThreeEndpoints(t *testing.T) {
	t.Parallel()

	ep1Ch := newFakeChannel(8)
	ep10Ch := newFakeChannel(8)

	// ep0 工厂：初始可用，后续通过 available 控制
	var ep0Available atomic.Bool
	ep0Available.Store(true)
	var ep0Mu sync.Mutex
	var latestEp0 *fakeChannel

	ep0Fn := func(ctx context.Context) (notifChannel, error) {
		if !ep0Available.Load() {
			return nil, fmt.Errorf("ep0 不可用")
		}
		ch := newFakeChannel(8)
		ep0Mu.Lock()
		latestEp0 = ch
		ep0Mu.Unlock()
		return ch, nil
	}
	ep1Fn := func(ctx context.Context) (notifChannel, error) {
		return ep1Ch, nil
	}
	ep10Fn := func(ctx context.Context) (notifChannel, error) {
		return ep10Ch, nil
	}

	cfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 100 * time.Millisecond,
	}

	// 用兼容构造先创建，然后添加第三端点
	fm := newFailoverManager(ep0Fn, ep1Fn, cfg)
	fm.AddEndpoint(EndpointConfig{
		ID:       "ep10",
		Priority: 10,
		Factory:  ep10Fn,
	})

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 等待连接 ep0
	time.Sleep(50 * time.Millisecond)

	// 初始连接的是 ep0（工厂返回的新通道），通过 latestEp0 推信号
	ep0Mu.Lock()
	le0 := latestEp0
	ep0Mu.Unlock()
	if le0 == nil {
		t.Fatal("ep0 未被连接")
	}
	le0.Push(Signal{Changed: true, Generation: 1})
	sig := waitSig(t, fm.Signals(), 2*time.Second, "ep0 首个信号")
	if sig.Generation != 1 {
		t.Errorf("ep0 信号 generation=%d，期望 1", sig.Generation)
	}

	// 断开 ep0：标记不可用 + 关闭当前通道
	ep0Available.Store(false)
	le0.Close()

	// 等切到 ep1
	time.Sleep(100 * time.Millisecond)

	ep1Ch.Push(Signal{Changed: true, Generation: 2})
	sig = waitSig(t, fm.Signals(), 2*time.Second, "ep1 信号")
	if sig.Generation != 2 {
		t.Errorf("ep1 信号 generation=%d，期望 2", sig.Generation)
	}

	// 恢复 ep0
	ep0Available.Store(true)

	// 等待迟滞切回（retryInterval=30ms + switchBackAfter=100ms，给足余量）
	time.Sleep(400 * time.Millisecond)

	// 切回后通过 latestEp0 推信号
	ep0Mu.Lock()
	le0 = latestEp0
	ep0Mu.Unlock()
	if le0 != nil {
		le0.Push(Signal{Changed: true, Generation: 3})
	}
	sig = waitSig(t, fm.Signals(), 2*time.Second, "ep0 切回信号")
	if sig.Generation != 3 {
		t.Errorf("ep0 切回后信号 generation=%d，期望 3", sig.Generation)
	}

	cancel()
}

// ─── TestMultiEndpoint_AddEndpointRuntime ────────────────────────────────────
// 运行中 AddEndpoint(priority=10)，ep0 和 ep1 都断 → 切到 ep10。
func TestMultiEndpoint_AddEndpointRuntime(t *testing.T) {
	t.Parallel()

	var ep0Available atomic.Bool
	ep0Available.Store(true)
	ep0Ch := newFakeChannel(8)
	var ep0Mu sync.Mutex
	var latestEp0 *fakeChannel

	ep0Fn := func(ctx context.Context) (notifChannel, error) {
		if !ep0Available.Load() {
			return nil, fmt.Errorf("ep0 不可用")
		}
		ch := newFakeChannel(8)
		ep0Mu.Lock()
		latestEp0 = ch
		ep0Mu.Unlock()
		return ch, nil
	}

	var ep1Available atomic.Bool
	ep1Available.Store(true)
	ep1Fn := func(ctx context.Context) (notifChannel, error) {
		if !ep1Available.Load() {
			return nil, fmt.Errorf("ep1 不可用")
		}
		return ep0Ch, nil // 复用（不重要，反正会断）
	}

	cfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 100 * time.Millisecond,
	}

	fm := newFailoverManager(ep0Fn, ep1Fn, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 等待连接 ep0
	time.Sleep(50 * time.Millisecond)

	ep0Mu.Lock()
	le0 := latestEp0
	ep0Mu.Unlock()
	if le0 != nil {
		le0.Push(Signal{Changed: true, Generation: 1})
		sig := waitSig(t, fm.Signals(), 2*time.Second, "ep0 信号")
		if sig.Generation != 1 {
			t.Errorf("ep0 信号 generation=%d，期望 1", sig.Generation)
		}
	}

	// 断开 ep0 和 ep1
	ep0Available.Store(false)
	ep1Available.Store(false)
	if le0 != nil {
		le0.Close()
	}

	// 等一会让 fm 发现都连不上
	time.Sleep(100 * time.Millisecond)

	// 运行时添加 ep10
	ep10Ch := newFakeChannel(8)
	fm.AddEndpoint(EndpointConfig{
		ID:       "ep10",
		Priority: 10,
		Factory: func(ctx context.Context) (notifChannel, error) {
			return ep10Ch, nil
		},
	})

	// 等待切到 ep10
	time.Sleep(200 * time.Millisecond)

	ep10Ch.Push(Signal{Changed: true, Generation: 10})
	sig := waitSig(t, fm.Signals(), 2*time.Second, "ep10 信号")
	if sig.Generation != 10 {
		t.Errorf("ep10 信号 generation=%d，期望 10", sig.Generation)
	}

	cancel()
}

// ─── TestMultiEndpoint_BackwardCompat ────────────────────────────────────────
// newFailoverManager(primary, secondary, cfg) → 行为与旧二态机一致。
func TestMultiEndpoint_BackwardCompat(t *testing.T) {
	t.Parallel()

	primaryCh := newFakeChannel(4)
	secondaryCh := newFakeChannel(8)

	var primaryCallCount atomic.Int32
	primaryFn := func(ctx context.Context) (notifChannel, error) {
		cnt := primaryCallCount.Add(1)
		if cnt == 1 {
			return primaryCh, nil
		}
		// 后续重试：返回永不断的通道
		return newFakeChannel(4), nil
	}
	secondaryFn := func(ctx context.Context) (notifChannel, error) {
		return secondaryCh, nil
	}

	fCfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 500 * time.Millisecond,
	}

	fm := newFailoverManager(primaryFn, secondaryFn, fCfg)

	// 验证内部结构：2 个端点
	fm.mu.Lock()
	if len(fm.endpoints) != 2 {
		t.Fatalf("endpoints 数量=%d，期望 2", len(fm.endpoints))
	}
	if fm.endpoints[0].ID != "primary" || fm.endpoints[0].Priority != 0 {
		t.Errorf("endpoints[0] = %+v，期望 primary/0", fm.endpoints[0])
	}
	if fm.endpoints[1].ID != "secondary" || fm.endpoints[1].Priority != 1 {
		t.Errorf("endpoints[1] = %+v，期望 secondary/1", fm.endpoints[1])
	}
	fm.mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 让 failover 启动并连接主通道
	time.Sleep(30 * time.Millisecond)

	// 通过主通道推一个信号
	primaryCh.Push(Signal{Changed: true, Generation: 1})
	sig1 := waitSig(t, fm.Signals(), 2*time.Second, "主通道信号")
	if sig1.Generation != 1 {
		t.Errorf("主通道信号 generation=%d，期望 1", sig1.Generation)
	}

	// 断开主通道
	primaryCh.Close()

	// 等切换到备通道
	time.Sleep(150 * time.Millisecond)

	// 通过备通道推信号
	secondaryCh.Push(Signal{Changed: true, Generation: 2})
	sig2 := waitSig(t, fm.Signals(), 2*time.Second, "备通道信号")
	if sig2.Generation != 2 {
		t.Errorf("备通道信号 generation=%d，期望 2", sig2.Generation)
	}

	cancel()
}

// ─── TestMultiEndpoint_SkipToLowerPriority ───────────────────────────────────
// ep0 和 ep1 均断 → 直接跳到 ep10（不卡在中间）。
func TestMultiEndpoint_SkipToLowerPriority(t *testing.T) {
	t.Parallel()

	ep0Fn := func(ctx context.Context) (notifChannel, error) {
		return nil, fmt.Errorf("ep0 不可用")
	}
	ep1Fn := func(ctx context.Context) (notifChannel, error) {
		return nil, fmt.Errorf("ep1 不可用")
	}
	ep10Ch := newFakeChannel(8)
	ep10Fn := func(ctx context.Context) (notifChannel, error) {
		return ep10Ch, nil
	}

	cfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 100 * time.Millisecond,
	}

	fm := newFailoverManager(ep0Fn, ep1Fn, cfg)
	fm.AddEndpoint(EndpointConfig{ID: "ep10", Priority: 10, Factory: ep10Fn})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 等待切到 ep10
	time.Sleep(200 * time.Millisecond)

	ep10Ch.Push(Signal{Changed: true, Generation: 42})
	sig := waitSig(t, fm.Signals(), 2*time.Second, "ep10 信号")
	if sig.Generation != 42 {
		t.Errorf("ep10 信号 generation=%d，期望 42", sig.Generation)
	}

	cancel()
}

// ─── TestMultiEndpoint_HysteresisFromMiddle ──────────────────────────────────
// 活跃在 ep1 时，后台检测 ep0 恢复，经迟滞窗口后切回 ep0。
func TestMultiEndpoint_HysteresisFromMiddle(t *testing.T) {
	t.Parallel()

	var ep0Available atomic.Bool
	ep0Available.Store(false) // 初始不可用

	var ep0Mu sync.Mutex
	var latestEp0 *fakeChannel

	ep0Fn := func(ctx context.Context) (notifChannel, error) {
		if !ep0Available.Load() {
			return nil, fmt.Errorf("ep0 不可用")
		}
		ch := newFakeChannel(8)
		ep0Mu.Lock()
		latestEp0 = ch
		ep0Mu.Unlock()
		return ch, nil
	}

	ep1Ch := newFakeChannel(8)
	ep1Fn := func(ctx context.Context) (notifChannel, error) {
		return ep1Ch, nil
	}

	cfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 100 * time.Millisecond,
	}

	fm := newFailoverManager(ep0Fn, ep1Fn, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// ep0 不可用 → 切到 ep1
	time.Sleep(100 * time.Millisecond)

	ep1Ch.Push(Signal{Changed: true, Generation: 1})
	sig := waitSig(t, fm.Signals(), 2*time.Second, "ep1 信号")
	if sig.Generation != 1 {
		t.Errorf("ep1 信号 generation=%d，期望 1", sig.Generation)
	}

	// 恢复 ep0
	ep0Available.Store(true)

	// 等迟滞窗口
	time.Sleep(400 * time.Millisecond)

	// 切回 ep0 后推信号
	ep0Mu.Lock()
	le0 := latestEp0
	ep0Mu.Unlock()
	if le0 == nil {
		t.Fatal("ep0 未被重新连接（迟滞切回失败）")
	}
	le0.Push(Signal{Changed: true, Generation: 2})
	sig = waitSig(t, fm.Signals(), 2*time.Second, "ep0 切回信号")
	if sig.Generation != 2 {
		t.Errorf("ep0 切回信号 generation=%d，期望 2", sig.Generation)
	}

	cancel()
}
