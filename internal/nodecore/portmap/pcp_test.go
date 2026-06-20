package portmap

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// --- PCP 纯函数：编解码单测 ---

func TestEncodePCPMapRequest(t *testing.T) {
	var nonce pcpNonce
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	clientIP := net.IPv4(192, 168, 1, 50)
	b := encodePCPMapRequest(nonce, clientIP, pcpProtoUDP, 51820, 51820, nil, 7200)

	if len(b) != pcpReqLen {
		t.Fatalf("len = %d, want %d", len(b), pcpReqLen)
	}
	// 公共头。
	if b[0] != pcpVersion {
		t.Errorf("version = %d, want %d", b[0], pcpVersion)
	}
	if b[1] != pcpOpMap {
		t.Errorf("opcode = 0x%02x, want 0x%02x (MAP request, R=0)", b[1], pcpOpMap)
	}
	if b[2] != 0 || b[3] != 0 {
		t.Errorf("header reserved = %d,%d, want 0,0", b[2], b[3])
	}
	if got := binary.BigEndian.Uint32(b[4:8]); got != 7200 {
		t.Errorf("lifetime = %d, want 7200", got)
	}
	// client IP 应为 IPv4-mapped-IPv6（::ffff:192.168.1.50）。
	wantCIP := ipToMapped16(clientIP)
	if !equalBytes(b[8:24], wantCIP[:]) {
		t.Errorf("clientIP = % x, want % x", b[8:24], wantCIP[:])
	}
	// MAP 数据。
	if !equalBytes(b[24:36], nonce[:]) {
		t.Errorf("nonce = % x, want % x", b[24:36], nonce[:])
	}
	if b[36] != pcpProtoUDP {
		t.Errorf("protocol = %d, want %d (UDP)", b[36], pcpProtoUDP)
	}
	if b[37] != 0 || b[38] != 0 || b[39] != 0 {
		t.Errorf("map reserved = %d,%d,%d, want 0,0,0", b[37], b[38], b[39])
	}
	if got := binary.BigEndian.Uint16(b[40:42]); got != 51820 {
		t.Errorf("internalPort = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint16(b[42:44]); got != 51820 {
		t.Errorf("suggestedExtPort = %d, want 51820", got)
	}
	// suggested external IP = nil → 全 0。
	var zero16 [16]byte
	if !equalBytes(b[44:60], zero16[:]) {
		t.Errorf("suggestedExtIP = % x, want all-zero", b[44:60])
	}
}

func TestEncodePCPMapRequestTCP(t *testing.T) {
	var nonce pcpNonce
	b := encodePCPMapRequest(nonce, net.IPv4(10, 0, 0, 1), pcpProtoTCP, 443, 0, nil, 0)
	if b[36] != pcpProtoTCP {
		t.Errorf("protocol = %d, want %d (TCP)", b[36], pcpProtoTCP)
	}
	if got := binary.BigEndian.Uint32(b[4:8]); got != 0 {
		t.Errorf("lifetime = %d, want 0", got)
	}
}

func TestDecodePCPMapResponse(t *testing.T) {
	var nonce pcpNonce
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	buf := buildPCPResponse(t, nonce, pcpProtoUDP, 0, 51820, 40000, net.IPv4(198, 51, 100, 23), 3600)

	rc, gotNonce, proto, internal, external, extIP, lifetime, err := decodePCPMapResponse(buf)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rc != 0 {
		t.Errorf("resultCode = %d, want 0", rc)
	}
	if gotNonce != nonce {
		t.Errorf("nonce = % x, want % x", gotNonce, nonce)
	}
	if proto != pcpProtoUDP {
		t.Errorf("proto = %d, want %d", proto, pcpProtoUDP)
	}
	if internal != 51820 || external != 40000 {
		t.Errorf("ports internal=%d external=%d, want 51820/40000", internal, external)
	}
	if extIP != "198.51.100.23" {
		t.Errorf("externalIP = %q, want 198.51.100.23", extIP)
	}
	if lifetime != 3600 {
		t.Errorf("lifetime = %d, want 3600", lifetime)
	}
}

func TestDecodePCPMapResponseResultCode(t *testing.T) {
	var nonce pcpNonce
	buf := buildPCPResponse(t, nonce, pcpProtoUDP, 8, 51820, 40000, net.IPv4(1, 2, 3, 4), 0)
	rc, _, _, _, _, _, _, err := decodePCPMapResponse(buf)
	if err != nil {
		t.Fatalf("decode should succeed even with non-zero rc, got %v", err)
	}
	if rc != 8 {
		t.Errorf("rc = %d, want 8", rc)
	}
}

func TestDecodePCPMapResponseBad(t *testing.T) {
	good := func() []byte {
		var nonce pcpNonce
		return buildPCPResponse(t, nonce, pcpProtoUDP, 0, 0, 0, net.IPv4(1, 1, 1, 1), 0)
	}
	cases := []struct {
		name string
		buf  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"truncated", make([]byte, pcpRespLen-1)},
		{"bad version", func() []byte { b := good(); b[0] = 0; return b }()},
		{"opcode without R bit", func() []byte { b := good(); b[1] = pcpOpMap; return b }()},
		{"bad opcode", func() []byte { b := good(); b[1] = 0x82; return b }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, _, _, _, _, err := decodePCPMapResponse(c.buf); err == nil {
				t.Errorf("expected err for %s, got nil", c.name)
			}
		})
	}
}

func TestIPToMapped16(t *testing.T) {
	got := ipToMapped16(net.IPv4(203, 0, 113, 7))
	want := [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 203, 0, 113, 7}
	if got != want {
		t.Errorf("ipToMapped16 = % x, want % x", got, want)
	}
	// nil → 全 0。
	if ipToMapped16(nil) != ([16]byte{}) {
		t.Errorf("ipToMapped16(nil) should be all-zero")
	}
}

func TestMapped16ToIPv4String(t *testing.T) {
	mapped := ipToMapped16(net.IPv4(198, 51, 100, 1))
	if got := mapped16ToIPv4String(mapped[:]); got != "198.51.100.1" {
		t.Errorf("got %q, want 198.51.100.1", got)
	}
}

// --- mock PCP UDP server ---

// mockPCPServer 在本地回环监听 UDP，回显请求 nonce 并按配置构造 MAP 响应。
type mockPCPServer struct {
	t          *testing.T
	conn       *net.UDPConn
	extIP      [4]byte // assigned external IP
	extPort    uint16  // assigned external port
	lifetime   uint32  // 授予租约
	resultCode byte    // 响应 result code
	silent     bool    // true=不回响应（模拟超时）
	badNonce   bool    // true=故意回显错误 nonce

	lastReq chan []byte // 最近一次收到的请求原始字节，供断言。
}

func newMockPCPServer(t *testing.T, opts ...func(*mockPCPServer)) *mockPCPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &mockPCPServer{
		t:        t,
		conn:     conn,
		extIP:    [4]byte{198, 51, 100, 77},
		extPort:  40002,
		lifetime: 3600,
		lastReq:  make(chan []byte, 8),
	}
	// 配置在 serve goroutine 启动前完成，避免与 serve 读取产生数据竞争。
	for _, opt := range opts {
		opt(s)
	}
	go s.serve()
	return s
}

func (s *mockPCPServer) addr() string { return s.conn.LocalAddr().String() }

func (s *mockPCPServer) close() { s.conn.Close() }

func (s *mockPCPServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // conn 关闭
		}
		if s.silent || n < pcpReqLen {
			continue
		}
		req := append([]byte(nil), buf[:n]...)
		select {
		case s.lastReq <- req:
		default:
		}
		// 从请求回显 nonce/protocol/internalPort。
		var nonce pcpNonce
		copy(nonce[:], req[24:36])
		if s.badNonce {
			nonce[0] ^= 0xff
		}
		proto := req[36]
		internal := binary.BigEndian.Uint16(req[40:42])
		reqLifetime := binary.BigEndian.Uint32(req[4:8])
		outLife := s.lifetime
		if reqLifetime == 0 {
			outLife = 0 // 删除请求回 0。
		}
		resp := buildPCPResponse(s.t, nonce, proto, s.resultCode, internal, s.extPort, net.IP(s.extIP[:]), outLife)
		s.conn.WriteToUDP(resp, raddr)
	}
}

// buildPCPResponse 构造一个 60 字节 PCP MAP 响应。
func buildPCPResponse(t *testing.T, nonce pcpNonce, proto, resultCode byte, internalPort, externalPort uint16, extIP net.IP, lifetime uint32) []byte {
	t.Helper()
	b := make([]byte, pcpRespLen)
	b[0] = pcpVersion
	b[1] = pcpOpMap | pcpRespBit // 响应 R bit=1 → 0x81。
	b[3] = resultCode
	binary.BigEndian.PutUint32(b[4:8], lifetime)
	binary.BigEndian.PutUint32(b[8:12], 1) // epoch
	copy(b[24:36], nonce[:])
	b[36] = proto
	binary.BigEndian.PutUint16(b[40:42], internalPort)
	binary.BigEndian.PutUint16(b[42:44], externalPort)
	eip := ipToMapped16(extIP)
	copy(b[44:60], eip[:])
	return b
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- PCP mock server 真往返 ---

func TestPCPMapSuccess(t *testing.T) {
	s := newMockPCPServer(t)
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m, err := pcpMap(ctx, s.addr(), 51820, 51820, true, 7200)
	if err != nil {
		t.Fatalf("pcpMap: %v", err)
	}
	if m.Protocol != ProtocolPCP {
		t.Errorf("Protocol = %v, want PCP", m.Protocol)
	}
	if m.ExternalIP != "198.51.100.77" {
		t.Errorf("ExternalIP = %q, want 198.51.100.77", m.ExternalIP)
	}
	if m.ExternalPort != 40002 {
		t.Errorf("ExternalPort = %d, want 40002", m.ExternalPort)
	}
	if m.InternalPort != 51820 {
		t.Errorf("InternalPort = %d, want 51820", m.InternalPort)
	}
	if !m.TransportUDP {
		t.Errorf("TransportUDP = false, want true")
	}
	if m.TTL != 3600*time.Second {
		t.Errorf("TTL = %v, want 3600s", m.TTL)
	}
	if m.Gateway != s.addr() {
		t.Errorf("Gateway = %q, want %q", m.Gateway, s.addr())
	}
}

func TestPCPMapTCP(t *testing.T) {
	s := newMockPCPServer(t)
	defer s.close()
	ctx := context.Background()

	if _, err := pcpMap(ctx, s.addr(), 443, 443, false, 7200); err != nil {
		t.Fatalf("pcpMap TCP: %v", err)
	}
	req := <-s.lastReq
	if req[36] != pcpProtoTCP {
		t.Errorf("protocol = %d, want %d (TCP)", req[36], pcpProtoTCP)
	}
	if req[0] != pcpVersion {
		t.Errorf("version = %d, want %d", req[0], pcpVersion)
	}
	if req[1] != pcpOpMap {
		t.Errorf("opcode = 0x%02x, want 0x%02x", req[1], pcpOpMap)
	}
}

func TestPCPMapResultCodeError(t *testing.T) {
	s := newMockPCPServer(t, func(s *mockPCPServer) { s.resultCode = 8 }) // NO_RESOURCES 之类
	defer s.close()

	ctx := context.Background()
	if _, err := pcpMap(ctx, s.addr(), 51820, 51820, true, 7200); err == nil {
		t.Fatal("expected error for non-zero resultCode, got nil")
	}
}

func TestPCPMapNonceMismatch(t *testing.T) {
	s := newMockPCPServer(t, func(s *mockPCPServer) { s.badNonce = true })
	defer s.close()

	ctx := context.Background()
	if _, err := pcpMap(ctx, s.addr(), 51820, 51820, true, 7200); err == nil {
		t.Fatal("expected error for nonce mismatch, got nil")
	}
}

func TestPCPMapTimeout(t *testing.T) {
	s := newMockPCPServer(t, func(s *mockPCPServer) { s.silent = true })
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := pcpMap(ctx, s.addr(), 51820, 51820, true, 7200); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestPCPRefresh(t *testing.T) {
	s := newMockPCPServer(t)
	defer s.close()
	ctx := context.Background()

	m := &Mapping{
		Protocol:     ProtocolPCP,
		ExternalPort: 40002,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      s.addr(),
	}
	if err := pcpRefresh(ctx, m); err != nil {
		t.Fatalf("pcpRefresh: %v", err)
	}
	req := <-s.lastReq
	if got := binary.BigEndian.Uint16(req[40:42]); got != 51820 {
		t.Errorf("refresh internalPort = %d, want 51820", got)
	}
	// 续期须保持同一外部端口（RFC 6887 §11.2.3）：suggested external port 取 m.ExternalPort。
	if got := binary.BigEndian.Uint16(req[42:44]); got != 40002 {
		t.Errorf("refresh suggestedExtPort = %d, want 40002 (m.ExternalPort)", got)
	}
	if got := binary.BigEndian.Uint32(req[4:8]); got != 7200 {
		t.Errorf("refresh lifetime = %d, want 7200", got)
	}
}

func TestPCPUnmap(t *testing.T) {
	s := newMockPCPServer(t)
	defer s.close()
	ctx := context.Background()

	m := &Mapping{
		Protocol:     ProtocolPCP,
		ExternalPort: 40002,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      s.addr(),
	}
	if err := pcpUnmap(ctx, m); err != nil {
		t.Fatalf("pcpUnmap: %v", err)
	}
	req := <-s.lastReq
	if got := binary.BigEndian.Uint32(req[4:8]); got != 0 {
		t.Errorf("unmap lifetime = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint16(req[40:42]); got != 51820 {
		t.Errorf("unmap internalPort = %d, want 51820", got)
	}
}

func TestPCPMapNilMapping(t *testing.T) {
	ctx := context.Background()
	if err := pcpRefresh(ctx, nil); err == nil {
		t.Error("pcpRefresh(nil) should error")
	}
	if err := pcpUnmap(ctx, nil); err == nil {
		t.Error("pcpUnmap(nil) should error")
	}
}

// TestPCPMapGrantedZeroRejected 复现 bug #20（PCP 分支）：请求 lifetime>0 但
// 网关授予 0 时必须返回 error，不组装 TTL=0 的 Mapping。
func TestPCPMapGrantedZeroRejected(t *testing.T) {
	s := newMockPCPServer(t, func(s *mockPCPServer) { s.lifetime = 0 }) // 异常网关：授予 0
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m, err := pcpMap(ctx, s.addr(), 51820, 51820, true, 7200) // 请求 7200s
	if err == nil {
		t.Fatalf("expected error for grantedLife=0, got mapping TTL=%v", m.TTL)
	}
}
