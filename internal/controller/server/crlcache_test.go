package server_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/server"
)

// TestCRLCache_TTL 验证：首次拉取调底层一次，TTL 内复用缓存（不再调），
// TTL 过期后重新拉取。
func TestCRLCache_TTL(t *testing.T) {
	var calls int32
	now := time.Now()
	clock := func() time.Time { return now }
	src := func(validFor time.Duration) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte("crl-der"), nil
	}

	cache := server.NewCRLCache(src, 100*time.Millisecond)
	cache.SetClock(clock)

	// 首次：拉取一次
	if _, err := cache.Get(); err != nil {
		t.Fatalf("Get#1: %v", err)
	}
	// TTL 内：复用，不再调底层
	if _, err := cache.Get(); err != nil {
		t.Fatalf("Get#2: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("TTL 内应只拉取 1 次，实际 %d", got)
	}

	// 推进时钟越过 TTL → 重新拉取
	now = now.Add(200 * time.Millisecond)
	if _, err := cache.Get(); err != nil {
		t.Fatalf("Get#3: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("TTL 过期后应重新拉取，calls=%d", got)
	}
}
