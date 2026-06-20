// Package ipam 提供虚拟 IP 分配与回收（基于 store.Lease，CIDR 顺序分配）。
package ipam

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/x6nux/corelink/internal/controller/store"
)

// ErrExhausted 虚拟 IP 地址空间已耗尽。
var ErrExhausted = errors.New("ipam: 地址空间已耗尽")

// Allocator 从指定 CIDR 分配/回收 /32 虚拟 IP。
type Allocator struct {
	mu      sync.Mutex
	network *net.IPNet
	used    map[uint32]struct{} // 已占用的主机地址（uint32 big-endian）
	st      *store.Store
}

// New 用给定 CIDR 和 store 创建 Allocator，自动从 store.ListLeases 重建已占用集合。
func New(cidr string, st *store.Store) (*Allocator, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("ipam: 非法 CIDR %q: %w", cidr, err)
	}

	a := &Allocator{
		network: network,
		used:    make(map[uint32]struct{}),
		st:      st,
	}

	// 从 store 重建已占用集合
	leases, err := st.ListLeases()
	if err != nil {
		return nil, fmt.Errorf("ipam: 读取租约失败: %w", err)
	}
	for _, l := range leases {
		ip := net.ParseIP(l.IP).To4()
		if ip == nil {
			continue
		}
		a.used[ipToUint32(ip)] = struct{}{}
	}

	return a, nil
}

// Allocate 为 nodeID 分配一个未占用的 /32 IP，调用 store.AllocateLease 持久化。
// 跳过：网络地址（host bits=0）、首个网关地址（host bits=1）、广播地址（host bits=全1）。
func (a *Allocator) Allocate(nodeID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	netIP := a.network.IP.To4()
	if netIP == nil {
		return "", fmt.Errorf("ipam: 仅支持 IPv4 CIDR")
	}
	networkAddr := ipToUint32(netIP)
	mask := a.network.Mask
	ones, bits := mask.Size()
	hostBits := bits - ones
	if hostBits > 24 {
		return "", fmt.Errorf("ipam: CIDR /%d 地址空间过大（最大支持 /%d）", ones, bits-24)
	}
	totalHosts := uint32(1) << uint(hostBits) // 包含网络/广播
	broadcast := networkAddr + totalHosts - 1

	// 从 +2 开始（跳过网络地址+2 和网关地址+1）
	for offset := uint32(2); offset < totalHosts-1; offset++ {
		candidate := networkAddr + offset
		if candidate == broadcast {
			break
		}
		if _, occupied := a.used[candidate]; occupied {
			continue
		}
		ip := uint32ToIP(candidate)
		ipStr := ip.String()
		// 持久化到 store
		if err := a.st.AllocateLease(ipStr, nodeID); err != nil {
			return "", fmt.Errorf("ipam: 分配租约失败 %s: %w", ipStr, err)
		}
		a.used[candidate] = struct{}{}
		return ipStr, nil
	}

	return "", ErrExhausted
}

// Release 回收 IP，从 store 删除租约并清除内存占用标记。
func (a *Allocator) Release(ip string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return fmt.Errorf("ipam: 非法 IP %q", ip)
	}
	addr := ipToUint32(parsed)
	if err := a.st.ReleaseLease(ip); err != nil {
		return fmt.Errorf("ipam: 释放租约失败: %w", err)
	}
	delete(a.used, addr)
	return nil
}

// ---------- 内部工具 ----------

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return binary.BigEndian.Uint32(ip)
}

func uint32ToIP(n uint32) net.IP {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return net.IP(b)
}
