package fib

import (
	"net/netip"
	"testing"
)

func TestFIBLookup_ExactMatch(t *testing.T) {
	fib := NewFIB()
	fib.Insert(netip.MustParsePrefix("10.0.0.2/32"), []NextHop{
		{PeerID: "relay-1", Weight: 100},
	})
	nhs, ok := fib.Lookup(netip.MustParseAddr("10.0.0.2"))
	if !ok {
		t.Fatal("should find 10.0.0.2")
	}
	if len(nhs) != 1 || nhs[0].PeerID != "relay-1" {
		t.Errorf("unexpected: %+v", nhs)
	}
}

func TestFIBLookup_Miss(t *testing.T) {
	fib := NewFIB()
	_, ok := fib.Lookup(netip.MustParseAddr("10.0.0.99"))
	if ok {
		t.Error("should miss for unknown IP")
	}
}

func TestFIBLookup_ECMP(t *testing.T) {
	fib := NewFIB()
	fib.Insert(netip.MustParsePrefix("10.0.0.2/32"), []NextHop{
		{PeerID: "relay-1", Weight: 100},
		{PeerID: "relay-2", Weight: 100},
	})
	nhs, ok := fib.Lookup(netip.MustParseAddr("10.0.0.2"))
	if !ok || len(nhs) != 2 {
		t.Fatalf("ECMP should return 2 next-hops, got %d", len(nhs))
	}
}

func TestFIBRemove(t *testing.T) {
	fib := NewFIB()
	p := netip.MustParsePrefix("10.0.0.2/32")
	fib.Insert(p, []NextHop{{PeerID: "relay-1", Weight: 100}})
	fib.Remove(p)
	_, ok := fib.Lookup(netip.MustParseAddr("10.0.0.2"))
	if ok {
		t.Error("should not find removed entry")
	}
}

func TestIPTTLDecrement(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45 // IPv4, IHL=5
	pkt[8] = 10   // TTL=10
	// 设置有效校验和
	pkt[10], pkt[11] = 0, 0
	sum := ipChecksum(pkt[:20])
	pkt[10] = byte(sum >> 8)
	pkt[11] = byte(sum)

	err := DecrementTTL(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if pkt[8] != 9 {
		t.Errorf("TTL: got %d, want 9", pkt[8])
	}
}

func TestIPTTLDecrement_Expired(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45
	pkt[8] = 1
	err := DecrementTTL(pkt)
	if err != ErrTTLExpired {
		t.Errorf("TTL=1 should expire, got %v", err)
	}
}

func TestExtractDstIP_v4(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45
	pkt[16], pkt[17], pkt[18], pkt[19] = 10, 0, 0, 2
	addr, ok := ExtractDstIP(pkt)
	if !ok {
		t.Fatal("should extract IPv4 dst")
	}
	if addr != netip.MustParseAddr("10.0.0.2") {
		t.Errorf("got %s, want 10.0.0.2", addr)
	}
}

func TestExtractDstIP_TooShort(t *testing.T) {
	_, ok := ExtractDstIP(make([]byte, 5))
	if ok {
		t.Error("should fail for short packet")
	}
}

// ipChecksum 辅助函数用于测试
func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i < len(hdr)-1; i += 2 {
		sum += uint32(hdr[i])<<8 | uint32(hdr[i+1])
	}
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}
