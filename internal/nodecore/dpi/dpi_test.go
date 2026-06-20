package dpi

import (
	"encoding/binary"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/metadata"
)

// buildClientHello 构造一个包含指定 SNI 的最小 TLS ClientHello 报文，用于测试。
func buildClientHello(t *testing.T, sni string) []byte {
	t.Helper()

	sniName := []byte(sni)
	// SNI extension 数据体：name_type(1) + name_len(2) + name
	sniEntry := make([]byte, 3+len(sniName))
	sniEntry[0] = 0x00 // name_type: host_name
	binary.BigEndian.PutUint16(sniEntry[1:], uint16(len(sniName)))
	copy(sniEntry[3:], sniName)

	// SNI extension 完整体：list_len(2) + entry
	sniExtData := make([]byte, 2+len(sniEntry))
	binary.BigEndian.PutUint16(sniExtData[0:], uint16(len(sniEntry)))
	copy(sniExtData[2:], sniEntry)

	// Extension：type(2) + data_len(2) + data
	extType := []byte{0x00, 0x00} // SNI
	extLen := make([]byte, 2)
	binary.BigEndian.PutUint16(extLen, uint16(len(sniExtData)))
	ext := append(extType, extLen...)
	ext = append(ext, sniExtData...)

	// Extensions 块：total_len(2) + extensions
	extBlock := make([]byte, 2+len(ext))
	binary.BigEndian.PutUint16(extBlock[0:], uint16(len(ext)))
	copy(extBlock[2:], ext)

	// ClientHello body：
	//   Version(2) + Random(32) + SessionIDLen(1) + CipherSuitesLen(2) +
	//   CipherSuites(2) + CompressionLen(1) + Compression(1) + Extensions
	chBody := make([]byte, 0, 2+32+1+2+2+1+1+len(extBlock))
	chBody = append(chBody, 0x03, 0x03)          // TLS 1.2
	chBody = append(chBody, make([]byte, 32)...) // Random
	chBody = append(chBody, 0x00)                // SessionID length = 0
	chBody = append(chBody, 0x00, 0x02)          // CipherSuites length = 2
	chBody = append(chBody, 0x00, 0x2F)          // TLS_RSA_WITH_AES_128_CBC_SHA
	chBody = append(chBody, 0x01, 0x00)          // CompressionMethods: len=1, null
	chBody = append(chBody, extBlock...)

	// Handshake 头：HandshakeType(1) + Length(3)
	hsLen := make([]byte, 3)
	hsLen[0] = byte(len(chBody) >> 16)
	hsLen[1] = byte(len(chBody) >> 8)
	hsLen[2] = byte(len(chBody))
	hs := append([]byte{0x01}, hsLen...)
	hs = append(hs, chBody...)

	// TLS Record 头：ContentType(1) + Version(2) + Length(2)
	recLen := make([]byte, 2)
	binary.BigEndian.PutUint16(recLen, uint16(len(hs)))
	rec := []byte{0x16, 0x03, 0x01}
	rec = append(rec, recLen...)
	rec = append(rec, hs...)

	return rec
}

// ----- Inspect 原有函数测试 -----

func TestInspect_TLS(t *testing.T) {
	payload := buildClientHello(t, "example.com")
	r := Inspect(payload)
	if !r.Done {
		t.Fatal("Done 应为 true")
	}
	if r.Protocol != metadata.ProtocolTLS {
		t.Errorf("Protocol = %q, 期望 tls", r.Protocol)
	}
	if r.Domain != "example.com" {
		t.Errorf("Domain = %q, 期望 example.com", r.Domain)
	}
}

func TestInspect_DNS(t *testing.T) {
	pkt := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm', 0x00, 0x00, 0x01, 0x00, 0x01,
	}
	r := Inspect(pkt)
	if !r.Done {
		t.Fatal("Done 应为 true")
	}
	if r.Protocol != metadata.ProtocolDNS {
		t.Errorf("Protocol = %q, 期望 dns", r.Protocol)
	}
}

func TestInspect_HTTP(t *testing.T) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	r := Inspect(payload)
	if !r.Done {
		t.Fatal("Done 应为 true")
	}
	if r.Protocol != metadata.ProtocolHTTP {
		t.Errorf("Protocol = %q, 期望 http", r.Protocol)
	}
	if r.Domain != "example.com" {
		t.Errorf("Domain = %q, 期望 example.com", r.Domain)
	}
}

func TestInspect_Unknown(t *testing.T) {
	r := Inspect([]byte{0xFF, 0xFE, 0xFD})
	if !r.Done {
		t.Fatal("未知协议 Done 应为 true")
	}
	if r.Protocol != metadata.ProtocolUnknown {
		t.Errorf("Protocol = %q, 期望 unknown", r.Protocol)
	}
}

func TestInspect_Empty(t *testing.T) {
	r := Inspect([]byte{})
	if r.Done {
		t.Error("空 payload Done 应为 false")
	}
}

// ----- InspectCtx 新函数测试 -----

func TestInspectCtx_TLS(t *testing.T) {
	ctx := &metadata.InboundContext{}
	payload := buildClientHello(t, "example.com")
	done := InspectCtx(payload, ctx)
	if !done {
		t.Fatal("should be done")
	}
	if ctx.Protocol != metadata.ProtocolTLS {
		t.Errorf("Protocol = %q, want tls", ctx.Protocol)
	}
	if ctx.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", ctx.Domain)
	}
}

func TestInspectCtx_DNS(t *testing.T) {
	ctx := &metadata.InboundContext{}
	pkt := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm', 0x00, 0x00, 0x01, 0x00, 0x01,
	}
	done := InspectCtx(pkt, ctx)
	if !done {
		t.Fatal("should be done")
	}
	if ctx.Protocol != metadata.ProtocolDNS {
		t.Errorf("Protocol = %q, want dns", ctx.Protocol)
	}
}

func TestInspectCtx_HTTP(t *testing.T) {
	ctx := &metadata.InboundContext{}
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	done := InspectCtx(payload, ctx)
	if !done {
		t.Fatal("should be done")
	}
	if ctx.Protocol != metadata.ProtocolHTTP {
		t.Errorf("Protocol = %q, want http", ctx.Protocol)
	}
	if ctx.Domain != "example.com" {
		t.Errorf("Domain = %q", ctx.Domain)
	}
}

func TestInspectCtx_Unknown(t *testing.T) {
	ctx := &metadata.InboundContext{}
	done := InspectCtx([]byte{0xFF, 0xFE, 0xFD}, ctx)
	if !done {
		t.Fatal("should be done for unknown")
	}
	if ctx.Protocol != metadata.ProtocolUnknown {
		t.Errorf("Protocol = %q, want empty", ctx.Protocol)
	}
}

func TestInspectCtx_Empty(t *testing.T) {
	ctx := &metadata.InboundContext{}
	done := InspectCtx([]byte{}, ctx)
	if done {
		t.Error("should not be done for empty payload")
	}
}
