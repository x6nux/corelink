package server_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/enroll"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/server"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/pki"
)

// ---------- 测试辅助 ----------

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func mustCA(t *testing.T, st *store.Store) *ca.Manager {
	t.Helper()
	encKey := make([]byte, 32)
	m, err := ca.EnsureCA(st, "TestCA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	return m
}

func mustIPAM(t *testing.T, st *store.Store) *ipam.Allocator {
	t.Helper()
	a, err := ipam.New("10.100.0.0/24", st)
	if err != nil {
		t.Fatalf("ipam.New: %v", err)
	}
	return a
}

// ---------- mTLS 服务器测试 ----------

// TestMTLSServer_ValidClientCert 合法节点证书 mTLS 调通。
func TestMTLSServer_ValidClientCert(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	// 用 CA 给自己签 server 证书（BuildServerCert）
	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}

	// 构建 CA 池
	caPool := server.BuildCAPool(caM.Cert())

	// 创建 mTLS server
	grpcSrv := server.NewMTLSServer(caPool, serverCert, nil,
		func(s *grpc.Server) {
			ipamA := mustIPAM(t, st)
			enrollSvc := enroll.NewService(st, caM, ipamA)
			genv1.RegisterEnrollServiceServer(s, enrollSvc)
		},
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// 客户端：用 CA 签发的节点证书做 mTLS
	clientCSR, clientKey, err := pki.GenerateCSR("client-node")
	if err != nil {
		t.Fatalf("GenerateCSR for client: %v", err)
	}
	clientCertDER, err := caM.Issue(clientCSR, "client-node", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue client cert: %v", err)
	}
	clientTLSCert, err := server.AssembleTLSCert(caM, clientCertDER, clientKey)
	if err != nil {
		t.Fatalf("AssembleTLSCert: %v", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		RootCAs:      caPool,
		ServerName:   "controller-server",
		MinVersion:   tls.VersionTLS12,
	}
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	// 任意 RPC 调用成功（会返回 NotFound 因为没有 key，但握手通过）
	client := genv1.NewEnrollServiceClient(conn)
	_, err = client.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "no-such-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		CsrDer:        clientCSR,
	})
	// 期望 NotFound（握手已通过，服务层正常返回）
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound (TLS handshake OK), got %v: %v", status.Code(err), err)
	}
}

// TestMTLSServer_NoClientCert 无客户端证书被拒绝。
func TestMTLSServer_NoClientCert(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	caPool := server.BuildCAPool(caM.Cert())

	grpcSrv := server.NewMTLSServer(caPool, serverCert, nil,
		func(s *grpc.Server) {
			ipamA := mustIPAM(t, st)
			enrollSvc := enroll.NewService(st, caM, ipamA)
			genv1.RegisterEnrollServiceServer(s, enrollSvc)
		},
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// 无客户端证书
	tlsCfg := &tls.Config{
		RootCAs:    caPool,
		ServerName: "controller-server",
		MinVersion: tls.VersionTLS12,
	}
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	client := genv1.NewEnrollServiceClient(conn)
	_, err = client.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "k",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		CsrDer:        []byte("x"),
	})
	// 应该是 TLS 握手失败（Unavailable 或类似）
	if err == nil {
		t.Fatal("expected error for no client cert, got nil")
	}
	code := status.Code(err)
	if code == codes.OK {
		t.Errorf("expected non-OK, got OK")
	}
	// TLS 握手被拒一般表现为 Unavailable 或 Unknown
	t.Logf("got expected error (code=%v): %v", code, err)
}

// TestMTLSServer_WrongCAClientCert 非此 CA 签发的客户端证书被拒绝。
func TestMTLSServer_WrongCAClientCert(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	caPool := server.BuildCAPool(caM.Cert())

	grpcSrv := server.NewMTLSServer(caPool, serverCert, nil,
		func(s *grpc.Server) {
			ipamA := mustIPAM(t, st)
			enrollSvc := enroll.NewService(st, caM, ipamA)
			genv1.RegisterEnrollServiceServer(s, enrollSvc)
		},
	)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// 另一个 CA 签发客户端证书
	st2 := mustStore(t)
	caM2 := mustCA(t, st2)
	clientCSR, clientKey, err := pki.GenerateCSR("rogue-client")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	clientCertDER, err := caM2.Issue(clientCSR, "rogue-client", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clientTLSCert, err := server.AssembleTLSCert(caM2, clientCertDER, clientKey)
	if err != nil {
		t.Fatalf("AssembleTLSCert: %v", err)
	}

	// 客户端信任自己的 CA（它的 CA 没在服务端的 pool 里，服务端拒绝）
	otherPool := server.BuildCAPool(caM2.Cert())
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		// 让客户端连接时跳过 server cert 验证（只测服务端对客户端 cert 的拒绝）
		RootCAs:            otherPool,
		ServerName:         "controller-server",
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // 仅测试
	}
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	client := genv1.NewEnrollServiceClient(conn)
	_, err = client.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "k",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		CsrDer:        []byte("x"),
	})
	if err == nil {
		t.Fatal("expected error for wrong CA client cert, got nil")
	}
	t.Logf("got expected error (code=%v): %v", status.Code(err), err)
}

// TestEnrollServer_CASigned enroll server 出示 CA 签发的完整链，
// client 用 CA pool 验链通过（无客户端证书），握手成功后无效 key 返回 NotFound。
func TestEnrollServer_CASigned(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	enrollSvc := enroll.NewService(st, caM, ipamA)

	grpcSrv, leafCert, err := server.NewEnrollServer(caM, enrollSvc)
	if err != nil {
		t.Fatalf("NewEnrollServer: %v", err)
	}
	// 返回的 leaf 应由 CA 签发（能用 CA pool 验链）
	caPool := server.BuildCAPool(caM.Cert())
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:     caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("enroll leaf 应由 CA 签发: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// client 用 CA pool 验链；SNI 用 leaf CN（controller-server）
	tlsCfg := &tls.Config{
		RootCAs:    caPool,
		ServerName: "controller-server",
		MinVersion: tls.VersionTLS12,
	}
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	client := genv1.NewEnrollServiceClient(conn)
	_, err = client.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "no-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		CsrDer:        []byte("x"),
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound (handshake OK), got %v: %v", status.Code(err), err)
	}
}

// TestAssembleTLSCert_FullChain 断言 BuildServerCert 出示的 tls.Certificate
// 包含完整链 [leafDER, caDER]，第二张恰为 CA 证书，使 CA-pinning verifier
// 能从握手链中按 SPKI 定位 CA。
func TestAssembleTLSCert_FullChain(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	cert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	if len(cert.Certificate) != 2 {
		t.Fatalf("应出示 2 张证书（leaf+CA），实际 %d 张", len(cert.Certificate))
	}
	// 第一张：leaf，Leaf 字段一致
	if cert.Leaf == nil {
		t.Fatal("Leaf 字段不应为 nil")
	}
	// 第二张：必须等于 CA 证书 DER
	if string(cert.Certificate[1]) != string(caM.Cert().Raw) {
		t.Errorf("第二张证书应为 CA 证书 DER")
	}
	// leaf 能用 CA 验链
	pool := x509.NewCertPool()
	pool.AddCert(caM.Cert())
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("leaf 验链失败: %v", err)
	}
}

// TestMTLSServer_RevokedCertRejected 装了 CRL 拦截器的 mTLS server
// 对已吊销客户端证书的 RPC 返回 Unauthenticated。
func TestMTLSServer_RevokedCertRejected(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	serverCert, err := server.BuildServerCert(caM, "")
	if err != nil {
		t.Fatalf("BuildServerCert: %v", err)
	}
	caPool := server.BuildCAPool(caM.Cert())
	cache := server.NewCRLCache(caM.CurrentCRL, time.Second)

	grpcSrv := server.NewMTLSServer(caPool, serverCert, cache,
		func(s *grpc.Server) {
			ipamA := mustIPAM(t, st)
			genv1.RegisterEnrollServiceServer(s, enroll.NewService(st, caM, ipamA))
		},
	)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// 客户端用 CA 签的证书，然后吊销它
	clientCSR, clientKey, err := pki.GenerateCSR("revoked-client")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	clientCertDER, err := caM.Issue(clientCSR, "revoked-client", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clientCert, _ := x509.ParseCertificate(clientCertDER)
	if err := caM.Revoke(clientCert.SerialNumber.Text(10)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	clientTLSCert, err := server.AssembleTLSCert(caM, clientCertDER, clientKey)
	if err != nil {
		t.Fatalf("AssembleTLSCert: %v", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		RootCAs:      caPool,
		ServerName:   "controller-server",
		MinVersion:   tls.VersionTLS12,
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	client := genv1.NewEnrollServiceClient(conn)
	_, err = client.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "k",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		CsrDer:        clientCSR,
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("吊销证书应被拒 Unauthenticated，得 %v: %v", status.Code(err), err)
	}
}
