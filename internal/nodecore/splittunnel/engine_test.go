package splittunnel

import (
	"net/netip"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/geoip"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestParseDstIPv4(t *testing.T) {
	pkt := makeIPv4Packet(netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("8.8.8.8"), 6, 12345, 443)
	got := parseDstIPv4(pkt)
	if got != netip.MustParseAddr("8.8.8.8") {
		t.Fatalf("期望 8.8.8.8, got %v", got)
	}
}

func TestParseDstIPv4_TooShort(t *testing.T) {
	got := parseDstIPv4([]byte{0x45, 0x00})
	if got.IsValid() {
		t.Fatal("短包应返回无效地址")
	}
}

func TestExtractConnKey(t *testing.T) {
	pkt := makeIPv4Packet(netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("8.8.8.8"), 6, 12345, 443)
	key := extractConnKey(pkt)
	if key.dstIP != netip.MustParseAddr("8.8.8.8") || key.dstPort != 443 || key.proto != 6 {
		t.Fatalf("key 错误: %+v", key)
	}
}

func TestWrapperDecision(t *testing.T) {
	// 验证 Router 在 wrapper 中被正确调用
	matcher, _ := geoip.LoadBytes(geoip.BuildTestDat(t))
	router := &Router{
		forceDirectIPs: map[netip.Addr]bool{},
		matcher:        matcher,
		rules:          []*genv1.SplitRule{{Match: "geoip:cn", Action: "direct"}},
		defaultAct:     ActionProxy,
	}

	// 1.2.3.4 is CN → direct
	if router.Decide(netip.MustParseAddr("1.2.3.4")) != ActionDirect {
		t.Fatal("CN 应直连")
	}
	// 9.9.9.9 not CN → proxy
	if router.Decide(netip.MustParseAddr("9.9.9.9")) != ActionProxy {
		t.Fatal("非 CN 应代理")
	}
}

// makeIPv4Packet 构造最小 IPv4 包头。
func makeIPv4Packet(src, dst netip.Addr, proto uint8, srcPort, dstPort uint16) []byte {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	pkt[9] = proto
	s4 := src.As4()
	d4 := dst.As4()
	copy(pkt[12:16], s4[:])
	copy(pkt[16:20], d4[:])
	pkt[20] = byte(srcPort >> 8)
	pkt[21] = byte(srcPort)
	pkt[22] = byte(dstPort >> 8)
	pkt[23] = byte(dstPort)
	return pkt
}
