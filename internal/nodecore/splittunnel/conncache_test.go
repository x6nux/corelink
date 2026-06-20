package splittunnel

import (
	"net/netip"
	"testing"
	"time"
)

func TestConnCache_HitMiss(t *testing.T) {
	c := newConnCache(5 * time.Minute)
	key := connKey{
		srcIP: netip.MustParseAddr("10.0.0.1"), dstIP: netip.MustParseAddr("8.8.8.8"),
		srcPort: 12345, dstPort: 443, proto: 6,
	}
	if _, ok := c.get(key); ok {
		t.Fatal("期望未命中")
	}
	c.put(key, ActionProxy)
	act, ok := c.get(key)
	if !ok || act != ActionProxy {
		t.Fatalf("期望命中 proxy, got ok=%v act=%v", ok, act)
	}
}

func TestConnCache_Expire(t *testing.T) {
	c := newConnCache(1 * time.Millisecond)
	key := connKey{dstIP: netip.MustParseAddr("1.1.1.1"), proto: 6}
	c.put(key, ActionDirect)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.get(key); ok {
		t.Fatal("期望过期后未命中")
	}
}

func TestConnCache_Evict(t *testing.T) {
	c := newConnCache(time.Hour)
	c.max = 10
	for i := 0; i < 15; i++ {
		key := connKey{dstPort: uint16(i), proto: 6}
		c.put(key, ActionDirect)
	}
	if len(c.table) > 10 {
		t.Fatalf("淘汰后期望 <=10, got %d", len(c.table))
	}
}
