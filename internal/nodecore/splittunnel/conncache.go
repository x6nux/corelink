package splittunnel

import (
	"net/netip"
	"sync"
	"time"
)

// Action 表示分流决策动作
type Action uint8

const (
	// ActionDirect 直连(不经过代理)
	ActionDirect Action = iota
	// ActionProxy 走代理隧道
	ActionProxy
)

// connKey 五元组连接标识(源IP、目的IP、源端口、目的端口、协议号)
type connKey struct {
	srcIP, dstIP     netip.Addr
	srcPort, dstPort uint16
	proto            uint8
}

// connEntry 缓存条目(决策动作 + 写入时间戳)
type connEntry struct {
	action Action
	ts     time.Time
}

// connCache 连接级分流缓存,避免每包都做 GeoIP 查询;
// 仅首包需要完整决策,后续包直接命中缓存。
type connCache struct {
	mu    sync.RWMutex
	table map[connKey]connEntry
	ttl   time.Duration
	max   int
}

// newConnCache 创建连接缓存,ttl 为条目过期时长
func newConnCache(ttl time.Duration) *connCache {
	return &connCache{table: make(map[connKey]connEntry), ttl: ttl, max: 100_000}
}

// get 查找缓存;若未命中或已过期返回 false
func (c *connCache) get(key connKey) (Action, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.table[key]
	if !ok || time.Since(e.ts) > c.ttl {
		return 0, false
	}
	return e.action, true
}

// put 写入/更新缓存;若超出容量上限则先淘汰
func (c *connCache) put(key connKey, act Action) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.table) >= c.max {
		c.evict()
	}
	c.table[key] = connEntry{action: act, ts: time.Now()}
}

// evict 两阶段淘汰:先删除已过期条目,若仍超 75% 容量则随机淘汰
func (c *connCache) evict() {
	cutoff := time.Now().Add(-c.ttl)
	for k, v := range c.table {
		if v.ts.Before(cutoff) {
			delete(c.table, k)
		}
	}
	// 随机淘汰至 75% 水位
	target := c.max * 3 / 4
	for k := range c.table {
		if len(c.table) <= target {
			break
		}
		delete(c.table, k)
	}
}
