package ingress

import (
	"encoding/binary"
	"fmt"
	"net"
)

// STUN protocol constants (RFC 5389). This is an independent server-side
// implementation, deliberately not depending on the node-side client encoder in
// internal/nodecore/ingress/stun.go, but symmetric to it.
const (
	stunBindingRequest         = 0x0001
	stunBindingSuccessResponse = 0x0101
	stunMagicCookie            = 0x2112A442

	stunAttrXorMappedAddress = 0x0020
	stunFamilyIPv4           = 0x01
	stunFamilyIPv6           = 0x02
)

// StunReflector is the controller's built-in UDP STUN reflection endpoint
// (spec §3.3): nodes send a RFC 5389 binding request and get back their
// reflexive source ip:port encoded as XOR-MAPPED-ADDRESS. This lets a node learn
// its public-facing mapping without depending on an external STUN service.
//
// Only the standard library is used. The reflector runs a single read loop in a
// background goroutine until Close.
type StunReflector struct {
	conn *net.UDPConn
}

// NewStunReflector binds a UDP socket at listenAddr ("host:port"; an empty or
// ":0" port picks an ephemeral one) and starts the reflection read loop.
func NewStunReflector(listenAddr string) (*StunReflector, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", listenAddr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %q: %w", listenAddr, err)
	}

	r := &StunReflector{conn: conn}
	go r.serve()
	return r, nil
}

// Addr returns the local address the reflector is listening on.
func (r *StunReflector) Addr() net.Addr {
	return r.conn.LocalAddr()
}

// Close stops the reflector and releases its socket. The read loop unblocks and
// exits because the closed socket makes ReadFromUDP return an error.
func (r *StunReflector) Close() error {
	return r.conn.Close()
}

// serve is the read loop: for each datagram that is a valid binding request it
// writes back a binding success response carrying the sender's reflexive address
// as XOR-MAPPED-ADDRESS. Malformed datagrams and non-requests are ignored.
func (r *StunReflector) serve() {
	buf := make([]byte, 1500)
	for {
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			// Socket closed (Close) or a transient read error; either way the
			// loop ends. There is no separate stop channel because closing the
			// socket is the only shutdown path.
			return
		}

		txid, ok := parseBindingRequestHeader(buf[:n])
		if !ok {
			continue
		}

		resp := buildBindingSuccessResponse(txid, src)
		if _, err := r.conn.WriteToUDP(resp, src); err != nil {
			// Best-effort: a failed write to one client must not kill the loop.
			continue
		}
	}
}

// parseBindingRequestHeader validates a 20-byte STUN binding request header
// (message type, magic cookie) and returns the transaction id. It returns
// ok=false for anything that is not a well-formed binding request.
func parseBindingRequestHeader(msg []byte) (txid [12]byte, ok bool) {
	if len(msg) < 20 {
		return txid, false
	}
	if binary.BigEndian.Uint16(msg[0:2]) != stunBindingRequest {
		return txid, false
	}
	if binary.BigEndian.Uint32(msg[4:8]) != stunMagicCookie {
		return txid, false
	}
	copy(txid[:], msg[8:20])
	return txid, true
}

// buildBindingSuccessResponse encodes a STUN binding success response (type
// 0x0101) carrying a single XOR-MAPPED-ADDRESS attribute (0x0020) that encodes
// src's ip:port per RFC 5389: the port is XORed with the high 16 bits of the
// magic cookie, and the address is XORed with the cookie (IPv4) or the cookie
// concatenated with the transaction id (IPv6).
func buildBindingSuccessResponse(txid [12]byte, src *net.UDPAddr) []byte {
	ip4 := src.IP.To4()

	var attrVal []byte
	if ip4 != nil {
		attrVal = make([]byte, 8)
		attrVal[0] = 0 // reserved
		attrVal[1] = stunFamilyIPv4
		binary.BigEndian.PutUint16(attrVal[2:4], uint16(src.Port)^uint16(stunMagicCookie>>16))
		binary.BigEndian.PutUint32(attrVal[4:8], binary.BigEndian.Uint32(ip4)^stunMagicCookie)
	} else {
		ip6 := src.IP.To16()
		attrVal = make([]byte, 20)
		attrVal[0] = 0 // reserved
		attrVal[1] = stunFamilyIPv6
		binary.BigEndian.PutUint16(attrVal[2:4], uint16(src.Port)^uint16(stunMagicCookie>>16))
		var key [16]byte
		binary.BigEndian.PutUint32(key[0:4], stunMagicCookie)
		copy(key[4:16], txid[:])
		for i := 0; i < 16; i++ {
			attrVal[4+i] = ip6[i] ^ key[i]
		}
	}

	// Header (20) + attribute header (4) + attribute value.
	msg := make([]byte, 20+4+len(attrVal))
	binary.BigEndian.PutUint16(msg[0:2], stunBindingSuccessResponse)
	binary.BigEndian.PutUint16(msg[2:4], uint16(4+len(attrVal))) // message length: body only
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txid[:])

	binary.BigEndian.PutUint16(msg[20:22], stunAttrXorMappedAddress)
	binary.BigEndian.PutUint16(msg[22:24], uint16(len(attrVal)))
	copy(msg[24:], attrVal)

	return msg
}
