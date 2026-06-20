package server

import (
	"context"
	"crypto/x509"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/pki"
)

// crlGetter 提供当前 CRL DER（由 CRLCache 实现）。
type crlGetter interface {
	Get() ([]byte, error)
}

// peerCertRevoked 从 TLS 链取 leaf 序列号并查 CRL。无证书视为不可判定（按拒绝处理由调用方决定）。
// 返回 (revoked, hasCert, err)。
func peerCertRevoked(crl crlGetter, certs []*x509.Certificate) (revoked, hasCert bool, err error) {
	if len(certs) == 0 {
		return false, false, nil
	}
	der, err := crl.Get()
	if err != nil {
		return false, true, err
	}
	// CRL 为 nil/空 → 无吊销列表，视为未吊销。
	if len(der) == 0 {
		return false, true, nil
	}
	rev, err := pki.IsRevoked(der, certs[0].SerialNumber)
	if err != nil {
		return false, true, err
	}
	return rev, true, nil
}

// peerCertsFromCtx 从 gRPC context 取 mTLS peer 证书链。
func peerCertsFromCtx(ctx context.Context) []*x509.Certificate {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil
	}
	return tlsInfo.State.PeerCertificates
}

// NewCRLUnaryInterceptor 返回 Unary 拦截器：peer 证书命中 CRL → Unauthenticated。
func NewCRLUnaryInterceptor(crl crlGetter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		revoked, _, err := peerCertRevoked(crl, peerCertsFromCtx(ctx))
		if err != nil {
			return nil, status.Error(codes.Internal, "CRL 校验失败")
		}
		if revoked {
			return nil, status.Error(codes.Unauthenticated, "证书已吊销")
		}
		return handler(ctx, req)
	}
}

// NewCRLStreamInterceptor 返回 Stream 拦截器：peer 证书命中 CRL → Unauthenticated。
func NewCRLStreamInterceptor(crl crlGetter) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		revoked, _, err := peerCertRevoked(crl, peerCertsFromCtx(ss.Context()))
		if err != nil {
			return status.Error(codes.Internal, "CRL 校验失败")
		}
		if revoked {
			return status.Error(codes.Unauthenticated, "证书已吊销")
		}
		return handler(srv, ss)
	}
}

// NewCRLHTTPMiddleware 返回 HTTP 中间件：r.TLS peer 证书命中 CRL → 403。
func NewCRLHTTPMiddleware(crl crlGetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var certs []*x509.Certificate
			if r.TLS != nil {
				certs = r.TLS.PeerCertificates
			}
			revoked, _, err := peerCertRevoked(crl, certs)
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			if revoked {
				http.Error(w, "Forbidden: certificate revoked", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
