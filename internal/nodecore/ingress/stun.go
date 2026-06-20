// Package ingress discovers a node's reachable public entry points (ingresses)
// for CoreLink: it probes STUN servers to learn the public-facing address and
// infer the NAT type, enumerates local interfaces, queries public-IP URLs, and
// merges these signals into a deduplicated candidate ingress set.
//
// This file implements a lightweight, dependency-free STUN client (RFC 5389
// binding request / XOR-MAPPED-ADDRESS) plus a simple NAT-type inference that
// compares the reflexive mappings observed from a *single local socket* when it
// is reflected off several distinct STUN targets.
package ingress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// STUN protocol constants (RFC 5389).
const (
	bindingRequest         = 0x0001
	bindingSuccessResponse = 0x0101
	magicCookie            = 0x2112A442

	attrMappedAddress    = 0x0001
	attrXorMappedAddress = 0x0020

	familyIPv4 = 0x01
	familyIPv6 = 0x02
)

// defaultBindingTimeout is the per-endpoint timeout. It is intentionally short
// so that StunProbe can fall through several dead endpoints within a larger
// overall deadline.
const defaultBindingTimeout = 2 * time.Second

// stunBinding performs a single RFC 5389 binding transaction against stunAddr
// ("host:port") using a fresh local socket, returning the reflexive public
// address (host string, port).
//
// It is a convenience wrapper over stunBindingOnConn for callers that only need
// one probe. NAT-type inference must instead reuse a single socket across
// targets (see StunProbe / stunBindingOnConn), because a fresh socket implies a
// fresh source port and would make even a full-cone NAT look like a symmetric
// one. Only the standard library is used (net, encoding/binary, crypto/rand).
func stunBinding(ctx context.Context, stunAddr string) (host string, port uint32, err error) {
	lc := net.ListenConfig{Control: tunnel.BindControl}
	pc, err := lc.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		return "", 0, fmt.Errorf("listen udp: %w", err)
	}
	defer pc.Close()
	conn := pc.(*net.UDPConn)

	ap, err := stunBindingOnConn(ctx, conn, stunAddr)
	if err != nil {
		return "", 0, err
	}
	return ap.Addr().String(), uint32(ap.Port()), nil
}

// stunBindingOnConn performs one binding transaction against stunAddr over the
// already-open UDP socket conn (preserving its source port across calls). It
// applies a short per-target deadline bounded by ctx, and is the unit reused by
// StunProbe to compare mappings from the same source port across targets.
func stunBindingOnConn(ctx context.Context, conn *net.UDPConn, stunAddr string) (netip.AddrPort, error) {
	var zero netip.AddrPort

	raddr, err := net.ResolveUDPAddr("udp", stunAddr)
	if err != nil {
		return zero, fmt.Errorf("resolve %q: %w", stunAddr, err)
	}

	// Each target gets its own short timeout so that a single dead server cannot
	// consume the caller's whole budget. We still cap by the ctx deadline if it
	// is sooner (e.g. an overall probe deadline).
	deadline := time.Now().Add(defaultBindingTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return zero, fmt.Errorf("set deadline: %w", err)
	}
	// Reset the deadline when we return so a later call on the same socket is not
	// left with a stale (already-expired) deadline.
	defer conn.SetDeadline(time.Time{})

	// Abort the blocking read if ctx is cancelled before the deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	var txid [12]byte
	if _, err := rand.Read(txid[:]); err != nil {
		return zero, fmt.Errorf("gen transaction id: %w", err)
	}

	req := buildBindingRequest(txid)
	if _, err := conn.WriteToUDP(req, raddr); err != nil {
		return zero, fmt.Errorf("write request to %q: %w", stunAddr, err)
	}

	// Read until we get a response whose transaction id matches our request, so
	// that stray/late datagrams from a previous target on the same socket do not
	// confuse the current transaction.
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return zero, fmt.Errorf("stun %q: %w", stunAddr, ctx.Err())
			}
			return zero, fmt.Errorf("read response from %q: %w", stunAddr, err)
		}
		ap, err := parseBindingResponse(buf[:n], txid)
		if err != nil {
			// Could be a mismatched/late datagram; keep reading until deadline.
			continue
		}
		return ap, nil
	}
}

// buildBindingRequest encodes a 20-byte STUN binding request header with the
// given transaction id and no attributes.
func buildBindingRequest(txid [12]byte) []byte {
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // message length: no attributes
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txid[:])
	return msg
}

// parseBindingResponse validates a STUN response and extracts the mapped
// address, preferring XOR-MAPPED-ADDRESS and falling back to MAPPED-ADDRESS.
func parseBindingResponse(msg []byte, txid [12]byte) (netip.AddrPort, error) {
	var zero netip.AddrPort
	if len(msg) < 20 {
		return zero, errors.New("short message")
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		return zero, errors.New("bad magic cookie")
	}
	if !bytes.Equal(msg[8:20], txid[:]) {
		return zero, errors.New("transaction id mismatch")
	}
	msgLen := int(binary.BigEndian.Uint16(msg[2:4]))
	if 20+msgLen > len(msg) {
		return zero, errors.New("declared length exceeds message")
	}

	body := msg[20 : 20+msgLen]
	var fallback netip.AddrPort
	var haveFallback bool

	for len(body) >= 4 {
		attrType := binary.BigEndian.Uint16(body[0:2])
		attrLen := int(binary.BigEndian.Uint16(body[2:4]))
		if 4+attrLen > len(body) {
			return zero, errors.New("truncated attribute")
		}
		val := body[4 : 4+attrLen]

		switch attrType {
		case attrXorMappedAddress:
			ap, err := decodeMappedAddress(val, true, txid)
			if err == nil {
				return ap, nil // XOR-MAPPED-ADDRESS is preferred; return immediately.
			}
		case attrMappedAddress:
			if !haveFallback {
				if ap, err := decodeMappedAddress(val, false, txid); err == nil {
					fallback = ap
					haveFallback = true
				}
			}
		}

		// Advance past this attribute, accounting for 4-byte padding.
		advance := 4 + attrLen
		if rem := attrLen % 4; rem != 0 {
			advance += 4 - rem
		}
		if advance > len(body) {
			break
		}
		body = body[advance:]
	}

	if haveFallback {
		return fallback, nil
	}
	return zero, errors.New("no mapped address attribute")
}

// decodeMappedAddress decodes a (XOR-)MAPPED-ADDRESS attribute value. When xor
// is true, the port and address are XOR-decoded per RFC 5389. IPv4 and IPv6
// are both supported.
func decodeMappedAddress(val []byte, xor bool, txid [12]byte) (netip.AddrPort, error) {
	var zero netip.AddrPort
	if len(val) < 4 {
		return zero, errors.New("short address attribute")
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:4])
	if xor {
		port ^= uint16(magicCookie >> 16)
	}

	switch family {
	case familyIPv4:
		if len(val) < 8 {
			return zero, errors.New("short IPv4 address")
		}
		var ip [4]byte
		copy(ip[:], val[4:8])
		if xor {
			binary.BigEndian.PutUint32(ip[:], binary.BigEndian.Uint32(ip[:])^magicCookie)
		}
		return netip.AddrPortFrom(netip.AddrFrom4(ip), port), nil
	case familyIPv6:
		if len(val) < 20 {
			return zero, errors.New("short IPv6 address")
		}
		var ip [16]byte
		copy(ip[:], val[4:20])
		if xor {
			// X-Address = address XOR (magic cookie || transaction id).
			var key [16]byte
			binary.BigEndian.PutUint32(key[0:4], magicCookie)
			copy(key[4:16], txid[:])
			for i := range ip {
				ip[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(ip), port), nil
	default:
		return zero, fmt.Errorf("unknown address family 0x%02x", family)
	}
}

// StunProbe reflects a *single local UDP socket* off the given STUN targets (in
// order, with per-target fault tolerance) to determine the node's public
// reflexive address and infer whether its NAT mapping is stable. It returns a
// host/port/nat triple suitable for filling a genv1.Ingress.
//
// Crucially, all targets are queried from the *same source port* (one
// net.ListenUDP socket reused via WriteToUDP). This is what makes cone-vs-
// symmetric inference meaningful: using a fresh socket per target would assign a
// fresh source port each time, so even a full-cone NAT would hand out a
// different mapped port per query and look symmetric.
//
// Inference compares the mappings observed from the first two targets that
// answer (the first answer also provides the reported host/port):
//   - same external ip:port for both targets  -> endpoint-independent mapping
//     -> NAT_TYPE_FULL_CONE (the mapping is stable and can be used as a relay
//     entry point)
//   - mapping varies by target                -> endpoint-dependent mapping
//     -> NAT_TYPE_SYMMETRIC (the mapping is unstable; cannot serve as an entry)
//   - only a single target answered           -> NAT_TYPE_UNKNOWN (one data
//     point is insufficient to judge stability)
//   - no target answered                      -> error
//
// Simplification (intentional): a binding-only probe cannot observe *filtering*
// behaviour (which source addresses/ports are allowed inbound), so this code
// does NOT distinguish RESTRICTED / PORT_RESTRICTED cones. For CoreLink's
// forced-relay use case -- a node actively dials out to build a tunnel and keeps
// the mapping alive with UDP keepalives -- filtering is irrelevant; the only
// thing that matters is whether the *mapping is stable* (endpoint-independent).
// Hence any stable mapping is reported uniformly as FULL_CONE, aligning with
// spec §3.3 (deciding whether a node has a stable public entry point for
// inbound access). OPEN likewise cannot be distinguished from FULL_CONE without
// an inbound-reachability test, so the conservative FULL_CONE is reported.
func StunProbe(ctx context.Context, stunAddrs []string) (host string, port uint32, nat genv1.NatType, err error) {
	if len(stunAddrs) == 0 {
		return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, errors.New("no stun addresses provided")
	}

	// One socket (one source port) reused across all targets.
	// 注入 SO_BINDTODEVICE 绑定物理网卡，绕过 TUN 路由。
	lc := net.ListenConfig{Control: tunnel.BindControl}
	pc, err := lc.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, fmt.Errorf("listen udp: %w", err)
	}
	defer pc.Close()
	conn := pc.(*net.UDPConn)

	var mappings []netip.AddrPort
	var lastErr error

	for _, addr := range stunAddrs {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		ap, e := stunBindingOnConn(ctx, conn, addr)
		if e != nil {
			lastErr = e
			continue // per-target fault tolerance: skip dead/failed targets
		}
		mappings = append(mappings, ap)
		// Two successful, distinct probes are enough to judge mapping stability.
		if len(mappings) >= 2 {
			break
		}
	}

	if len(mappings) == 0 {
		if lastErr == nil {
			lastErr = errors.New("all stun endpoints failed")
		}
		return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, lastErr
	}

	first := mappings[0]
	host = first.Addr().String()
	port = uint32(first.Port())

	// #18：反射地址若非可用公网 IP（私有/CGNAT/回环/链路本地等，复用 #8 收紧后的
	// isUsablePublicIP，其内部委托 isPubliclyRoutable），则该地址不可作为高置信公网入口。
	// host/port 仍返回供 relay 出口拨号参考，但 NAT 类型强制降级为 UNKNOWN，避免被上报为稳定公网入口。
	if !isUsablePublicIP(first.Addr()) {
		return host, port, genv1.NatType_NAT_TYPE_UNKNOWN, nil
	}

	if len(mappings) == 1 {
		// Single data point: cannot judge stability, report UNKNOWN conservatively.
		return host, port, genv1.NatType_NAT_TYPE_UNKNOWN, nil
	}

	nat = inferNatType(first, mappings[1])
	return host, port, nat, nil
}

// inferNatType classifies the NAT mapping stability from two reflexive mappings
// observed from the *same* local source port via two different STUN targets.
//
// See StunProbe for the full rationale; in short: identical mapping across
// targets means an endpoint-independent (stable) mapping, reported as FULL_CONE;
// a mapping that changes with the target is endpoint-dependent (unstable),
// reported as SYMMETRIC. Filtering-based sub-types (RESTRICTED/PORT_RESTRICTED)
// are deliberately not distinguished because a binding-only probe cannot observe
// them and they do not affect the keep-alive'd outbound-tunnel relay use case.
func inferNatType(a, b netip.AddrPort) genv1.NatType {
	if a == b {
		return genv1.NatType_NAT_TYPE_FULL_CONE
	}
	return genv1.NatType_NAT_TYPE_SYMMETRIC
}
