package portmap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- SSDP M-SEARCH 构造 ---

func TestBuildMSearch(t *testing.T) {
	b := string(buildMSearch(2))
	mustContain := []string{
		"M-SEARCH * HTTP/1.1\r\n",
		"HOST: 239.255.255.250:1900\r\n",
		"MAN: \"ssdp:discover\"\r\n", // MAN 值带双引号
		"ST: " + igdDeviceST + "\r\n",
		"MX: 2\r\n",
	}
	for _, s := range mustContain {
		if !strings.Contains(b, s) {
			t.Errorf("m-search missing %q\n--- got ---\n%q", s, b)
		}
	}
	if !strings.HasSuffix(b, "\r\n\r\n") {
		t.Errorf("m-search should end with blank line (CRLFCRLF), got %q", b)
	}
}

// --- SSDP LOCATION 解析（纯函数） ---

func TestParseSSDPLocation(t *testing.T) {
	cases := []struct {
		name    string
		resp    string
		wantLoc string
		wantOK  bool
	}{
		{
			name:    "normal",
			resp:    "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLOCATION: http://192.168.1.1:5000/rootDesc.xml\r\nST: urn:foo\r\n\r\n",
			wantLoc: "http://192.168.1.1:5000/rootDesc.xml",
			wantOK:  true,
		},
		{
			name:    "mixed case header",
			resp:    "HTTP/1.1 200 OK\r\nLoCaTiOn: http://10.0.0.1:1900/desc.xml\r\n\r\n",
			wantLoc: "http://10.0.0.1:1900/desc.xml",
			wantOK:  true,
		},
		{
			name:    "extra whitespace",
			resp:    "HTTP/1.1 200 OK\r\nLOCATION:    http://172.16.0.1/d.xml   \r\n\r\n",
			wantLoc: "http://172.16.0.1/d.xml",
			wantOK:  true,
		},
		{
			name:    "header order arbitrary",
			resp:    "HTTP/1.1 200 OK\r\nSERVER: x\r\nUSN: y\r\nLOCATION: http://a/b.xml\r\nEXT:\r\n\r\n",
			wantLoc: "http://a/b.xml",
			wantOK:  true,
		},
		{
			name:   "no location",
			resp:   "HTTP/1.1 200 OK\r\nSERVER: x\r\nST: urn:foo\r\n\r\n",
			wantOK: false,
		},
		{
			name:   "empty location value",
			resp:   "HTTP/1.1 200 OK\r\nLOCATION:   \r\n\r\n",
			wantOK: false,
		},
		{
			name:   "garbage no panic",
			resp:   "not-an-http-response-at-all",
			wantOK: false,
		},
		{
			name:   "empty",
			resp:   "",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc, ok := parseSSDPLocation([]byte(c.resp))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (loc=%q)", ok, c.wantOK, loc)
			}
			if ok && loc != c.wantLoc {
				t.Errorf("loc = %q, want %q", loc, c.wantLoc)
			}
		})
	}
}

// --- ssdpDiscover：注入 fake transport ---

// fakeSSDPTransport 返回预设响应字节，不依赖真实组播网络。
type fakeSSDPTransport struct {
	responses  [][]byte
	err        error
	gotMSearch []byte
}

func (f *fakeSSDPTransport) SendRecv(ctx context.Context, msearch []byte, timeout time.Duration) ([][]byte, error) {
	f.gotMSearch = append([]byte(nil), msearch...)
	if f.err != nil {
		return nil, f.err
	}
	return f.responses, nil
}

func TestSSDPDiscoverDedup(t *testing.T) {
	loc := "http://192.168.1.1:5000/rootDesc.xml"
	tr := &fakeSSDPTransport{
		responses: [][]byte{
			[]byte("HTTP/1.1 200 OK\r\nLOCATION: " + loc + "\r\n\r\n"),
			[]byte("HTTP/1.1 200 OK\r\nLOCATION: " + loc + "\r\n\r\n"), // 重复 → 去重
			[]byte("HTTP/1.1 200 OK\r\nSERVER: x\r\n\r\n"),             // 无 LOCATION → 跳过
			[]byte("HTTP/1.1 200 OK\r\nLOCATION: http://10.0.0.1/d.xml\r\n\r\n"),
		},
	}
	got, err := ssdpDiscoverWith(context.Background(), tr, 2*time.Second)
	if err != nil {
		t.Fatalf("ssdpDiscoverWith: %v", err)
	}
	want := []string{loc, "http://10.0.0.1/d.xml"}
	if len(got) != len(want) {
		t.Fatalf("got %d locations %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("location[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// 验证发出的 m-search 报文正确。
	if !strings.Contains(string(tr.gotMSearch), "MAN: \"ssdp:discover\"") {
		t.Errorf("m-search not well-formed: %q", tr.gotMSearch)
	}
}

func TestSSDPDiscoverEmpty(t *testing.T) {
	tr := &fakeSSDPTransport{responses: nil} // 无响应（超时模拟）
	got, err := ssdpDiscoverWith(context.Background(), tr, time.Second)
	if err != nil {
		t.Fatalf("ssdpDiscoverWith: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestSSDPDiscoverCanceledCtxNoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	tr := &fakeSSDPTransport{responses: [][]byte{
		[]byte("HTTP/1.1 200 OK\r\nLOCATION: http://a/b.xml\r\n\r\n"),
	}}
	// 不应 panic；返回结果取决于 fake（fake 不检查 ctx，这里仅验证不 panic）。
	if _, err := ssdpDiscoverWith(ctx, tr, time.Second); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

// --- fetchControlURL：httptest.Server ---

// deviceXML：WAN service 嵌在 deviceList 子 device，controlURL 为相对路径。
const deviceXMLWANIP = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/ctl/IPConn</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`

func TestFetchControlURLRelative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(deviceXMLWANIP))
	}))
	defer srv.Close()

	loc := srv.URL + "/rootDesc.xml"
	ctl, st, err := fetchControlURL(context.Background(), loc, srv.Client())
	if err != nil {
		t.Fatalf("fetchControlURL: %v", err)
	}
	wantCtl := srv.URL + "/ctl/IPConn"
	if ctl != wantCtl {
		t.Errorf("controlURL = %q, want %q (absolute, resolved vs location)", ctl, wantCtl)
	}
	if st != serviceTypeWANIPConnection {
		t.Errorf("serviceType = %q, want %q", st, serviceTypeWANIPConnection)
	}
}

func TestFetchControlURLURLBasePriority(t *testing.T) {
	const base = "http://192.168.50.1:6000"
	xml := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <URLBase>` + base + `</URLBase>
  <device>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
        <controlURL>/ctl/IPConn</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()

	// location 与 URLBase 不同：断言用 URLBase 解析（优先）。
	ctl, _, err := fetchControlURL(context.Background(), srv.URL+"/desc.xml", srv.Client())
	if err != nil {
		t.Fatalf("fetchControlURL: %v", err)
	}
	want := base + "/ctl/IPConn"
	if ctl != want {
		t.Errorf("controlURL = %q, want %q (URLBase priority)", ctl, want)
	}
}

func TestFetchControlURLWANPPPFallback(t *testing.T) {
	// 无 WANIPConnection，只有 WANPPPConnection → fallback。
	xml := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceList>
      <device>
        <serviceList>
          <service>
            <serviceType>urn:schemas-upnp-org:service:WANPPPConnection:1</serviceType>
            <controlURL>/ctl/PPPConn</controlURL>
          </service>
        </serviceList>
      </device>
    </deviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()

	ctl, st, err := fetchControlURL(context.Background(), srv.URL+"/d.xml", srv.Client())
	if err != nil {
		t.Fatalf("fetchControlURL: %v", err)
	}
	if st != serviceTypeWANPPPConnection {
		t.Errorf("serviceType = %q, want %q", st, serviceTypeWANPPPConnection)
	}
	if want := srv.URL + "/ctl/PPPConn"; ctl != want {
		t.Errorf("controlURL = %q, want %q", ctl, want)
	}
}

func TestFetchControlURLErrors(t *testing.T) {
	t.Run("bad xml", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<root><<<not xml"))
		}))
		defer srv.Close()
		if _, _, err := fetchControlURL(context.Background(), srv.URL+"/d.xml", srv.Client()); err == nil {
			t.Error("expected error for bad xml, got nil")
		}
	})

	t.Run("no wan service", func(t *testing.T) {
		xml := `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:Layer3Forwarding:1</serviceType>
        <controlURL>/ctl/L3F</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(xml))
		}))
		defer srv.Close()
		if _, _, err := fetchControlURL(context.Background(), srv.URL+"/d.xml", srv.Client()); err == nil {
			t.Error("expected error for no WAN service, got nil")
		}
	})

	t.Run("http 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusNotFound)
		}))
		defer srv.Close()
		if _, _, err := fetchControlURL(context.Background(), srv.URL+"/d.xml", srv.Client()); err == nil {
			t.Error("expected error for HTTP 404, got nil")
		}
	})
}

// ============================ SOAP 操作（U2.2） ============================

// igdControlHandler 是 mock IGD control 端点的 HTTP handler。按 SOAPAction header
// 区分操作，返回对应 SOAP 响应。
func igdControlHandler(t *testing.T, extIP string, faultAction string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		soapAction := r.Header.Get("SOAPAction")
		contentType := r.Header.Get("Content-Type")
		if !strings.Contains(contentType, "text/xml") {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}

		// 读取请求 body（供断言用）。
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		_ = body

		// 根据 SOAPAction 返回响应。
		switch {
		case strings.Contains(soapAction, "AddPortMapping"):
			if faultAction == "AddPortMapping" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(soapFaultBody("ConflictInMappingEntry")))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
  </s:Body>
</s:Envelope>`))

		case strings.Contains(soapAction, "GetExternalIPAddress"):
			if faultAction == "GetExternalIPAddress" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(soapFaultBody("ActionFailed")))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>` + extIP + `</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`))

		case strings.Contains(soapAction, "DeletePortMapping"):
			if faultAction == "DeletePortMapping" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(soapFaultBody("NoSuchEntryInArray")))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
  </s:Body>
</s:Envelope>`))

		default:
			http.Error(w, "unknown action: "+soapAction, http.StatusBadRequest)
		}
	}
}

// soapFaultBody 生成标准 SOAP fault 响应 XML。
func soapFaultBody(desc string) string {
	return `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <s:Fault>
      <faultcode>s:Client</faultcode>
      <faultstring>UPnPError</faultstring>
      <detail>
        <UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
          <errorCode>718</errorCode>
          <errorDescription>` + desc + `</errorDescription>
        </UPnPError>
      </detail>
    </s:Fault>
  </s:Body>
</s:Envelope>`
}

func TestIGDMapSuccess(t *testing.T) {
	const wantExtIP = "203.0.113.1"
	srv := httptest.NewServer(igdControlHandler(t, wantExtIP, ""))
	defer srv.Close()

	m, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		51820, 51820, "192.168.1.100", true, 3600, srv.Client())
	if err != nil {
		t.Fatalf("igdMap: %v", err)
	}

	if m.Protocol != ProtocolUPnP {
		t.Errorf("Protocol = %v, want %v", m.Protocol, ProtocolUPnP)
	}
	if m.ExternalIP != wantExtIP {
		t.Errorf("ExternalIP = %q, want %q", m.ExternalIP, wantExtIP)
	}
	if m.ExternalPort != 51820 {
		t.Errorf("ExternalPort = %d, want 51820", m.ExternalPort)
	}
	if m.InternalPort != 51820 {
		t.Errorf("InternalPort = %d, want 51820", m.InternalPort)
	}
	if !m.TransportUDP {
		t.Error("TransportUDP = false, want true")
	}
	if m.TTL != 3600*time.Second {
		t.Errorf("TTL = %v, want %v", m.TTL, 3600*time.Second)
	}

	// Gateway 含 controlURL。
	if !strings.Contains(m.Gateway, srv.URL+"/ctl") {
		t.Errorf("Gateway %q should contain controlURL %q", m.Gateway, srv.URL+"/ctl")
	}
	// Gateway 含 serviceType（tab 分隔编码）。
	if !strings.Contains(m.Gateway, serviceTypeWANIPConnection) {
		t.Errorf("Gateway %q should contain serviceType", m.Gateway)
	}
}

func TestIGDMapTCP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		soapAction := r.Header.Get("SOAPAction")

		switch {
		case strings.Contains(soapAction, "AddPortMapping"):
			// 断言 NewProtocol = TCP。
			if !strings.Contains(string(body), "<NewProtocol>TCP</NewProtocol>") {
				t.Errorf("expected TCP protocol in request body, got:\n%s", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`))

		case strings.Contains(soapAction, "GetExternalIPAddress"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewExternalIPAddress>198.51.100.1</NewExternalIPAddress>
</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`))
		}
	}))
	defer srv.Close()

	m, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		8080, 8080, "192.168.1.50", false, 7200, srv.Client())
	if err != nil {
		t.Fatalf("igdMap TCP: %v", err)
	}
	if m.TransportUDP {
		t.Error("TransportUDP = true, want false for TCP")
	}
}

func TestIGDMapSOAPFault(t *testing.T) {
	srv := httptest.NewServer(igdControlHandler(t, "1.2.3.4", "AddPortMapping"))
	defer srv.Close()

	_, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		51820, 51820, "192.168.1.100", true, 3600, srv.Client())
	if err == nil {
		t.Fatal("expected error for SOAP fault, got nil")
	}
	if !strings.Contains(err.Error(), "ConflictInMappingEntry") {
		t.Errorf("error should contain fault description, got: %v", err)
	}
}

// igdConflictThenSuccessHandler 模拟「第一次 AddPortMapping 因端口冲突失败，
// 之后换端口重试成功」的网关：当请求体中外部端口 == conflictPort 时回 718 fault，
// 否则成功。记录所有被尝试过的外部端口供断言。
func igdConflictThenSuccessHandler(t *testing.T, conflictPort uint16, extIP string, tried *[]uint16) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		soapAction := r.Header.Get("SOAPAction")
		switch {
		case strings.Contains(soapAction, "AddPortMapping"):
			// 从请求体里抠出 NewExternalPort 值。
			s := string(body)
			start := strings.Index(s, "<NewExternalPort>") + len("<NewExternalPort>")
			end := strings.Index(s, "</NewExternalPort>")
			p, _ := strconv.ParseUint(s[start:end], 10, 16)
			*tried = append(*tried, uint16(p))
			if uint16(p) == conflictPort {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(soapFaultBody("ConflictInMappingEntry")))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`))
		case strings.Contains(soapAction, "GetExternalIPAddress"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewExternalIPAddress>` + extIP + `</NewExternalIPAddress>
</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`))
		default:
			http.Error(w, "unknown action: "+soapAction, http.StatusBadRequest)
		}
	}
}

// TestIGDMapRetriesOnConflict：第一轮外部端口冲突（718）→ 第二轮应换一个不同端口
// 而非重复同端口，并最终映射成功。
func TestIGDMapRetriesOnConflict(t *testing.T) {
	const wantExtIP = "203.0.113.7"
	var tried []uint16
	srv := httptest.NewServer(igdConflictThenSuccessHandler(t, 51820, wantExtIP, &tried))
	defer srv.Close()

	m, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		51820, 51820, "192.168.1.100", true, 3600, srv.Client())
	if err != nil {
		t.Fatalf("igdMap should succeed after retrying a new port, got: %v", err)
	}
	if len(tried) < 2 {
		t.Fatalf("expected at least 2 AddPortMapping attempts (conflict then retry), got %d: %v", len(tried), tried)
	}
	// 第一轮必须是请求的 51820；第二轮必须换成不同端口（不能重复）。
	if tried[0] != 51820 {
		t.Errorf("first attempt port = %d, want 51820", tried[0])
	}
	if tried[1] == tried[0] {
		t.Errorf("second attempt repeated conflicting port %d instead of switching", tried[1])
	}
	// 返回的 Mapping 外部端口必须是最终成功的那个（非冲突端口）。
	if m.ExternalPort == 51820 {
		t.Errorf("Mapping.ExternalPort = %d, must differ from conflicting port 51820", m.ExternalPort)
	}
	if m.ExternalIP != wantExtIP {
		t.Errorf("ExternalIP = %q, want %q", m.ExternalIP, wantExtIP)
	}
}

// TestIGDMapConflictExhaustsCandidates：所有候选端口都冲突 → 返回 error（含冲突描述），
// 不无限循环。用 conflictPort=0 的特殊语义让 handler 对一切端口都回 718。
func TestIGDMapConflictExhaustsCandidates(t *testing.T) {
	var tried []uint16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.Header.Get("SOAPAction"), "AddPortMapping") {
			s := string(body)
			start := strings.Index(s, "<NewExternalPort>") + len("<NewExternalPort>")
			end := strings.Index(s, "</NewExternalPort>")
			p, _ := strconv.ParseUint(s[start:end], 10, 16)
			tried = append(tried, uint16(p))
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(soapFaultBody("ConflictInMappingEntry")))
	}))
	defer srv.Close()

	_, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		51820, 51820, "192.168.1.100", true, 3600, srv.Client())
	if err == nil {
		t.Fatal("expected error when every candidate port conflicts, got nil")
	}
	if !strings.Contains(err.Error(), "ConflictInMappingEntry") {
		t.Errorf("error should preserve conflict description, got: %v", err)
	}
	// 必须有限次尝试（>1 表示真的换过端口，但不能无界）。
	if len(tried) < 2 {
		t.Errorf("expected multiple bounded retries, got %d: %v", len(tried), tried)
	}
}

func TestIGDMapGetIPFault(t *testing.T) {
	// AddPortMapping 成功但 GetExternalIPAddress fault。
	srv := httptest.NewServer(igdControlHandler(t, "", "GetExternalIPAddress"))
	defer srv.Close()

	_, err := igdMap(context.Background(), srv.URL+"/ctl", serviceTypeWANIPConnection,
		51820, 51820, "192.168.1.100", true, 3600, srv.Client())
	if err == nil {
		t.Fatal("expected error when GetExternalIPAddress faults, got nil")
	}
	if !strings.Contains(err.Error(), "ActionFailed") {
		t.Errorf("error should contain fault description, got: %v", err)
	}
}

func TestIGDRefresh(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	m := &Mapping{
		Protocol:     ProtocolUPnP,
		ExternalIP:   "203.0.113.1",
		ExternalPort: 51820,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          3600 * time.Second,
		Gateway:      encodeIGDGateway(srv.URL+"/ctl", serviceTypeWANIPConnection),
	}

	err := igdRefresh(context.Background(), m, "192.168.1.100", srv.Client())
	if err != nil {
		t.Fatalf("igdRefresh: %v", err)
	}

	// 断言请求 body 正确：同端口同参数。
	if !strings.Contains(gotBody, "<NewExternalPort>51820</NewExternalPort>") {
		t.Errorf("refresh body missing NewExternalPort=51820:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "<NewInternalPort>51820</NewInternalPort>") {
		t.Errorf("refresh body missing NewInternalPort=51820:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "<NewProtocol>UDP</NewProtocol>") {
		t.Errorf("refresh body missing NewProtocol=UDP:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "<NewLeaseDuration>3600</NewLeaseDuration>") {
		t.Errorf("refresh body missing NewLeaseDuration=3600:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "AddPortMapping") {
		t.Errorf("refresh should use AddPortMapping action:\n%s", gotBody)
	}
}

func TestIGDUnmap(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		soapAction := r.Header.Get("SOAPAction")
		if !strings.Contains(soapAction, "DeletePortMapping") {
			t.Errorf("expected DeletePortMapping action, got %q", soapAction)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:DeletePortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
</s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	m := &Mapping{
		Protocol:     ProtocolUPnP,
		ExternalPort: 51820,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          3600 * time.Second,
		Gateway:      encodeIGDGateway(srv.URL+"/ctl", serviceTypeWANIPConnection),
	}

	err := igdUnmap(context.Background(), m, srv.Client())
	if err != nil {
		t.Fatalf("igdUnmap: %v", err)
	}

	// 断言 DeletePortMapping body 正确。
	if !strings.Contains(gotBody, "<NewExternalPort>51820</NewExternalPort>") {
		t.Errorf("unmap body missing NewExternalPort=51820:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "<NewProtocol>UDP</NewProtocol>") {
		t.Errorf("unmap body missing NewProtocol=UDP:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "<NewRemoteHost></NewRemoteHost>") {
		t.Errorf("unmap body missing empty NewRemoteHost:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "DeletePortMapping") {
		t.Errorf("unmap body should contain DeletePortMapping:\n%s", gotBody)
	}
}

func TestIGDUnmapFault(t *testing.T) {
	srv := httptest.NewServer(igdControlHandler(t, "", "DeletePortMapping"))
	defer srv.Close()

	m := &Mapping{
		Protocol:     ProtocolUPnP,
		ExternalPort: 51820,
		InternalPort: 51820,
		TransportUDP: true,
		Gateway:      encodeIGDGateway(srv.URL+"/ctl", serviceTypeWANIPConnection),
	}

	err := igdUnmap(context.Background(), m, srv.Client())
	if err == nil {
		t.Fatal("expected error for DeletePortMapping fault, got nil")
	}
	if !strings.Contains(err.Error(), "NoSuchEntryInArray") {
		t.Errorf("error should contain fault description, got: %v", err)
	}
}

func TestSOAPRequestBadXML(t *testing.T) {
	// 响应 200 但 body 是坏 XML。soapRequest 本身不解析 XML（交给调用方），
	// 但我们测试 igdGetExternalIP 面对坏 XML 不 panic。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<root><<<not xml at all"))
	}))
	defer srv.Close()

	_, err := igdGetExternalIP(context.Background(), srv.Client(), srv.URL+"/ctl", serviceTypeWANIPConnection)
	if err == nil {
		t.Fatal("expected error for bad XML response, got nil")
	}
}

func TestDecodeIGDGatewayRoundtrip(t *testing.T) {
	ctl := "http://192.168.1.1:5000/ctl/IPConn"
	st := serviceTypeWANIPConnection
	gw := encodeIGDGateway(ctl, st)
	gotCtl, gotSt, err := decodeIGDGateway(gw)
	if err != nil {
		t.Fatalf("decodeIGDGateway: %v", err)
	}
	if gotCtl != ctl {
		t.Errorf("controlURL = %q, want %q", gotCtl, ctl)
	}
	if gotSt != st {
		t.Errorf("serviceType = %q, want %q", gotSt, st)
	}
}

func TestDecodeIGDGatewayMalformed(t *testing.T) {
	for _, gw := range []string{"", "no-tab-here", "\t", "url\t", "\ttype"} {
		if _, _, err := decodeIGDGateway(gw); err == nil {
			t.Errorf("decodeIGDGateway(%q): expected error, got nil", gw)
		}
	}
}

func TestIGDNilMapping(t *testing.T) {
	ctx := context.Background()
	if err := igdRefresh(ctx, nil, "192.168.1.1", nil); err == nil {
		t.Error("igdRefresh(nil) should error")
	}
	if err := igdUnmap(ctx, nil, nil); err == nil {
		t.Error("igdUnmap(nil) should error")
	}
}
