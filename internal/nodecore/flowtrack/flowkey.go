// Package flowtrack 提供五元组流识别、TCP 状态机、分段锁流表与过期 GC。
// 这是数据面管道的第一阶段：TUN → FlowTracker → DPI → Route → ConnPool → Transport。
package flowtrack

import (
	"encoding/binary"
	"hash/fnv"
	"net/netip"
)

// FlowKey 是五元组流标识：源/目的 IP + 协议 + 源/目的端口。
type FlowKey struct {
	SrcIP   netip.Addr
	DstIP   netip.Addr
	Proto   uint8 // TCP=6, UDP=17, ICMP=1
	SrcPort uint16
	DstPort uint16
}

// Hash 使用 FNV-1a 计算 FlowKey 的哈希值，用于分片选择。
func (k FlowKey) Hash() uint64 {
	h := fnv.New64a()

	// 写入源 IP
	src := k.SrcIP.As16()
	h.Write(src[:])

	// 写入目的 IP
	dst := k.DstIP.As16()
	h.Write(dst[:])

	// 写入协议 + 端口（7 字节，打包为紧凑格式）
	var buf [5]byte
	buf[0] = k.Proto
	binary.BigEndian.PutUint16(buf[1:3], k.SrcPort)
	binary.BigEndian.PutUint16(buf[3:5], k.DstPort)
	h.Write(buf[:])

	return h.Sum64()
}
