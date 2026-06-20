package portmap

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// --- 纯函数：编解码单测 ---

func TestProtocolString(t *testing.T) {
	cases := []struct {
		p    Protocol
		want string
	}{
		{ProtocolNATPMP, "NAT-PMP"},
		{ProtocolPCP, "PCP"},
		{ProtocolUPnP, "UPnP"},
		{Protocol(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Protocol(%d).String() = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestEncodeNATPMPMapRequest(t *testing.T) {
	// opcode=1(UDP), internalPort=51820, suggestedExt=51820, lifetime=7200
	b := encodeNATPMPMapRequest(natpmpOpMapUDP, 51820, 51820, 7200)
	if len(b) != 12 {
		t.Fatalf("len = %d, want 12", len(b))
	}
	if b[0] != 0 {
		t.Errorf("version = %d, want 0", b[0])
	}
	if b[1] != natpmpOpMapUDP {
		t.Errorf("opcode = %d, want %d", b[1], natpmpOpMapUDP)
	}
	if b[2] != 0 || b[3] != 0 {
		t.Errorf("reserved = %d,%d, want 0,0", b[2], b[3])
	}
	if got := binary.BigEndian.Uint16(b[4:6]); got != 51820 {
		t.Errorf("internalPort = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint16(b[6:8]); got != 51820 {
		t.Errorf("suggestedExt = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint32(b[8:12]); got != 7200 {
		t.Errorf("lifetime = %d, want 7200", got)
	}
}

func TestEncodeNATPMPMapRequestTCP(t *testing.T) {
	b := encodeNATPMPMapRequest(natpmpOpMapTCP, 443, 0, 0)
	if b[1] != natpmpOpMapTCP {
		t.Errorf("opcode = %d, want %d (TCP)", b[1], natpmpOpMapTCP)
	}
	if got := binary.BigEndian.Uint16(b[6:8]); got != 0 {
		t.Errorf("suggestedExt = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint32(b[8:12]); got != 0 {
		t.Errorf("lifetime = %d, want 0", got)
	}
}

func TestEncodeNATPMPExternalIPRequest(t *testing.T) {
	b := encodeNATPMPExternalIPRequest()
	if len(b) != 2 {
		t.Fatalf("len = %d, want 2", len(b))
	}
	if b[0] != 0 || b[1] != natpmpOpExternalIP {
		t.Errorf("got %v, want [0 %d]", b, natpmpOpExternalIP)
	}
}

func TestDecodeNATPMPMapResponse(t *testing.T) {
	// 构造一个合法 Map(UDP) 响应：opcode=1+128, resultCode=0, internal=51820,
	// external=40000, lifetime=3600。
	buf := make([]byte, 16)
	buf[0] = 0
	buf[1] = natpmpOpMapUDP + 128
	binary.BigEndian.PutUint16(buf[2:4], 0)     // resultCode
	binary.BigEndian.PutUint32(buf[4:8], 12345) // epoch（被忽略）
	binary.BigEndian.PutUint16(buf[8:10], 51820)
	binary.BigEndian.PutUint16(buf[10:12], 40000)
	binary.BigEndian.PutUint32(buf[12:16], 3600)

	rc, internal, external, lifetime, err := decodeNATPMPMapResponse(buf)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rc != 0 || internal != 51820 || external != 40000 || lifetime != 3600 {
		t.Errorf("got rc=%d internal=%d external=%d lifetime=%d", rc, internal, external, lifetime)
	}
}

func TestDecodeNATPMPMapResponseTCP(t *testing.T) {
	buf := make([]byte, 16)
	buf[1] = natpmpOpMapTCP + 128
	if _, _, _, _, err := decodeNATPMPMapResponse(buf); err != nil {
		t.Errorf("TCP map response should decode, got %v", err)
	}
}

func TestDecodeNATPMPMapResponseBad(t *testing.T) {
	good := func() []byte {
		b := make([]byte, 16)
		b[0] = 0
		b[1] = natpmpOpMapUDP + 128
		return b
	}
	cases := []struct {
		name string
		buf  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"truncated", make([]byte, 15)},
		{"bad version", func() []byte { b := good(); b[0] = 1; return b }()},
		{"bad opcode", func() []byte { b := good(); b[1] = 5; return b }()},
		{"opcode below 128", func() []byte { b := good(); b[1] = natpmpOpMapUDP; return b }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, _, err := decodeNATPMPMapResponse(c.buf); err == nil {
				t.Errorf("expected err for %s, got nil", c.name)
			}
		})
	}
}

func TestDecodeNATPMPExternalIPResponse(t *testing.T) {
	buf := make([]byte, 12)
	buf[0] = 0
	buf[1] = natpmpOpExternalIP + 128
	binary.BigEndian.PutUint16(buf[2:4], 0) // resultCode
	binary.BigEndian.PutUint32(buf[4:8], 999)
	buf[8], buf[9], buf[10], buf[11] = 203, 0, 113, 7 // 203.0.113.7

	rc, ip, err := decodeNATPMPExternalIPResponse(buf)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rc != 0 || ip != "203.0.113.7" {
		t.Errorf("got rc=%d ip=%q", rc, ip)
	}
}

func TestDecodeNATPMPExternalIPResponseBad(t *testing.T) {
	good := func() []byte {
		b := make([]byte, 12)
		b[1] = natpmpOpExternalIP + 128
		return b
	}
	cases := []struct {
		name string
		buf  []byte
	}{
		{"nil", nil},
		{"truncated", make([]byte, 11)},
		{"bad version", func() []byte { b := good(); b[0] = 2; return b }()},
		{"bad opcode", func() []byte { b := good(); b[1] = 3; return b }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := decodeNATPMPExternalIPResponse(c.buf); err == nil {
				t.Errorf("expected err for %s, got nil", c.name)
			}
		})
	}
}

func TestDecodeNATPMPMapResponseResultCode(t *testing.T) {
	buf := make([]byte, 16)
	buf[1] = natpmpOpMapUDP + 128
	binary.BigEndian.PutUint16(buf[2:4], 2) // 非零 resultCode（解码本身成功）
	rc, _, _, _, err := decodeNATPMPMapResponse(buf)
	if err != nil {
		t.Fatalf("decode should succeed even with non-zero rc, got %v", err)
	}
	if rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
}

// --- mock NAT-PMP UDP server ---

// mockNATPMPServer 在本地回环监听 UDP，按请求 opcode 返回可配置响应。
type mockNATPMPServer struct {
	t          *testing.T
	conn       *net.UDPConn
	extIP      [4]byte // External IP 响应返回的 IP
	extPort    uint16  // Map 响应返回的 external port
	lifetime   uint32  // Map/ExternalIP 响应返回的 lifetime
	resultCode uint16  // 两类响应的 resultCode
	silent     bool    // true=不回任何响应（模拟超时）

	// lastMapRequest 记录最近一次收到的 Map 请求原始字节，供断言。
	lastMapReq chan []byte
}

func newMockNATPMPServer(t *testing.T, opts ...func(*mockNATPMPServer)) *mockNATPMPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &mockNATPMPServer{
		t:          t,
		conn:       conn,
		extIP:      [4]byte{198, 51, 100, 23},
		extPort:    40001,
		lifetime:   3600,
		lastMapReq: make(chan []byte, 8),
	}
	// 配置在 serve goroutine 启动前完成，避免与 serve 读取产生数据竞争。
	for _, opt := range opts {
		opt(s)
	}
	go s.serve()
	return s
}

func (s *mockNATPMPServer) addr() string { return s.conn.LocalAddr().String() }

func (s *mockNATPMPServer) close() { s.conn.Close() }

func (s *mockNATPMPServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // conn 关闭
		}
		if s.silent || n < 2 {
			continue
		}
		req := append([]byte(nil), buf[:n]...)
		opcode := req[1]
		switch opcode {
		case natpmpOpExternalIP: // 0
			resp := make([]byte, 12)
			resp[0] = 0
			resp[1] = natpmpOpExternalIP + 128
			binary.BigEndian.PutUint16(resp[2:4], s.resultCode)
			binary.BigEndian.PutUint32(resp[4:8], 1)
			copy(resp[8:12], s.extIP[:])
			s.conn.WriteToUDP(resp, raddr)
		case natpmpOpMapUDP, natpmpOpMapTCP: // 1 / 2
			select {
			case s.lastMapReq <- req:
			default:
			}
			internal := binary.BigEndian.Uint16(req[4:6])
			reqLifetime := binary.BigEndian.Uint32(req[8:12])
			resp := make([]byte, 16)
			resp[0] = 0
			resp[1] = opcode + 128
			binary.BigEndian.PutUint16(resp[2:4], s.resultCode)
			binary.BigEndian.PutUint32(resp[4:8], 1)
			binary.BigEndian.PutUint16(resp[8:10], internal)
			binary.BigEndian.PutUint16(resp[10:12], s.extPort)
			// 删除（lifetime=0）时回 0，否则回配置的 lifetime。
			outLife := s.lifetime
			if reqLifetime == 0 {
				outLife = 0
			}
			binary.BigEndian.PutUint32(resp[12:16], outLife)
			s.conn.WriteToUDP(resp, raddr)
		}
	}
}

func TestNATPMPMapSuccess(t *testing.T) {
	s := newMockNATPMPServer(t)
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m, err := natpmpMap(ctx, s.addr(), 51820, 51820, true, 7200)
	if err != nil {
		t.Fatalf("natpmpMap: %v", err)
	}
	if m.Protocol != ProtocolNATPMP {
		t.Errorf("Protocol = %v, want NAT-PMP", m.Protocol)
	}
	if m.ExternalIP != "198.51.100.23" {
		t.Errorf("ExternalIP = %q, want 198.51.100.23", m.ExternalIP)
	}
	if m.ExternalPort != 40001 {
		t.Errorf("ExternalPort = %d, want 40001", m.ExternalPort)
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

func TestNATPMPMapTCP(t *testing.T) {
	s := newMockNATPMPServer(t)
	defer s.close()
	ctx := context.Background()

	if _, err := natpmpMap(ctx, s.addr(), 443, 443, false, 7200); err != nil {
		t.Fatalf("natpmpMap TCP: %v", err)
	}
	req := <-s.lastMapReq
	if req[1] != natpmpOpMapTCP {
		t.Errorf("opcode = %d, want %d (TCP)", req[1], natpmpOpMapTCP)
	}
}

func TestNATPMPMapResultCodeError(t *testing.T) {
	s := newMockNATPMPServer(t, func(s *mockNATPMPServer) { s.resultCode = 2 }) // not authorized 之类
	defer s.close()

	ctx := context.Background()
	if _, err := natpmpMap(ctx, s.addr(), 51820, 51820, true, 7200); err == nil {
		t.Fatal("expected error for non-zero resultCode, got nil")
	}
}

func TestNATPMPMapTimeout(t *testing.T) {
	s := newMockNATPMPServer(t, func(s *mockNATPMPServer) { s.silent = true }) // 不回任何响应
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := natpmpMap(ctx, s.addr(), 51820, 51820, true, 7200); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestNATPMPRefresh(t *testing.T) {
	s := newMockNATPMPServer(t)
	defer s.close()
	ctx := context.Background()

	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      s.addr(),
	}
	if err := natpmpRefresh(ctx, s.addr(), m); err != nil {
		t.Fatalf("natpmpRefresh: %v", err)
	}
	req := <-s.lastMapReq
	if got := binary.BigEndian.Uint16(req[4:6]); got != 51820 {
		t.Errorf("refresh internalPort = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint32(req[8:12]); got != 7200 {
		t.Errorf("refresh lifetime = %d, want 7200", got)
	}
	if req[1] != natpmpOpMapUDP {
		t.Errorf("refresh opcode = %d, want %d (UDP)", req[1], natpmpOpMapUDP)
	}
}

// TestNATPMPRefreshUsesExternalPort 回归 #19：续期请求必须用映射的 ExternalPort
// 作为 suggestedExt 字段，而非误用 InternalPort。外部端口≠内部端口时，续期应保持
// 同一外部端口（对齐 pcpRefresh 的行为）。
func TestNATPMPRefreshUsesExternalPort(t *testing.T) {
	s := newMockNATPMPServer(t)
	defer s.close()
	ctx := context.Background()

	// 故意让 ExternalPort 与 InternalPort 不同，才能区分两个字段。
	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      s.addr(),
	}
	if err := natpmpRefresh(ctx, s.addr(), m); err != nil {
		t.Fatalf("natpmpRefresh: %v", err)
	}

	req := <-s.lastMapReq
	// req[4:6]=internalPort，req[6:8]=suggestedExt（见 encodeNATPMPMapRequest）。
	if got := binary.BigEndian.Uint16(req[4:6]); got != 51820 {
		t.Errorf("refresh internalPort = %d, want 51820", got)
	}
	if got := binary.BigEndian.Uint16(req[6:8]); got != 40001 {
		t.Errorf("refresh suggestedExt = %d, want 40001 (ExternalPort)", got)
	}
}

func TestNATPMPUnmap(t *testing.T) {
	s := newMockNATPMPServer(t)
	defer s.close()
	ctx := context.Background()

	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      s.addr(),
	}
	if err := natpmpUnmap(ctx, s.addr(), m); err != nil {
		t.Fatalf("natpmpUnmap: %v", err)
	}
	req := <-s.lastMapReq
	if got := binary.BigEndian.Uint32(req[8:12]); got != 0 {
		t.Errorf("unmap lifetime = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint16(req[6:8]); got != 0 {
		t.Errorf("unmap suggestedExt = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint16(req[4:6]); got != 51820 {
		t.Errorf("unmap internalPort = %d, want 51820", got)
	}
}

// TestNATPMPMapGrantedZeroRejected 复现 bug #20：请求 lifetime>0 但网关授予
// grantedLife=0 属异常，必须返回 error，避免上层用 TTL=0 进入「每 Tick 删自己」循环。
func TestNATPMPMapGrantedZeroRejected(t *testing.T) {
	s := newMockNATPMPServer(t, func(s *mockNATPMPServer) { s.lifetime = 0 }) // 异常网关：授予 0
	defer s.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m, err := natpmpMap(ctx, s.addr(), 51820, 51820, true, 7200) // 请求 7200s
	if err == nil {
		t.Fatalf("expected error for grantedLife=0, got mapping TTL=%v", m.TTL)
	}
}

// TestValidateGrantedTTL 直接覆盖共享校验函数的三类输入。
func TestValidateGrantedTTL(t *testing.T) {
	// 正常授予：原样返回。
	if got, err := validateGrantedTTL(7200, 3600); err != nil || got != 3600 {
		t.Errorf("validateGrantedTTL(7200,3600) = (%d,%v), want (3600,nil)", got, err)
	}
	// 请求 >0 却授予 0：异常，返回 error。
	if _, err := validateGrantedTTL(7200, 0); err == nil {
		t.Error("validateGrantedTTL(7200,0): expected error, got nil")
	}
	// 请求本就为 0（删除语义）授予 0：合法，不报错。
	if got, err := validateGrantedTTL(0, 0); err != nil || got != 0 {
		t.Errorf("validateGrantedTTL(0,0) = (%d,%v), want (0,nil)", got, err)
	}
}
