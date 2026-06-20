package server_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/server"
	"github.com/x6nux/corelink/internal/pki"
)

// peerCtxFromCert 构造带 mTLS peer 证书的 gRPC context。
func peerCtxFromCert(cert *x509.Certificate) context.Context {
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
	}}
	return peer.NewContext(context.Background(), p)
}

// issueClientCert 用 CA 签一张 client 证书并返回解析后的 *x509.Certificate。
func issueClientCert(t *testing.T, caM *ca.Manager, cn string) *x509.Certificate {
	t.Helper()
	csr, _, err := pki.GenerateCSR(cn)
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	der, err := caM.Issue(csr, cn, "node", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func pkiIsRevoked(der []byte, serial *big.Int) (bool, error) {
	return pki.IsRevoked(der, serial)
}

// TestCRLUnaryInterceptor 已吊销证书的请求被拒 Unauthenticated；未吊销放行。
func TestCRLUnaryInterceptor(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)

	// 签两张证书：good 保持有效，bad 吊销并进 CRL。
	goodCert := issueClientCert(t, caM, "good-node")
	badCert := issueClientCert(t, caM, "bad-node")
	if err := caM.Revoke(badCert.SerialNumber.Text(10)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	cache := server.NewCRLCache(caM.CurrentCRL, time.Second)
	interceptor := server.NewCRLUnaryInterceptor(cache)

	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	// good → 放行
	if _, err := interceptor(peerCtxFromCert(goodCert), nil, info, handler); err != nil {
		t.Errorf("good 证书应放行，得 %v", err)
	}
	// bad → Unauthenticated
	_, err := interceptor(peerCtxFromCert(badCert), nil, info, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("bad 证书应被拒 Unauthenticated，得 %v", status.Code(err))
	}
}

// TestCRLHTTPMiddleware 已吊销证书 HTTP 请求返回 403；未吊销透传 200。
func TestCRLHTTPMiddleware(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	goodCert := issueClientCert(t, caM, "good-http")
	badCert := issueClientCert(t, caM, "bad-http")
	if err := caM.Revoke(badCert.SerialNumber.Text(10)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	cache := server.NewCRLCache(caM.CurrentCRL, time.Second)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := server.NewCRLHTTPMiddleware(cache)(next)

	// good → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{goodCert}}
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("good 证书应 200，得 %d", rec.Code)
	}
	// bad → 403
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{badCert}}
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad 证书应 403，得 %d", rec.Code)
	}
}

// TestCRLSanity 序列号确实进了 CRL。
func TestCRLSanity(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	c := issueClientCert(t, caM, "s")
	_ = caM.Revoke(c.SerialNumber.Text(10))
	der, _ := caM.CurrentCRL(time.Hour)
	got, _ := pkiIsRevoked(der, c.SerialNumber)
	if !got {
		t.Fatal("吊销序列号应在 CRL 中")
	}
}
