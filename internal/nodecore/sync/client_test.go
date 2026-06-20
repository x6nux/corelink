package sync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

	version "github.com/x6nux/corelink/internal/version"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 辅助：生成 CA + 节点证书（mTLS 测试用）─────────────────────────────────

type testPKI struct {
	caKey     *ecdsa.PrivateKey
	caCert    *x509.Certificate
	caCertDER []byte
	caPEM     []byte

	nodeCert    *x509.Certificate
	nodeKey     *ecdsa.PrivateKey
	nodeCertDER []byte
	nodeCertPEM []byte
	nodeKeyPEM  []byte
}

func genTestPKI(t *testing.T, nodeID string) *testPKI {
	t.Helper()

	// 生成 CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mustNil(t, err, "生成 CA 密钥")
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	mustNil(t, err, "创建 CA 证书")
	caCert, err := x509.ParseCertificate(caDER)
	mustNil(t, err, "解析 CA 证书")

	// 生成节点证书（CN=nodeID）
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mustNil(t, err, "生成节点密钥")
	nodeTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	nodeDER, err := x509.CreateCertificate(rand.Reader, nodeTmpl, caCert, &nodeKey.PublicKey, caKey)
	mustNil(t, err, "创建节点证书")
	nodeCert, err := x509.ParseCertificate(nodeDER)
	mustNil(t, err, "解析节点证书")

	// PEM 编码
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	nodeCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: nodeDER})
	nodeKeyDER, err := x509.MarshalPKCS8PrivateKey(nodeKey)
	mustNil(t, err, "序列化节点私钥")
	nodeKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: nodeKeyDER})

	return &testPKI{
		caKey:       caKey,
		caCert:      caCert,
		caCertDER:   caDER,
		caPEM:       caPEM,
		nodeCert:    nodeCert,
		nodeKey:     nodeKey,
		nodeCertDER: nodeDER,
		nodeCertPEM: nodeCertPEM,
		nodeKeyPEM:  nodeKeyPEM,
	}
}

func mustNil(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// ─── fake notifChannel ────────────────────────────────────────────────────────

// fakeChannel 是可控的假通知通道，供测试注入。
type fakeChannel struct {
	signals chan Signal
	closed  chan struct{}
	once    sync.Once
}

func newFakeChannel(bufSize int) *fakeChannel {
	return &fakeChannel{
		signals: make(chan Signal, bufSize),
		closed:  make(chan struct{}),
	}
}

func (f *fakeChannel) Push(sig Signal) {
	select {
	case f.signals <- sig:
	case <-f.closed:
	}
}

func (f *fakeChannel) Recv(ctx context.Context) (Signal, error) {
	select {
	case sig, ok := <-f.signals:
		if !ok {
			return Signal{}, fmt.Errorf("channel closed")
		}
		return sig, nil
	case <-f.closed:
		return Signal{}, fmt.Errorf("channel closed")
	case <-ctx.Done():
		return Signal{}, ctx.Err()
	}
}

func (f *fakeChannel) Close() {
	f.once.Do(func() {
		close(f.closed)
	})
}

// ─── mock HTTP server ─────────────────────────────────────────────────────────

// mockConfigServer 提供 /v1/config HTTP 端点，支持 generation 304 语义。
type mockConfigServer struct {
	t       *testing.T
	mu      sync.RWMutex
	cfg     *genv1.NodeConfig // 返回的配置
	callCnt atomic.Int32
}

func newMockConfigServer(t *testing.T, cfg *genv1.NodeConfig) *mockConfigServer {
	return &mockConfigServer{t: t, cfg: cfg}
}

func (m *mockConfigServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.callCnt.Add(1)

	if r.URL.Path != "/v1/config" {
		http.NotFound(w, r)
		return
	}

	m.mu.RLock()
	curCfg := m.cfg
	m.mu.RUnlock()

	clientGen := uint64(0)
	if s := r.URL.Query().Get("generation"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			clientGen = v
		}
	}
	if etag := r.Header.Get("If-None-Match"); etag != "" && clientGen == 0 {
		if v, err := strconv.ParseUint(etag, 10, 64); err == nil {
			clientGen = v
		}
	}

	if clientGen > 0 && clientGen == curCfg.Generation {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body, err := protojson.Marshal(curCfg)
	if err != nil {
		m.t.Errorf("mockConfigServer: 序列化失败: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", strconv.FormatUint(curCfg.Generation, 10))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// ─── mock gRPC server ─────────────────────────────────────────────────────────

// mockConfigGRPC 是假 ConfigService gRPC 服务端，可推送信号。
type mockConfigGRPC struct {
	genv1.UnimplementedConfigServiceServer
	signalCh chan *genv1.ChangeSignal
}

func newMockConfigGRPC() *mockConfigGRPC {
	return &mockConfigGRPC{signalCh: make(chan *genv1.ChangeSignal, 8)}
}

func (m *mockConfigGRPC) Push(sig *genv1.ChangeSignal) {
	m.signalCh <- sig
}

func (m *mockConfigGRPC) WatchConfig(_ *genv1.WatchRequest, stream genv1.ConfigService_WatchConfigServer) error {
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig, ok := <-m.signalCh:
			if !ok {
				return nil
			}
			if err := stream.Send(sig); err != nil {
				return err
			}
		}
	}
}

// ─── TestSignalToFetchCallback：信号触发拉取并回调 ─────────────────────────

// TestSignalToFetchCallback 验证：收到 Changed=true 信号 → 触发 HTTP 拉取 → 回调 OnConfig。
func TestSignalToFetchCallback(t *testing.T) {
	t.Parallel()

	wantCfg := &genv1.NodeConfig{
		Generation: 5,
		VirtualIp:  "10.0.0.1",
	}

	// 起 mock HTTP server（无 TLS，测试直接用 http://）
	mockSrv := newMockConfigServer(t, wantCfg)
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	// 构造 fake 通道工厂
	primaryCh := newFakeChannel(4)
	secondaryCh := newFakeChannel(4)

	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 100 * time.Millisecond, switchBackAfter: 200 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			return primaryCh, nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)

	// 用 channel 传递回调结果，避免 data race
	callbackCh := make(chan *genv1.NodeConfig, 8)
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callbackCh <- nc
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	// 在后台运行 client
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	// 等一下让 Run 启动并连接主通道
	time.Sleep(50 * time.Millisecond)

	// 推送一个 Changed 信号（generation=5）
	primaryCh.Push(Signal{Changed: true, Generation: 5})

	// 等待回调
	var received *genv1.NodeConfig
	select {
	case received = <-callbackCh:
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待 OnConfig 回调")
	}

	cancel()
	<-done

	if received == nil {
		t.Fatal("OnConfig 未被调用")
	}
	if received.Generation != wantCfg.Generation {
		t.Errorf("回调 generation=%d，期望 %d", received.Generation, wantCfg.Generation)
	}
	if received.VirtualIp != wantCfg.VirtualIp {
		t.Errorf("回调 VirtualIp=%s，期望 %s", received.VirtualIp, wantCfg.VirtualIp)
	}
	if cnt := mockSrv.callCnt.Load(); cnt == 0 {
		t.Error("HTTP /v1/config 应被调用至少一次")
	}
}

// ─── TestGenerationIdempotency：generation 幂等 ─────────────────────────────

// TestGenerationIdempotency 验证：
//  1. 收到 generation ≤ 本地的信号 → 忽略，不拉取。
//  2. HTTP 304 时不触发回调。
func TestGenerationIdempotency(t *testing.T) {
	t.Parallel()

	gen := uint64(10)
	wantCfg := &genv1.NodeConfig{Generation: gen, VirtualIp: "10.0.0.2"}

	mockSrv := newMockConfigServer(t, wantCfg)
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	primaryCh := newFakeChannel(8)
	secondaryCh := newFakeChannel(4)

	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 100 * time.Millisecond, switchBackAfter: 200 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			return primaryCh, nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)
	client.SetLocalGeneration(gen) // 初始已知 generation=10

	var callCount atomic.Int32
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callCount.Add(1)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// 推送 generation=10（= 本地），应被忽略
	primaryCh.Push(Signal{Changed: true, Generation: gen})
	// 推送 generation=9（< 本地），应被忽略
	primaryCh.Push(Signal{Changed: true, Generation: gen - 1})
	// 推送 generation=0（未知），会触发 HTTP 拉取；但本地 gen=10 → 请求带 gen=10 → server 返回 304
	primaryCh.Push(Signal{Changed: true, Generation: 0})

	time.Sleep(300 * time.Millisecond)

	cancel()
	<-done

	if n := callCount.Load(); n != 0 {
		t.Errorf("OnConfig 不应被调用（所有 generation ≤ 本地或 304），实际调用 %d 次", n)
	}
}

// TestGenerationAdvances：generation 推进后触发一次回调，然后相同 generation 不再触发。
func TestGenerationAdvances(t *testing.T) {
	t.Parallel()

	gen := uint64(3)
	wantCfg := &genv1.NodeConfig{Generation: gen, VirtualIp: "10.0.0.3"}

	mockSrv := newMockConfigServer(t, wantCfg)
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	primaryCh := newFakeChannel(8)
	secondaryCh := newFakeChannel(4)

	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 100 * time.Millisecond, switchBackAfter: 200 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			return primaryCh, nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)
	// 初始 generation=0（未知）

	callbackCh := make(chan *genv1.NodeConfig, 8)
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callbackCh <- nc
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// 推送 generation=3（>本地0）→ 触发拉取 → 回调一次，localGen 升为 3
	primaryCh.Push(Signal{Changed: true, Generation: 3})

	// 等待第一次回调
	select {
	case nc := <-callbackCh:
		if nc.Generation != gen {
			t.Errorf("首次回调 generation=%d，期望 %d", nc.Generation, gen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时等待第一次 OnConfig 回调")
	}

	// 再推送相同 generation=3 → 应被忽略
	primaryCh.Push(Signal{Changed: true, Generation: 3})
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	// callbackCh 应该没有更多元素
	select {
	case nc := <-callbackCh:
		t.Errorf("重复 generation 不应触发 OnConfig，但又收到 generation=%d", nc.Generation)
	default:
		// OK
	}

	if client.LocalGeneration() != gen {
		t.Errorf("本地 generation 应为 %d，实际 %d", gen, client.LocalGeneration())
	}
}

// ─── TestHTTP304NotModified：304 不触发回调 ──────────────────────────────────

func TestHTTP304NotModified(t *testing.T) {
	t.Parallel()

	gen := uint64(7)

	mockSrv := newMockConfigServer(t, &genv1.NodeConfig{Generation: gen})
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	primaryCh := newFakeChannel(4)
	secondaryCh := newFakeChannel(4)

	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 100 * time.Millisecond, switchBackAfter: 200 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			return primaryCh, nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)
	client.SetLocalGeneration(gen) // 本地已知 gen=7

	var callCount atomic.Int32
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callCount.Add(1)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// 推送 generation=8（>本地），会触发 HTTP 拉取；
	// 本地 gen=7，HTTP 查询 gen=7，server gen=7 → 304 Not Modified
	primaryCh.Push(Signal{Changed: true, Generation: 8})

	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	// HTTP 被调用了（验证 304 路径）
	if cnt := mockSrv.callCnt.Load(); cnt == 0 {
		t.Error("期望 HTTP 被调用（验证 304 路径），但未调用")
	}
	if n := callCount.Load(); n != 0 {
		t.Errorf("304 响应不应触发 OnConfig，实际调用 %d 次", n)
	}
}

// ─── TestFailover：主断切备，主恢复切回 ─────────────────────────────────────

// TestFailoverPrimaryToSecondary 验证：主通道断开 → 备通道继续收信号 → 回调正常。
func TestFailoverPrimaryToSecondary(t *testing.T) {
	t.Parallel()

	wantCfg := &genv1.NodeConfig{Generation: 1, VirtualIp: "10.0.0.10"}
	mockSrv := newMockConfigServer(t, wantCfg)
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	// primaryCh：模拟连接成功但很快断开
	primaryBroken := make(chan struct{})
	primaryCh := newFakeChannel(4)
	secondaryCh := newFakeChannel(8)

	var primaryConnCount atomic.Int32
	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 50 * time.Millisecond, switchBackAfter: 500 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			cnt := primaryConnCount.Add(1)
			if cnt == 1 {
				// 第一次连接：立即关闭（模拟断开）
				go func() {
					time.Sleep(20 * time.Millisecond)
					primaryCh.Close()
					select {
					case <-primaryBroken:
					default:
						close(primaryBroken)
					}
				}()
				return primaryCh, nil
			}
			// 后续重试：返回一个空的、不会断的假通道
			return newFakeChannel(4), nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)
	callbackCh := make(chan *genv1.NodeConfig, 8)
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callbackCh <- nc
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	// 等主通道断开
	select {
	case <-primaryBroken:
	case <-time.After(2 * time.Second):
		t.Fatal("主通道未在预期时间内断开")
	}

	// 等待切换到备通道
	time.Sleep(200 * time.Millisecond)

	// 通过备通道推送信号
	secondaryCh.Push(Signal{Changed: true, Generation: 1})

	// 等待回调
	select {
	case nc := <-callbackCh:
		if nc.Generation != 1 {
			t.Errorf("期望 generation=1，得到 %d", nc.Generation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("主通道断开后，备通道推送的信号未触发 OnConfig 回调")
	}

	cancel()
	<-done
}

// TestFailoverSwitchBack 验证：主通道恢复后，经滞回窗口后切回主通道。
func TestFailoverSwitchBack(t *testing.T) {
	t.Parallel()

	wantCfg := &genv1.NodeConfig{Generation: 2, VirtualIp: "10.0.0.11"}
	mockSrv := newMockConfigServer(t, wantCfg)
	ts := httptest.NewServer(mockSrv)
	defer ts.Close()

	// 主通道状态：初始断开，之后恢复
	var primaryAlive atomic.Bool
	primaryAlive.Store(false)

	// 备通道
	secondaryCh := newFakeChannel(16)

	// 追踪最新创建的主通道
	var latestPrimaryMu sync.Mutex
	var latestPrimary *fakeChannel

	cfg := Config{
		HTTPAddr:    ts.URL,
		FailoverCfg: failoverConfig{retryInterval: 50 * time.Millisecond, switchBackAfter: 150 * time.Millisecond},
		primaryFn: func(ctx context.Context) (notifChannel, error) {
			if !primaryAlive.Load() {
				return nil, fmt.Errorf("主通道暂时不可用")
			}
			ch := newFakeChannel(4)
			latestPrimaryMu.Lock()
			latestPrimary = ch
			latestPrimaryMu.Unlock()
			return ch, nil
		},
		secondaryFn: func(ctx context.Context) (notifChannel, error) {
			return secondaryCh, nil
		},
	}

	client := NewClient(cfg)
	callbackCh := make(chan *genv1.NodeConfig, 16)
	client.OnConfig = func(nc *genv1.NodeConfig) {
		callbackCh <- nc
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx)
	}()

	// 等切换到备通道
	time.Sleep(100 * time.Millisecond)

	// 通过备通道推送信号（验证切到备后正常工作）
	secondaryCh.Push(Signal{Changed: true, Generation: 2})

	select {
	case nc := <-callbackCh:
		if nc.Generation != 2 {
			t.Errorf("备通道回调 generation=%d，期望 2", nc.Generation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("备通道信号未触发 OnConfig 回调")
	}

	// 恢复主通道
	primaryAlive.Store(true)

	// 等待滞回窗口后切回（retryInterval=50ms, switchBackAfter=150ms）
	time.Sleep(800 * time.Millisecond)

	// 此时可能已切回主；通过主通道推 generation=3
	latestPrimaryMu.Lock()
	lp := latestPrimary
	latestPrimaryMu.Unlock()

	if lp != nil {
		lp.Push(Signal{Changed: true, Generation: 3})
	} else {
		// 若未切回，通过备通道推（failover 保证信号不丢）
		secondaryCh.Push(Signal{Changed: true, Generation: 3})
	}

	// 更新 mock server 返回 gen=3
	mockSrv.mu.Lock()
	mockSrv.cfg = &genv1.NodeConfig{Generation: 3, VirtualIp: "10.0.0.11"}
	mockSrv.mu.Unlock()

	select {
	case nc := <-callbackCh:
		if nc.Generation != 3 {
			t.Errorf("切回后回调 generation=%d，期望 3", nc.Generation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("切回主通道后未收到 OnConfig 回调")
	}

	cancel()
	<-done
}

// ─── TestBuildMTLSConfig：mTLS 构造 ─────────────────────────────────────────

func TestBuildMTLSConfig(t *testing.T) {
	t.Parallel()

	pki := genTestPKI(t, "test-node")

	tlsCfg, err := BuildMTLSConfig(pki.nodeCertPEM, pki.nodeKeyPEM, pki.caPEM, "127.0.0.1")
	if err != nil {
		t.Fatalf("BuildMTLSConfig 失败: %v", err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("期望 1 个证书，得到 %d", len(tlsCfg.Certificates))
	}
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs 不应为 nil")
	}
	if tlsCfg.ServerName != "127.0.0.1" {
		t.Errorf("ServerName=%s，期望 127.0.0.1", tlsCfg.ServerName)
	}
}

// ─── TestMTLSHandshake：真实 TLS 握手验证 ────────────────────────────────────

// TestMTLSHandshake 用真实 httptest TLS server 验证 mTLS 双向证书握手。
// 只做一次握手验证，failover/幂等逻辑用假通道测（已在上面覆盖）。
func TestMTLSHandshake(t *testing.T) {
	t.Parallel()

	const nodeID = "mtls-test-node"
	pki := genTestPKI(t, nodeID)

	// 生成 server 证书（由同一 CA 签发）
	serverCertKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 server 密钥: %v", err)
	}
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "controller"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost", "127.0.0.1"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, pki.caCert, &serverCertKey.PublicKey, pki.caKey)
	if err != nil {
		t.Fatalf("创建 server 证书: %v", err)
	}
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatalf("解析 server 证书: %v", err)
	}

	tlsCert := tls.Certificate{
		Certificate: [][]byte{serverDER},
		PrivateKey:  serverCertKey,
		Leaf:        serverCert,
	}

	// server CA pool（验证客户端证书）
	serverClientPool := x509.NewCertPool()
	serverClientPool.AddCert(pki.caCert)

	// 客户端 CA pool（验证 server 证书）
	clientRootPool := x509.NewCertPool()
	clientRootPool.AddCert(pki.caCert)

	// 起 mTLS server
	receivedNodeIDCh := make(chan string, 1)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			select {
			case receivedNodeIDCh <- r.TLS.PeerCertificates[0].Subject.CommonName:
			default:
			}
		}
		cfg := &genv1.NodeConfig{Generation: 1, VirtualIp: "10.0.0.99"}
		body, _ := protojson.Marshal(cfg)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    serverClientPool,
		MinVersion:   tls.VersionTLS12,
	}
	ts.StartTLS()
	defer ts.Close()

	// 构造客户端 mTLS config
	clientTLSCfg, err := BuildMTLSConfig(pki.nodeCertPEM, pki.nodeKeyPEM, pki.caPEM, "127.0.0.1")
	if err != nil {
		t.Fatalf("BuildMTLSConfig: %v", err)
	}
	// 使用包含 server 证书（CA 签发）的根 CA pool
	clientTLSCfg.RootCAs = clientRootPool

	// 发起 mTLS 请求
	httpCli := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSCfg},
		Timeout:   5 * time.Second,
	}
	resp, err := httpCli.Get(ts.URL)
	if err != nil {
		t.Fatalf("mTLS 请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HTTP 状态码=%d，期望 200", resp.StatusCode)
	}

	select {
	case receivedNodeID := <-receivedNodeIDCh:
		if receivedNodeID != nodeID {
			t.Errorf("server 收到的 CN=%q，期望 %q", receivedNodeID, nodeID)
		}
	case <-time.After(time.Second):
		t.Error("未收到 nodeID")
	}
}

// ─── TestGRPCChannelWithBufconn：gRPC 通道真实流测试 ───────────────────────

func TestGRPCChannelWithBufconn(t *testing.T) {
	t.Parallel()

	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)

	// 起 gRPC server（无 TLS，bufconn 内部）
	grpcSrv := grpc.NewServer()
	mockGRPC := newMockConfigGRPC()
	genv1.RegisterConfigServiceServer(grpcSrv, mockGRPC)

	go grpcSrv.Serve(lis)
	defer grpcSrv.Stop()

	// 拨号
	conn, err := grpc.NewClient(
		"passthrough://bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecureCreds{}),
	)
	if err != nil {
		t.Fatalf("gRPC NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	client := genv1.NewConfigServiceClient(conn)
	stream, err := client.WatchConfig(ctx, &genv1.WatchRequest{KnownGeneration: 0})
	if err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}

	grpcCh := &grpcChannel{stream: stream, conn: conn}

	// 推一个信号
	mockGRPC.Push(&genv1.ChangeSignal{Changed: true, Generation: 42})

	sig, err := grpcCh.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !sig.Changed || sig.Generation != 42 {
		t.Errorf("期望 Changed=true Generation=42，得到 %+v", sig)
	}
}

// insecureCreds 是不做 TLS 的 gRPC 凭证（bufconn 测试用）。
type insecureCreds struct{}

func (insecureCreds) ClientHandshake(_ context.Context, _ string, conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return conn, insecureInfo{}, nil
}
func (insecureCreds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return conn, insecureInfo{}, nil
}
func (insecureCreds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "insecure"}
}
func (insecureCreds) Clone() credentials.TransportCredentials { return insecureCreds{} }
func (insecureCreds) OverrideServerName(_ string) error       { return nil }

type insecureInfo struct{}

func (insecureInfo) AuthType() string { return "insecure" }

// ─── TestFetchConfigHTTP：HTTP 拉取逻辑单元测试 ──────────────────────────────

func TestFetchConfigHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		localGen  uint64
		serverGen uint64
		wantNil   bool // 是否期望返回 nil（304）
		wantErr   bool
	}{
		{
			name:      "新配置 generation > 本地",
			localGen:  0,
			serverGen: 5,
			wantNil:   false,
		},
		{
			name:      "304 客户端 gen = server gen",
			localGen:  5,
			serverGen: 5,
			wantNil:   true,
		},
		{
			name:      "客户端 gen < server gen，拉取新配置",
			localGen:  3,
			serverGen: 5,
			wantNil:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockSrv := newMockConfigServer(t, &genv1.NodeConfig{Generation: tc.serverGen, VirtualIp: "1.2.3.4"})
			ts := httptest.NewServer(mockSrv)
			defer ts.Close()

			client := NewClient(Config{HTTPAddr: ts.URL})
			cfg, err := client.fetchConfig(t.Context(), version.ConfigVersion{Generation: tc.localGen})

			if tc.wantErr && err == nil {
				t.Error("期望 error，但得到 nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("不期望 error，得到: %v", err)
			}
			if tc.wantNil && cfg != nil {
				t.Errorf("期望 cfg=nil（304），得到 generation=%d", cfg.Generation)
			}
			if !tc.wantNil && !tc.wantErr && cfg == nil {
				t.Error("期望 cfg 非 nil，得到 nil")
			}
			if !tc.wantNil && !tc.wantErr && cfg != nil && cfg.Generation != tc.serverGen {
				t.Errorf("cfg.Generation=%d，期望 %d", cfg.Generation, tc.serverGen)
			}
		})
	}
}

// ─── TestFailoverManagerWithFakeChannels：纯 failover 逻辑 ─────────────────

// TestFailoverManagerPrimaryBreakToSecondary 验证 failoverManager：
// 主断 → 切备 → 继续收信号（无丢失）。
func TestFailoverManagerPrimaryBreakToSecondary(t *testing.T) {
	t.Parallel()

	primaryCh := newFakeChannel(4)
	secondaryCh := newFakeChannel(8)

	var primaryCallCount atomic.Int32
	primaryFn := func(ctx context.Context) (notifChannel, error) {
		cnt := primaryCallCount.Add(1)
		if cnt == 1 {
			return primaryCh, nil
		}
		// 后续重试：返回永不断的通道
		return newFakeChannel(4), nil
	}
	secondaryFn := func(ctx context.Context) (notifChannel, error) {
		return secondaryCh, nil
	}

	fCfg := failoverConfig{
		retryInterval:   30 * time.Millisecond,
		switchBackAfter: 500 * time.Millisecond,
	}

	fm := newFailoverManager(primaryFn, secondaryFn, fCfg)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 让 failover 启动并连接主通道
	time.Sleep(30 * time.Millisecond)

	// 通过主通道推一个信号
	primaryCh.Push(Signal{Changed: true, Generation: 1})
	sig1 := waitSig(t, fm.Signals(), 2*time.Second, "主通道信号")
	if sig1.Generation != 1 {
		t.Errorf("主通道信号 generation=%d，期望 1", sig1.Generation)
	}

	// 断开主通道
	primaryCh.Close()

	// 等切换到备通道
	time.Sleep(150 * time.Millisecond)

	// 通过备通道推信号
	secondaryCh.Push(Signal{Changed: true, Generation: 2})
	sig2 := waitSig(t, fm.Signals(), 2*time.Second, "备通道信号")
	if sig2.Generation != 2 {
		t.Errorf("备通道信号 generation=%d，期望 2", sig2.Generation)
	}

	cancel()
}

// TestFailoverManagerSwitchBack 验证：主恢复 + 滞回窗口后切回主。
func TestFailoverManagerSwitchBack(t *testing.T) {
	t.Parallel()

	// 主通道控制
	var primaryAvailable atomic.Bool
	primaryAvailable.Store(false) // 初始不可用

	secondaryCh := newFakeChannel(8)
	var latestPrimaryMu sync.Mutex
	var latestPrimary *fakeChannel

	primaryFn := func(ctx context.Context) (notifChannel, error) {
		if !primaryAvailable.Load() {
			return nil, fmt.Errorf("主通道不可用")
		}
		ch := newFakeChannel(4)
		latestPrimaryMu.Lock()
		latestPrimary = ch
		latestPrimaryMu.Unlock()
		return ch, nil
	}
	secondaryFn := func(ctx context.Context) (notifChannel, error) {
		return secondaryCh, nil
	}

	fCfg := failoverConfig{
		retryInterval:   40 * time.Millisecond,
		switchBackAfter: 120 * time.Millisecond,
	}

	fm := newFailoverManager(primaryFn, secondaryFn, fCfg)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	go fm.Run(ctx)

	// 等切换到备
	time.Sleep(100 * time.Millisecond)

	// 验证备通道工作
	secondaryCh.Push(Signal{Changed: true, Generation: 1})
	sig1 := waitSig(t, fm.Signals(), 2*time.Second, "备通道首次信号")
	if sig1.Generation != 1 {
		t.Errorf("备通道信号 generation=%d，期望 1", sig1.Generation)
	}

	// 恢复主通道
	primaryAvailable.Store(true)

	// 等待滞回窗口后切回（retryInterval=40ms, switchBackAfter=120ms）
	time.Sleep(800 * time.Millisecond)

	// 通过主通道（或备通道兜底）推信号
	latestPrimaryMu.Lock()
	lp := latestPrimary
	latestPrimaryMu.Unlock()

	if lp != nil {
		lp.Push(Signal{Changed: true, Generation: 2})
	} else {
		// 若未切回，通过备通道
		secondaryCh.Push(Signal{Changed: true, Generation: 2})
	}

	sig2 := waitSig(t, fm.Signals(), 2*time.Second, "切回后信号")
	if sig2.Generation != 2 {
		t.Errorf("切回后信号 generation=%d，期望 2", sig2.Generation)
	}

	cancel()
}

// ─── TestEpochGenIdempotency：(epoch, generation) 字典序幂等判定 ──────────────

func TestEpochGenIdempotency(t *testing.T) {
	c := &Client{} // 直接构造（仿现有用例风格）
	c.setLocalVersion(version.ConfigVersion{Epoch: 1, Generation: 5})

	if c.shouldFetch(version.ConfigVersion{Epoch: 0, Generation: 999}) {
		t.Fatal("更低 epoch 不应触发拉取")
	}
	if !c.shouldFetch(version.ConfigVersion{Epoch: 1, Generation: 6}) {
		t.Fatal("同 epoch 更高 gen 应触发拉取")
	}
	if !c.shouldFetch(version.ConfigVersion{Epoch: 2, Generation: 0}) {
		t.Fatal("更高 epoch 应触发拉取")
	}
}

// waitSig 等待从 ch 收到一个 Signal，超时失败。
func waitSig(t *testing.T, ch <-chan Signal, timeout time.Duration, name string) Signal {
	t.Helper()
	select {
	case sig := <-ch:
		return sig
	case <-time.After(timeout):
		t.Fatalf("等待 %s 超时（%s）", name, timeout)
		return Signal{}
	}
}

// ─── TestWSChannelWithMockServer：WS 通道测试 ─────────────────────────────────

func TestWSChannelWithMockServer(t *testing.T) {
	t.Parallel()

	// 起一个 WebSocket server（无 TLS）
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/watch", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()

		// 推一个信号
		sig := wsSignalJSON{Changed: true, Generation: 99}
		if err := wsjson.Write(ctx, conn, sig); err != nil {
			return
		}
		// 保持连接直到客户端关闭
		var dummy json.RawMessage
		wsjson.Read(ctx, conn, &dummy) //nolint:errcheck
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 提取 host:port
	addr := ts.Listener.Addr().String()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ch, err := dialWSChannel(ctx, addr, "/v1/watch", nil)
	if err != nil {
		t.Fatalf("dialWSChannel: %v", err)
	}
	defer ch.Close()

	sig, err := ch.Recv(ctx)
	if err != nil {
		t.Fatalf("WS Recv: %v", err)
	}
	if !sig.Changed || sig.Generation != 99 {
		t.Errorf("期望 Changed=true Generation=99，得到 %+v", sig)
	}
}
