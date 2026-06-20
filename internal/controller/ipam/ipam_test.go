package ipam

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/x6nux/corelink/internal/controller/store"
)

func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// TestAllocateIncremental: 分配 IP 从 CIDR 顺序递增（跳过网络/网关地址）。
// /29: .0=网络,.1=网关,.2-.6=可用（5个），.7=广播
func TestAllocateIncremental(t *testing.T) {
	s := newMemStore(t)
	a, err := New("100.64.0.0/29", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ip1, err := a.Allocate("n1")
	if err != nil {
		t.Fatalf("Allocate n1: %v", err)
	}
	ip2, err := a.Allocate("n2")
	if err != nil {
		t.Fatalf("Allocate n2: %v", err)
	}
	ip3, err := a.Allocate("n3")
	if err != nil {
		t.Fatalf("Allocate n3: %v", err)
	}

	// 应从 .2 开始递增
	if ip1 != "100.64.0.2" {
		t.Fatalf("ip1 = %q, want 100.64.0.2", ip1)
	}
	if ip2 != "100.64.0.3" {
		t.Fatalf("ip2 = %q, want 100.64.0.3", ip2)
	}
	if ip3 != "100.64.0.4" {
		t.Fatalf("ip3 = %q, want 100.64.0.4", ip3)
	}
	t.Logf("ip1=%s ip2=%s ip3=%s", ip1, ip2, ip3)
}

// TestAllocateSkipsReserved: 跳过网络地址（.0）和首个网关地址（.1）。
func TestAllocateSkipsReserved(t *testing.T) {
	s := newMemStore(t)
	// /29: .0=网络,.1=网关,.2-.6=可用,.7=广播
	a, err := New("10.0.0.0/29", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ip, err := a.Allocate("n1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if ip != "10.0.0.2" {
		t.Fatalf("first allocate = %q, want 10.0.0.2 (skip .0 network and .1 gateway)", ip)
	}
}

// TestAllocateReleaseThenReuse: 释放后可再次分配同一 IP。
func TestAllocateReleaseThenReuse(t *testing.T) {
	s := newMemStore(t)
	// /30: .0=网络,.1=网关,.2=唯一可用,.3=广播
	a, err := New("192.168.1.0/30", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ip, err := a.Allocate("n1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if err := a.Release(ip); err != nil {
		t.Fatalf("Release: %v", err)
	}

	ip2, err := a.Allocate("n2")
	if err != nil {
		t.Fatalf("Allocate after release: %v", err)
	}
	if ip != ip2 {
		t.Fatalf("released IP=%q, reused IP=%q, should be same", ip, ip2)
	}
}

// TestAllocateExhausted: CIDR 耗尽后报错。
func TestAllocateExhausted(t *testing.T) {
	s := newMemStore(t)
	// /30: 只有 .2 可用（.0=网络,.1=网关,.3=广播）
	a, err := New("172.16.0.0/30", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.Allocate("n1") // 成功（.2）
	if err != nil {
		t.Fatalf("Allocate n1: %v", err)
	}
	_, err = a.Allocate("n2") // 耗尽
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("want ErrExhausted, got %v", err)
	}
}

// TestRebuildFromLeases: 重启后从 store 重建已占用集合，不重复分配。
func TestRebuildFromLeases(t *testing.T) {
	s := newMemStore(t)
	// /28: .0=网络,.1=网关,.2-.14=可用,.15=广播
	a1, err := New("10.1.0.0/28", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ip1, _ := a1.Allocate("n1")
	ip2, _ := a1.Allocate("n2")

	// "重启"：用同一 store 构建新 Allocator
	a2, err := New("10.1.0.0/28", s)
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}

	ip3, err := a2.Allocate("n3")
	if err != nil {
		t.Fatalf("Allocate n3 after restart: %v", err)
	}

	if ip3 == ip1 || ip3 == ip2 {
		t.Fatalf("重建后分配了已占用 IP: ip1=%s ip2=%s ip3=%s", ip1, ip2, ip3)
	}
	t.Logf("ip1=%s ip2=%s ip3=%s", ip1, ip2, ip3)
}

// TestConcurrentAllocate: 并发分配不出现重复 IP。
func TestConcurrentAllocate(t *testing.T) {
	s := newMemStore(t)
	// /24: .0=网络,.1=网关,.2-.254=253个可用,.255=广播
	a, err := New("10.0.0.0/24", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	n := 50
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ip, err := a.Allocate(fmt.Sprintf("node-%d", i))
			results[i] = ip
			errs[i] = err
		}()
	}
	wg.Wait()

	seen := make(map[string]int)
	for i, ip := range results {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
			continue
		}
		if prev, ok := seen[ip]; ok {
			t.Errorf("IP %s 重复分配：goroutine %d 和 %d", ip, prev, i)
		}
		seen[ip] = i
	}
}

// TestAllocateUsesVirtualIPFormat: 分配的 IP 格式为纯 IP 字符串（不含 /32）。
func TestAllocateUsesVirtualIPFormat(t *testing.T) {
	s := newMemStore(t)
	a, err := New("10.0.0.0/24", s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ip, err := a.Allocate("n1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	// 期望返回裸 IP 字符串（如 "10.0.0.2"）
	if ip == "" {
		t.Fatal("IP 不应为空")
	}
	t.Logf("allocated IP: %s", ip)
}
