// Package fib 提供转发面的核心组件：FIB 路由查找、IP TTL 处理、IP 头部解析。
package fib

import (
	"errors"
	"net/netip"
	"sync"
)

// ErrTTLExpired 表示 IP 包的 TTL 已耗尽，不应继续转发。
var ErrTTLExpired = errors.New("fib: TTL expired")

// NextHop 描述一条转发路径的下一跳信息。
type NextHop struct {
	PeerID    string // 目标 relay / node 的标识
	Weight    uint32 // ECMP 权重
	IngressID string // 入口标识（可选）
}

// FIB (Forwarding Information Base) 维护 prefix -> next-hops 的映射表。
// 线程安全，适合并发读写。
type FIB struct {
	mu      sync.RWMutex
	entries map[netip.Prefix][]NextHop
}

// NewFIB 创建一个空的 FIB 实例。
func NewFIB() *FIB {
	return &FIB{
		entries: make(map[netip.Prefix][]NextHop),
	}
}

// Insert 向 FIB 插入或覆盖一条前缀路由。
func (f *FIB) Insert(prefix netip.Prefix, nhs []NextHop) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 存储副本，避免外部修改
	cp := make([]NextHop, len(nhs))
	copy(cp, nhs)
	f.entries[prefix] = cp
}

// Remove 从 FIB 删除指定前缀的路由条目。
func (f *FIB) Remove(prefix netip.Prefix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, prefix)
}

// Lookup 根据目标地址在 FIB 中查找匹配的下一跳。
// 采用最长前缀匹配（LPM）策略。
func (f *FIB) Lookup(addr netip.Addr) ([]NextHop, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	bestLen := -1
	var bestHops []NextHop

	for prefix, nhs := range f.entries {
		if prefix.Contains(addr) {
			if prefix.Bits() > bestLen {
				bestLen = prefix.Bits()
				bestHops = nhs
			}
		}
	}

	if bestLen < 0 {
		return nil, false
	}
	return bestHops, true
}

// DecrementTTL 就地递减 IPv4 包的 TTL 字段并增量更新头部校验和（RFC 1624）。
// 如果 TTL <= 1 则返回 ErrTTLExpired，不修改包内容。
func DecrementTTL(pkt []byte) error {
	if len(pkt) < 20 {
		return errors.New("fib: packet too short for IPv4 header")
	}

	ttl := pkt[8]
	if ttl <= 1 {
		return ErrTTLExpired
	}

	// 递减 TTL
	pkt[8] = ttl - 1

	// 增量校验和更新（RFC 1624）
	// TTL 位于 IPv4 头部 byte 8（word 4 的高字节），减 1 等效于原值加 0x0100。
	// 新校验和 = ~(~旧校验和 + 0x0100)
	oldCheck := uint32(pkt[10])<<8 | uint32(pkt[11])
	// ~旧校验和 即 one's complement 取反
	newSum := (0xFFFF ^ oldCheck) + 0x0100
	// 处理进位折叠
	for newSum > 0xFFFF {
		newSum = (newSum & 0xFFFF) + (newSum >> 16)
	}
	// 再取反得到新校验和
	newCheck := 0xFFFF ^ newSum
	pkt[10] = byte(newCheck >> 8)
	pkt[11] = byte(newCheck)

	return nil
}

// ExtractDstIP 从 IP 包头提取目标地址。
// 支持 IPv4（version=4）和 IPv6（version=6）。
func ExtractDstIP(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 1 {
		return netip.Addr{}, false
	}

	version := pkt[0] >> 4

	switch version {
	case 4:
		// IPv4：目标地址在 byte 16-19
		if len(pkt) < 20 {
			return netip.Addr{}, false
		}
		addr := netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]})
		return addr, true

	case 6:
		// IPv6：目标地址在 byte 24-39
		if len(pkt) < 40 {
			return netip.Addr{}, false
		}
		var b [16]byte
		copy(b[:], pkt[24:40])
		addr := netip.AddrFrom16(b)
		return addr, true

	default:
		return netip.Addr{}, false
	}
}
