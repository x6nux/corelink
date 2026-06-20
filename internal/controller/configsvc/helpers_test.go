package configsvc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// ─── stripIPMask 测试 ────────────────────────────────────────────────────────

func TestStripIPMask(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"100.64.0.1/32", "100.64.0.1"},
		{"10.0.0.1/24", "10.0.0.1"},
		{"fd00::1/128", "fd00::1"},
		{"192.168.1.1", "192.168.1.1"}, // 无掩码，原样返回
		{"", ""},                       // 空字符串
		{"1.2.3.4/", "1.2.3.4"},        // 末尾有 / 但无数字
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripIPMask(tt.input)
			if got != tt.want {
				t.Errorf("stripIPMask(%q) = %q, 期望 %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── parseEndpoint 测试 ──────────────────────────────────────────────────────

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		host   string
		port   uint32
	}{
		{"正常 host:port", "relay.example.com:443", true, "relay.example.com", 443},
		{"IP:port", "10.0.0.1:8080", true, "10.0.0.1", 8080},
		{"端口 0", "host:0", true, "host", 0},
		{"无端口", "host-only", false, "", 0},
		{"空字符串", "", false, "", 0},
		{"IPv6", "[::1]:443", true, "::1", 443},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := parseEndpoint(tt.input)
			if tt.wantOK {
				if ep == nil {
					t.Fatalf("parseEndpoint(%q) = nil, 期望非 nil", tt.input)
				}
				if ep.Host != tt.host {
					t.Errorf("Host = %q, 期望 %q", ep.Host, tt.host)
				}
				if ep.Port != tt.port {
					t.Errorf("Port = %d, 期望 %d", ep.Port, tt.port)
				}
			} else {
				if ep != nil {
					t.Errorf("parseEndpoint(%q) = %+v, 期望 nil", tt.input, ep)
				}
			}
		})
	}
}

// ─── extractClientUint 测试 ──────────────────────────────────────────────────

func TestExtractClientUint(t *testing.T) {
	tests := []struct {
		name  string
		query string
		param string
		want  uint64
	}{
		{"正常值", "?generation=42", "generation", 42},
		{"零值", "?generation=0", "generation", 0},
		{"大数", "?epoch=18446744073709551615", "epoch", 18446744073709551615},
		{"无参数", "", "generation", 0},
		{"非法值", "?generation=abc", "generation", 0},
		{"负数", "?generation=-1", "generation", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/path"+tt.query, nil)
			got := extractClientUint(req, tt.param)
			if got != tt.want {
				t.Errorf("extractClientUint = %d, 期望 %d", got, tt.want)
			}
		})
	}
}

// ─── extractClientGeneration 测试 ────────────────────────────────────────────

func TestExtractClientGeneration(t *testing.T) {
	// query 参数优先于 If-None-Match。
	t.Run("query 优先", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/path?generation=10", nil)
		req.Header.Set("If-None-Match", "99")
		got := extractClientGeneration(req)
		if got != 10 {
			t.Errorf("got %d, 期望 10（query 优先）", got)
		}
	})

	t.Run("回退 If-None-Match", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/path", nil)
		req.Header.Set("If-None-Match", "55")
		got := extractClientGeneration(req)
		if got != 55 {
			t.Errorf("got %d, 期望 55", got)
		}
	})

	t.Run("均无", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/path", nil)
		got := extractClientGeneration(req)
		if got != 0 {
			t.Errorf("got %d, 期望 0", got)
		}
	})

	t.Run("If-None-Match 非数字", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/path", nil)
		req.Header.Set("If-None-Match", "W/\"abc\"")
		got := extractClientGeneration(req)
		if got != 0 {
			t.Errorf("got %d, 期望 0", got)
		}
	})
}

// ─── NodeIDFromTLSCerts 测试 ─────────────────────────────────────────────────

func TestNodeIDFromTLSCerts_NilState(t *testing.T) {
	_, err := NodeIDFromTLSCerts(nil)
	if err == nil {
		t.Fatal("nil TLS state 应返回错误")
	}
}

func TestNodeIDFromTLSCerts_NoCerts(t *testing.T) {
	state := &tls.ConnectionState{}
	_, err := NodeIDFromTLSCerts(state)
	if err == nil {
		t.Fatal("无证书应返回错误")
	}
}

func TestNodeIDFromTLSCerts_EmptyCN(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{}, // CN 为空
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	_, err := NodeIDFromTLSCerts(state)
	if err == nil {
		t.Fatal("空 CN 应返回错误")
	}
}

func TestNodeIDFromTLSCerts_OK(t *testing.T) {
	state := fakeTLSState("test-node-id")
	nodeID, err := NodeIDFromTLSCerts(state)
	if err != nil {
		t.Fatalf("期望成功, 得到错误: %v", err)
	}
	if nodeID != "test-node-id" {
		t.Errorf("nodeID = %q, 期望 test-node-id", nodeID)
	}
}

// ─── StoreAdapter 测试 ───────────────────────────────────────────────────────

func TestStoreAdapter_GetNodeInfo(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{
		ID: "sa-node", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.10/32", Generation: 5,
	}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)

	info, err := adapter.GetNodeInfo("sa-node")
	if err != nil {
		t.Fatalf("GetNodeInfo: %v", err)
	}
	if info.Generation != 5 {
		t.Errorf("Generation = %d, 期望 5", info.Generation)
	}
}

func TestStoreAdapter_GetNodeInfoNotFound(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)
	_, err = adapter.GetNodeInfo("nonexistent")
	if err == nil {
		t.Fatal("不存在的节点应返回错误")
	}
}

func TestStoreAdapter_BumpGeneration(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{
		ID: "bump-node", Role: "node", WGPubKey: "pk2", VirtualIP: "100.64.0.11/32",
	}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)

	gen, err := adapter.BumpGeneration("bump-node")
	if err != nil {
		t.Fatalf("BumpGeneration: %v", err)
	}
	if gen != 1 {
		t.Errorf("BumpGeneration 返回 %d, 期望 1", gen)
	}

	gen2, err := adapter.BumpGeneration("bump-node")
	if err != nil {
		t.Fatal(err)
	}
	if gen2 != 2 {
		t.Errorf("第二次 BumpGeneration 返回 %d, 期望 2", gen2)
	}
}

func TestStoreAdapter_ListNodes(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{ID: "n1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)
	nodes, err := adapter.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "n1" {
		t.Errorf("ListNodes = %v", nodes)
	}
}

func TestStoreAdapter_GetLatestACLPolicy(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)

	// 无策略时返回空策略。
	p, err := adapter.GetLatestACLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 0 {
		t.Errorf("空策略 Version = %d", p.Version)
	}
}

func TestStoreAdapter_ListRelayInfo(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRelayInfo(&store.RelayInfo{NodeID: "r1", Priority: 5}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)
	infos, err := adapter.ListRelayInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].NodeID != "r1" {
		t.Errorf("ListRelayInfo = %v", infos)
	}
}

func TestStoreAdapter_GetNode(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{ID: "gn1", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)
	n, err := adapter.GetNode("gn1")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "gn1" {
		t.Errorf("GetNode ID = %q", n.ID)
	}
}

// ─── CRLProviderFunc 测试 ────────────────────────────────────────────────────

func TestCRLProviderFunc(t *testing.T) {
	fn := CRLProviderFunc(func(dur time.Duration) ([]byte, error) {
		return []byte("crl-data"), nil
	})
	data, err := fn.CurrentCRL(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "crl-data" {
		t.Errorf("CRL data = %q", data)
	}
}

// ─── ConfigHTTP.loadEpoch 测试 ───────────────────────────────────────────────

func TestConfigHTTP_LoadEpochNilPointer(t *testing.T) {
	// epoch 指针为 nil 时返回 0（向后兼容）。
	h := NewConfigHTTP(&stubConfigStore{}, &stubCRL{}, nil)
	if h.loadEpoch() != 0 {
		t.Errorf("nil epoch 应返回 0，实际 %d", h.loadEpoch())
	}
}

func TestConfigHTTP_LoadEpochNonNil(t *testing.T) {
	var ep atomic.Uint64
	ep.Store(42)
	h := newConfigHTTPWithEpoch(&stubConfigStore{}, &stubCRL{}, nil, &ep)
	if h.loadEpoch() != 42 {
		t.Errorf("loadEpoch = %d, 期望 42", h.loadEpoch())
	}
}

// ─── ConfigGRPC.loadEpoch 测试 ───────────────────────────────────────────────

func TestConfigGRPC_LoadEpochNilPointer(t *testing.T) {
	g := NewConfigGRPC(nil, nil)
	if g.loadEpoch() != 0 {
		t.Errorf("nil epoch 应返回 0，实际 %d", g.loadEpoch())
	}
}

func TestConfigGRPC_LoadEpochNonNil(t *testing.T) {
	var ep atomic.Uint64
	ep.Store(99)
	g := newConfigGRPCWithEpoch(nil, nil, &ep)
	if g.loadEpoch() != 99 {
		t.Errorf("loadEpoch = %d, 期望 99", g.loadEpoch())
	}
}

// ─── ConfigWS.loadEpoch 测试 ─────────────────────────────────────────────────

func TestConfigWS_LoadEpochNilPointer(t *testing.T) {
	ws := NewConfigWS(nil, nil)
	if ws.loadEpoch() != 0 {
		t.Errorf("nil epoch 应返回 0，实际 %d", ws.loadEpoch())
	}
}

func TestConfigWS_LoadEpochNonNil(t *testing.T) {
	var ep atomic.Uint64
	ep.Store(77)
	ws := newConfigWSWithEpoch(nil, nil, &ep)
	if ws.loadEpoch() != 77 {
		t.Errorf("loadEpoch = %d, 期望 77", ws.loadEpoch())
	}
}

// ─── ConfigHTTP MethodNotAllowed 测试 ────────────────────────────────────────

func TestConfigHTTP_MethodNotAllowed(t *testing.T) {
	h := NewConfigHTTP(&stubConfigStore{}, &stubCRL{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/config", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 应返回 405，实际 %d", w.Code)
	}
}

// ─── buildSnapshot 测试 ──────────────────────────────────────────────────────

func TestBuildSnapshot_NilPolicy(t *testing.T) {
	// policy 为 nil 时不 panic。
	snap := buildSnapshot(
		[]store.Node{{ID: "n1", User: "alice", WGPubKey: "pk1", VirtualIP: "100.64.0.1/32"}},
		nil,
		nil,
		nil,
	)
	if snap.Policy != nil {
		t.Error("nil policy 时 Snapshot.Policy 应为 nil")
	}
	if len(snap.Nodes) != 1 {
		t.Errorf("Nodes 数量 = %d, 期望 1", len(snap.Nodes))
	}
}

func TestBuildSnapshot_EmptyPolicyDoc(t *testing.T) {
	// 空 document 时 Policy 为 nil。
	snap := buildSnapshot(nil, &store.ACLPolicy{Document: ""}, nil, nil)
	if snap.Policy != nil {
		t.Error("空 document 时 Policy 应为 nil")
	}
}

func TestBuildSnapshot_InvalidPolicyDoc(t *testing.T) {
	// 无效 JSON policy document 时忽略（不 panic）。
	snap := buildSnapshot(nil, &store.ACLPolicy{Document: "not-json"}, nil, nil)
	if snap.Policy != nil {
		t.Error("无效 document 时 Policy 应为 nil")
	}
}

func TestBuildSnapshot_WithRelayInfo(t *testing.T) {
	relays := []store.RelayInfo{
		{NodeID: "r1", TunnelEndpoint: "relay.com:443", UDPEndpoint: "relay.com:3478", Protocols: "TLS_RAW,WEBSOCKET", Priority: 10},
	}
	snap := buildSnapshot(nil, nil, relays, nil)
	if len(snap.Relays) != 1 {
		t.Fatalf("Relays 数量 = %d, 期望 1", len(snap.Relays))
	}
	rv := snap.Relays[0]
	if rv.ID != "r1" || rv.Priority != 10 {
		t.Errorf("relay view 错误: %+v", rv)
	}
	if rv.Endpoint == nil {
		t.Error("TunnelEndpoint 应解析为非 nil Endpoint")
	}
	if rv.UDP == nil {
		t.Error("UDPEndpoint 应解析为非 nil Endpoint")
	}
}

func TestBuildSnapshot_WithNodeRelayFn(t *testing.T) {
	fn := func() map[string]string {
		return map[string]string{"n1": "r1"}
	}
	snap := buildSnapshot(nil, nil, nil, fn)
	if snap.NodeRelay == nil || snap.NodeRelay["n1"] != "r1" {
		t.Errorf("NodeRelay = %v", snap.NodeRelay)
	}
}

func TestBuildSnapshot_NilNodeRelayFn(t *testing.T) {
	snap := buildSnapshot(nil, nil, nil, nil)
	if snap.NodeRelay != nil {
		t.Errorf("nil nodeRelayFn 时 NodeRelay 应为 nil, 实际 %v", snap.NodeRelay)
	}
}

// ─── Services Epoch 双向往返测试 ─────────────────────────────────────────────

func TestServicesEpochRoundTrip(t *testing.T) {
	// 不构造完整 Services，直接测试 epoch 原子值。
	var svc Services
	if svc.Epoch() != 0 {
		t.Fatalf("初始 epoch 应为 0，实际 %d", svc.Epoch())
	}
	svc.SetEpoch(123)
	if svc.Epoch() != 123 {
		t.Fatalf("SetEpoch(123) 后 Epoch() = %d", svc.Epoch())
	}
}

// ─── nextSubID 并发唯一性 ────────────────────────────────────────────────────

func TestNextSubID_Unique(t *testing.T) {
	const n = 100
	ids := make(map[subID]bool, n)
	for i := 0; i < n; i++ {
		id := nextSubID()
		if ids[id] {
			t.Fatalf("重复 subID: %d", id)
		}
		ids[id] = true
	}
}

// ─── wsSignal JSON 字段验证 ──────────────────────────────────────────────────

func TestWSSignal_JSONFields(t *testing.T) {
	// 验证 wsSignal 的 JSON tag 正确。
	sig := wsSignal{Changed: true, Generation: 42, Epoch: 5}
	_ = sig // 仅确认编译通过、字段对齐
	if sig.Changed != true || sig.Generation != 42 || sig.Epoch != 5 {
		t.Errorf("字段赋值错误: %+v", sig)
	}
}
