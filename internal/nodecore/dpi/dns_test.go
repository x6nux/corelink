// internal/nodecore/dpi/dns_test.go
package dpi

import "testing"

func TestSniffDNS_ValidQuery(t *testing.T) {
	pkt := []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // Flags: RD=1, QR=0 (query)
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT=0
		0x00, 0x00, // NSCOUNT=0
		0x00, 0x00, // ARCOUNT=0
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,
		0x00, 0x01, // QTYPE=A
		0x00, 0x01, // QCLASS=IN
	}
	if !SniffDNS(pkt) {
		t.Error("should detect valid DNS query")
	}
}

func TestSniffDNS_Response(t *testing.T) {
	pkt := []byte{
		0x12, 0x34,
		0x81, 0x80, // QR=1
		0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
	}
	if SniffDNS(pkt) {
		t.Error("should not detect DNS response as query")
	}
}

func TestSniffDNS_TooShort(t *testing.T) {
	if SniffDNS([]byte{0x12, 0x34}) {
		t.Error("should reject short packet")
	}
}

func TestSniffDNS_ZeroQuestions(t *testing.T) {
	pkt := []byte{
		0x12, 0x34,
		0x01, 0x00, // QR=0
		0x00, 0x00, // QDCOUNT=0
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	if SniffDNS(pkt) {
		t.Error("should reject zero-question packet")
	}
}

func TestSniffDNS_NotDNS(t *testing.T) {
	if SniffDNS([]byte{0x47, 0x45, 0x54, 0x20, 0x2f}) {
		t.Error("should not detect HTTP as DNS")
	}
}

func TestSniffDNS_EmptyPayload(t *testing.T) {
	if SniffDNS(nil) {
		t.Error("should reject nil payload")
	}
	if SniffDNS([]byte{}) {
		t.Error("should reject empty payload")
	}
}
