// Package metadata 定义数据面每条连接/流的统一元数据结构。
// 该包不依赖任何其他 internal/nodecore/* 包，避免循环导入。
// dpi、route、dataplane 等包均可安全导入本包。
package metadata

import (
	"net/netip"
	"time"
)

// FlowState 表示流的生命周期状态（独立定义，避免导入 flowtrack 产生循环依赖）。
type FlowState int

const (
	FlowNew         FlowState = iota
	FlowEstablished           // 已建立
	FlowClosing               // 正在关闭
	FlowClosed                // 已关闭
)

// Network 常量
const (
	NetworkTCP  = "tcp"
	NetworkUDP  = "udp"
	NetworkICMP = "icmp"
)

// Protocol 常量 — 嗅探/匹配到的应用层协议
const (
	ProtocolUnknown    = ""
	ProtocolDNS        = "dns"
	ProtocolTLS        = "tls"
	ProtocolHTTP       = "http"
	ProtocolQUIC       = "quic"
	ProtocolSSH        = "ssh"
	ProtocolSTUN       = "stun"
	ProtocolWireGuard  = "wireguard"
	ProtocolBitTorrent = "bittorrent"
	ProtocolDTLS       = "dtls"
	ProtocolNTP        = "ntp"
	ProtocolRDP        = "rdp"
)

// InboundContext 是数据面每条连接/流的完整元数据，贯穿 TUN → Sniff → Route → Send 全链路。
type InboundContext struct {
	// 基础网络信息
	Network     string         // "tcp" / "udp" / "icmp"
	IPVersion   uint8          // 4 / 6
	Source      netip.AddrPort // 源地址+端口
	Destination netip.AddrPort // 目的地址+端口

	// 协议嗅探
	Protocol string // 见 Protocol* 常量
	Domain   string // TLS SNI / HTTP Host
	Client   string // 客户端指纹（预留）

	// 路由决策
	NextHop  string // 下一跳节点标识
	Via      string // "direct" / "relay" / "local"
	RelayID  string // 中继节点 ID
	Rule     string // 匹配的路由规则名称
	Priority int    // 规则优先级：L5=5, L4=4, L3=3

	// 流状态
	FlowID    uint64    // 流唯一标识
	FlowState FlowState // 当前流状态
	CreatedAt time.Time // 流创建时间
	LastSeen  time.Time // 最后活跃时间

	// DNS 劫持
	DNSHijacked bool // 是否被 DNS 劫持处理

	// 统计
	Bytes   uint64 // 累计字节数
	Packets uint64 // 累计包数
}
