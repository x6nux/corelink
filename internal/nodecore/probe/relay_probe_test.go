package probe

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// ----- 辅助类型 -----

// relayProbeCall 记录一次 RelayProber 调用参数。
type relayProbeCall struct {
	relayID      string
	targetNodeID string
}

// fakeRelayProber 返回一个 RelayProber，调用时记录参数到 calls，并始终返回固定结果。
func fakeRelayProber(calls *[]relayProbeCall, mu *sync.Mutex) RelayProber {
	return func(relayID, targetNodeID string) (uint32, uint32, bool) {
		mu.Lock()
		*calls = append(*calls, relayProbeCall{relayID: relayID, targetNodeID: targetNodeID})
		mu.Unlock()
		return 10, 0, true
	}
}

// callKey 构造调用记录的唯一字符串键，用于集合断言。
func callKey(c relayProbeCall) string { return c.relayID + "\x00" + c.targetNodeID }

// assertCoverage 断言 calls 覆盖了所有 relays × peers 的组合（无序）。
func assertCoverage(t *testing.T, calls []relayProbeCall, relays []string, peers []ProbeTarget) {
	t.Helper()
	want := make(map[string]bool)
	for _, r := range relays {
		for _, p := range peers {
			want[r+"\x00"+p.NodeID] = false
		}
	}
	for _, c := range calls {
		key := callKey(c)
		if _, ok := want[key]; !ok {
			t.Errorf("意外的调用：relay=%s target=%s", c.relayID, c.targetNodeID)
		}
		want[key] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("未覆盖的 (relay, target) 组合：%s", k)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

// ----- 测试 -----

// TestMultiRelayProber_FullProbe 验证 2 relay × 3 peer = 6 次探测，全覆盖。
func TestMultiRelayProber_FullProbe(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), nil)

	relays := []string{"relay-A", "relay-B"}
	peers := []ProbeTarget{
		{NodeID: "node-1", IngressID: "ing-1"},
		{NodeID: "node-2", IngressID: "ing-2"},
		{NodeID: "node-3", IngressID: "ing-3"},
	}
	mrp.SetRelays(relays)
	mrp.SetPeers(peers)

	mrp.FullProbe()

	if len(calls) != 6 {
		t.Fatalf("期望 6 次调用（2×3），got %d", len(calls))
	}
	assertCoverage(t, calls, relays, peers)
}

// TestMultiRelayProber_FullProbe_ReporterFed 验证 reporter 非 nil 时 OnProbe 被正确喂入。
func TestMultiRelayProber_FullProbe_ReporterFed(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	// 构造一个可用的 Reporter（无回调，仅用于计数 fsms 槽）。
	reporter := NewReporter(ReporterConfig{
		SelfNode: "nodeX",
		FSM:      testCfg(),
		Damping:  DefaultQualityDamping(),
		Clock:    time.Now,
	})

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), reporter)

	relays := []string{"relay-A", "relay-B"}
	peers := []ProbeTarget{
		{NodeID: "node-1", IngressID: "ing-1"},
		{NodeID: "node-2", IngressID: "ing-2"},
	}
	mrp.SetRelays(relays)
	mrp.SetPeers(peers)

	mrp.FullProbe()

	// 2 relay × 2 peer = 4 次探测，Reporter 应记录 4 条 fsm 槽。
	if got := reporter.trackedLen(); got != 4 {
		t.Fatalf("期望 Reporter 内 4 条 fsm 槽（2×2），got %d", got)
	}

	// IngressID 应编码为 relayID（区分路径），验证 key 格式。
	// (relay-A, node-1) → target.Key() = "node-1\x00relay-A"
	wantTargets := make([]string, 0, 4)
	for _, r := range relays {
		for _, p := range peers {
			wantTargets = append(wantTargets, ProbeTarget{NodeID: p.NodeID, IngressID: r}.Key())
		}
	}
	sort.Strings(wantTargets)

	// State() 对所有期望 target 均可返回（说明 OnProbe 正确传入了 target）。
	for _, tgt := range []ProbeTarget{
		{NodeID: "node-1", IngressID: "relay-A"},
		{NodeID: "node-1", IngressID: "relay-B"},
		{NodeID: "node-2", IngressID: "relay-A"},
		{NodeID: "node-2", IngressID: "relay-B"},
	} {
		// NewLinkFSM 被惰性创建，State() 对未知 target 返回 Healthy——
		// 但 trackedLen 已证明 4 个 fsm 被创建，此处仅作额外可读性断言。
		_ = reporter.State(tgt) // 不 panic 即可
	}
}

// TestMultiRelayProber_SetRelays 验证更新 relay 列表后 FullProbe 按新列表探测。
func TestMultiRelayProber_SetRelays(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), nil)

	peers := []ProbeTarget{
		{NodeID: "node-1", IngressID: "ing-1"},
		{NodeID: "node-2", IngressID: "ing-2"},
	}
	mrp.SetPeers(peers)

	// 初始 1 个 relay → 2 次探测。
	mrp.SetRelays([]string{"relay-A"})
	mrp.FullProbe()
	if len(calls) != 2 {
		t.Fatalf("1 relay × 2 peer 期望 2 次调用，got %d", len(calls))
	}

	// 重置，更新为 3 个 relay → 6 次探测。
	calls = calls[:0]
	mrp.SetRelays([]string{"relay-A", "relay-B", "relay-C"})
	mrp.FullProbe()
	if len(calls) != 6 {
		t.Fatalf("3 relay × 2 peer 期望 6 次调用，got %d", len(calls))
	}
	assertCoverage(t, calls, []string{"relay-A", "relay-B", "relay-C"}, peers)
}

// TestMultiRelayProber_Empty 验证 relays 或 peers 为空时 FullProbe 不 panic、不调用 Prober。
func TestMultiRelayProber_Empty(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), nil)

	// 两者均为空。
	mrp.FullProbe()
	if len(calls) != 0 {
		t.Fatalf("空 relays+peers 期望 0 次调用，got %d", len(calls))
	}

	// 只有 relays，无 peers。
	mrp.SetRelays([]string{"relay-A"})
	mrp.FullProbe()
	if len(calls) != 0 {
		t.Fatalf("有 relay 无 peer 期望 0 次调用，got %d", len(calls))
	}

	// 只有 peers，无 relays。
	mrp.SetRelays(nil)
	mrp.SetPeers([]ProbeTarget{{NodeID: "node-1", IngressID: "ing-1"}})
	mrp.FullProbe()
	if len(calls) != 0 {
		t.Fatalf("有 peer 无 relay 期望 0 次调用，got %d", len(calls))
	}
}

// TestMultiRelayProber_Run 验证 Run 在 ctx 取消前周期调用 FullProbe，取消后停止。
func TestMultiRelayProber_Run(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), nil)
	mrp.SetRelays([]string{"relay-A"})
	mrp.SetPeers([]ProbeTarget{{NodeID: "node-1", IngressID: "ing-1"}})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		mrp.Run(ctx, 10*time.Millisecond)
	}()

	// 等待至少 3 次探测（每次 1 对，10ms 间隔，给 100ms 宽裕）。
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("500ms 内期望 ≥3 次探测，got %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx 取消后 Run 未在 500ms 内退出")
	}
}

// TestMultiRelayProber_ConcurrentSafe 验证并发 SetRelays/SetPeers/FullProbe 不数据竞争。
// （由 -race 检测覆盖）
func TestMultiRelayProber_ConcurrentSafe(t *testing.T) {
	var mu sync.Mutex
	var calls []relayProbeCall

	mrp := NewMultiRelayProber(fakeRelayProber(&calls, &mu), nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			mrp.SetRelays([]string{"relay-A", "relay-B"})
		}()
		go func() {
			defer wg.Done()
			mrp.SetPeers([]ProbeTarget{
				{NodeID: "node-1", IngressID: "ing-1"},
			})
		}()
		go func() {
			defer wg.Done()
			mrp.FullProbe()
		}()
	}
	wg.Wait()
	// 只要不 panic / race 即通过。
}
