package flowtrack

import (
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestTrackerNewFlow(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02)

	f, isNew := tr.Track(pkt)
	if f == nil {
		t.Fatal("新流不应为 nil")
	}
	if !isNew {
		t.Error("第一个包应创建新流")
	}
	if f.State != FlowNew {
		t.Errorf("新流状态应为 FlowNew, got %d", f.State)
	}
	if f.Packets != 1 {
		t.Errorf("新流包计数应为 1, got %d", f.Packets)
	}
	if f.Bytes != uint64(len(pkt)) {
		t.Errorf("新流字节数不匹配: got %d, want %d", f.Bytes, len(pkt))
	}
}

func TestTrackerExistingFlow(t *testing.T) {
	tr := NewTracker(DefaultConfig())
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02)

	f1, isNew1 := tr.Track(pkt)
	if !isNew1 {
		t.Fatal("第一个包应创建新流")
	}

	f2, isNew2 := tr.Track(pkt)
	if isNew2 {
		t.Error("第二个包不应创建新流")
	}
	if f2 != f1 {
		t.Error("同一五元组应返回同一 Flow 指针")
	}
	if f2.Packets != 2 {
		t.Errorf("包计数应为 2, got %d", f2.Packets)
	}
}

func TestTrackerDifferentFlows(t *testing.T) {
	tr := NewTracker(DefaultConfig())

	pkt1 := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02)
	pkt2 := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12346, 80, 0x02)
	pkt3 := buildIPv4Packet(17, "10.0.0.1", "10.0.0.2", 12345, 53, 0)

	f1, isNew1 := tr.Track(pkt1)
	f2, isNew2 := tr.Track(pkt2)
	f3, isNew3 := tr.Track(pkt3)

	if !isNew1 || !isNew2 || !isNew3 {
		t.Error("不同五元组应创建不同的新流")
	}
	if f1 == f2 || f1 == f3 || f2 == f3 {
		t.Error("不同五元组应返回不同的 Flow 指针")
	}
	if tr.Count() != 3 {
		t.Errorf("流数量应为 3, got %d", tr.Count())
	}
}

func TestTrackerExpireUDP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UDPTimeout = 10 * time.Millisecond // 极短超时便于测试
	cfg.Shards = 4

	tr := NewTracker(cfg)
	pkt := buildIPv4Packet(17, "10.0.0.1", "10.0.0.2", 55555, 53, 0)

	_, isNew := tr.Track(pkt)
	if !isNew {
		t.Fatal("应创建新流")
	}
	if tr.Count() != 1 {
		t.Fatalf("流数量应为 1, got %d", tr.Count())
	}

	// 等待超时
	time.Sleep(20 * time.Millisecond)
	tr.Expire()

	if tr.Count() != 0 {
		t.Errorf("UDP 流应已过期, 当前流数=%d", tr.Count())
	}
}

func TestTrackerExpireTCP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TCPTimeout = 10 * time.Millisecond
	cfg.Shards = 4

	tr := NewTracker(cfg)
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02)

	_, isNew := tr.Track(pkt)
	if !isNew {
		t.Fatal("应创建新流")
	}

	// 未超时时不应过期
	tr.Expire()
	if tr.Count() != 1 {
		t.Error("TCP 流尚未超时不应被清除")
	}

	// 等待超时
	time.Sleep(20 * time.Millisecond)
	tr.Expire()

	if tr.Count() != 0 {
		t.Errorf("TCP 流应已过期, 当前流数=%d", tr.Count())
	}
}

func TestTrackerExpireICMP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ICMPTimeout = 10 * time.Millisecond
	cfg.Shards = 4

	tr := NewTracker(cfg)
	pkt := buildIPv4Packet(1, "10.0.0.1", "10.0.0.2", 0, 0, 0)

	tr.Track(pkt)
	time.Sleep(20 * time.Millisecond)
	tr.Expire()

	if tr.Count() != 0 {
		t.Errorf("ICMP 流应已过期, 当前流数=%d", tr.Count())
	}
}

func TestTrackerExpireTCPClosing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TCPTimeout = 1 * time.Hour // TCP 正常超时很长
	cfg.Shards = 4

	tr := NewTracker(cfg)
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02)

	f, _ := tr.Track(pkt)

	// 手动设置为 FlowClosing 状态
	f.mu.Lock()
	f.State = FlowClosing
	f.LastSeen = time.Now().Add(-11 * time.Second) // 超过 10 秒 closing timeout
	f.mu.Unlock()

	tr.Expire()

	if tr.Count() != 0 {
		t.Errorf("FlowClosing 超时的流应被清除, 当前流数=%d", tr.Count())
	}
}

func TestTrackerCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Shards = 8
	tr := NewTracker(cfg)

	if tr.Count() != 0 {
		t.Errorf("空流表计数应为 0, got %d", tr.Count())
	}

	// 插入多个不同流
	for i := range 100 {
		port := uint16(10000 + i)
		pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", port, 80, 0x02)
		tr.Track(pkt)
	}

	if tr.Count() != 100 {
		t.Errorf("流数量应为 100, got %d", tr.Count())
	}
}

func TestTrackerMaxFlows(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFlows = 5
	cfg.Shards = 2
	tr := NewTracker(cfg)

	// 填满流表
	for i := range 5 {
		port := uint16(10000 + i)
		pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", port, 80, 0x02)
		f, isNew := tr.Track(pkt)
		if f == nil || !isNew {
			t.Fatalf("第 %d 个流应成功创建", i+1)
		}
	}

	// 超过上限
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 20000, 80, 0x02)
	f, isNew := tr.Track(pkt)
	if f != nil || isNew {
		t.Error("超过 MaxFlows 应返回 (nil, false)")
	}
}

func TestTrackerInvalidPacket(t *testing.T) {
	tr := NewTracker(DefaultConfig())

	// 太短的包
	f, isNew := tr.Track([]byte{0x45, 0x00})
	if f != nil || isNew {
		t.Error("无效包应返回 (nil, false)")
	}
}

func TestTrackerConcurrent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Shards = 16
	tr := NewTracker(cfg)

	const goroutines = 32
	const packetsPerGoroutine = 500

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			// 每个 goroutine 使用不同的源端口范围
			basePort := uint16(10000 + id*packetsPerGoroutine)
			for i := range packetsPerGoroutine {
				port := basePort + uint16(i%100) // 100 个不同流，每个发 5 个包
				pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", port, 80, 0x02)
				tr.Track(pkt)
			}
		}(g)
	}

	wg.Wait()

	count := tr.Count()
	if count <= 0 {
		t.Error("并发 Track 后流数量应 > 0")
	}
	t.Logf("并发完成: %d 个 goroutine, 最终流数=%d", goroutines, count)
}

func TestTrackerConcurrentTrackAndExpire(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UDPTimeout = 5 * time.Millisecond
	cfg.Shards = 8
	tr := NewTracker(cfg)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines + 1) // +1 for expire goroutine

	// 并发 Track
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range 200 {
				port := uint16(10000 + id*200 + i%50)
				pkt := buildIPv4Packet(17, "10.0.0.1", "10.0.0.2", port, 53, 0)
				tr.Track(pkt)
			}
		}(g)
	}

	// 并发 Expire
	go func() {
		defer wg.Done()
		for range 50 {
			tr.Expire()
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
	// 不 panic 即为通过
	t.Logf("并发 Track+Expire 完成, 最终流数=%d", tr.Count())
}

func TestFlowKeyHash(t *testing.T) {
	k1 := FlowKey{
		SrcIP:   netip.MustParseAddr("10.0.0.1"),
		DstIP:   netip.MustParseAddr("10.0.0.2"),
		Proto:   6,
		SrcPort: 12345,
		DstPort: 80,
	}

	k2 := FlowKey{
		SrcIP:   netip.MustParseAddr("10.0.0.1"),
		DstIP:   netip.MustParseAddr("10.0.0.2"),
		Proto:   6,
		SrcPort: 12346, // 不同源端口
		DstPort: 80,
	}

	h1 := k1.Hash()
	h2 := k2.Hash()

	// 相同 key 的 hash 应一致
	if k1.Hash() != h1 {
		t.Error("相同 FlowKey 的 Hash 应稳定")
	}

	// 不同 key 的 hash 应不同（极小概率碰撞）
	if h1 == h2 {
		t.Error("不同 FlowKey 的 Hash 不应相同")
	}
}
