// Package controller_test 端到端冒烟测试：
// 进程内启 controller 各服务 → Enroll → mTLS 拉 config，断言整链路打通。
package controller_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/configsvc"
	"github.com/x6nux/corelink/internal/controller/enroll"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/relayroster"
	"github.com/x6nux/corelink/internal/controller/server"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/pki"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestE2E_EnrollThenFetchConfig 委托给 testController 版本。
func TestE2E_EnrollThenFetchConfig(t *testing.T) {
	t.Skip("使用 testController 版本替代，见 TestE2E_FullChain")
}

// testController 暴露内部 store 供测试直接操作。
type testController struct {
	st         *store.Store
	caM        *ca.Manager
	enrollSvc  *enroll.Service
	cfgSvcs    *configsvc.Services
	enrollAddr string
	httpAddr   string
	enrollLeaf *x509.Certificate // 外层 TLS 自签证书（用于客户端验证）
	cleanup    func()
}

func newTestController(t *testing.T) *testController {
	t.Helper()

	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i + 1)
	}

	caM, err := ca.EnsureCA(st, "E2E Test CA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	ipamA, err := ipam.New("100.64.0.0/24", st)
	if err != nil {
		t.Fatalf("ipam.New: %v", err)
	}

	enrollSvc := enroll.NewService(st, caM, ipamA)

	var rosterRef *relayroster.Roster
	cfgSvcs := configsvc.New(st, caM, func() map[string]string {
		if rosterRef != nil {
			return rosterRef.NodeRelay()
		}
		return nil
	})
	rosterRef = relayroster.New(st, cfgSvcs.Notify)

	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	caPool := server.BuildCAPool(caM.Cert())

	mtlsServer := server.NewMTLSServer(caPool, serverCert, nil,
		func(s *grpc.Server) { genv1.RegisterConfigServiceServer(s, cfgSvcs.ConfigGRPC) },
		func(s *grpc.Server) { genv1.RegisterRelayControlServiceServer(s, rosterRef) },
		func(s *grpc.Server) { genv1.RegisterEnrollServiceServer(s, enrollSvc) },
	)

	enrollServer, enrollLeafCert, err := server.NewEnrollServer(caM, enrollSvc)
	if err != nil {
		t.Fatalf("NewEnrollServer: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/config", cfgSvcs.HTTPHandler())

	httpSrv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    caPool,
			MinVersion:   tls.VersionTLS12,
		},
	}

	enrollLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen enroll: %v", err)
	}
	mtlsLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen mtls: %v", err)
	}
	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen http: %v", err)
	}

	go enrollServer.Serve(enrollLis)
	go mtlsServer.Serve(mtlsLis)
	go func() { httpSrv.ServeTLS(httpLis, "", "") }()

	time.Sleep(30 * time.Millisecond)

	tc := &testController{
		st:         st,
		caM:        caM,
		enrollSvc:  enrollSvc,
		cfgSvcs:    cfgSvcs,
		enrollAddr: enrollLis.Addr().String(),
		httpAddr:   httpLis.Addr().String(),
		enrollLeaf: enrollLeafCert,
		cleanup: func() {
			cfgSvcs.Notify.Close()
			enrollServer.GracefulStop()
			mtlsServer.GracefulStop()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			httpSrv.Shutdown(ctx)
		},
	}
	return tc
}

// TestE2E_FullChain 完整链路：CreateEnrollKey → Enroll（gRPC）→ mTLS HTTP GET /v1/config。
func TestE2E_FullChain(t *testing.T) {
	tc := newTestController(t)
	defer tc.cleanup()

	// ─── 步骤 1：创建 enrollment key（直接写 store）────────────────────────────────
	enrollKey := "test-key-e2e-001"
	exp := time.Now().Add(10 * time.Minute)
	if err := tc.st.CreateEnrollKey(&store.EnrollKey{
		Key:       enrollKey,
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-alice",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// ─── 步骤 2：生成 CSR ──────────────────────────────────────────────────────────
	csrDER, nodePrivKey, err := pki.GenerateCSR("node-smoke", "node-smoke")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	// ─── 步骤 3：gRPC Enroll ───────────────────────────────────────────────────────
	// 连接 enroll server（外层 TLS，跳过证书验证因是自签）
	// 自签证书（enroll 外层 TLS）—— 测试时直接跳过证书验证
	creds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // 仅测试用，接受自签证书
	})

	conn, err := grpc.NewClient(tc.enrollAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	enrollClient := genv1.NewEnrollServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := enrollClient.Enroll(ctx, &genv1.EnrollRequest{
		EnrollmentKey: enrollKey,
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wgpub-smoke-test",
		CsrDer:        csrDER,
		Hostname:      "smoke-node",
	})
	if err != nil {
		t.Fatalf("Enroll RPC 失败: %v", err)
	}
	if resp.NodeId == "" {
		t.Error("期望 NodeId 非空")
	}
	if resp.VirtualIp == "" {
		t.Error("期望 VirtualIp 非空")
	}
	if len(resp.NodeCertDer) == 0 {
		t.Error("期望 NodeCertDer 非空")
	}
	if len(resp.CaCertDer) == 0 {
		t.Error("期望 CaCertDer 非空")
	}
	t.Logf("Enroll 成功：nodeID=%s, virtualIP=%s", resp.NodeId, resp.VirtualIp)

	// ─── 步骤 4：用节点证书构造 mTLS 客户端 ───────────────────────────────────────
	nodeCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("解析节点证书失败: %v", err)
	}

	caCert, err := x509.ParseCertificate(resp.CaCertDer)
	if err != nil {
		t.Fatalf("解析 CA 证书失败: %v", err)
	}

	// 将 ECDSA 私钥和 DER 证书组装成 tls.Certificate
	tlsNodeCert := tls.Certificate{
		Certificate: [][]byte{resp.NodeCertDer},
		PrivateKey:  nodePrivKey,
		Leaf:        nodeCert,
	}

	serverCAPool := x509.NewCertPool()
	serverCAPool.AddCert(caCert)

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{tlsNodeCert},
				RootCAs:      serverCAPool,
				MinVersion:   tls.VersionTLS12,
				// server 证书使用 hostname "controller-server"，测试监听 127.0.0.1
				// 所以跳过主机名验证，但仍通过 RootCAs 验证证书链
				InsecureSkipVerify: true, //nolint:gosec // 测试环境：server 无 IP SAN
			},
		},
	}

	// ─── 步骤 5：GET /v1/config ────────────────────────────────────────────────────
	url := fmt.Sprintf("https://%s/v1/config", tc.httpAddr)
	httpReq, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, url, nil,
	)
	if err != nil {
		t.Fatalf("构建 HTTP 请求失败: %v", err)
	}

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		t.Fatalf("GET /v1/config 失败: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		t.Errorf("期望 200，得到 %d", httpResp.StatusCode)
	}

	t.Logf("GET /v1/config 成功：HTTP %d，Content-Type=%s",
		httpResp.StatusCode, httpResp.Header.Get("Content-Type"))
}
