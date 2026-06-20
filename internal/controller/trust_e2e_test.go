// Package controller_test 端到端信任测试：验证 Part A 两阶段信任 + 吊销整体行为。
package controller_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/configsvc"
	"github.com/x6nux/corelink/internal/controller/enroll"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/relayroster"
	"github.com/x6nux/corelink/internal/controller/server"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/jointoken"
	"github.com/x6nux/corelink/internal/pki"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// trustController 复刻 e2e_test.go 的进程内 controller 搭建，但走新信任模型：
//   - enroll server 出示 CA 签发的完整链 [leaf, CA]（Section 2）
//   - mTLS server / HTTP 挂 CRL 拦截器（Section 3）
//   - 暴露 CA SPKI 哈希（caHash）供 node 阶段① ca_hash 验链
type trustController struct {
	st         *store.Store
	caM        *ca.Manager
	enrollSvc  *enroll.Service
	cfgSvcs    *configsvc.Services
	enrollAddr string
	mtlsAddr   string
	httpAddr   string
	caHash     string // CA SPKI 哈希 "sha256:<hex>"
	cleanup    func()
}

func newTrustController(t *testing.T) *trustController {
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
	caM, err := ca.EnsureCA(st, "Trust E2E CA", encKey)
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

	// mTLS server 证书：CA 签发，出示完整链 [leaf, CA]（Section 2 改造后的 BuildServerCert）。
	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	caPool := server.BuildCAPool(caM.Cert())

	// CRL 缓存：mTLS/HTTP 热路径共用（Section 2 的 NewCRLCache，provider = caM.CurrentCRL）。
	crlCache := server.NewCRLCache(caM.CurrentCRL, time.Second)

	// mTLS gRPC server：挂 CRL 拦截器（Section 2/3 新签名带 crl 实参）。
	mtlsServer := server.NewMTLSServer(caPool, serverCert, crlCache,
		func(s *grpc.Server) { genv1.RegisterConfigServiceServer(s, cfgSvcs.ConfigGRPC) },
		func(s *grpc.Server) { genv1.RegisterRelayControlServiceServer(s, rosterRef) },
		func(s *grpc.Server) { genv1.RegisterEnrollServiceServer(s, enrollSvc) },
	)

	// enroll server：CA 签发证书 + 出示完整链（Section 2 新签名 NewEnrollServer(caM, svc) 返回 3 值）。
	enrollServer, _, err := server.NewEnrollServer(caM, enrollSvc)
	if err != nil {
		t.Fatalf("NewEnrollServer: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/config", cfgSvcs.HTTPHandler())
	httpSrv := &http.Server{
		// HTTP 也包 CRL 中间件（Section 2 的 NewCRLHTTPMiddleware），吊销证书访问 /v1/config 返回 403。
		Handler: server.NewCRLHTTPMiddleware(crlCache)(mux),
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
	go func() { _ = httpSrv.ServeTLS(httpLis, "", "") }()
	time.Sleep(30 * time.Millisecond)

	tc := &trustController{
		st:         st,
		caM:        caM,
		enrollSvc:  enrollSvc,
		cfgSvcs:    cfgSvcs,
		enrollAddr: enrollLis.Addr().String(),
		mtlsAddr:   mtlsLis.Addr().String(),
		httpAddr:   httpLis.Addr().String(),
		caHash:     tunnel.CASPKIHash(caM.Cert()),
		cleanup: func() {
			cfgSvcs.Notify.Close()
			enrollServer.GracefulStop()
			mtlsServer.GracefulStop()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(ctx)
		},
	}
	return tc
}

// enrollOverCAHash 用 ca_hash 经 caPinnedVerifier 验 enroll server 链，跑一次 Enroll。
// 返回 EnrollResponse（含 NodeCertDer/CaCertDer/VirtualIp/NodeId）与节点私钥。
func enrollOverCAHash(t *testing.T, addr, caHash, enrollKey, hostname string) (*genv1.EnrollResponse, *ecdsa.PrivateKey) {
	t.Helper()
	csrDER, nodeKey, err := pki.GenerateCSR(hostname)
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	// 阶段①：node 仅有 token 里的 ca_hash，用 caPinnedVerifier 验 controller 出示的链。
	tlsCfg, err := tunnel.ClientTLSConfig(&tunnel.TLSOptions{
		Mode:         tunnel.TLSModePinned,
		ServerName:   serverNameOf(addr),
		PinnedCAHash: caHash,
	})
	if err != nil {
		t.Fatalf("ClientTLSConfig(pinned ca_hash): %v", err)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := genv1.NewEnrollServiceClient(conn).Enroll(ctx, &genv1.EnrollRequest{
		EnrollmentKey: enrollKey,
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wgpub-" + hostname,
		CsrDer:        csrDER,
		Hostname:      hostname,
	})
	if err != nil {
		t.Fatalf("Enroll RPC: %v", err)
	}
	return resp, nodeKey
}

func serverNameOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// TestTrustE2E_TwoPhase 验证 Part A 两阶段信任完整链路：
//
//	阶段①  enroll：错误 ca_hash 被拒、正确 ca_hash 通过并拿到身份。
//	阶段②  用本地 CA（resp.CaCertDer）的 SPKI 作 wantHash，经 caPinnedVerifier 访问 mTLS HTTP /v1/config 成功。
func TestTrustE2E_TwoPhase(t *testing.T) {
	tc := newTrustController(t)
	defer tc.cleanup()

	// 准备 enrollment key。
	enrollKey := "trust-e2e-key-001"
	exp := time.Now().Add(10 * time.Minute)
	if err := tc.st.CreateEnrollKey(&store.EnrollKey{
		Key: enrollKey, Reusable: false, ExpiresAt: &exp, Tag: "user-alice",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// ── 阶段①-A：错误 ca_hash → caPinnedVerifier 链中无 SPKI 命中 → 握手失败被拒 ──
	badHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	badCfg, err := tunnel.ClientTLSConfig(&tunnel.TLSOptions{
		Mode:         tunnel.TLSModePinned,
		ServerName:   serverNameOf(tc.enrollAddr),
		PinnedCAHash: badHash,
	})
	if err != nil {
		t.Fatalf("ClientTLSConfig(bad): %v", err)
	}
	badConn, err := grpc.NewClient(tc.enrollAddr, grpc.WithTransportCredentials(credentials.NewTLS(badCfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient(bad): %v", err)
	}
	defer badConn.Close()
	ctxBad, cancelBad := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelBad()
	if _, err := genv1.NewEnrollServiceClient(badConn).Enroll(ctxBad, &genv1.EnrollRequest{
		EnrollmentKey: enrollKey,
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wgpub-bad",
		CsrDer:        []byte{0x01},
		Hostname:      "bad-node",
	}); err == nil {
		t.Fatal("期望错误 ca_hash 导致握手失败被拒，实际 Enroll 成功")
	}

	// ── 阶段①-B：正确 ca_hash → 验链通过 → Enroll 成功拿到身份 ──
	resp, nodeKey := enrollOverCAHash(t, tc.enrollAddr, tc.caHash, enrollKey, "good-node")
	if resp.NodeId == "" || resp.VirtualIp == "" || len(resp.NodeCertDer) == 0 || len(resp.CaCertDer) == 0 {
		t.Fatalf("Enroll 响应字段不完整: %+v", resp)
	}

	// ── 阶段②：用本地 CA（resp.CaCertDer）SPKI 作 wantHash，经 caPinnedVerifier 访问 mTLS HTTP ──
	localCA, err := x509.ParseCertificate(resp.CaCertDer)
	if err != nil {
		t.Fatalf("解析下发 CA: %v", err)
	}
	wantHash := tunnel.CASPKIHash(localCA)
	if wantHash != tc.caHash {
		t.Fatalf("下发 CA SPKI 哈希 %s 应与 controller caHash %s 一致", wantHash, tc.caHash)
	}

	nodeCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("解析节点证书: %v", err)
	}
	tlsNodeCert := tls.Certificate{
		Certificate: [][]byte{resp.NodeCertDer},
		PrivateKey:  nodeKey,
		Leaf:        nodeCert,
	}

	// 阶段② 客户端：mTLS 出示节点证书 + 用 caPinnedVerifier(wantHash) 验 controller 服务端链。
	clientTLS, err := tunnel.ClientTLSConfig(&tunnel.TLSOptions{
		Mode:         tunnel.TLSModePinned,
		ServerName:   serverNameOf(tc.httpAddr),
		PinnedCAHash: wantHash,
	})
	if err != nil {
		t.Fatalf("ClientTLSConfig(阶段②): %v", err)
	}
	clientTLS.Certificates = []tls.Certificate{tlsNodeCert}

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	url := fmt.Sprintf("https://%s/v1/config", tc.httpAddr)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构建请求: %v", err)
	}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		t.Fatalf("GET /v1/config（阶段②）: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Errorf("阶段② 期望 200，得到 %d", httpResp.StatusCode)
	}
}

// TestTrustE2E_RevocationEnforced 验证吊销在控制面真正生效（#5+#15）：
//  1. node enroll 拿到证书并 mTLS 访问 /v1/config 成功（吊销前基线）。
//  2. Revoke 该节点证书 → 其 mTLS 访问被 CRL 拦截器拒绝（HTTP 403）。
//  3. 其 Renew（gRPC mTLS）被前置吊销校验拒绝（PermissionDenied）。
func TestTrustE2E_RevocationEnforced(t *testing.T) {
	tc := newTrustController(t)
	defer tc.cleanup()

	enrollKey := "trust-revoke-key-001"
	exp := time.Now().Add(10 * time.Minute)
	if err := tc.st.CreateEnrollKey(&store.EnrollKey{
		Key: enrollKey, Reusable: false, ExpiresAt: &exp, Tag: "user-bob",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 入网拿身份。
	resp, nodeKey := enrollOverCAHash(t, tc.enrollAddr, tc.caHash, enrollKey, "revoke-node")
	nodeCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("解析节点证书: %v", err)
	}
	localCA, err := x509.ParseCertificate(resp.CaCertDer)
	if err != nil {
		t.Fatalf("解析 CA: %v", err)
	}
	wantHash := tunnel.CASPKIHash(localCA)
	tlsNodeCert := tls.Certificate{
		Certificate: [][]byte{resp.NodeCertDer},
		PrivateKey:  nodeKey,
		Leaf:        nodeCert,
	}

	// 构造阶段② mTLS HTTP 客户端（caPinnedVerifier）。
	mkHTTPClient := func() *http.Client {
		cfg, err := tunnel.ClientTLSConfig(&tunnel.TLSOptions{
			Mode: tunnel.TLSModePinned, ServerName: serverNameOf(tc.httpAddr), PinnedCAHash: wantHash,
		})
		if err != nil {
			t.Fatalf("ClientTLSConfig: %v", err)
		}
		cfg.Certificates = []tls.Certificate{tlsNodeCert}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	}
	doConfigGet := func(c *http.Client) (int, error) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			fmt.Sprintf("https://%s/v1/config", tc.httpAddr), nil)
		r, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		defer r.Body.Close()
		return r.StatusCode, nil
	}

	// 构造 mTLS gRPC 连接（Renew 用）。
	mkGRPC := func() *grpc.ClientConn {
		cfg, err := tunnel.ClientTLSConfig(&tunnel.TLSOptions{
			Mode: tunnel.TLSModePinned, ServerName: serverNameOf(tc.mtlsAddr), PinnedCAHash: wantHash,
		})
		if err != nil {
			t.Fatalf("ClientTLSConfig(grpc): %v", err)
		}
		cfg.Certificates = []tls.Certificate{tlsNodeCert}
		conn, err := grpc.NewClient(tc.mtlsAddr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatalf("grpc.NewClient(mtls): %v", err)
		}
		return conn
	}

	// ── 吊销前基线：mTLS /v1/config 成功（200）──
	if code, err := doConfigGet(mkHTTPClient()); err != nil || code != http.StatusOK {
		t.Fatalf("吊销前 /v1/config 应 200，得到 code=%d err=%v", code, err)
	}

	// ── 吊销该节点证书 ──
	serial := nodeCert.SerialNumber.Text(10)
	if err := tc.caM.Revoke(serial); err != nil {
		t.Fatalf("Revoke(%s): %v", serial, err)
	}
	// 确认 store 已记录吊销（IsCertRevoked，Section 3 新增）。
	if rev, err := tc.st.IsCertRevoked(serial); err != nil || !rev {
		t.Fatalf("IsCertRevoked(%s) 应为 true，得到 rev=%v err=%v", serial, rev, err)
	}

	// ── 吊销后：mTLS /v1/config 被 CRL 拦截器拒绝（HTTP 403）──
	// CRL 拦截器内部 TTL 缓存可能滞后，轮询直到拦截生效（或超时失败）。
	deadline := time.Now().Add(3 * time.Second)
	var lastCode int
	var lastErr error
	for time.Now().Before(deadline) {
		lastCode, lastErr = doConfigGet(mkHTTPClient())
		if lastCode == http.StatusForbidden {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastCode != http.StatusForbidden {
		t.Fatalf("吊销后 /v1/config 应被 CRL 拦截返回 403，得到 code=%d err=%v", lastCode, lastErr)
	}

	// ── 吊销后：Renew（gRPC mTLS）被拒 ──
	// 实测双重拦截下 mTLS CRL Unary 拦截器先于 Renew handler 命中：吊销证书的 Renew 在
	// 拦截器层即被拒，返回 codes.Unauthenticated（"证书已吊销"）；handler 内部的吊销前置
	// 校验返回 codes.PermissionDenied（"证书已吊销，拒绝续签"）是第二道闸。两者皆满足
	// spec A10「已吊销节点的 Renew 必须被拒」的语义，故断言接受这两种 denied 码之一。
	conn := mkGRPC()
	defer conn.Close()
	newCSR, _, err := pki.GenerateCSR("revoke-node-renew")
	if err != nil {
		t.Fatalf("GenerateCSR(renew): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := genv1.NewEnrollServiceClient(conn).Renew(ctx, &genv1.RenewRequest{CsrDer: newCSR}); err == nil {
		t.Fatal("吊销后 Renew 应被拒，实际成功")
	} else if code := status.Code(err); code != codes.PermissionDenied && code != codes.Unauthenticated {
		t.Fatalf("吊销后 Renew 应返回 PermissionDenied 或 Unauthenticated，得到 %v: %v", code, err)
	}
}

// TestTrustE2E_TokenRoundTrip 验证 A2/A4 一键安装的"配置落地"往返（不起真实 systemd）：
//
//	controller 端 Encode(token) → node 端 Decode(token) → 由解析结果生成 config（含
//	三地址 + enrollment_key + controller_ca_hash）→ 用该 config 的 ca_hash + enroll_addr
//	走两阶段信任完成一次真实 Enroll，证明 token 自洽（k 授权 + c 认证）。
func TestTrustE2E_TokenRoundTrip(t *testing.T) {
	tc := newTrustController(t)
	defer tc.cleanup()

	enrollKey := "trust-token-key-001"
	exp := time.Now().Add(10 * time.Minute)
	if err := tc.st.CreateEnrollKey(&store.EnrollKey{
		Key: enrollKey, Reusable: false, ExpiresAt: &exp, Tag: "user-carol",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// ── controller 端：拼 token（h=host、k=enrollment_key、c=ca_hash）→ Encode ──
	host := serverNameOf(tc.enrollAddr) // 127.0.0.1
	token, err := jointoken.Encode(jointoken.JoinToken{
		V: 1,
		H: host,
		K: enrollKey,
		C: tc.caHash,
	})
	if err != nil {
		t.Fatalf("jointoken.Encode: %v", err)
	}
	if token == "" {
		t.Fatal("token 不应为空")
	}

	// ── node 端：Decode token → 校验字段 ──
	jt, err := jointoken.Decode(token)
	if err != nil {
		t.Fatalf("jointoken.Decode: %v", err)
	}
	if jt.H != host || jt.K != enrollKey || jt.C != tc.caHash {
		t.Fatalf("Decode 结果与原值不符: %+v", jt)
	}

	// ── 由 token 解析结果生成 node 配置：用真实 enroll 端口（测试端口随机，
	//    故直接用监听地址而非 net.JoinHostPort(host, 7443)）──
	cfg := &config.Config{
		ControllerEnrollAddr: tc.enrollAddr, // 真实测试地址（生产由 net.JoinHostPort(jt.H, "7443") 拼）
		ControllerMTLSAddr:   tc.enrollAddr, // 测试用同一地址
		ControllerHTTPAddr:   tc.enrollAddr, // 测试用同一地址
		EnrollmentKey:        jt.K,
		ControllerCAHash:     jt.C, // A4：config 新增 controller_ca_hash 字段（Section 2/4 产）
		Role:                 config.RoleNode,
		Hostname:             "token-node",
		DataDir:              t.TempDir(),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.Validate: %v", err)
	}

	// ── 用 config 的 ca_hash + enroll_addr 走两阶段信任完成 Enroll（token 自洽验证）──
	resp, _ := enrollOverCAHash(t, cfg.ControllerEnrollAddr, cfg.ControllerCAHash, cfg.EnrollmentKey, cfg.Hostname)
	if resp.NodeId == "" || resp.VirtualIp == "" || len(resp.NodeCertDer) == 0 {
		t.Fatalf("token 往返后 Enroll 响应不完整: %+v", resp)
	}

	// 节点已建（User == key.Tag），证明 token 承载的 enrollment_key 真实生效。
	node, err := tc.st.GetNode(resp.NodeId)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.User != "user-carol" {
		t.Errorf("node.User = %q, want user-carol", node.User)
	}
}
