package flowtrack

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// buildIPv4Packet 构造一个最小有效的 IPv4 + TCP/UDP 测试包。
// tcpFlags 仅在 proto=6 (TCP) 时使用。
func buildIPv4Packet(proto uint8, srcIP, dstIP string, srcPort, dstPort uint16, tcpFlags byte) []byte {
	src := netip.MustParseAddr(srcIP).As4()
	dst := netip.MustParseAddr(dstIP).As4()

	// IPv4 头部（20 字节，IHL=5）
	ihl := 5
	header := make([]byte, ihl*4)

	// Version(4) + IHL(5)
	header[0] = 0x45
	// Protocol
	header[9] = proto
	// 源 IP
	copy(header[12:16], src[:])
	// 目的 IP
	copy(header[16:20], dst[:])

	var payload []byte

	switch proto {
	case 6: // TCP — 最少 20 字节头部
		payload = make([]byte, 20)
		binary.BigEndian.PutUint16(payload[0:2], srcPort)
		binary.BigEndian.PutUint16(payload[2:4], dstPort)
		// Data offset = 5 (20字节), 高 4 位
		payload[12] = 0x50
		// Flags 在 offset 13
		payload[13] = tcpFlags

	case 17: // UDP — 8 字节头部
		payload = make([]byte, 8)
		binary.BigEndian.PutUint16(payload[0:2], srcPort)
		binary.BigEndian.PutUint16(payload[2:4], dstPort)
		binary.BigEndian.PutUint16(payload[4:6], 8) // Length

	case 1: // ICMP — 最少 8 字节
		payload = make([]byte, 8)
		payload[0] = 8 // Echo Request
	}

	// 填写 Total Length
	totalLen := uint16(len(header) + len(payload))
	binary.BigEndian.PutUint16(header[2:4], totalLen)

	pkt := append(header, payload...)
	return pkt
}

func TestParseIPv4TCP_SYN(t *testing.T) {
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x02) // SYN

	key, flags, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if key.SrcIP != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("源 IP 不匹配: got %v, want 10.0.0.1", key.SrcIP)
	}
	if key.DstIP != netip.MustParseAddr("10.0.0.2") {
		t.Errorf("目的 IP 不匹配: got %v, want 10.0.0.2", key.DstIP)
	}
	if key.Proto != 6 {
		t.Errorf("协议不匹配: got %d, want 6", key.Proto)
	}
	if key.SrcPort != 12345 {
		t.Errorf("源端口不匹配: got %d, want 12345", key.SrcPort)
	}
	if key.DstPort != 80 {
		t.Errorf("目的端口不匹配: got %d, want 80", key.DstPort)
	}
	if !flags.SYN {
		t.Error("SYN 标志应为 true")
	}
	if flags.ACK {
		t.Error("ACK 标志应为 false")
	}
	if flags.FIN {
		t.Error("FIN 标志应为 false")
	}
	if flags.RST {
		t.Error("RST 标志应为 false")
	}
}

func TestParseIPv4TCP_SYNACK(t *testing.T) {
	pkt := buildIPv4Packet(6, "10.0.0.2", "10.0.0.1", 80, 12345, 0x12) // SYN+ACK

	key, flags, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if key.SrcPort != 80 || key.DstPort != 12345 {
		t.Errorf("端口不匹配: src=%d dst=%d", key.SrcPort, key.DstPort)
	}
	if !flags.SYN || !flags.ACK {
		t.Error("SYN+ACK 标志应均为 true")
	}
}

func TestParseIPv4TCP_FIN(t *testing.T) {
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x11) // FIN+ACK

	_, flags, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if !flags.FIN || !flags.ACK {
		t.Error("FIN+ACK 标志应均为 true")
	}
	if flags.SYN || flags.RST {
		t.Error("SYN 和 RST 标志应为 false")
	}
}

func TestParseIPv4TCP_RST(t *testing.T) {
	pkt := buildIPv4Packet(6, "10.0.0.1", "10.0.0.2", 12345, 80, 0x04) // RST

	_, flags, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if !flags.RST {
		t.Error("RST 标志应为 true")
	}
}

func TestParseIPv4UDP(t *testing.T) {
	pkt := buildIPv4Packet(17, "192.168.1.10", "8.8.8.8", 55555, 53, 0)

	key, flags, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if key.Proto != 17 {
		t.Errorf("协议不匹配: got %d, want 17", key.Proto)
	}
	if key.SrcIP != netip.MustParseAddr("192.168.1.10") {
		t.Errorf("源 IP 不匹配: got %v", key.SrcIP)
	}
	if key.DstIP != netip.MustParseAddr("8.8.8.8") {
		t.Errorf("目的 IP 不匹配: got %v", key.DstIP)
	}
	if key.SrcPort != 55555 {
		t.Errorf("源端口不匹配: got %d, want 55555", key.SrcPort)
	}
	if key.DstPort != 53 {
		t.Errorf("目的端口不匹配: got %d, want 53", key.DstPort)
	}
	// UDP 不应有 TCP 标志
	if flags.SYN || flags.ACK || flags.FIN || flags.RST {
		t.Error("UDP 包不应有任何 TCP 标志")
	}
}

func TestParseIPv4ICMP(t *testing.T) {
	pkt := buildIPv4Packet(1, "10.0.0.1", "10.0.0.2", 0, 0, 0)

	key, _, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if key.Proto != 1 {
		t.Errorf("协议不匹配: got %d, want 1", key.Proto)
	}
	if key.SrcPort != 0 || key.DstPort != 0 {
		t.Errorf("ICMP 端口应为 0: src=%d dst=%d", key.SrcPort, key.DstPort)
	}
}

func TestParseTooShort(t *testing.T) {
	tests := []struct {
		name string
		pkt  []byte
	}{
		{"空包", nil},
		{"1字节", []byte{0x45}},
		{"10字节", make([]byte, 10)},
		{"19字节", make([]byte, 19)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParsePacket(tt.pkt)
			if err == nil {
				t.Error("应返回错误")
			}
		})
	}
}

func TestParseNotIPv4(t *testing.T) {
	tests := []struct {
		name    string
		version byte
	}{
		{"IPv6", 6},
		{"版本0", 0},
		{"版本15", 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkt := make([]byte, 40)
			pkt[0] = (tt.version << 4) | 0x05 // version + IHL=5
			_, _, err := ParsePacket(pkt)
			if err == nil {
				t.Errorf("version=%d 应返回错误", tt.version)
			}
		})
	}
}

func TestParseTruncatedTCPHeader(t *testing.T) {
	// 构建一个 IPv4 头部完整但 TCP 头部截断的包
	pkt := make([]byte, 24) // 20 字节 IP + 4 字节（不够 TCP 的 14 字节最小需求）
	pkt[0] = 0x45           // Version=4, IHL=5
	pkt[9] = 6              // TCP
	binary.BigEndian.PutUint16(pkt[2:4], 24)

	_, _, err := ParsePacket(pkt)
	if err == nil {
		t.Error("截断的 TCP 头部应返回错误")
	}
}

func TestParseTruncatedUDPHeader(t *testing.T) {
	// 构建一个 IPv4 头部完整但 UDP 头部截断的包
	pkt := make([]byte, 22) // 20 字节 IP + 2 字节（不够 UDP 的 4 字节最小需求）
	pkt[0] = 0x45           // Version=4, IHL=5
	pkt[9] = 17             // UDP
	binary.BigEndian.PutUint16(pkt[2:4], 22)

	_, _, err := ParsePacket(pkt)
	if err == nil {
		t.Error("截断的 UDP 头部应返回错误")
	}
}

func TestParseIPv4WithOptions(t *testing.T) {
	// IHL=6（24 字节 IP 头部，含 4 字节选项）
	src := netip.MustParseAddr("10.1.1.1").As4()
	dst := netip.MustParseAddr("10.2.2.2").As4()

	header := make([]byte, 24) // IHL=6
	header[0] = 0x46           // Version=4, IHL=6
	header[9] = 17             // UDP
	copy(header[12:16], src[:])
	copy(header[16:20], dst[:])

	// UDP payload
	payload := make([]byte, 8)
	binary.BigEndian.PutUint16(payload[0:2], 1234)
	binary.BigEndian.PutUint16(payload[2:4], 5678)

	totalLen := uint16(len(header) + len(payload))
	binary.BigEndian.PutUint16(header[2:4], totalLen)

	pkt := append(header, payload...)

	key, _, err := ParsePacket(pkt)
	if err != nil {
		t.Fatalf("带选项的 IPv4 解析失败: %v", err)
	}

	if key.SrcPort != 1234 || key.DstPort != 5678 {
		t.Errorf("端口不匹配: src=%d dst=%d", key.SrcPort, key.DstPort)
	}
}
