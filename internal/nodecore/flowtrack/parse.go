package flowtrack

import (
	"fmt"
	"net/netip"
)

// ParsePacket 从原始 IP 包中提取五元组 FlowKey 和 TCP 标志位。
// 仅支持 IPv4（version=4）；对 TCP(6) 解析端口和标志，
// UDP(17) 解析端口，ICMP(1) 端口置零。
func ParsePacket(pkt []byte) (FlowKey, TCPFlags, error) {
	var key FlowKey
	var flags TCPFlags

	// 最小 IPv4 头部 20 字节
	if len(pkt) < 20 {
		return key, flags, fmt.Errorf("flowtrack: 包太短: 长度 %d < 20", len(pkt))
	}

	// 检查 IP 版本
	version := pkt[0] >> 4
	if version != 4 {
		return key, flags, fmt.Errorf("flowtrack: 非 IPv4 包: version=%d", version)
	}

	// IHL（IP 头部长度，单位 4 字节）
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 || len(pkt) < ihl {
		return key, flags, fmt.Errorf("flowtrack: IPv4 头部截断: ihl=%d, 包长=%d", ihl, len(pkt))
	}

	// 协议
	key.Proto = pkt[9]

	// 源 IP（byte 12-15）
	key.SrcIP = netip.AddrFrom4([4]byte{pkt[12], pkt[13], pkt[14], pkt[15]})

	// 目的 IP（byte 16-19）
	key.DstIP = netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]})

	// 根据协议解析传输层
	switch key.Proto {
	case 6: // TCP
		// TCP 头部至少需要 20 字节（端口在前 4 字节，标志在 offset 13）
		tcpStart := ihl
		if len(pkt) < tcpStart+14 {
			return key, flags, fmt.Errorf("flowtrack: TCP 头部截断: 需要 %d 字节, 包长=%d", tcpStart+14, len(pkt))
		}
		key.SrcPort = uint16(pkt[tcpStart])<<8 | uint16(pkt[tcpStart+1])
		key.DstPort = uint16(pkt[tcpStart+2])<<8 | uint16(pkt[tcpStart+3])

		// TCP flags 在 offset 13（从 TCP 头部起始）
		flagByte := pkt[tcpStart+13]
		flags.FIN = flagByte&0x01 != 0
		flags.SYN = flagByte&0x02 != 0
		flags.RST = flagByte&0x04 != 0
		flags.ACK = flagByte&0x10 != 0

	case 17: // UDP
		// UDP 头部 8 字节，端口在前 4 字节
		udpStart := ihl
		if len(pkt) < udpStart+4 {
			return key, flags, fmt.Errorf("flowtrack: UDP 头部截断: 需要 %d 字节, 包长=%d", udpStart+4, len(pkt))
		}
		key.SrcPort = uint16(pkt[udpStart])<<8 | uint16(pkt[udpStart+1])
		key.DstPort = uint16(pkt[udpStart+2])<<8 | uint16(pkt[udpStart+3])

	case 1: // ICMP
		// ICMP 无端口概念，保持为 0
		key.SrcPort = 0
		key.DstPort = 0

	default:
		// 其他协议：端口保持为 0
		key.SrcPort = 0
		key.DstPort = 0
	}

	return key, flags, nil
}
