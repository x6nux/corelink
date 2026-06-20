package ingress

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// decodeXorMappedAddress is a minimal client-side decoder used only by the test
// to verify the reflector's response. It mirrors the node-side client in
// internal/nodecore/ingress/stun.go (XOR-MAPPED-ADDRESS, IPv4).
func decodeXorMappedAddress(t *testing.T, msg []byte, txid [12]byte) (net.IP, uint16) {
	t.Helper()
	if len(msg) < 20 {
		t.Fatalf("response too short: %d bytes", len(msg))
	}
	if binary.BigEndian.Uint16(msg[0:2]) != stunBindingSuccessResponse {
		t.Fatalf("response type = 0x%04x, want success response", binary.BigEndian.Uint16(msg[0:2]))
	}
	if binary.BigEndian.Uint32(msg[4:8]) != stunMagicCookie {
		t.Fatalf("bad magic cookie in response")
	}
	for i := 0; i < 12; i++ {
		if msg[8+i] != txid[i] {
			t.Fatalf("transaction id mismatch at byte %d", i)
		}
	}

	msgLen := int(binary.BigEndian.Uint16(msg[2:4]))
	body := msg[20 : 20+msgLen]
	for len(body) >= 4 {
		attrType := binary.BigEndian.Uint16(body[0:2])
		attrLen := int(binary.BigEndian.Uint16(body[2:4]))
		val := body[4 : 4+attrLen]
		if attrType == stunAttrXorMappedAddress {
			if val[1] != stunFamilyIPv4 {
				t.Fatalf("expected IPv4 family, got 0x%02x", val[1])
			}
			port := binary.BigEndian.Uint16(val[2:4]) ^ uint16(stunMagicCookie>>16)
			var ip [4]byte
			binary.BigEndian.PutUint32(ip[:], binary.BigEndian.Uint32(val[4:8])^stunMagicCookie)
			return net.IP(ip[:]), port
		}
		advance := 4 + attrLen
		if rem := attrLen % 4; rem != 0 {
			advance += 4 - rem
		}
		body = body[advance:]
	}
	t.Fatalf("no XOR-MAPPED-ADDRESS attribute in response")
	return nil, 0
}

func TestStunReflectorReflectsSourceAddr(t *testing.T) {
	refl, err := NewStunReflector("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewStunReflector: %v", err)
	}
	defer refl.Close()

	// Local UDP client with a known source port.
	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer client.Close()

	clientPort := uint16(client.LocalAddr().(*net.UDPAddr).Port)

	var txid [12]byte
	for i := range txid {
		txid[i] = byte(i + 1)
	}

	// Build a binding request (header only, no attributes).
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], txid[:])

	if _, err := client.WriteToUDP(req, refl.Addr().(*net.UDPAddr)); err != nil {
		t.Fatalf("client write: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1500)
	n, _, err := client.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}

	ip, port := decodeXorMappedAddress(t, buf[:n], txid)
	if port != clientPort {
		t.Fatalf("reflected port = %d, want client source port %d", port, clientPort)
	}
	if !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("reflected ip = %s, want 127.0.0.1", ip)
	}
}

func TestStunReflectorIgnoresNonRequest(t *testing.T) {
	refl, err := NewStunReflector("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewStunReflector: %v", err)
	}
	defer refl.Close()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer client.Close()

	// Garbage datagram (no valid magic cookie): must be ignored, no response.
	if _, err := client.WriteToUDP([]byte("not a stun message at all"), refl.Addr().(*net.UDPAddr)); err != nil {
		t.Fatalf("client write: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1500)
	if _, _, err := client.ReadFromUDP(buf); err == nil {
		t.Fatalf("expected no response to garbage datagram")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		// A read timeout is the expected outcome; any other error is unexpected.
		t.Fatalf("expected read timeout, got: %v", err)
	}
}

func TestStunReflectorAddrAndClose(t *testing.T) {
	refl, err := NewStunReflector("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewStunReflector: %v", err)
	}
	if refl.Addr() == nil {
		t.Fatalf("Addr() returned nil")
	}
	if err := refl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
