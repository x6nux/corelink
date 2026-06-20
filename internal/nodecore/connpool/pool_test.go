package connpool

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// mockDialer 测试用拨号器：返回 nil 连接，不建立真实网络连接。
func mockDialer(_ context.Context, _ string, _ TransportType) (net.Conn, error) {
	return nil, nil
}

// newTestPool 创建测试用连接池，使用 mockDialer 和较小的配置参数。
func newTestPool(opts ...Option) *Pool {
	cfg := DefaultConfig()
	cfg.MaxFlowsPerConn = 10
	cfg.ScaleThreshold = 0.8
	cfg.MinConnsPerHop = 1
	cfg.MaxConnsPerHop = 4
	cfg.IdleTimeout = 100 * time.Millisecond
	allOpts := append([]Option{
		WithDialer(mockDialer),
		WithTCPPing(func(string) bool { return true }),
	}, opts...)
	return NewPool(cfg, allOpts...)
}

// addTestHop 添加一个测试用下一跳。
func addTestHop(p *Pool, hop, addr string) {
	p.Update(map[string]HopInfo{
		hop: {Addrs: []string{addr}, Transport: TransportUDP},
	})
	// 等待预拨号 goroutine 完成（Update 内部可能异步建连）
	time.Sleep(20 * time.Millisecond)
}

func TestComputeScore(t *testing.T) {
	tests := []struct {
		name string
		m    QualityMetrics
		want float64
	}{
		{
			name: "完美质量",
			m:    QualityMetrics{RTT: 0, LossPermille: 0, Jitter: 0},
			want: 1000,
		},
		{
			name: "典型局域网",
			m:    QualityMetrics{RTT: 5 * time.Millisecond, LossPermille: 0, Jitter: 1 * time.Millisecond},
			want: 1000 - 10 - 0 - 3, // 987
		},
		{
			name: "较差质量",
			m:    QualityMetrics{RTT: 200 * time.Millisecond, LossPermille: 100, Jitter: 50 * time.Millisecond},
			want: 0, // 原始分 -50 → 钳位到 0
		},
		{
			name: "中等质量",
			m:    QualityMetrics{RTT: 50 * time.Millisecond, LossPermille: 10, Jitter: 10 * time.Millisecond},
			want: 1000 - 100 - 50 - 30, // 820
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeScore(tt.m)
			if got != tt.want {
				t.Errorf("ComputeScore(%+v) = %v, 期望 %v", tt.m, got, tt.want)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxFlowsPerConn != 100 {
		t.Errorf("MaxFlowsPerConn = %d, 期望 100", cfg.MaxFlowsPerConn)
	}
	if cfg.ScaleThreshold != 0.8 {
		t.Errorf("ScaleThreshold = %f, 期望 0.8", cfg.ScaleThreshold)
	}
	if cfg.MinConnsPerHop != 1 {
		t.Errorf("MinConnsPerHop = %d, 期望 1", cfg.MinConnsPerHop)
	}
	if cfg.MaxConnsPerHop != 16 {
		t.Errorf("MaxConnsPerHop = %d, 期望 16", cfg.MaxConnsPerHop)
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("IdleTimeout = %v, 期望 5m", cfg.IdleTimeout)
	}
	if cfg.DialTimeout != 1*time.Second {
		t.Errorf("DialTimeout = %v, 期望 1s", cfg.DialTimeout)
	}
}

func TestAcquireCreatesConn(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	conn, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if conn == nil {
		t.Fatal("Acquire 返回 nil 连接")
	}
	if conn.NextHop != "hop-1" {
		t.Errorf("NextHop = %q, 期望 %q", conn.NextHop, "hop-1")
	}
	if conn.Addr != "10.0.0.1:7446" {
		t.Errorf("Addr = %q, 期望 %q", conn.Addr, "10.0.0.1:7446")
	}
	if conn.Transport != TransportUDP {
		t.Errorf("Transport = %q, 期望 %q", conn.Transport, TransportUDP)
	}
	if conn.FlowCount.Load() != 1 {
		t.Errorf("FlowCount = %d, 期望 1", conn.FlowCount.Load())
	}
	if conn.ID == 0 {
		t.Error("连接 ID 不应为 0")
	}
}

func TestAcquireSelectsLeastLoaded(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// 获取第一条连接并添加多条流
	c1, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// c1 目前流数 = 1，手动加到 5
	c1.FlowCount.Add(4) // 总共 5

	// 手动向组中添加一条空连接
	p.mu.RLock()
	g := p.groups["hop-1"]
	p.mu.RUnlock()

	c2, err := p.dialConn(g.info, "hop-1")
	if err != nil {
		t.Fatalf("dialConn 失败: %v", err)
	}
	g.mu.Lock()
	g.conns = append(g.conns, c2)
	g.mu.Unlock()

	// 再次 Acquire，应选择流数更少的 c2
	got, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if got.ID != c2.ID {
		t.Errorf("应选择流数最少的连接(ID=%d)，实际选择了 ID=%d", c2.ID, got.ID)
	}
	if got.FlowCount.Load() != 1 {
		t.Errorf("选中连接 FlowCount = %d, 期望 1", got.FlowCount.Load())
	}
}

func TestReleaseDecrementsFlowCount(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	conn, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if conn.FlowCount.Load() != 1 {
		t.Fatalf("Acquire 后 FlowCount = %d, 期望 1", conn.FlowCount.Load())
	}

	p.Release(conn)
	if conn.FlowCount.Load() != 0 {
		t.Errorf("Release 后 FlowCount = %d, 期望 0", conn.FlowCount.Load())
	}
}

func TestUpdateAddsNewHops(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
		"hop-2": {Addrs: []string{"10.0.0.2:7446"}, Transport: TransportTLS},
	})

	// 应能获取两个跳的连接
	c1, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire hop-1 失败: %v", err)
	}
	if c1.Addr != "10.0.0.1:7446" {
		t.Errorf("hop-1 Addr = %q, 期望 %q", c1.Addr, "10.0.0.1:7446")
	}

	c2, err := p.Acquire("hop-2")
	if err != nil {
		t.Fatalf("Acquire hop-2 失败: %v", err)
	}
	if c2.Transport != TransportTLS {
		t.Errorf("hop-2 Transport = %q, 期望 %q", c2.Transport, TransportTLS)
	}

	// Update 预拨号应创建 MinConnsPerHop 条连接
	if cnt := p.ConnCount("hop-1"); cnt < 1 {
		t.Errorf("hop-1 连接数 = %d, 期望至少 1", cnt)
	}
}

func TestUpdateRemovesOldHops(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	// 先添加两个跳
	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
		"hop-2": {Addrs: []string{"10.0.0.2:7446"}, Transport: TransportTLS},
	})

	// 确认 hop-2 存在
	if cnt := p.ConnCount("hop-2"); cnt < 1 {
		t.Fatalf("hop-2 连接数 = %d, 期望至少 1", cnt)
	}

	// 更新为只保留 hop-1
	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
	})

	// hop-2 应已被移除
	if cnt := p.ConnCount("hop-2"); cnt != 0 {
		t.Errorf("hop-2 移除后连接数 = %d, 期望 0", cnt)
	}

	// hop-2 获取应失败
	_, err := p.Acquire("hop-2")
	if err == nil {
		t.Error("Acquire 已移除的 hop-2 应返回错误")
	}
}

func TestAutoScale(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// 初始应有 MinConnsPerHop (1) 条连接（Update 预拨号）
	initial := p.ConnCount("hop-1")
	if initial < 1 {
		t.Fatalf("初始连接数 = %d, 期望至少 1", initial)
	}

	// 持续 Acquire 直到触发扩容阈值
	// MaxFlowsPerConn=10, ScaleThreshold=0.8 → 8 流触发扩容
	var conns []*Conn
	for range 8 {
		c, err := p.Acquire("hop-1")
		if err != nil {
			t.Fatalf("Acquire 失败: %v", err)
		}
		conns = append(conns, c)
	}

	// 等待异步扩容完成
	time.Sleep(50 * time.Millisecond)

	// 应已扩容（连接数 > 初始）
	after := p.ConnCount("hop-1")
	if after <= initial {
		t.Errorf("扩容后连接数 = %d, 期望大于初始 %d", after, initial)
	}

	// 释放所有流
	for _, c := range conns {
		p.Release(c)
	}
}

func TestConnCount(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	// 不存在的跳返回 0
	if cnt := p.ConnCount("不存在"); cnt != 0 {
		t.Errorf("不存在的跳连接数 = %d, 期望 0", cnt)
	}

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// Update 预拨号 MinConnsPerHop = 1 条
	cnt := p.ConnCount("hop-1")
	if cnt != 1 {
		t.Errorf("初始连接数 = %d, 期望 1", cnt)
	}
}

func TestAcquireUnknownHop(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	_, err := p.Acquire("不存在的跳")
	if err == nil {
		t.Error("Acquire 未知跳应返回错误")
	}
}

func TestConcurrentAcquireRelease(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				c, err := p.Acquire("hop-1")
				if err != nil {
					continue
				}
				// 模拟短暂使用
				p.Release(c)
			}
		}()
	}
	wg.Wait()

	// 所有流应已释放
	p.mu.RLock()
	g := p.groups["hop-1"]
	p.mu.RUnlock()

	g.mu.Lock()
	totalFlows := int32(0)
	for _, c := range g.conns {
		totalFlows += c.FlowCount.Load()
	}
	g.mu.Unlock()

	if totalFlows != 0 {
		t.Errorf("并发完成后总流数 = %d, 期望 0", totalFlows)
	}
}

func TestPoolClose(t *testing.T) {
	p := newTestPool()

	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
		"hop-2": {Addrs: []string{"10.0.0.2:7446"}, Transport: TransportTLS},
	})

	// 获取一些连接
	c1, _ := p.Acquire("hop-1")
	c2, _ := p.Acquire("hop-2")
	_ = c1
	_ = c2

	// 关闭池
	err := p.Close()
	if err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 关闭后所有组应已清空
	p.mu.RLock()
	groupCount := len(p.groups)
	p.mu.RUnlock()
	if groupCount != 0 {
		t.Errorf("Close 后组数 = %d, 期望 0", groupCount)
	}
}

func TestPoolCloseIdempotent(t *testing.T) {
	p := newTestPool()
	addTestHop(p, "hop-1", "10.0.0.1:7446")

	if err := p.Close(); err != nil {
		t.Fatalf("第一次 Close 失败: %v", err)
	}
	// 第二次 Close 不应 panic
	if err := p.Close(); err != nil {
		t.Fatalf("第二次 Close 失败: %v", err)
	}
}

func TestConnClose(t *testing.T) {
	c := &Conn{ID: 1, NextHop: "test"}

	// 首次关闭
	if err := c.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 幂等关闭
	if err := c.Close(); err != nil {
		t.Fatalf("重复 Close 失败: %v", err)
	}

	if !c.isClosed() {
		t.Error("关闭后 isClosed 应返回 true")
	}
}

func TestNilDialerTestMode(t *testing.T) {
	// 不注入 DialFunc，应以测试模式工作（Conn.NetConn 为 nil）
	cfg := DefaultConfig()
	cfg.MinConnsPerHop = 1
	p := NewPool(cfg, WithTCPPing(func(string) bool { return true })) // 无 WithDialer
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	c, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if c.NetConn != nil {
		t.Error("测试模式下 NetConn 应为 nil")
	}
}

func TestUpdatePreDialsMinConns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinConnsPerHop = 3
	cfg.MaxConnsPerHop = 8
	p := NewPool(cfg, WithDialer(mockDialer), WithTCPPing(func(string) bool { return true }))
	defer p.Close()

	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
	})

	// 应预拨号 3 条连接
	cnt := p.ConnCount("hop-1")
	if cnt != 3 {
		t.Errorf("预拨号连接数 = %d, 期望 3", cnt)
	}
}

func TestAutoScaleRespectsMaxConns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFlowsPerConn = 5
	cfg.ScaleThreshold = 0.6 // 3 流触发扩容
	cfg.MinConnsPerHop = 1
	cfg.MaxConnsPerHop = 3
	p := NewPool(cfg, WithDialer(mockDialer), WithTCPPing(func(string) bool { return true }))
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// 循环 Acquire 触发扩容多次
	var allConns []*Conn
	for range 30 {
		c, err := p.Acquire("hop-1")
		if err != nil {
			t.Fatalf("Acquire 失败: %v", err)
		}
		allConns = append(allConns, c)
		time.Sleep(5 * time.Millisecond) // 给异步扩容一点时间
	}

	time.Sleep(50 * time.Millisecond)

	// 不应超过 MaxConnsPerHop
	cnt := p.ConnCount("hop-1")
	if cnt > 3 {
		t.Errorf("连接数 = %d, 超过 MaxConnsPerHop=3", cnt)
	}

	for _, c := range allConns {
		p.Release(c)
	}
}

func TestShrinkOnce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFlowsPerConn = 10
	cfg.MinConnsPerHop = 1
	cfg.MaxConnsPerHop = 8
	cfg.IdleTimeout = 10 * time.Millisecond // 极短超时便于测试
	p := NewPool(cfg, WithDialer(mockDialer), WithTCPPing(func(string) bool { return true }))
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// 手动添加多条空闲连接
	p.mu.RLock()
	g := p.groups["hop-1"]
	p.mu.RUnlock()
	for range 3 {
		c, _ := p.dialConn(g.info, "hop-1")
		g.mu.Lock()
		g.conns = append(g.conns, c)
		g.mu.Unlock()
	}

	// 确认有多条连接
	before := p.ConnCount("hop-1")
	if before < 4 {
		t.Fatalf("添加后连接数 = %d, 期望至少 4", before)
	}

	// 等待 IdleTimeout 到期
	time.Sleep(20 * time.Millisecond)

	// 手动触发缩容
	p.shrinkOnce()

	// 应缩容到 MinConnsPerHop
	after := p.ConnCount("hop-1")
	if after != 1 {
		t.Errorf("缩容后连接数 = %d, 期望 %d (MinConnsPerHop)", after, 1)
	}
}

func TestShrinkKeepsActiveConns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFlowsPerConn = 10
	cfg.MinConnsPerHop = 1
	cfg.MaxConnsPerHop = 8
	cfg.IdleTimeout = 10 * time.Millisecond
	p := NewPool(cfg, WithDialer(mockDialer), WithTCPPing(func(string) bool { return true }))
	defer p.Close()

	addTestHop(p, "hop-1", "10.0.0.1:7446")

	// 获取连接并保持流
	c, err := p.Acquire("hop-1")
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}

	// 添加额外空闲连接
	p.mu.RLock()
	g := p.groups["hop-1"]
	p.mu.RUnlock()
	for range 2 {
		ec, _ := p.dialConn(g.info, "hop-1")
		g.mu.Lock()
		g.conns = append(g.conns, ec)
		g.mu.Unlock()
	}

	time.Sleep(20 * time.Millisecond)
	p.shrinkOnce()

	// 有流的连接不应被回收
	if c.isClosed() {
		t.Error("有活跃流的连接不应被缩容关闭")
	}
	if c.FlowCount.Load() != 1 {
		t.Errorf("活跃连接 FlowCount = %d, 期望 1", c.FlowCount.Load())
	}

	p.Release(c)
}

func TestUpdateChangesHopInfo(t *testing.T) {
	p := newTestPool()
	defer p.Close()

	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:7446"}, Transport: TransportUDP},
	})

	// 更新 HopInfo
	p.Update(map[string]HopInfo{
		"hop-1": {Addrs: []string{"10.0.0.1:8888"}, Transport: TransportTLS},
	})

	// 验证 HopInfo 已更新
	p.mu.RLock()
	g := p.groups["hop-1"]
	p.mu.RUnlock()

	g.mu.Lock()
	info := g.info
	g.mu.Unlock()

	if info.Addr() != "10.0.0.1:8888" {
		t.Errorf("Addr = %q, 期望 %q", info.Addr(), "10.0.0.1:8888")
	}
	if info.Transport != TransportTLS {
		t.Errorf("Transport = %q, 期望 %q", info.Transport, TransportTLS)
	}
}
