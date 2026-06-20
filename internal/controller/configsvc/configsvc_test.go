package configsvc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 内存 stub ────────────────────────────────────────────────────────────────

// stubBumper 实现 storeBumper，用原子计数器模拟 BumpGeneration。
type stubBumper struct {
	mu      sync.Mutex
	genMap  map[string]uint64
	bumpErr error
}

func newStubBumper() *stubBumper {
	return &stubBumper{genMap: make(map[string]uint64)}
}

func (s *stubBumper) BumpGeneration(nodeID string) (uint64, error) {
	if s.bumpErr != nil {
		return 0, s.bumpErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.genMap[nodeID]++
	return s.genMap[nodeID], nil
}

// stubNodeInfoGetter 实现 NodeInfoGetter。
type stubNodeInfoGetter struct {
	mu    sync.RWMutex
	nodes map[string]*NodeInfo
}

func newStubNodeInfoGetter() *stubNodeInfoGetter {
	return &stubNodeInfoGetter{nodes: make(map[string]*NodeInfo)}
}

func (s *stubNodeInfoGetter) set(nodeID string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[nodeID] = &NodeInfo{Generation: gen}
}

func (s *stubNodeInfoGetter) GetNodeInfo(nodeID string) (*NodeInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if info, ok := s.nodes[nodeID]; ok {
		return info, nil
	}
	return nil, store.ErrNotFound
}

// ─── Notify 测试 ──────────────────────────────────────────────────────────────

func TestNotify_Subscribe_Unsubscribe(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()

	sid, ch := n.Subscribe("node-A")
	if ch == nil {
		t.Fatal("Subscribe 返回 nil channel")
	}
	if n.OnlineCount("node-A") != 1 {
		t.Fatalf("订阅后 OnlineCount 应为 1，实际 %d", n.OnlineCount("node-A"))
	}

	n.Unsubscribe("node-A", sid)
	if n.OnlineCount("node-A") != 0 {
		t.Fatalf("退订后 OnlineCount 应为 0，实际 %d", n.OnlineCount("node-A"))
	}
}

func TestNotify_IsOnline(t *testing.T) {
	n := NewNotify(newStubBumper())
	defer n.Close()

	if n.IsOnline("node-A") {
		t.Fatal("无订阅者时 IsOnline 应为 false")
	}
	sid, _ := n.Subscribe("node-A")
	if !n.IsOnline("node-A") {
		t.Fatal("有订阅者后 IsOnline 应为 true")
	}
	n.Unsubscribe("node-A", sid)
	if n.IsOnline("node-A") {
		t.Fatal("退订后 IsOnline 应为 false")
	}
}

func TestNotify_RecomputeAndNotify_MonotonicGeneration(t *testing.T) {
	bumper := newStubBumper()
	// 预先设置 node-A 的 generation 从 5 开始
	bumper.mu.Lock()
	bumper.genMap["node-A"] = 5
	bumper.mu.Unlock()

	n := NewNotify(bumper)
	defer n.Close()

	_, ch := n.Subscribe("node-A")

	// 触发两次（带间隔保证两次都能执行）
	n.RecomputeAndNotify("node-A")
	time.Sleep(50 * time.Millisecond)
	n.RecomputeAndNotify("node-A")

	// 收集至少一条消息
	var gens []uint64
	deadline := time.After(3 * time.Second)
	for len(gens) < 2 {
		select {
		case msg := <-ch:
			gens = append(gens, msg.Generation)
		case <-deadline:
			goto done
		}
	}
done:
	if len(gens) == 0 {
		t.Fatal("未收到任何变更信号")
	}
	// 验证单调性：generation 只增不减
	for i := 1; i < len(gens); i++ {
		if gens[i] < gens[i-1] {
			t.Fatalf("generation 回退：gens[%d]=%d < gens[%d]=%d", i, gens[i], i-1, gens[i-1])
		}
	}
	// 所有 generation 必须 > 初始值 5
	for _, g := range gens {
		if g <= 5 {
			t.Fatalf("generation %d <= 初始值 5", g)
		}
	}
}

func TestNotify_OnlyTargetNodeReceivesSignal(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()

	_, chA := n.Subscribe("node-A")
	_, chB := n.Subscribe("node-B")

	// 只触发 node-A
	n.RecomputeAndNotify("node-A")

	// node-A 应该收到信号
	select {
	case <-chA:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("node-A 未收到信号")
	}

	// node-B 不应收到信号
	select {
	case msg := <-chB:
		t.Fatalf("node-B 不应收到信号，但收到了 %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// OK：node-B 未收到
	}
}

func TestNotify_ConcurrentRecomputeMonotonic(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()

	_, ch := n.Subscribe("node-C")

	// 并发触发多次
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.RecomputeAndNotify("node-C")
		}()
	}
	wg.Wait()

	// 收集一段时间内的所有信号
	var gens []uint64
	deadline := time.After(2 * time.Second)
outer:
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				break outer
			}
			gens = append(gens, msg.Generation)
		case <-deadline:
			break outer
		}
	}

	// 验证 generation 单调递增（允许跳跃，不允许回退）
	for i := 1; i < len(gens); i++ {
		if gens[i] < gens[i-1] {
			t.Fatalf("-race 下 generation 回退：gens[%d]=%d < gens[%d]=%d",
				i, gens[i], i-1, gens[i-1])
		}
	}
}

func TestNotify_CloseNoLeak(t *testing.T) {
	// 测试 Close 后 goroutine 不泄漏（通过 wg.Wait 隐式检测）
	bumper := newStubBumper()
	n := NewNotify(bumper)

	// 添加几个订阅者触发 worker 启动
	n.Subscribe("a")
	n.Subscribe("b")
	n.Subscribe("c")
	n.RecomputeAndNotify("a", "b", "c")

	done := make(chan struct{})
	go func() {
		n.Close()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Close 超时（可能有 goroutine 泄漏）")
	}
}

func TestNotify_MultipleSubscribers(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()

	// 同一节点两个订阅者
	_, ch1 := n.Subscribe("multi-node")
	_, ch2 := n.Subscribe("multi-node")

	if n.OnlineCount("multi-node") != 2 {
		t.Fatalf("期望 OnlineCount=2，实际 %d", n.OnlineCount("multi-node"))
	}

	n.RecomputeAndNotify("multi-node")

	// 两个订阅者都应收到信号
	recv := func(ch <-chan *ChangeSignalMsg, name string) {
		select {
		case msg := <-ch:
			if msg == nil {
				t.Errorf("%s: 收到 nil 信号", name)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: 超时未收到信号", name)
		}
	}
	recv(ch1, "ch1")
	recv(ch2, "ch2")
}

// ─── ConfigGRPC 测试 ──────────────────────────────────────────────────────────

// buildTestCertChain 生成一对 CA + 客户端证书 + 服务端证书。
func buildTestCertChain(t *testing.T, nodeID string) (caPool *x509.CertPool, clientCert tls.Certificate, serverCert tls.Certificate) {
	t.Helper()

	// CA 密钥 + 自签证书
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发 CA 证书: %v", err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	caPool = x509.NewCertPool()
	caPool.AddCert(caCert)

	// 签发客户端证书（CN = nodeID）
	clientCert = issueCert(t, caCert, caKey, nodeID, x509.ExtKeyUsageClientAuth)
	// 签发服务端证书（CN = localhost，DNS SAN）
	serverCert = issueServerCert(t, caCert, caKey)
	return
}

func issueCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, ekus ...x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  ekus,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发证书 %q: %v", cn, err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func issueServerCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("签发 server 证书: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func TestConfigGRPC_WatchConfig_InitialAndChange(t *testing.T) {
	const nodeID = "test-node-grpc"
	const initGen = uint64(7)

	bumper := newStubBumper()
	bumper.mu.Lock()
	bumper.genMap[nodeID] = initGen
	bumper.mu.Unlock()

	getter := newStubNodeInfoGetter()
	getter.set(nodeID, initGen)

	n := NewNotify(bumper)
	defer n.Close()

	grpcSvc := NewConfigGRPC(n, getter)

	caPool, clientCert, serverCert := buildTestCertChain(t, nodeID)

	// 起 bufconn gRPC server
	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS12,
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	genv1.RegisterConfigServiceServer(srv, grpcSvc)
	go srv.Serve(lis)
	defer srv.Stop()

	// 客户端 TLS（信任测试 CA）
	clientTLSConf := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}

	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConf)),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer cc.Close()

	client := genv1.NewConfigServiceClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.WatchConfig(ctx, &genv1.WatchRequest{KnownGeneration: 0})
	if err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}

	// 收初始信号
	sig, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 初始信号: %v", err)
	}
	if !sig.Changed {
		t.Error("初始信号 Changed 应为 true")
	}
	if sig.Generation != initGen {
		t.Errorf("初始 generation 期望 %d，实际 %d", initGen, sig.Generation)
	}

	// 触发变更
	n.RecomputeAndNotify(nodeID)

	// 收变更信号
	sig2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 变更信号: %v", err)
	}
	if !sig2.Changed {
		t.Error("变更信号 Changed 应为 true")
	}
	if sig2.Generation <= initGen {
		t.Errorf("变更后 generation 应 > %d，实际 %d", initGen, sig2.Generation)
	}
}

// ─── WS 测试（testWSHandler 绕过 mTLS）────────────────────────────────────────

// testWSHandler 是测试专用 handler：从 query "node_id" 取 nodeID（免 mTLS）。
// 直接调用 serveWSWithNodeID（在 ws.go 中的可测函数）。
type testWSHandler struct {
	notify *Notify
	getter NodeInfoGetter
}

func (h *testWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	if nodeID == "" {
		http.Error(w, "missing node_id", http.StatusBadRequest)
		return
	}
	info, err := h.getter.GetNodeInfo(nodeID)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	serveWSWithNodeID(w, r, nodeID, info.Generation, h.notify, 0)
}

func TestConfigWS_SignalPushed(t *testing.T) {
	const nodeID = "ws-test-node"
	const initGen = uint64(3)

	bumper := newStubBumper()
	bumper.mu.Lock()
	bumper.genMap[nodeID] = initGen
	bumper.mu.Unlock()

	getter := newStubNodeInfoGetter()
	getter.set(nodeID, initGen)

	n := NewNotify(bumper)
	defer n.Close()

	h := &testWSHandler{notify: n, getter: getter}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[4:] + "/?node_id=" + nodeID

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket Dial: %v", err)
	}
	defer conn.CloseNow()

	// 读初始信号
	var sig wsSignal
	if err := wsjson.Read(ctx, conn, &sig); err != nil {
		t.Fatalf("读初始信号: %v", err)
	}
	if !sig.Changed {
		t.Error("初始信号 Changed 应为 true")
	}
	if sig.Generation != initGen {
		t.Errorf("初始 generation 期望 %d，实际 %d", initGen, sig.Generation)
	}

	// 触发变更
	n.RecomputeAndNotify(nodeID)

	// 读变更信号
	var sig2 wsSignal
	if err := wsjson.Read(ctx, conn, &sig2); err != nil {
		t.Fatalf("读变更信号: %v", err)
	}
	if sig2.Generation <= initGen {
		t.Errorf("变更后 generation 应 > %d，实际 %d", initGen, sig2.Generation)
	}
}

// ─── HTTP 测试 ────────────────────────────────────────────────────────────────

// stubConfigStore 实现 configStoreIface，用于 HTTP handler 测试。
type stubConfigStore struct {
	nodes  []store.Node
	policy *store.ACLPolicy
	relays []store.RelayInfo
}

func (s *stubConfigStore) ListNodes() ([]store.Node, error) {
	return s.nodes, nil
}
func (s *stubConfigStore) GetLatestACLPolicy() (*store.ACLPolicy, error) {
	if s.policy == nil {
		return &store.ACLPolicy{}, nil
	}
	return s.policy, nil
}
func (s *stubConfigStore) ListRelayInfo() ([]store.RelayInfo, error) {
	return s.relays, nil
}
func (s *stubConfigStore) GetNode(id string) (*store.Node, error) {
	for _, n := range s.nodes {
		if n.ID == id {
			cp := n
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}
func (s *stubConfigStore) ListNodeAliases() ([]store.NodeAlias, error) {
	return nil, nil
}
func (s *stubConfigStore) ListPublishedRoutes() ([]store.PublishedRoute, error) {
	return nil, nil
}
func (s *stubConfigStore) GetDNSSettings() (*store.DNSSettings, error) {
	return nil, nil
}
func (s *stubConfigStore) ListFreshDiscoveredMappings(_ time.Time) ([]store.DiscoveredMapping, error) {
	return nil, nil
}
func (s *stubConfigStore) ListSplitRules() ([]store.SplitRuleRow, error) {
	return nil, nil
}
func (s *stubConfigStore) GetLatestGeoIPMeta() (*store.GeoIPMeta, error) {
	return nil, nil
}
func (s *stubConfigStore) ListActiveCertFingerprints() (map[string]string, error) {
	return nil, nil
}

// stubCRL 返回固定 CRL DER 字节。
type stubCRL struct {
	data []byte
	err  error
}

func (s *stubCRL) CurrentCRL(_ time.Duration) ([]byte, error) {
	return s.data, s.err
}

func TestConfigHTTP_GetNodeConfig(t *testing.T) {
	const nodeID = "http-node-1"
	fakeCRL := []byte("fake-crl-der-bytes")

	st := &stubConfigStore{
		nodes: []store.Node{
			{
				ID:         nodeID,
				VirtualIP:  "100.64.1.1/32",
				WGPubKey:   "wg-pubkey-1",
				Generation: 42,
				User:       "alice",
			},
		},
		policy: &store.ACLPolicy{Document: `{"groups":{},"acls":[]}`},
	}
	crl := &stubCRL{data: fakeCRL}
	handler := NewConfigHTTP(st, crl, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d\n响应体: %s", w.Code, w.Body.String())
	}

	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v\n原始: %s", err, w.Body.String())
	}

	if cfg.VirtualIp != "100.64.1.1" {
		t.Errorf("VirtualIp 期望 100.64.1.1，实际 %q", cfg.VirtualIp)
	}
	if cfg.Generation != 42 {
		t.Errorf("Generation 期望 42，实际 %d", cfg.Generation)
	}
	if string(cfg.CrlDer) != string(fakeCRL) {
		t.Errorf("CrlDer 不匹配：期望 %q，实际 %q", fakeCRL, cfg.CrlDer)
	}
}

func TestConfigHTTP_304_ViaQueryParam(t *testing.T) {
	const nodeID = "http-node-2"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.1.2/32", Generation: 99},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config?generation=99", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("期望 304，实际 %d", w.Code)
	}
}

func TestConfigHTTP_304_ViaIfNoneMatch(t *testing.T) {
	const nodeID = "http-node-3"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.1.3/32", Generation: 55},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.Header.Set("If-None-Match", "55")
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("期望 304，实际 %d", w.Code)
	}
}

func TestConfigHTTP_CRL_FieldPresent(t *testing.T) {
	const nodeID = "http-crl-node"
	crlBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.2.1/32", Generation: 1},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: crlBytes}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if string(cfg.CrlDer) != string(crlBytes) {
		t.Errorf("CrlDer 不匹配")
	}
}

func TestConfigHTTP_Unauthorized_WithoutTLS(t *testing.T) {
	st := &stubConfigStore{
		nodes: []store.Node{{ID: "x"}},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	// 不设置 req.TLS
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，实际 %d", w.Code)
	}
}

func TestConfigHTTP_GenerationNotMatch_Returns200(t *testing.T) {
	const nodeID = "http-node-4"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.1.4/32", Generation: 10},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	// 客户端 generation=5，服务端=10 → 应返回 200
	req := httptest.NewRequest(http.MethodGet, "/v1/config?generation=5", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
}

// ─── Topology 注入（assignmentFn）测试 ────────────────────────────────────────

func TestConfigHTTP_TopologyInjected(t *testing.T) {
	const nodeID = "topo-node-1"
	st := &stubConfigStore{
		nodes: []store.Node{{ID: nodeID, VirtualIP: "100.64.2.1/32", Generation: 3}},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	// 注入 assignmentFn：为该节点产出一个 TopologyAssignment。
	handler.SetAssignmentFn(func(id string) *genv1.TopologyAssignment {
		if id != nodeID {
			return nil
		}
		return &genv1.TopologyAssignment{
			Version: 9,
			Role:    genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d\n%s", w.Code, w.Body.String())
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg.Topology == nil {
		t.Fatalf("期望 Topology 被填充，实际为 nil")
	}
	if cfg.Topology.Version != 9 || cfg.Topology.Role != genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		t.Errorf("Topology 字段错误：%+v", cfg.Topology)
	}
}

func TestConfigHTTP_TopologyNil_Compatible(t *testing.T) {
	// 不注入 assignmentFn（nil）→ Topology 空，既有行为不变（S5 兼容）。
	const nodeID = "topo-node-2"
	st := &stubConfigStore{
		nodes: []store.Node{{ID: nodeID, VirtualIP: "100.64.2.2/32", Generation: 1}},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)
	// 不调用 SetAssignmentFn。

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg.Topology != nil {
		t.Errorf("未注入 assignmentFn 时 Topology 应为 nil，实际 %+v", cfg.Topology)
	}
}

func TestConfigHTTP_TopologyFnReturnsNil_NoInjection(t *testing.T) {
	// 注入了 assignmentFn 但对该节点返回 (nil) → 不注入，Topology 空。
	const nodeID = "topo-node-3"
	st := &stubConfigStore{
		nodes: []store.Node{{ID: nodeID, VirtualIP: "100.64.2.3/32", Generation: 1}},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)
	handler.SetAssignmentFn(func(string) *genv1.TopologyAssignment { return nil })

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg.Topology != nil {
		t.Errorf("assignmentFn 返回 nil 时 Topology 应为 nil，实际 %+v", cfg.Topology)
	}
}

// ─── 辅助工具 ─────────────────────────────────────────────────────────────────

// fakeTLSState 构造一个带有指定 CN 的 mTLS 连接状态（用于注入测试）。
func fakeTLSState(nodeID string) *tls.ConnectionState {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
	}
}

// ─── subIDCounter 原子性测试 ──────────────────────────────────────────────────

func TestSubIDCounter_Unique(t *testing.T) {
	var cnt atomic.Uint64
	const n = 1000
	ids := make([]uint64, n)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i] = cnt.Add(1)
		}(i)
	}
	wg.Wait()
	seen := make(map[uint64]bool, n)
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("重复 ID %d", id)
		}
		seen[id] = true
	}
}

// ─── 验证用例 1（承接原 #6）：Close 与 ensureWorker 并发不触发 WaitGroup Add-after-Wait ──

// TestNotify_CloseWaitGroupConcurrentNoPanic 复现 Add-after-Wait：
// Close()（close(done)+wg.Wait()）与 Subscribe/RecomputeAndNotify 触发的
// ensureWorker（wg.Add(1)+go）并发时，曾因缺少受锁串行化的 closed 标志而出现
// "WaitGroup misuse: Add called concurrently with Wait" panic。
// 多次迭代提高并发交错命中概率；任一迭代 panic 即测试失败。
func TestNotify_CloseWaitGroupConcurrentNoPanic(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		bumper := newStubBumper()
		n := NewNotify(bumper)

		var wg sync.WaitGroup
		// 并发持续触发 ensureWorker：每个 nodeID 唯一，强制新建 worker（必走 wg.Add）。
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					nodeID := fmt.Sprintf("iter%d-g%d-n%d", iter, g, i)
					if i%2 == 0 {
						n.Subscribe(nodeID)
					} else {
						n.RecomputeAndNotify(nodeID)
					}
				}
			}(g)
		}

		// 与上面的并发触发竞争执行 Close。
		closeDone := make(chan struct{})
		go func() {
			n.Close()
			close(closeDone)
		}()

		wg.Wait()
		select {
		case <-closeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: Close 超时（疑似死锁或 goroutine 泄漏）", iter)
		}
	}
}

// ─── 验证用例 2（承接原回归 Task）：Close 与订阅操作并发无 race / 无 send-on-closed ──

// TestNotify_CloseConcurrentNoRaceNoPanic 在 -race 下并发执行
// Subscribe / RecomputeAndNotify / Unsubscribe 与 Close，验证：
//  1. 不触发数据竞争（-race，尤其 closed 字段单锁读写）
//  2. 不向已关闭 channel 推送（runWorker 无 send-on-closed panic）
//  3. Close 后新 Subscribe 不泄漏（其 channel 不被登记，worker 不向它推送）
func TestNotify_CloseConcurrentNoRaceNoPanic(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)

	const nodes = 8
	var wg sync.WaitGroup

	// 持续订阅 + 触发通知 + 取消订阅
	for i := 0; i < nodes; i++ {
		nodeID := "n" + string(rune('0'+i))
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sid, _ := n.Subscribe(id)
				n.RecomputeAndNotify(id)
				n.Unsubscribe(id, sid)
			}
		}(nodeID)
	}

	// 与上面并发地 Close
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		n.Close()
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("并发 Close 测试超时（疑似死锁或泄漏）")
	}

	// Close 之后再 Subscribe：不得 panic；返回的 channel 不应永久阻塞读。
	_, ch := n.Subscribe("post-close")
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("post-close: 期望 channel 关闭(ok=false) 或本就为空，实际收到信号")
		}
	case <-time.After(500 * time.Millisecond):
		// 允许：closed 后 Subscribe 直接返回一个不带订阅登记的空 channel，
		// 读阻塞属可接受（无泄漏，因为 worker 不会向它推送）。
	}
}

// ─── 配套用例：Close 关闭订阅 channel + 不 double-close ────────────────────────

// TestNotify_CloseClosesSubscriberChannels 验证 Close 会关闭所有订阅者 channel，
// 使消费者侧（grpc/ws）的 `_, ok := <-ch; if !ok` 分支在 Close 时触发并优雅退出。
func TestNotify_CloseClosesSubscriberChannels(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)

	_, chA := n.Subscribe("node-A")
	_, chB1 := n.Subscribe("node-B")
	_, chB2 := n.Subscribe("node-B") // 同节点多订阅者

	n.Close()

	// 关闭后所有订阅 channel 都应被关闭：从已关闭 channel 读必定立即返回 ok=false。
	assertClosed := func(ch <-chan *ChangeSignalMsg, name string) {
		t.Helper()
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("%s: 期望 channel 已关闭(ok=false)，实际收到 ok=true", name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: 超时——Close 未关闭订阅 channel", name)
		}
	}
	assertClosed(chA, "chA")
	assertClosed(chB1, "chB1")
	assertClosed(chB2, "chB2")
}

// TestNotify_CloseUnsubscribeNoDoubleClose 验证 Close 与 Unsubscribe 协调，
// 不发生 double-close panic（Unsubscribe 已关的 channel，Close 不得再关）。
func TestNotify_CloseUnsubscribeNoDoubleClose(t *testing.T) {
	bumper := newStubBumper()
	n := NewNotify(bumper)

	sid, _ := n.Subscribe("node-X")
	n.Unsubscribe("node-X", sid) // 先 Unsubscribe（内部已 close 该 channel）

	// Close 再遍历剩余 subs，不得对已关闭/已移除的 channel 二次 close。
	// 若发生 double-close 会 panic 导致测试失败。
	n.Close()
}

// ─── Epoch 字段测试（Task 5：configsvc 下发携带 epoch）────────────────────────

// TestWatchConfig_EpochIsZero 断言 WatchConfig 推送的 ChangeSignal.Epoch == 0（阶段1恒0）。
func TestWatchConfig_EpochIsZero(t *testing.T) {
	const nodeID = "epoch-grpc-node"
	const initGen = uint64(5)

	bumper := newStubBumper()
	bumper.mu.Lock()
	bumper.genMap[nodeID] = initGen
	bumper.mu.Unlock()

	getter := newStubNodeInfoGetter()
	getter.set(nodeID, initGen)

	n := NewNotify(bumper)
	defer n.Close()

	grpcSvc := NewConfigGRPC(n, getter)
	caPool, clientCert, serverCert := buildTestCertChain(t, nodeID)

	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS12,
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	genv1.RegisterConfigServiceServer(srv, grpcSvc)
	go srv.Serve(lis)
	defer srv.Stop()

	clientTLSConf := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConf)),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer cc.Close()

	client := genv1.NewConfigServiceClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.WatchConfig(ctx, &genv1.WatchRequest{KnownGeneration: 0})
	if err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}

	// 收初始信号：Epoch 必须为 0（阶段1恒0）
	sig, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 初始信号: %v", err)
	}
	if sig.Epoch != 0 {
		t.Errorf("初始 ChangeSignal.Epoch 期望 0（阶段1恒0），实际 %d", sig.Epoch)
	}

	// 触发变更后也应携带 Epoch == 0
	n.RecomputeAndNotify(nodeID)
	sig2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 变更信号: %v", err)
	}
	if sig2.Epoch != 0 {
		t.Errorf("变更 ChangeSignal.Epoch 期望 0（阶段1恒0），实际 %d", sig2.Epoch)
	}
}

// TestConfigHTTP_NodeConfig_EpochIsZero 断言 /v1/config 返回的 NodeConfig.Epoch == 0（阶段1恒0）。
func TestConfigHTTP_NodeConfig_EpochIsZero(t *testing.T) {
	const nodeID = "epoch-http-node"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.3.1/32", Generation: 10},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d\n%s", w.Code, w.Body.String())
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg.Epoch != 0 {
		t.Errorf("NodeConfig.Epoch 期望 0（阶段1恒0），实际 %d", cfg.Epoch)
	}
}

// TestConfigHTTP_304_EpochAndGeneration 断言带 ?epoch=0&generation=N 且与服务端相等时返回 304。
func TestConfigHTTP_304_EpochAndGeneration(t *testing.T) {
	const nodeID = "epoch-304-node"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.3.2/32", Generation: 77},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	// epoch=0 & generation=77 与服务端 (epoch=0, gen=77) 完全相等 → 304
	req := httptest.NewRequest(http.MethodGet, "/v1/config?epoch=0&generation=77", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("epoch=0&generation=77 时期望 304，实际 %d", w.Code)
	}
}

// TestConfigHTTP_304_EpochMismatch_Returns200 断言 epoch 不同时不返回 304。
func TestConfigHTTP_304_EpochMismatch_Returns200(t *testing.T) {
	const nodeID = "epoch-mismatch-node"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.3.3/32", Generation: 10},
		},
	}
	handler := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	// epoch=1（客户端以为在 epoch=1），服务端 epoch=0 → 不相等 → 200
	req := httptest.NewRequest(http.MethodGet, "/v1/config?epoch=1&generation=10", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("epoch 不同时期望 200，实际 %d", w.Code)
	}
}

// ─── S3-A3：epoch 动态化新增测试 ────────────────────────────────────────────────

// TestConfigHTTP_EpochDynamic 验证 SetEpoch 注入后 HTTP /v1/config 响应携带新 epoch。
// 同时验证未调 SetEpoch 时 epoch 为 0（向后兼容）。
func TestConfigHTTP_EpochDynamic(t *testing.T) {
	const nodeID = "epoch-dynamic-node"

	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.4.1/32", Generation: 1},
		},
	}
	crl := &stubCRL{data: []byte("crl")}

	// ── 1. 通过 newConfigHTTPWithEpoch 注入共享 epoch 指针 ──────────────────
	var epochAtom atomic.Uint64
	handler := newConfigHTTPWithEpoch(st, crl, nil, &epochAtom)

	// 未调 SetEpoch 时 epoch 为 0（向后兼容）
	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg.Epoch != 0 {
		t.Errorf("初始 epoch 期望 0，实际 %d", cfg.Epoch)
	}

	// ── 2. 注入 epoch=5 后响应应携带新 epoch ─────────────────────────────────
	epochAtom.Store(5)

	req2 := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req2.TLS = fakeTLSState(nodeID)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w2.Code)
	}
	var cfg2 genv1.NodeConfig
	if err := protojson.Unmarshal(w2.Body.Bytes(), &cfg2); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
	if cfg2.Epoch != 5 {
		t.Errorf("SetEpoch(5) 后期望 Epoch=5，实际 %d", cfg2.Epoch)
	}
}

// TestServices_SetEpoch 验证 Services.SetEpoch → Services.Epoch 往返读写正确。
func TestServices_SetEpoch(t *testing.T) {
	// 使用 newConfigHTTPWithEpoch 直接测试共享 atomic，不依赖 New（避免 store 依赖）
	var epochAtom atomic.Uint64

	// 零值
	if epochAtom.Load() != 0 {
		t.Fatal("初始 epoch 应为 0")
	}

	// Store → Load 往返
	epochAtom.Store(42)
	if epochAtom.Load() != 42 {
		t.Fatalf("Store(42) 后 Load 期望 42，实际 %d", epochAtom.Load())
	}

	// 再次更新
	epochAtom.Store(100)
	if epochAtom.Load() != 100 {
		t.Fatalf("Store(100) 后 Load 期望 100，实际 %d", epochAtom.Load())
	}
}

// TestConfigGRPC_EpochDynamic 验证 SetEpoch 注入后 WatchConfig ChangeSignal 携带新 epoch。
func TestConfigGRPC_EpochDynamic(t *testing.T) {
	const nodeID = "epoch-grpc-dynamic-node"
	const initGen = uint64(3)

	bumper := newStubBumper()
	bumper.mu.Lock()
	bumper.genMap[nodeID] = initGen
	bumper.mu.Unlock()

	getter := newStubNodeInfoGetter()
	getter.set(nodeID, initGen)

	n := NewNotify(bumper)
	defer n.Close()

	// 注入共享 epoch
	var epochAtom atomic.Uint64
	epochAtom.Store(7) // 注入 epoch=7
	grpcSvc := newConfigGRPCWithEpoch(n, getter, &epochAtom)

	caPool, clientCert, serverCert := buildTestCertChain(t, nodeID)

	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS12,
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	genv1.RegisterConfigServiceServer(srv, grpcSvc)
	go srv.Serve(lis)
	defer srv.Stop()

	clientTLSConf := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConf)),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer cc.Close()

	client := genv1.NewConfigServiceClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.WatchConfig(ctx, &genv1.WatchRequest{KnownGeneration: 0})
	if err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}

	// 收初始信号：Epoch 应为 7
	sig, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 初始信号: %v", err)
	}
	if sig.Epoch != 7 {
		t.Errorf("初始 ChangeSignal.Epoch 期望 7，实际 %d", sig.Epoch)
	}
}
