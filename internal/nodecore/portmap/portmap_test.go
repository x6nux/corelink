package portmap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────
// mock 基础设施
// ────────────────────────────────────────────────────────────────

// mapperNATPMPServer 是 DefaultMapper 测试用的 mock NAT-PMP server。
// 与 natpmp_test.go 的 mockNATPMPServer 独立，避免名称冲突与耦合。
type mapperNATPMPServer struct {
	conn       *net.UDPConn
	extIP      [4]byte
	extPort    uint16
	lifetime   uint32
	resultCode uint16
	silent     bool          // 不回响应（模拟超时/不可达）。
	delay      time.Duration // 应答前延迟（竞速测试用）。
}

func newMapperNATPMPServer(t *testing.T, opts ...func(*mapperNATPMPServer)) *mapperNATPMPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &mapperNATPMPServer{
		conn:     conn,
		extIP:    [4]byte{198, 51, 100, 23},
		extPort:  40001,
		lifetime: 3600,
	}
	for _, opt := range opts {
		opt(s)
	}
	go s.serve()
	return s
}

func (s *mapperNATPMPServer) addr() string { return s.conn.LocalAddr().String() }
func (s *mapperNATPMPServer) close()       { s.conn.Close() }

func (s *mapperNATPMPServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if s.silent || n < 2 {
			continue
		}
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		req := append([]byte(nil), buf[:n]...)
		opcode := req[1]
		switch opcode {
		case natpmpOpExternalIP:
			resp := make([]byte, 12)
			resp[1] = natpmpOpExternalIP + 128
			binary.BigEndian.PutUint16(resp[2:4], s.resultCode)
			binary.BigEndian.PutUint32(resp[4:8], 1)
			copy(resp[8:12], s.extIP[:])
			s.conn.WriteToUDP(resp, raddr)
		case natpmpOpMapUDP, natpmpOpMapTCP:
			internal := binary.BigEndian.Uint16(req[4:6])
			reqLife := binary.BigEndian.Uint32(req[8:12])
			resp := make([]byte, 16)
			resp[1] = opcode + 128
			binary.BigEndian.PutUint16(resp[2:4], s.resultCode)
			binary.BigEndian.PutUint32(resp[4:8], 1)
			binary.BigEndian.PutUint16(resp[8:10], internal)
			binary.BigEndian.PutUint16(resp[10:12], s.extPort)
			outLife := s.lifetime
			if reqLife == 0 {
				outLife = 0
			}
			binary.BigEndian.PutUint32(resp[12:16], outLife)
			s.conn.WriteToUDP(resp, raddr)
		}
	}
}

// mapperPCPServer 是 DefaultMapper 测试用的 mock PCP server。
type mapperPCPServer struct {
	conn       *net.UDPConn
	extIP      [4]byte
	extPort    uint16
	lifetime   uint32
	resultCode byte
	silent     bool
	delay      time.Duration
}

func newMapperPCPServer(t *testing.T, opts ...func(*mapperPCPServer)) *mapperPCPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &mapperPCPServer{
		conn:     conn,
		extIP:    [4]byte{198, 51, 100, 77},
		extPort:  40002,
		lifetime: 3600,
	}
	for _, opt := range opts {
		opt(s)
	}
	go s.serve()
	return s
}

func (s *mapperPCPServer) addr() string { return s.conn.LocalAddr().String() }
func (s *mapperPCPServer) close()       { s.conn.Close() }

func (s *mapperPCPServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if s.silent || n < pcpReqLen {
			continue
		}
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		req := append([]byte(nil), buf[:n]...)
		var nonce pcpNonce
		copy(nonce[:], req[24:36])
		proto := req[36]
		internal := binary.BigEndian.Uint16(req[40:42])
		reqLife := binary.BigEndian.Uint32(req[4:8])
		outLife := s.lifetime
		if reqLife == 0 {
			outLife = 0
		}
		resp := make([]byte, pcpRespLen)
		resp[0] = pcpVersion
		resp[1] = pcpOpMap | pcpRespBit
		resp[3] = s.resultCode
		binary.BigEndian.PutUint32(resp[4:8], outLife)
		binary.BigEndian.PutUint32(resp[8:12], 1)
		copy(resp[24:36], nonce[:])
		resp[36] = proto
		binary.BigEndian.PutUint16(resp[40:42], internal)
		binary.BigEndian.PutUint16(resp[42:44], s.extPort)
		eip := ipToMapped16(net.IP(s.extIP[:]))
		copy(resp[44:60], eip[:])
		s.conn.WriteToUDP(resp, raddr)
	}
}

// mapperIGDServer 创建一个 mock UPnP-IGD HTTP 服务器（SSDP 发现 XML + SOAP 控制）。
// 返回 httptest.Server + fake ssdpTransport（指向该 server 的 location）。
func mapperIGDServer(t *testing.T, extIP string, fault string, delay time.Duration) (*httptest.Server, *fakeSSDPTransport) {
	t.Helper()

	var requestCount atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		// 路由：/rootDesc.xml → device XML；/ctl → SOAP 控制。
		switch {
		case strings.HasSuffix(r.URL.Path, "/rootDesc.xml"):
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceList>
      <device>
        <deviceList>
          <device>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/ctl</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`)

		case strings.HasSuffix(r.URL.Path, "/ctl"):
			requestCount.Add(1)
			soapAction := r.Header.Get("SOAPAction")
			body, _ := io.ReadAll(r.Body)
			_ = body

			switch {
			case strings.Contains(soapAction, "AddPortMapping"):
				if fault == "AddPortMapping" {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body><s:Fault><faultstring>UPnPError</faultstring>
    <detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
      <errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription>
    </UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
					return
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`)

			case strings.Contains(soapAction, "GetExternalIPAddress"):
				if fault == "GetExternalIPAddress" {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body><s:Fault><faultstring>UPnPError</faultstring>
    <detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
      <errorCode>501</errorCode><errorDescription>ActionFailed</errorDescription>
    </UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
					return
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewExternalIPAddress>%s</NewExternalIPAddress>
</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`, extIP)

			case strings.Contains(soapAction, "DeletePortMapping"):
				if fault == "DeletePortMapping" {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body><s:Fault><faultstring>UPnPError</faultstring>
    <detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
      <errorCode>714</errorCode><errorDescription>NoSuchEntryInArray</errorDescription>
    </UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
					return
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`)

			default:
				http.Error(w, "unknown action", http.StatusBadRequest)
			}

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))

	loc := srv.URL + "/rootDesc.xml"
	tr := &fakeSSDPTransport{
		responses: [][]byte{
			[]byte("HTTP/1.1 200 OK\r\nLOCATION: " + loc + "\r\n\r\n"),
		},
	}

	return srv, tr
}

// emptySSDPTransport 是一个不返回任何 location 的 ssdpTransport。
type emptySSDPTransport struct{}

func (emptySSDPTransport) SendRecv(ctx context.Context, msearch []byte, timeout time.Duration) ([][]byte, error) {
	return nil, nil
}

// slowSSDPTransport 延迟后返回空结果。
type slowSSDPTransport struct {
	delay time.Duration
}

func (s slowSSDPTransport) SendRecv(ctx context.Context, msearch []byte, timeout time.Duration) ([][]byte, error) {
	select {
	case <-time.After(s.delay):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ────────────────────────────────────────────────────────────────
// 测试用例
// ────────────────────────────────────────────────────────────────

// TestMapperNATPMPFirst：三协议都可用，NAT-PMP 先成功（PCP/UPnP 有延迟），
// 断言返回 NAT-PMP Mapping。
func TestMapperNATPMPFirst(t *testing.T) {
	// NAT-PMP：无延迟，立即成功。
	natSrv := newMapperNATPMPServer(t)
	defer natSrv.close()

	// PCP：延迟 500ms。
	pcpSrv := newMapperPCPServer(t, func(s *mapperPCPServer) {
		s.delay = 500 * time.Millisecond
	})
	defer pcpSrv.close()

	// UPnP：延迟 500ms。
	igdSrv, ssdpTr := mapperIGDServer(t, "203.0.113.99", "", 500*time.Millisecond)
	defer igdSrv.Close()

	mapper := New(Config{
		GatewayFn:     func() []string { return []string{natSrv.addr()} },
		SSDPTransport: ssdpTr,
		HTTPClient:    igdSrv.Client(),
		DialTimeout:   5 * time.Second,
	})

	// 使用独立的候选网关列表，NAT-PMP 和 PCP 共用同一个地址。
	// 但 NAT-PMP server 监听的端口和 PCP server 不同——所以这里需要让 GatewayFn
	// 返回 NAT-PMP server 的地址（含端口），而 PCP 因为地址不同不会命中。
	// 实际上，NAT-PMP 和 PCP 共用 5351 端口，这里 mock 返回不同地址即可。

	ctx := context.Background()
	m, err := mapper.Map(ctx, 51820, true, 7200*time.Second)
	if err != nil {
		t.Fatalf("Map: %v", err)
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
		t.Error("TransportUDP = false, want true")
	}
}

// TestMapperUPnPOnly：NAT-PMP/PCP 失败（result code 错误），UPnP 成功。
func TestMapperUPnPOnly(t *testing.T) {
	// NAT-PMP：返回错误 result code。
	natSrv := newMapperNATPMPServer(t, func(s *mapperNATPMPServer) {
		s.resultCode = 5 // 非零 → 失败
	})
	defer natSrv.close()

	// PCP：返回错误 result code。
	pcpSrv := newMapperPCPServer(t, func(s *mapperPCPServer) {
		s.resultCode = 8 // 非零 → 失败
	})
	defer pcpSrv.close()

	// UPnP：正常。
	igdSrv, ssdpTr := mapperIGDServer(t, "203.0.113.99", "", 0)
	defer igdSrv.Close()

	mapper := New(Config{
		GatewayFn: func() []string {
			return []string{natSrv.addr()}
		},
		SSDPTransport: ssdpTr,
		HTTPClient:    igdSrv.Client(),
		DialTimeout:   5 * time.Second,
	})

	ctx := context.Background()
	m, err := mapper.Map(ctx, 51820, true, 3600*time.Second)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	if m.Protocol != ProtocolUPnP {
		t.Errorf("Protocol = %v, want UPnP", m.Protocol)
	}
	if m.ExternalIP != "203.0.113.99" {
		t.Errorf("ExternalIP = %q, want 203.0.113.99", m.ExternalIP)
	}
	if m.InternalPort != 51820 {
		t.Errorf("InternalPort = %d, want 51820", m.InternalPort)
	}
}

// TestMapperAllFail：三协议全失败 → err，不阻塞。
func TestMapperAllFail(t *testing.T) {
	// NAT-PMP：返回错误。
	natSrv := newMapperNATPMPServer(t, func(s *mapperNATPMPServer) {
		s.resultCode = 5
	})
	defer natSrv.close()

	// PCP：返回错误。
	pcpSrv := newMapperPCPServer(t, func(s *mapperPCPServer) {
		s.resultCode = 8
	})
	defer pcpSrv.close()

	// UPnP：SOAP fault。
	igdSrv, ssdpTr := mapperIGDServer(t, "", "AddPortMapping", 0)
	defer igdSrv.Close()

	mapper := New(Config{
		GatewayFn: func() []string {
			return []string{natSrv.addr()}
		},
		SSDPTransport: ssdpTr,
		HTTPClient:    igdSrv.Client(),
		DialTimeout:   5 * time.Second,
	})

	start := time.Now()
	_, err := mapper.Map(context.Background(), 51820, true, 3600*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when all protocols fail, got nil")
	}
	// 不应阻塞过久（全部协议应很快返回错误）。
	if elapsed > 3*time.Second {
		t.Errorf("Map took %v, expected to complete quickly", elapsed)
	}
}

// TestMapperRefreshDispatch：按 Protocol 正确分派 Refresh。
func TestMapperRefreshDispatch(t *testing.T) {
	// NAT-PMP server 用于 refresh。
	natSrv := newMapperNATPMPServer(t)
	defer natSrv.close()

	// PCP server 用于 refresh。
	pcpSrv := newMapperPCPServer(t)
	defer pcpSrv.close()

	// UPnP server 用于 refresh。
	igdSrv, _ := mapperIGDServer(t, "203.0.113.99", "", 0)
	defer igdSrv.Close()

	mapper := New(Config{
		GatewayFn:   func() []string { return []string{natSrv.addr()} },
		HTTPClient:  igdSrv.Client(),
		DialTimeout: 5 * time.Second,
	})

	ctx := context.Background()

	t.Run("NAT-PMP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolNATPMP,
			InternalPort: 51820,
			ExternalPort: 40001,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      natSrv.addr(),
		}
		if err := mapper.Refresh(ctx, m); err != nil {
			t.Errorf("Refresh NAT-PMP: %v", err)
		}
	})

	t.Run("PCP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolPCP,
			InternalPort: 51820,
			ExternalPort: 40002,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      pcpSrv.addr(),
		}
		if err := mapper.Refresh(ctx, m); err != nil {
			t.Errorf("Refresh PCP: %v", err)
		}
	})

	t.Run("UPnP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolUPnP,
			InternalPort: 51820,
			ExternalPort: 51820,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      encodeIGDGateway(igdSrv.URL+"/ctl", serviceTypeWANIPConnection),
		}
		if err := mapper.Refresh(ctx, m); err != nil {
			t.Errorf("Refresh UPnP: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		m := &Mapping{Protocol: Protocol(99)}
		if err := mapper.Refresh(ctx, m); err == nil {
			t.Error("Refresh unknown protocol: expected error, got nil")
		}
	})
}

// TestMapperUnmapDispatch：按 Protocol 正确分派 Unmap。
func TestMapperUnmapDispatch(t *testing.T) {
	natSrv := newMapperNATPMPServer(t)
	defer natSrv.close()

	pcpSrv := newMapperPCPServer(t)
	defer pcpSrv.close()

	igdSrv, _ := mapperIGDServer(t, "203.0.113.99", "", 0)
	defer igdSrv.Close()

	mapper := New(Config{
		GatewayFn:   func() []string { return []string{natSrv.addr()} },
		HTTPClient:  igdSrv.Client(),
		DialTimeout: 5 * time.Second,
	})

	ctx := context.Background()

	t.Run("NAT-PMP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolNATPMP,
			InternalPort: 51820,
			ExternalPort: 40001,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      natSrv.addr(),
		}
		if err := mapper.Unmap(ctx, m); err != nil {
			t.Errorf("Unmap NAT-PMP: %v", err)
		}
	})

	t.Run("PCP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolPCP,
			InternalPort: 51820,
			ExternalPort: 40002,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      pcpSrv.addr(),
		}
		if err := mapper.Unmap(ctx, m); err != nil {
			t.Errorf("Unmap PCP: %v", err)
		}
	})

	t.Run("UPnP", func(t *testing.T) {
		m := &Mapping{
			Protocol:     ProtocolUPnP,
			InternalPort: 51820,
			ExternalPort: 51820,
			TransportUDP: true,
			TTL:          3600 * time.Second,
			Gateway:      encodeIGDGateway(igdSrv.URL+"/ctl", serviceTypeWANIPConnection),
		}
		if err := mapper.Unmap(ctx, m); err != nil {
			t.Errorf("Unmap UPnP: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		m := &Mapping{Protocol: Protocol(99)}
		if err := mapper.Unmap(ctx, m); err == nil {
			t.Error("Unmap unknown protocol: expected error, got nil")
		}
	})
}

// TestMapperNilMapping：Refresh(nil)/Unmap(nil) 返回 err，不 panic。
func TestMapperNilMapping(t *testing.T) {
	mapper := New(Config{
		GatewayFn:   func() []string { return nil },
		DialTimeout: time.Second,
	})
	ctx := context.Background()

	if err := mapper.Refresh(ctx, nil); err == nil {
		t.Error("Refresh(nil) should return error, got nil")
	}
	if err := mapper.Unmap(ctx, nil); err == nil {
		t.Error("Unmap(nil) should return error, got nil")
	}
}

// TestMapperTimeout：三协议都不响应 → DialTimeout 到达后返回 err。
// 断言耗时 < DialTimeout + margin，不无限阻塞。
func TestMapperTimeout(t *testing.T) {
	// NAT-PMP：静默。
	natSrv := newMapperNATPMPServer(t, func(s *mapperNATPMPServer) { s.silent = true })
	defer natSrv.close()

	// PCP：静默。
	pcpSrv := newMapperPCPServer(t, func(s *mapperPCPServer) { s.silent = true })
	defer pcpSrv.close()

	// UPnP：SSDP 超时（slowSSDPTransport 会被 ctx 取消）。
	slowTr := slowSSDPTransport{delay: 10 * time.Second}

	dialTimeout := 500 * time.Millisecond

	mapper := New(Config{
		GatewayFn:     func() []string { return []string{natSrv.addr()} },
		SSDPTransport: slowTr,
		DialTimeout:   dialTimeout,
	})

	start := time.Now()
	_, err := mapper.Map(context.Background(), 51820, true, 3600*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// 允许合理的 margin。natpmpExchange 内部有 natpmpMaxRetries(3) * natpmpTimeout(2s)
	// 的单协议超时，但 ctx 的 DialTimeout 会先到期取消它们。
	margin := 2 * time.Second
	if elapsed > dialTimeout+margin {
		t.Errorf("Map took %v, expected <= %v (DialTimeout %v + margin %v)", elapsed, dialTimeout+margin, dialTimeout, margin)
	}
}

// TestMapperRaceNoGateways：无候选网关时快速返回 error。
func TestMapperRaceNoGateways(t *testing.T) {
	mapper := New(Config{
		GatewayFn:     func() []string { return nil },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   time.Second,
	})

	start := time.Now()
	_, err := mapper.Map(context.Background(), 51820, true, 3600*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when no gateways, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Map took %v with no gateways, expected fast failure", elapsed)
	}
}

// TestMapperPCPWins：让 PCP 先成功（NAT-PMP 延迟 + UPnP 延迟）。
func TestMapperPCPWins(t *testing.T) {
	// NAT-PMP：延迟 1s。
	natSrv := newMapperNATPMPServer(t, func(s *mapperNATPMPServer) {
		s.delay = time.Second
	})
	defer natSrv.close()

	// PCP：无延迟（但 NAT-PMP 和 PCP 共用端口，这里 mock 是独立端口）。
	// 因为 GatewayFn 返回 NAT-PMP server 地址，PCP 会尝试同一地址。
	// 但 PCP 要 version=2 报文，NAT-PMP server 会当坏报文忽略。
	// 所以需要给 PCP 一个独立地址。
	pcpSrv := newMapperPCPServer(t)
	defer pcpSrv.close()

	// UPnP：延迟 1s。
	igdSrv, ssdpTr := mapperIGDServer(t, "203.0.113.99", "", time.Second)
	defer igdSrv.Close()

	// GatewayFn 返回两个地址：NAT-PMP server 和 PCP server。
	// NAT-PMP 尝试两个都会走 natpmpMap，PCP 尝试两个都会走 pcpMap。
	// 但 natpmpMap 发 version=0 报文给 PCP server（version=2），PCP server 忽略。
	// pcpMap 发 version=2 报文给 NAT-PMP server（version=0），NAT-PMP server 忽略。
	// 所以实际上 NAT-PMP 只命中 natSrv（有 1s 延迟），PCP 只命中 pcpSrv（无延迟）。
	mapper := New(Config{
		GatewayFn:     func() []string { return []string{natSrv.addr(), pcpSrv.addr()} },
		SSDPTransport: ssdpTr,
		HTTPClient:    igdSrv.Client(),
		DialTimeout:   5 * time.Second,
	})

	ctx := context.Background()
	m, err := mapper.Map(ctx, 51820, true, 3600*time.Second)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	if m.Protocol != ProtocolPCP {
		t.Errorf("Protocol = %v, want PCP", m.Protocol)
	}
	if m.ExternalPort != 40002 {
		t.Errorf("ExternalPort = %d, want 40002", m.ExternalPort)
	}
}

// TestMapperMapCancelsPeers：成功后其余 goroutine 能退出（-race 检测泄漏）。
func TestMapperMapCancelsPeers(t *testing.T) {
	// NAT-PMP：立即成功。
	natSrv := newMapperNATPMPServer(t)
	defer natSrv.close()

	// UPnP：返回空。
	mapper := New(Config{
		GatewayFn:     func() []string { return []string{natSrv.addr()} },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   5 * time.Second,
	})

	// 连续调用多次，-race 检测不应报告 data race。
	for range 5 {
		m, err := mapper.Map(context.Background(), 51820, true, 3600*time.Second)
		if err != nil {
			t.Fatalf("Map: %v", err)
		}
		if m.Protocol != ProtocolNATPMP {
			t.Errorf("Protocol = %v, want NAT-PMP", m.Protocol)
		}
	}
}

// TestMapperNewDefaults：验证 New 对零值 Config 填充合理默认值。
func TestMapperNewDefaults(t *testing.T) {
	m := New(Config{})
	if m.gatewayFn == nil {
		t.Error("gatewayFn should be non-nil")
	}
	if m.httpClient == nil {
		t.Error("httpClient should be non-nil")
	}
	if m.dialTimeout <= 0 {
		t.Error("dialTimeout should be positive")
	}
	if m.clock == nil {
		t.Error("clock should be non-nil")
	}
}

// TestMapperInterface：编译期验证 *DefaultMapper 实现 Mapper。
func TestMapperInterface(t *testing.T) {
	var _ Mapper = (*DefaultMapper)(nil)
}

// ────────────────────────────────────────────────────────────────
// Lifecycle 测试：mock Mapper + 注入 clock，确定性驱动
// ────────────────────────────────────────────────────────────────

// mockMapper 是 Lifecycle 测试用的可编程 Mapper mock。
type mockMapper struct {
	mu sync.Mutex

	mapFn      func(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error)
	refreshFn  func(ctx context.Context, m *Mapping) error
	unmapFn    func(ctx context.Context, m *Mapping) error
	mapCount   int
	refreshCnt int
	unmapCount int
}

func (mm *mockMapper) Map(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error) {
	mm.mu.Lock()
	mm.mapCount++
	fn := mm.mapFn
	mm.mu.Unlock()
	if fn != nil {
		return fn(ctx, internalPort, udp, ttl)
	}
	return &Mapping{
		Protocol:     ProtocolNATPMP,
		ExternalIP:   "198.51.100.1",
		ExternalPort: 40001,
		InternalPort: internalPort,
		TransportUDP: udp,
		TTL:          ttl,
		Gateway:      "127.0.0.1:5351",
	}, nil
}

func (mm *mockMapper) Refresh(ctx context.Context, m *Mapping) error {
	mm.mu.Lock()
	mm.refreshCnt++
	fn := mm.refreshFn
	mm.mu.Unlock()
	if fn != nil {
		return fn(ctx, m)
	}
	return nil
}

func (mm *mockMapper) Unmap(ctx context.Context, m *Mapping) error {
	mm.mu.Lock()
	mm.unmapCount++
	fn := mm.unmapFn
	mm.mu.Unlock()
	if fn != nil {
		return fn(ctx, m)
	}
	return nil
}

func (mm *mockMapper) getRefreshCount() int {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.refreshCnt
}

func (mm *mockMapper) getMapCount() int {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.mapCount
}

func (mm *mockMapper) getUnmapCount() int {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.unmapCount
}

// TestLifecycleRenew：Manage 一个 TTL=100s 的 Mapping → clock 推进到 50s（TTL/2）→
// Tick → Refresh 被调（mock 计数 +1）→ 保活。
func TestLifecycleRenew(t *testing.T) {
	mm := &mockMapper{}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock: func() time.Time { return now },
	})
	defer lc.Close()

	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}
	lc.Manage(m)

	// Tick 在 49s：不到续期点，不触发 Refresh。
	lc.Tick(now.Add(49 * time.Second))
	if got := mm.getRefreshCount(); got != 0 {
		t.Fatalf("Tick@49s: refreshCount = %d, want 0", got)
	}

	// Tick 在 50s（TTL/2）：到续期点，触发 Refresh。
	lc.Tick(now.Add(50 * time.Second))
	if got := mm.getRefreshCount(); got != 1 {
		t.Fatalf("Tick@50s: refreshCount = %d, want 1", got)
	}

	// Tick 在 99s：不到下一个续期点（50s + 50s = 100s），不触发。
	lc.Tick(now.Add(99 * time.Second))
	if got := mm.getRefreshCount(); got != 1 {
		t.Fatalf("Tick@99s: refreshCount = %d, want 1", got)
	}

	// Tick 在 100s：到第二个续期点（50s + 50s），触发。
	lc.Tick(now.Add(100 * time.Second))
	if got := mm.getRefreshCount(); got != 2 {
		t.Fatalf("Tick@100s: refreshCount = %d, want 2", got)
	}
}

// TestLifecycleRenewFail：Refresh 返回 error → OnMappingLost 被触发 → 退避后 Map 重建尝试。
func TestLifecycleRenewFail(t *testing.T) {
	mm := &mockMapper{
		refreshFn: func(ctx context.Context, m *Mapping) error {
			return errors.New("refresh failed")
		},
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var lostCount atomic.Int64
	var lostMapping atomic.Value

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock:       func() time.Time { return now },
		BackoffBase: 10 * time.Second,
		OnMappingLost: func(m *Mapping) {
			lostCount.Add(1)
			lostMapping.Store(m)
		},
	})
	defer lc.Close()

	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}
	lc.Manage(m)

	// Tick 在 50s（续期点）：Refresh 失败 → OnMappingLost 触发。
	lc.Tick(now.Add(50 * time.Second))
	if got := lostCount.Load(); got != 1 {
		t.Fatalf("lostCount = %d, want 1", got)
	}
	if got, ok := lostMapping.Load().(*Mapping); !ok || got != m {
		t.Fatalf("lostMapping = %v, want original mapping", got)
	}

	// 退避期（BackoffBase=10s），Tick 在 55s 不触发 Map 重建。
	lc.Tick(now.Add(55 * time.Second))
	if got := mm.getMapCount(); got != 0 {
		t.Fatalf("Tick@55s: mapCount = %d, want 0 (still in backoff)", got)
	}

	// Tick 在 60s（50s + 10s backoff）：Map 重建尝试。
	lc.Tick(now.Add(60 * time.Second))
	if got := mm.getMapCount(); got != 1 {
		t.Fatalf("Tick@60s: mapCount = %d, want 1", got)
	}
}

// TestLifecycleMultiMapping：Manage UDP+TCP 两个 Mapping → 一个 Refresh 失败只回调该入口
// 不影响另一个。
func TestLifecycleMultiMapping(t *testing.T) {
	var failUDP atomic.Bool
	failUDP.Store(true) // 只让 UDP 的 Refresh 失败。

	mm := &mockMapper{
		refreshFn: func(ctx context.Context, m *Mapping) error {
			if m.TransportUDP && failUDP.Load() {
				return errors.New("UDP refresh failed")
			}
			return nil
		},
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var lostMappings []*Mapping
	var lostMu sync.Mutex

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock:       func() time.Time { return now },
		BackoffBase: 10 * time.Second,
		OnMappingLost: func(m *Mapping) {
			lostMu.Lock()
			lostMappings = append(lostMappings, m)
			lostMu.Unlock()
		},
	})
	defer lc.Close()

	udpMapping := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}
	tcpMapping := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 443,
		ExternalPort: 40002,
		TransportUDP: false,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}

	lc.Manage(udpMapping)
	lc.Manage(tcpMapping)

	// Tick 在 50s：两个都到续期点。UDP Refresh 失败，TCP 成功。
	lc.Tick(now.Add(50 * time.Second))

	lostMu.Lock()
	if len(lostMappings) != 1 {
		t.Fatalf("lostMappings len = %d, want 1", len(lostMappings))
	}
	if lostMappings[0] != udpMapping {
		t.Fatalf("lostMapping should be UDP mapping, got %+v", lostMappings[0])
	}
	lostMu.Unlock()

	// TCP 的 Refresh 应该已被调用并成功（不在 lost 中）。
	// refreshCount: UDP 1 次（失败）+ TCP 1 次（成功）= 2。
	if got := mm.getRefreshCount(); got != 2 {
		t.Fatalf("refreshCount = %d, want 2", got)
	}
}

// TestLifecycleClose：Close → 所有活跃 Mapping 被 Unmap（mock Unmap 计数 == 活跃数）。
func TestLifecycleClose(t *testing.T) {
	mm := &mockMapper{}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock: func() time.Time { return now },
	})

	m1 := &Mapping{
		Protocol: ProtocolNATPMP, InternalPort: 51820, TransportUDP: true,
		TTL: 100 * time.Second, Gateway: "127.0.0.1:5351",
	}
	m2 := &Mapping{
		Protocol: ProtocolNATPMP, InternalPort: 443, TransportUDP: false,
		TTL: 100 * time.Second, Gateway: "127.0.0.1:5351",
	}
	lc.Manage(m1)
	lc.Manage(m2)

	lc.Close()

	if got := mm.getUnmapCount(); got != 2 {
		t.Fatalf("unmapCount = %d, want 2", got)
	}

	// Close 后 Tick 不 panic（no-op）。
	lc.Tick(now.Add(999 * time.Second))

	// Close 后 Manage 不 panic（no-op）。
	lc.Manage(&Mapping{TTL: 100 * time.Second})

	// Close 是幂等的。
	lc.Close()
	if got := mm.getUnmapCount(); got != 2 {
		t.Fatalf("second Close: unmapCount = %d, want 2 (idempotent)", got)
	}
}

// TestLifecycleCloseUnmapFail：Close 时 Unmap 失败不阻塞（best-effort）。
func TestLifecycleCloseUnmapFail(t *testing.T) {
	mm := &mockMapper{
		unmapFn: func(ctx context.Context, m *Mapping) error {
			return errors.New("unmap failed")
		},
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock: func() time.Time { return now },
	})

	lc.Manage(&Mapping{
		Protocol: ProtocolNATPMP, InternalPort: 51820, TransportUDP: true,
		TTL: 100 * time.Second, Gateway: "127.0.0.1:5351",
	})
	lc.Manage(&Mapping{
		Protocol: ProtocolNATPMP, InternalPort: 443, TransportUDP: false,
		TTL: 100 * time.Second, Gateway: "127.0.0.1:5351",
	})

	// Close 不应因 Unmap 失败而阻塞或 panic。
	lc.Close()

	// Unmap 仍被尝试调用。
	if got := mm.getUnmapCount(); got != 2 {
		t.Fatalf("unmapCount = %d, want 2", got)
	}
}

// TestLifecycleRebuild：Refresh 失败 → 退避到达 → Map 重建成功 →
// 新 Mapping 替换旧的 → 续期继续。
func TestLifecycleRebuild(t *testing.T) {
	refreshFail := true
	mm := &mockMapper{
		refreshFn: func(ctx context.Context, m *Mapping) error {
			if refreshFail {
				return errors.New("refresh failed")
			}
			return nil
		},
		mapFn: func(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error) {
			return &Mapping{
				Protocol:     ProtocolPCP,
				ExternalIP:   "198.51.100.99",
				ExternalPort: 50001,
				InternalPort: internalPort,
				TransportUDP: udp,
				TTL:          ttl,
				Gateway:      "127.0.0.1:5351",
			}, nil
		},
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock:       func() time.Time { return now },
		BackoffBase: 10 * time.Second,
	})
	defer lc.Close()

	original := &Mapping{
		Protocol:     ProtocolNATPMP,
		ExternalIP:   "198.51.100.1",
		ExternalPort: 40001,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}
	lc.Manage(original)

	// Tick@50s：Refresh 失败 → 进入退避重建。
	lc.Tick(now.Add(50 * time.Second))
	if got := mm.getRefreshCount(); got != 1 {
		t.Fatalf("refreshCount = %d, want 1", got)
	}

	// Tick@60s（50+10 backoff）：Map 重建成功。
	lc.Tick(now.Add(60 * time.Second))
	if got := mm.getMapCount(); got != 1 {
		t.Fatalf("mapCount = %d, want 1", got)
	}

	// 重建后，Refresh 不再失败。
	refreshFail = false

	// Tick@110s（60 + TTL/2=50）：新映射续期 → Refresh 成功。
	lc.Tick(now.Add(110 * time.Second))
	if got := mm.getRefreshCount(); got != 2 {
		t.Fatalf("refreshCount = %d, want 2 (after rebuild, normal renew)", got)
	}
}

// TestLifecycleNilCallback：OnMappingLost 为 nil → Refresh 失败不 panic。
func TestLifecycleNilCallback(t *testing.T) {
	mm := &mockMapper{
		refreshFn: func(ctx context.Context, m *Mapping) error {
			return errors.New("refresh failed")
		},
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock:         func() time.Time { return now },
		OnMappingLost: nil, // 显式 nil。
	})
	defer lc.Close()

	lc.Manage(&Mapping{
		Protocol: ProtocolNATPMP, InternalPort: 51820, TransportUDP: true,
		TTL: 100 * time.Second, Gateway: "127.0.0.1:5351",
	})

	// 不应 panic。
	lc.Tick(now.Add(50 * time.Second))

	if got := mm.getRefreshCount(); got != 1 {
		t.Fatalf("refreshCount = %d, want 1", got)
	}
}

// TestLifecycleBackoffExponential：连续失败 → backoff 从 BackoffBase 翻倍增长
// 至 BackoffMax 封顶。
//
// 场景：BackoffBase=10s, BackoffMax=60s。Refresh 失败后进入重建退避，Map 也
// 连续失败 → backoff 应为 10s → 20s → 40s → 60s（封顶）→ 60s。
func TestLifecycleBackoffExponential(t *testing.T) {
	mm := &mockMapper{
		refreshFn: func(ctx context.Context, m *Mapping) error {
			return errors.New("refresh always fails")
		},
		mapFn: func(ctx context.Context, internalPort uint16, udp bool, ttl time.Duration) (*Mapping, error) {
			return nil, errors.New("map always fails")
		},
	}

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	lc := NewLifecycle(mm, LifecycleConfig{
		Clock:       func() time.Time { return now },
		BackoffBase: 10 * time.Second,
		BackoffMax:  60 * time.Second,
	})
	defer lc.Close()

	m := &Mapping{
		Protocol:     ProtocolNATPMP,
		InternalPort: 51820,
		ExternalPort: 40001,
		TransportUDP: true,
		TTL:          100 * time.Second,
		Gateway:      "127.0.0.1:5351",
	}
	lc.Manage(m)

	// Tick@50s：续期失败 → 进入退避重建，backoff=10s，rebuildAt=50+10=60s。
	lc.Tick(now.Add(50 * time.Second))
	if got := mm.getRefreshCount(); got != 1 {
		t.Fatalf("after initial refresh fail: refreshCount = %d, want 1", got)
	}

	// 期望的退避序列及对应 Tick 时刻（每次 Map 失败后 backoff 翻倍）：
	//   retry 1: rebuildAt=60s,  backoff doubles to 20s → next rebuildAt=60+20=80s
	//   retry 2: rebuildAt=80s,  backoff doubles to 40s → next rebuildAt=80+40=120s
	//   retry 3: rebuildAt=120s, backoff doubles to 80s → capped at 60s → next rebuildAt=120+60=180s
	//   retry 4: rebuildAt=180s, backoff stays 60s (already capped)
	type step struct {
		tickAt      time.Duration // 触发重建的时刻（距 now）
		tooEarlyAt  time.Duration // 此时仍在退避中，不应触发重建
		wantMapCnt  int           // 到 tickAt 之后的累计 Map 调用数
		wantBackoff time.Duration // 此次重建失败后的 backoff（翻倍或封顶值）
	}

	steps := []step{
		{tickAt: 60 * time.Second, tooEarlyAt: 59 * time.Second, wantMapCnt: 1, wantBackoff: 20 * time.Second},
		{tickAt: 80 * time.Second, tooEarlyAt: 79 * time.Second, wantMapCnt: 2, wantBackoff: 40 * time.Second},
		{tickAt: 120 * time.Second, tooEarlyAt: 119 * time.Second, wantMapCnt: 3, wantBackoff: 60 * time.Second},
		{tickAt: 180 * time.Second, tooEarlyAt: 179 * time.Second, wantMapCnt: 4, wantBackoff: 60 * time.Second},
	}

	for i, s := range steps {
		// 退避期内 Tick 不应触发 Map。
		lc.Tick(now.Add(s.tooEarlyAt))
		if got := mm.getMapCount(); got != s.wantMapCnt-1 {
			t.Fatalf("step %d tooEarly: mapCount = %d, want %d", i, got, s.wantMapCnt-1)
		}

		// 到达退避时刻，触发 Map 重建（失败）。
		lc.Tick(now.Add(s.tickAt))
		if got := mm.getMapCount(); got != s.wantMapCnt {
			t.Fatalf("step %d rebuild: mapCount = %d, want %d", i, got, s.wantMapCnt)
		}

		// 验证当前 backoff 值（通过 entry 内部状态）。
		lc.mu.Lock()
		if len(lc.entries) != 1 {
			lc.mu.Unlock()
			t.Fatalf("step %d: entries len = %d, want 1", i, len(lc.entries))
		}
		gotBackoff := lc.entries[0].backoff
		lc.mu.Unlock()
		if gotBackoff != s.wantBackoff {
			t.Fatalf("step %d: backoff = %v, want %v", i, gotBackoff, s.wantBackoff)
		}
	}
}

// TestMapperRaceSeamDefaults：New 默认填充三协议函数 seam（非 nil），
// 且覆盖 natpmpMapFn 后 mapRace 会调用注入的实现而非包级默认。
func TestMapperRaceSeamDefaults(t *testing.T) {
	// 默认 seam 必须非 nil（否则 mapRace 会 panic）。
	def := New(Config{
		GatewayFn:     func() []string { return nil },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   time.Second,
	})
	if def.natpmpMapFn == nil || def.pcpMapFn == nil || def.igdMapFn == nil {
		t.Fatal("New 必须为三协议函数 seam 填充非 nil 默认值")
	}

	// 注入自定义 natpmpMapFn：返回成功，断言被 mapRace 调用。
	var called atomic.Bool
	m := New(Config{
		GatewayFn:     func() []string { return []string{"192.0.2.1:5351"} },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   time.Second,
	})
	m.natpmpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		called.Store(true)
		return &Mapping{Protocol: ProtocolNATPMP, ExternalPort: 51820, InternalPort: internal, Gateway: gw}, nil
	}
	// pcp/igd 注入恒失败，避免命中真实网络。
	m.pcpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		return nil, errors.New("pcp disabled in test")
	}
	m.igdMapFn = func(ctx context.Context, controlURL, serviceType string, internal, ext uint16, ic string, udp bool, ttlSec uint32, hc *http.Client) (*Mapping, error) {
		return nil, errors.New("igd disabled in test")
	}

	res, err := m.Map(context.Background(), 51820, true, 3600*time.Second)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if !called.Load() {
		t.Fatal("mapRace 未调用注入的 natpmpMapFn")
	}
	if res.Protocol != ProtocolNATPMP {
		t.Errorf("Protocol = %v, want NAT-PMP", res.Protocol)
	}
}

// TestMapperRaceLateSuccessNotDropped：构造「早失败 + 晚成功」竞速——
// 多个 fn 立即失败先占满 channel，唯一成功者延迟到达；旧实现 cap=1 + default
// 会把成功结果丢弃导致 Map 失败。修复后必须返回该成功映射。
func TestMapperRaceLateSuccessNotDropped(t *testing.T) {
	// 两个网关 → natpmp/pcp 各 2 个 fn + 1 个 upnp，共 5 个发送者。
	gws := []string{"192.0.2.1:5351", "192.0.2.2:5351"}
	m := New(Config{
		GatewayFn:     func() []string { return gws },
		SSDPTransport: emptySSDPTransport{}, // upnp 立即「无 IGD」失败
		DialTimeout:   2 * time.Second,
	})
	// natpmp 两个网关都立即失败（先占 channel）。
	m.natpmpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		return nil, errors.New("natpmp fail " + gw)
	}
	// pcp：第一个网关立即失败；第二个网关延迟 200ms 后成功（晚成功）。
	m.pcpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		if gw == gws[1] {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &Mapping{Protocol: ProtocolPCP, ExternalPort: 40002, InternalPort: internal, Gateway: gw}, nil
		}
		return nil, errors.New("pcp fail " + gw)
	}
	m.igdMapFn = func(ctx context.Context, controlURL, serviceType string, internal, ext uint16, ic string, udp bool, ttlSec uint32, hc *http.Client) (*Mapping, error) {
		return nil, errors.New("igd unused")
	}

	res, err := m.Map(context.Background(), 51820, true, 3600*time.Second)
	if err != nil {
		t.Fatalf("晚到的成功结果被丢弃了：Map 返回 err=%v", err)
	}
	if res.Protocol != ProtocolPCP || res.ExternalPort != 40002 {
		t.Errorf("got %v:%d, want PCP:40002", res.Protocol, res.ExternalPort)
	}
}

// TestMapperRaceWinnerUnmapsLosers：多个协议同时成功，胜者返回后，
// 后到的成功映射（loser）不应泄漏——须被 Unmap 清理。
func TestMapperRaceWinnerUnmapsLosers(t *testing.T) {
	var pcpUnmapped atomic.Bool
	m := New(Config{
		GatewayFn:     func() []string { return []string{"192.0.2.1:5351"} },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   2 * time.Second,
	})
	// natpmp 立即成功（胜者）。
	m.natpmpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		return &Mapping{Protocol: ProtocolNATPMP, ExternalPort: 51820, InternalPort: internal, Gateway: gw}, nil
	}
	// pcp 稍晚也成功（loser）—— 标记一个独特 Gateway 以便 Unmap 时识别。
	// 此处刻意不在 ctx 取消时提前返回：模拟一个「胜者已选定 + rctx 已 cancel」之后
	// 仍完成建链的 loser，验证其成功映射会被 mapRace 清理（不依赖 ctx 取消时序）。
	m.pcpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		time.Sleep(100 * time.Millisecond)
		return &Mapping{Protocol: ProtocolPCP, ExternalPort: 40002, InternalPort: internal, Gateway: "loser-pcp"}, nil
	}
	m.igdMapFn = func(ctx context.Context, controlURL, serviceType string, internal, ext uint16, ic string, udp bool, ttlSec uint32, hc *http.Client) (*Mapping, error) {
		return nil, errors.New("igd unused")
	}
	// 注入 unmap seam 捕获 loser 清理。
	m.unmapFn = func(ctx context.Context, mp *Mapping) error {
		if mp.Protocol == ProtocolPCP && mp.Gateway == "loser-pcp" {
			pcpUnmapped.Store(true)
		}
		return nil
	}

	res, err := m.Map(context.Background(), 51820, true, 3600*time.Second)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Protocol != ProtocolNATPMP {
		t.Fatalf("胜者应为 NAT-PMP，got %v", res.Protocol)
	}
	// 给后台清理 goroutine 一点时间。
	deadline := time.Now().Add(2 * time.Second)
	for !pcpUnmapped.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pcpUnmapped.Load() {
		t.Error("loser PCP 映射未被 Unmap，发生泄漏")
	}
}

// TestMapperRaceAllFailJoined（#34）：三协议全失败，聚合错误须含全部原因。
func TestMapperRaceAllFailJoined(t *testing.T) {
	m := New(Config{
		GatewayFn:     func() []string { return []string{"192.0.2.1:5351"} },
		SSDPTransport: emptySSDPTransport{},
		DialTimeout:   2 * time.Second,
	})
	m.natpmpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		return nil, errors.New("natpmp-reason")
	}
	m.pcpMapFn = func(ctx context.Context, gw string, internal, ext uint16, udp bool, ttlSec uint32) (*Mapping, error) {
		return nil, errors.New("pcp-reason")
	}
	m.igdMapFn = func(ctx context.Context, controlURL, serviceType string, internal, ext uint16, ic string, udp bool, ttlSec uint32, hc *http.Client) (*Mapping, error) {
		return nil, errors.New("igd-reason")
	}

	_, err := m.Map(context.Background(), 51820, true, 3600*time.Second)
	if err == nil {
		t.Fatal("全失败应返回 error")
	}
	msg := err.Error()
	for _, want := range []string{"natpmp-reason", "pcp-reason"} {
		if !strings.Contains(msg, want) {
			t.Errorf("聚合错误缺少 %q：%s", want, msg)
		}
	}
}

// TestLifecycleNoSelfDeleteOnZeroTTL 复现 bug #20 的最终后果：若一个 TTL=0 的
// Mapping 进入 Lifecycle，RenewInterval(0)=0 会让 renewAt=now，每个 Tick 立即
// "续期"——而 NAT-PMP/PCP 续期复用 Map 报文、lifetime=0 即删除自己。这里断言
// Manage 一个 TTL=0 映射后，前若干个 Tick 不会触发反复的 Refresh 风暴。
func TestLifecycleNoSelfDeleteOnZeroTTL(t *testing.T) {
	mm := &mockMapper{}
	now := time.Unix(1_700_000_000, 0)
	lc := NewLifecycle(mm, LifecycleConfig{Clock: func() time.Time { return now }})

	// 注意：正常路径下 Map 层（#20a/#20b）已拦截 TTL=0，这里直接构造 TTL=0
	// 模拟"漏网"映射，验证 Lifecycle 层不会陷入每 Tick 删自己的循环。
	lc.Manage(&Mapping{Protocol: ProtocolNATPMP, InternalPort: 51820, TransportUDP: true, TTL: 0})

	for i := 0; i < 5; i++ {
		now = now.Add(time.Second)
		lc.Tick(now)
	}
	if got := mm.getRefreshCount(); got > 1 {
		t.Errorf("TTL=0 映射触发 Refresh 风暴：%d 次（want <=1），疑似每 Tick 删自己", got)
	}
}
