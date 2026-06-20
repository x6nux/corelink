package enroll_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/enroll"
	"github.com/x6nux/corelink/internal/nodecore/keystore"
	"github.com/x6nux/corelink/internal/pki"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// ─────────────────────── mock 服务端 ───────────────────────

// mockEnrollServer 实现 genv1.EnrollServiceServer，返回测试 CA 签发的证书。
type mockEnrollServer struct {
	genv1.UnimplementedEnrollServiceServer
	ca        *pki.CA                     // 内层 CA，用于签发节点证书
	enrollKey string                      // 期望的 enrollment key
	mutate    func(*genv1.EnrollResponse) // 可选：返回前篡改响应（测试非法响应用）
}

func (m *mockEnrollServer) Enroll(_ context.Context, req *genv1.EnrollRequest) (*genv1.EnrollResponse, error) {
	if req.EnrollmentKey != m.enrollKey {
		return nil, fmt.Errorf("mock: invalid enrollment key: %q", req.EnrollmentKey)
	}

	nodeID := "test-node-id-mock"
	certDER, err := m.ca.IssueFromCSR(req.CsrDer, nodeID, pki.NodeRoleNode, 24*time.Hour)
	if err != nil {
		return nil, err
	}

	resp := &genv1.EnrollResponse{
		NodeId:      nodeID,
		VirtualIp:   "10.99.0.1/32",
		NodeCertDer: certDER,
		CaCertDer:   m.ca.Cert().Raw,
	}
	if m.mutate != nil {
		m.mutate(resp)
	}
	return resp, nil
}

// ─────────────────────── 测试 helpers ───────────────────────

// startMockServer 启动使用 CA 签发证书的 gRPC 服务，server 出示完整链 [leaf, CA]，
// 返回监听地址、controller CA 哈希（client 钉扎用）和停止函数。
func startMockServer(t *testing.T, ca *pki.CA, svc genv1.EnrollServiceServer) (addr string, caHash string, stop func()) {
	t.Helper()

	// CA 签发服务端证书（含 127.0.0.1 SAN），出示完整链 leaf+CA。
	srvCert, err := buildCASignedServerCert(t, ca, "127.0.0.1")
	if err != nil {
		t.Fatalf("buildCASignedServerCert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		MinVersion:   tls.VersionTLS12,
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	genv1.RegisterEnrollServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), tunnel.CASPKIHash(ca.Cert()), func() { srv.GracefulStop() }
}

// buildCASignedServerCert 用 CA 签发一张 serverAuth 叶证书，返回含完整链 [leaf, CA] 的 tls.Certificate。
func buildCASignedServerCert(t *testing.T, ca *pki.CA, host string) (tls.Certificate, error) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	// 用 CA 的 IssueFromCSR 走标准签发：先造 CSR。
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: host}}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	// server 证书：WithServerAuth 附加 ServerAuth 用途并复制 CSR 中的 SAN。
	leafDER, err := ca.IssueFromCSR(csrDER, host, pki.NodeRoleNode, 24*time.Hour, pki.WithServerAuth())
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	return tls.Certificate{
		Certificate: [][]byte{leafDER, ca.Cert().Raw}, // 完整链
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// makeTestCA 生成测试用 CA。
func makeTestCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA("TestCA")
	if err != nil {
		t.Fatalf("pki.NewCA: %v", err)
	}
	return ca
}

// buildSelfSigned 生成自签证书（测试预存身份用）。
func buildSelfSigned(t *testing.T) (key *ecdsa.PrivateKey, certDER []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pre-saved-id"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return k, der
}

// preSaveIdentity 预存一个身份到 keystore（测试幂等用）。
func preSaveIdentity(t *testing.T, ks *keystore.KeyStore) {
	t.Helper()
	key, certDER := buildSelfSigned(t)
	if err := ks.SaveIdentity(certDER, key, certDER, "10.0.0.99/32", "pre-saved-id"); err != nil {
		t.Fatalf("preSaveIdentity: %v", err)
	}
}

// ─────────────────────── 测试 ───────────────────────

// TestEnroll_Success 成功注册并持久化身份。
func TestEnroll_Success(t *testing.T) {
	ca := makeTestCA(t)
	svc := &mockEnrollServer{ca: ca, enrollKey: "enroll-key-ok"}
	addr, caHash, stop := startMockServer(t, ca, svc)
	defer stop()

	dir := t.TempDir()
	ks := keystore.New(dir)
	cfg := &config.Config{
		ControllerEnrollAddr: addr,
		EnrollmentKey:        "enroll-key-ok",
		ControllerCAHash:     caHash,
		DataDir:              dir,
		Role:                 config.RoleNode,
		Hostname:             "testhost",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := enroll.Enroll(ctx, cfg, ks); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if !ks.HasIdentity() {
		t.Fatal("注册后 HasIdentity 应为 true")
	}

	id, err := ks.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id.NodeID != "test-node-id-mock" {
		t.Errorf("NodeID 错误: got %q", id.NodeID)
	}
	if id.VirtualIP != "10.99.0.1/32" {
		t.Errorf("VirtualIP 错误: got %q", id.VirtualIP)
	}

	// 节点证书应能被测试 CA 验证
	block, _ := pem.Decode(id.NodeCertPEM)
	if block == nil {
		t.Fatal("NodeCertPEM 无法解码")
	}
	nodeCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("节点证书验证失败: %v", err)
	}

	// WG 密钥也应存在（EnsureWGKey 在 Enroll 内调用）
	pub, err := ks.WGPublicKey()
	if err != nil || pub == "" {
		t.Errorf("注册后应有 WG 公钥: err=%v pub=%q", err, pub)
	}
}

// TestEnroll_SkipIfHasIdentity 已有身份时跳过注册（幂等）。
func TestEnroll_SkipIfHasIdentity(t *testing.T) {
	dir := t.TempDir()
	ks := keystore.New(dir)
	preSaveIdentity(t, ks)

	cfg := &config.Config{
		ControllerEnrollAddr: "127.0.0.1:19999", // 不会真正连接
		EnrollmentKey:        "any-key",
		ControllerCAHash:     "sha256:aabb", // 无效哈希，若真连接则 TLS 构造失败
		DataDir:              dir,
		Role:                 config.RoleNode,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := enroll.Enroll(ctx, cfg, ks); err != nil {
		t.Fatalf("已有身份时 Enroll 应返回 nil，got: %v", err)
	}

	// 确认身份未被覆盖
	id, _ := ks.LoadIdentity()
	if id.NodeID != "pre-saved-id" {
		t.Errorf("身份不应被覆盖: NodeID=%q", id.NodeID)
	}
}

// TestEnroll_RejectInvalidResponse：响应字段缺失/不可解析时 Enroll 报错，不落地身份。
func TestEnroll_RejectInvalidResponse(t *testing.T) {
	ca := makeTestCA(t)
	cases := []struct {
		name string
		mut  func(*genv1.EnrollResponse)
	}{
		{"NodeCertDer 空", func(r *genv1.EnrollResponse) { r.NodeCertDer = nil }},
		{"CaCertDer 空", func(r *genv1.EnrollResponse) { r.CaCertDer = nil }},
		{"NodeCertDer 不可解析", func(r *genv1.EnrollResponse) { r.NodeCertDer = []byte("not-a-cert") }},
		{"VirtualIp 空", func(r *genv1.EnrollResponse) { r.VirtualIp = "" }},
		{"NodeId 空", func(r *genv1.EnrollResponse) { r.NodeId = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockEnrollServer{ca: ca, enrollKey: "k", mutate: tc.mut}
			addr, caHash, stop := startMockServer(t, ca, svc)
			defer stop()
			dir := t.TempDir()
			ks := keystore.New(dir)
			cfg := &config.Config{
				ControllerEnrollAddr: addr,
				EnrollmentKey:        "k",
				ControllerCAHash:     caHash,
				DataDir:              dir,
				Role:                 config.RoleNode,
				Hostname:             "h",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := enroll.Enroll(ctx, cfg, ks); err == nil {
				t.Fatal("非法响应应使 Enroll 报错")
			}
			if ks.HasIdentity() {
				t.Error("非法响应不应落地身份")
			}
		})
	}
}
