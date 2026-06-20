// Package route 实现多层路由引擎：L3 FIB 最长前缀匹配 + L4 端口/协议策略 + L5 SNI/Host 规则。
// 路由优先级：L5 > L4 > L3。引擎使用 atomic.Pointer 实现无锁快照替换，支持并发 Route() 调用。
package route

import "net/netip"

// HopType 表示下一跳类型：直连或经由 relay 中转。
type HopType int

const (
	HopDirect HopType = iota // 直连（点对点 WireGuard）
	HopRelay                 // 经由 relay 中转
)

// Decision 是路由引擎的匹配结果。
type Decision struct {
	NextHop  string  // 下一跳节点标识；空字符串表示无路由
	Via      HopType // 下一跳类型
	RelayID  string  // Via=HopRelay 时的 relay 节点 ID
	Priority int     // 调试用：匹配层级（5=L5, 4=L4, 3=L3）
	Rule     string  // 调试用：匹配的规则名称
}

// FIBEntry 将目的前缀映射到下一跳，构成 L3 转发信息表。
type FIBEntry struct {
	Prefix  netip.Prefix
	NextHop string
	Via     HopType
	RelayID string
}

// L4Rule 是基于目的前缀 + 端口 + 协议的 L4 策略规则。
type L4Rule struct {
	Name      string
	DstPrefix netip.Prefix
	DstPort   uint16 // 0 表示匹配所有端口
	Proto     uint8  // 0 表示匹配所有协议
	NextHop   string
	Via       HopType
	RelayID   string
}

// L5Rule 是基于 SNI / HTTP Host 的 L5 应用层规则。
type L5Rule struct {
	Name        string
	SNIPattern  string // glob 模式，如 "*.openai.com"
	HostPattern string // glob 模式，如 "*.example.com"（HTTP Host）
	NextHop     string
	Via         HopType
	RelayID     string
}

// RouteConfig 是路由引擎的完整配置，包含三层规则。
type RouteConfig struct {
	FIB     []FIBEntry
	L4Rules []L4Rule
	L5Rules []L5Rule
}
