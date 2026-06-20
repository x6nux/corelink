package transport

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestEncodeDecodeStreamFrame(t *testing.T) {
	// IPv4 roundtrip
	dstVIP := netip.MustParseAddr("100.64.0.1")
	var dstRelay uint16 = 42
	var ttl uint8 = 7
	payload := []byte("hello corelink")

	var buf bytes.Buffer
	if err := WriteStreamFrame(&buf, dstVIP, dstRelay, ttl, payload); err != nil {
		t.Fatalf("WriteStreamFrame: %v", err)
	}

	gotVIP, gotRelay, gotTTL, gotPayload, err := ReadStreamFrame(&buf)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestEncodeDecodeStreamFrameIPv6(t *testing.T) {
	dstVIP := netip.MustParseAddr("fd00::1")
	var dstRelay uint16 = 1000
	var ttl uint8 = 64
	payload := []byte("ipv6 test payload")

	var buf bytes.Buffer
	if err := WriteStreamFrame(&buf, dstVIP, dstRelay, ttl, payload); err != nil {
		t.Fatalf("WriteStreamFrame: %v", err)
	}

	gotVIP, gotRelay, gotTTL, gotPayload, err := ReadStreamFrame(&buf)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestEncodeDecodeDatagramFrame(t *testing.T) {
	dstVIP := netip.MustParseAddr("100.64.1.2")
	var dstRelay uint16 = 5
	var ttl uint8 = 3
	payload := []byte("datagram payload")

	data := EncodeDatagram(dstVIP, dstRelay, ttl, payload)

	gotVIP, gotRelay, gotTTL, gotPayload, err := DecodeDatagram(data)
	if err != nil {
		t.Fatalf("DecodeDatagram: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestEncodeDecodeDatagramFrameIPv6(t *testing.T) {
	dstVIP := netip.MustParseAddr("fd00::abcd")
	var dstRelay uint16 = 9999
	var ttl uint8 = 128
	payload := []byte("ipv6 datagram")

	data := EncodeDatagram(dstVIP, dstRelay, ttl, payload)

	gotVIP, gotRelay, gotTTL, gotPayload, err := DecodeDatagram(data)
	if err != nil {
		t.Fatalf("DecodeDatagram: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestKeepaliveFrame(t *testing.T) {
	var seq uint64 = 0x123456789ABCDEF0

	// Keepalive
	var buf bytes.Buffer
	if err := WriteStreamKeepalive(&buf, seq); err != nil {
		t.Fatalf("WriteStreamKeepalive: %v", err)
	}

	_, _, _, payload, err := ReadStreamFrame(&buf)
	if err != nil {
		t.Fatalf("ReadStreamFrame (keepalive): %v", err)
	}

	if len(payload) != 8 {
		t.Fatalf("keepalive payload 长度: got %d, want 8", len(payload))
	}
	gotSeq := binary.BigEndian.Uint64(payload)
	if gotSeq != seq {
		t.Errorf("keepalive seq: got 0x%x, want 0x%x", gotSeq, seq)
	}

	// KeepaliveEcho
	buf.Reset()
	if err := WriteStreamKeepaliveEcho(&buf, seq); err != nil {
		t.Fatalf("WriteStreamKeepaliveEcho: %v", err)
	}

	// 手动读取以验证 flags
	raw2 := buf.Bytes()
	// 4B length + 1B flags ...
	flags := raw2[4]
	if !IsKeepalive(flags) {
		t.Error("KeepaliveEcho 应该设置 keepalive 位")
	}
	if !IsControl(flags) {
		t.Error("KeepaliveEcho 应该设置 control 位")
	}
}

func TestKeepaliveStreamRoundtrip(t *testing.T) {
	var seq uint64 = 42

	var buf bytes.Buffer
	if err := WriteStreamKeepalive(&buf, seq); err != nil {
		t.Fatalf("WriteStreamKeepalive: %v", err)
	}

	// 手动解析验证 flags 和 payload
	raw := buf.Bytes()
	// Length (4B) + flags (1B) + TTL (1B) + VIP (4B IPv4 zero) + relay (2B) + payload (8B)
	// Total after length = 1+1+4+2+8 = 16
	length := binary.BigEndian.Uint32(raw[0:4])
	if length != 16 {
		t.Fatalf("keepalive length: got %d, want 16", length)
	}
	flags := raw[4]
	if flags != FlagKeepalive {
		t.Errorf("keepalive flags: got 0x%02x, want 0x%02x", flags, FlagKeepalive)
	}
}

func TestStreamFrameEmptyPayload(t *testing.T) {
	dstVIP := netip.MustParseAddr("100.64.0.1")
	var buf bytes.Buffer
	if err := WriteStreamFrame(&buf, dstVIP, 0, 0, nil); err != nil {
		t.Fatalf("WriteStreamFrame: %v", err)
	}

	gotVIP, gotRelay, gotTTL, gotPayload, err := ReadStreamFrame(&buf)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != 0 {
		t.Errorf("Relay: got %d, want 0", gotRelay)
	}
	if gotTTL != 0 {
		t.Errorf("TTL: got %d, want 0", gotTTL)
	}
	if len(gotPayload) != 0 {
		t.Errorf("Payload: got %q, want empty", gotPayload)
	}
}

func TestMultipleStreamFrames(t *testing.T) {
	// 验证从同一个 stream 连续读多帧。
	// 注意：需用同一个 bufio.Reader 包装底层 reader，避免 read-ahead 数据丢失。
	var buf bytes.Buffer
	addrs := []netip.Addr{
		netip.MustParseAddr("100.64.0.1"),
		netip.MustParseAddr("fd00::1"),
		netip.MustParseAddr("100.64.0.2"),
	}
	payloads := [][]byte{
		[]byte("frame1"),
		[]byte("frame2-ipv6"),
		[]byte("frame3"),
	}

	for i, addr := range addrs {
		if err := WriteStreamFrame(&buf, addr, uint16(i), uint8(i+1), payloads[i]); err != nil {
			t.Fatalf("WriteStreamFrame[%d]: %v", i, err)
		}
	}

	// 用 bufio.Reader 包装，确保多次 ReadStreamFrame 复用同一个 buffered reader。
	br := bufio.NewReader(&buf)
	for i, addr := range addrs {
		gotVIP, gotRelay, gotTTL, gotPayload, err := ReadStreamFrame(br)
		if err != nil {
			t.Fatalf("ReadStreamFrame[%d]: %v", i, err)
		}
		if gotVIP != addr {
			t.Errorf("[%d] VIP: got %v, want %v", i, gotVIP, addr)
		}
		if gotRelay != uint16(i) {
			t.Errorf("[%d] Relay: got %d, want %d", i, gotRelay, i)
		}
		if gotTTL != uint8(i+1) {
			t.Errorf("[%d] TTL: got %d, want %d", i, gotTTL, i+1)
		}
		if !bytes.Equal(gotPayload, payloads[i]) {
			t.Errorf("[%d] Payload: got %q, want %q", i, gotPayload, payloads[i])
		}
	}
}

func TestFlagHelpers(t *testing.T) {
	if IsKeepalive(0x00) {
		t.Error("0x00 不应是 keepalive")
	}
	if !IsKeepalive(0x02) {
		t.Error("0x02 应是 keepalive")
	}
	if !IsKeepalive(0x06) {
		t.Error("0x06 应是 keepalive")
	}
	if IsControl(0x00) {
		t.Error("0x00 不应是 control")
	}
	if !IsControl(0x04) {
		t.Error("0x04 应是 control")
	}
	if !IsControl(0x06) {
		t.Error("0x06 应是 control")
	}
}

func TestDatagramTooShort(t *testing.T) {
	// 数据不足以解析头部
	_, _, _, _, err := DecodeDatagram([]byte{0x00})
	if err == nil {
		t.Error("期望 DecodeDatagram 对过短数据返回错误")
	}
}

func TestStreamFrameOversize(t *testing.T) {
	// 超过 maxFrameLen
	dstVIP := netip.MustParseAddr("100.64.0.1")
	bigPayload := make([]byte, MaxFrameLen+1)
	var buf bytes.Buffer
	err := WriteStreamFrame(&buf, dstVIP, 0, 0, bigPayload)
	if err == nil {
		t.Error("期望 WriteStreamFrame 对超大 payload 返回错误")
	}
}

func BenchmarkEncodeDecodeStream(b *testing.B) {
	dstVIP := netip.MustParseAddr("100.64.0.1")
	payload := make([]byte, 1400) // 典型 WG 包大小
	var buf bytes.Buffer

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf.Reset()
		_ = WriteStreamFrame(&buf, dstVIP, 42, 7, payload)
		_, _, _, _, _ = ReadStreamFrame(&buf)
	}
}

func BenchmarkEncodeDatagram(b *testing.B) {
	dstVIP := netip.MustParseAddr("100.64.0.1")
	payload := make([]byte, 1400)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		data := EncodeDatagram(dstVIP, 42, 7, payload)
		_, _, _, _, _ = DecodeDatagram(data)
	}
}
