// Package discovery 提供 ARP/邻居表解析和发现上报。
package discovery

import (
	"bufio"
	"fmt"
	"net/netip"
	"strings"
)

// NeighborEntry 表示一条邻居表条目。
type NeighborEntry struct {
	IP    netip.Addr
	State string // REACHABLE, STALE, DELAY, etc.
}

// Mapping 表示一个发现到的 VIP↔Target 映射。
type Mapping struct {
	TargetIP string
	VIPIP    string
}

// ParseIPNeigh 解析 `ip neigh` 命令的输出。
func ParseIPNeigh(output string) []NeighborEntry {
	var entries []NeighborEntry
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(fields[0])
		if err != nil {
			continue
		}
		state := fields[len(fields)-1]
		entries = append(entries, NeighborEntry{IP: ip, State: state})
	}
	return entries
}

// FilterReachable 过滤出状态为 REACHABLE 或 STALE 的条目。
func FilterReachable(entries []NeighborEntry) []NeighborEntry {
	var out []NeighborEntry
	for _, e := range entries {
		switch strings.ToUpper(e.State) {
		case "REACHABLE", "STALE", "DELAY", "PROBE":
			out = append(out, e)
		}
	}
	return out
}

// FilterByCIDR 过滤出在指定 CIDR 范围内的条目。
func FilterByCIDR(entries []NeighborEntry, cidr string) []NeighborEntry {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil
	}
	var out []NeighborEntry
	for _, e := range entries {
		if prefix.Contains(e.IP) {
			out = append(out, e)
		}
	}
	return out
}

// MapToVIP 将目标 CIDR 内的 IP 映射到 VIP CIDR 内的对应偏移位置。
func MapToVIP(targetIP string, targetCIDR, vipCIDR string) (string, error) {
	tip, err := netip.ParseAddr(targetIP)
	if err != nil {
		return "", err
	}
	tPrefix, err := netip.ParsePrefix(targetCIDR)
	if err != nil {
		return "", err
	}
	vPrefix, err := netip.ParsePrefix(vipCIDR)
	if err != nil {
		return "", err
	}

	if !tip.Is4() {
		return "", fmt.Errorf("仅支持 IPv4")
	}

	tBase := tPrefix.Addr().As4()
	vBase := vPrefix.Addr().As4()
	tIP := tip.As4()

	offset := ipv4Diff(tIP, tBase)
	vipBytes := ipv4Add(vBase, offset)
	vip := netip.AddrFrom4(vipBytes)
	return vip.String(), nil
}

func ipv4Diff(a, b [4]byte) uint32 {
	var av, bv uint32
	for i := 0; i < 4; i++ {
		av = av<<8 | uint32(a[i])
		bv = bv<<8 | uint32(b[i])
	}
	return av - bv
}

func ipv4Add(base [4]byte, offset uint32) [4]byte {
	var bv uint32
	for i := 0; i < 4; i++ {
		bv = bv<<8 | uint32(base[i])
	}
	sum := bv + offset
	return [4]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
}
