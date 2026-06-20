package store

import (
	"sync"
	"testing"
)

// ─── BumpGeneration 进阶测试 ─────────────────────────────────────────────────

// TestBumpGenerationMonotonic 验证连续 BumpGeneration 严格递增。
func TestBumpGenerationMonotonic(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "mono", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2/32"}); err != nil {
		t.Fatal(err)
	}
	var prev uint64
	for i := 0; i < 10; i++ {
		gen, err := s.BumpGeneration("mono")
		if err != nil {
			t.Fatal(err)
		}
		if gen <= prev {
			t.Fatalf("generation 不单调: prev=%d, cur=%d", prev, gen)
		}
		prev = gen
	}
}

// TestBumpGenerationIndependent 验证不同节点 generation 互不影响。
func TestBumpGenerationIndependent(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "n1", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2/32"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(&Node{ID: "n2", Role: "node", WGPubKey: "pk2", VirtualIP: "100.64.0.3/32"}); err != nil {
		t.Fatal(err)
	}
	// Bump n1 三次。
	for i := 0; i < 3; i++ {
		if _, err := s.BumpGeneration("n1"); err != nil {
			t.Fatal(err)
		}
	}
	// n2 仍为 0；bump n2 一次应得 1。
	gen, err := s.BumpGeneration("n2")
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 {
		t.Errorf("n2 generation = %d, 期望 1（n1 的 bump 不应影响 n2）", gen)
	}
}

// TestBumpGenerationConcurrent 并发 bump 同一节点，结果应无重复且单调。
func TestBumpGenerationConcurrent(t *testing.T) {
	s := newMemStore(t)
	// :memory: SQLite 单连接模式保证并发安全。
	sqlDB, err := s.DB().DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := s.CreateNode(&Node{ID: "conc", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2/32"}); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		gens []uint64
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen, err := s.BumpGeneration("conc")
			if err != nil {
				t.Errorf("BumpGeneration: %v", err)
				return
			}
			mu.Lock()
			gens = append(gens, gen)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 验证无重复。
	seen := make(map[uint64]bool)
	for _, g := range gens {
		if seen[g] {
			t.Fatalf("重复 generation: %d", g)
		}
		seen[g] = true
	}
	if len(gens) != n {
		t.Fatalf("收集到 %d 个 generation, 期望 %d", len(gens), n)
	}
}

// TestCreateNodeDuplicateIDFails 验证重复主键创建失败。
func TestCreateNodeDuplicateIDFails(t *testing.T) {
	s := newMemStore(t)
	n := &Node{ID: "dup", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2/32"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	n2 := &Node{ID: "dup", Role: "node", WGPubKey: "pk2", VirtualIP: "100.64.0.3/32"}
	if err := s.CreateNode(n2); err == nil {
		t.Fatal("重复 ID 创建应失败")
	}
}

// TestCreateNodeDuplicateWGPubKeyAllowed 验证 WGPubKey 允许重复（deprecated，新数据面不依赖 WG 公钥）。
func TestCreateNodeDuplicateWGPubKeyAllowed(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "n1", WGPubKey: "same-pk", VirtualIP: "100.64.0.1/32"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(&Node{ID: "n2", WGPubKey: "same-pk", VirtualIP: "100.64.0.2/32"}); err != nil {
		t.Fatalf("重复 WGPubKey 创建应允许（已移除 uniqueIndex）: %v", err)
	}
}

// TestCreateNodeEmptyWGPubKeyAllowed 验证空 WGPubKey 允许创建（新数据面节点无 WG 密钥）。
func TestCreateNodeEmptyWGPubKeyAllowed(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "n1", WGPubKey: "", VirtualIP: "100.64.0.1/32"}); err != nil {
		t.Fatalf("空 WGPubKey 创建应允许: %v", err)
	}
	if err := s.CreateNode(&Node{ID: "n2", WGPubKey: "", VirtualIP: "100.64.0.2/32"}); err != nil {
		t.Fatalf("多个空 WGPubKey 创建应允许: %v", err)
	}
}

// TestCreateNodeDuplicateVirtualIPFails 验证 VirtualIP 唯一索引。
func TestCreateNodeDuplicateVirtualIPFails(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "n1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(&Node{ID: "n2", WGPubKey: "pk2", VirtualIP: "100.64.0.1/32"}); err == nil {
		t.Fatal("重复 VirtualIP 创建应失败")
	}
}

// TestGetNodeReturnsAllFields 验证 GetNode 返回完整字段。
func TestGetNodeReturnsAllFields(t *testing.T) {
	s := newMemStore(t)
	n := &Node{
		ID:        "full",
		Role:      "node",
		Hostname:  "host-1",
		WGPubKey:  "pk-full",
		VirtualIP: "100.64.0.99/32",
		User:      "bob",
	}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode("full")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "node" || got.Hostname != "host-1" || got.User != "bob" {
		t.Errorf("字段不完整: %+v", got)
	}
	if got.VirtualIP != "100.64.0.99/32" || got.WGPubKey != "pk-full" {
		t.Errorf("关键字段错误: %+v", got)
	}
}

// TestAllocateAndReleaseLeaseRoundTrip 验证租约分配→释放→重分配。
func TestAllocateAndReleaseLeaseRoundTrip(t *testing.T) {
	s := newMemStore(t)
	if err := s.AllocateLease("100.64.1.1", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLease("100.64.1.1"); err != nil {
		t.Fatal(err)
	}
	// 释放后可重新分配。
	if err := s.AllocateLease("100.64.1.1", "n2"); err != nil {
		t.Fatalf("释放后重新分配失败: %v", err)
	}
}
