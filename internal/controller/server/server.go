// Package server 组装 CoreLink controller 的两套独立 gRPC server（§5.1/§8）：
//
//   - EnrollServer：外层 TLS（ClientAuth=NoClientCert），仅供注册使用。
//   - MTLSServer：mTLS（ClientAuth=RequireAndVerifyClientCert），挂鉴权服务。
package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/pki"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// BuildCAPool 从 CA 证书构建 x509.CertPool（用于 mTLS ClientCAs 或客户端 RootCAs）。
func BuildCAPool(caCert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool
}

// BuildServerCert 加载或签发 controller 自身 server 证书，返回 tls.Certificate。
// cachePath 非空时优先从该文件加载（cert+key PEM），证书剩余有效期 >30 天则复用；
// 否则签发新证书并写入 cachePath，避免每次重启都签发新证书。
func BuildServerCert(caM *ca.Manager, cachePath string) (tls.Certificate, error) {
	// 尝试从缓存文件加载
	if cachePath != "" {
		if cert, err := loadServerCertCache(cachePath); err == nil {
			// 检查剩余有效期 > 30 天
			if cert.Leaf != nil && time.Until(cert.Leaf.NotAfter) > 30*24*time.Hour {
				// 追加 CA cert 到链末尾：对端 CA-pinning verifier 需要从链中按 SPKI 定位 CA。
				cert.Certificate = append(cert.Certificate, caM.Cert().Raw)
				slog.Info("server: 复用缓存 server 证书", "not_after", cert.Leaf.NotAfter.Format("2006-01-02"))
				return cert, nil
			}
			slog.Info("server: 缓存证书即将过期，重新签发", "cache", cachePath)
		}
	}

	// 签发新证书
	csrDER, key, err := pki.GenerateCSR("controller-server", "controller-server")
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: 生成 server CSR 失败: %w", err)
	}
	certDER, err := caM.IssueServer(csrDER, "controller-server", "agent", 365*24*time.Hour)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: 签发 server 证书失败: %w", err)
	}
	tlsCert, err := AssembleTLSCert(caM, certDER, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	// 写入缓存
	if cachePath != "" {
		if err := saveServerCertCache(cachePath, certDER, key); err != nil {
			slog.Warn("server: 写入证书缓存失败（不影响运行）", "err", err)
		} else {
			slog.Info("server: server 证书已缓存", "path", cachePath)
		}
	}
	return tlsCert, nil
}

// loadServerCertCache 从 PEM 文件加载 cert+key。
func loadServerCertCache(path string) (tls.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, err
	}
	rest := data
	var certPEM, keyPEM []byte
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = r
		if block.Type == "CERTIFICATE" {
			certPEM = pem.EncodeToMemory(block)
		} else if strings.Contains(block.Type, "PRIVATE KEY") {
			keyPEM = pem.EncodeToMemory(block)
		}
	}
	if certPEM == nil || keyPEM == nil {
		return tls.Certificate{}, fmt.Errorf("缓存文件缺少 cert 或 key")
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// saveServerCertCache 把 cert+key 写入 PEM 文件。
func saveServerCertCache(path string, certDER []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化私钥失败: %w", err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}
	if err := pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0600)
}

// AssembleTLSCert 将 DER 证书与 ECDSA 私钥组装成 tls.Certificate，
// 出示完整链 [leafDER, caDER]，使对端 CA-pinning verifier 能从握手链按 SPKI 定位 CA。
func AssembleTLSCert(caM *ca.Manager, certDER []byte, key *ecdsa.PrivateKey) (tls.Certificate, error) {
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server: 解析证书失败: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{certDER, caM.Cert().Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// RegisterFunc 是注册 gRPC 服务的回调函数类型。
type RegisterFunc func(s *grpc.Server)

// NewUnifiedServer 构造统一 gRPC server（证书可选：VerifyClientCertIfGiven）。
// 返回 gRPC server（不含 TLS transport，由外层 http.Server 处理 TLS）和 TLS 配置。
// skipCertMethods 中列出的 gRPC 方法不要求客户端证书（如 Enroll）。
func NewUnifiedServer(serverCert tls.Certificate, caPool *x509.CertPool, crl crlGetter, registerFns ...RegisterFunc) (*grpc.Server, *tls.Config) {
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}

	// gRPC 拦截器：CRL 检查（无证书跳过）+ 非 Enroll 方法要求证书
	var opts []grpc.ServerOption
	skipCert := map[string]bool{
		"/corelink.v1.EnrollService/Enroll": true,
	}
	certInterceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		certs := peerCertsFromCtx(ctx)
		// CRL 检查（有证书才查）
		if crl != nil && len(certs) > 0 {
			revoked, _, err := peerCertRevoked(crl, certs)
			if err != nil {
				return nil, status.Error(codes.Internal, "CRL 校验失败")
			}
			if revoked {
				return nil, status.Error(codes.Unauthenticated, "证书已吊销")
			}
		}
		// 非豁免方法要求证书
		if !skipCert[info.FullMethod] && len(certs) == 0 {
			return nil, status.Error(codes.Unauthenticated, "需要客户端证书")
		}
		return handler(ctx, req)
	}
	certStreamInterceptor := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		certs := peerCertsFromCtx(ss.Context())
		if crl != nil && len(certs) > 0 {
			revoked, _, err := peerCertRevoked(crl, certs)
			if err != nil {
				return status.Error(codes.Internal, "CRL 校验失败")
			}
			if revoked {
				return status.Error(codes.Unauthenticated, "证书已吊销")
			}
		}
		if !skipCert[info.FullMethod] && len(certs) == 0 {
			return status.Error(codes.Unauthenticated, "需要客户端证书")
		}
		return handler(srv, ss)
	}
	opts = append(opts,
		grpc.ChainUnaryInterceptor(certInterceptor),
		grpc.ChainStreamInterceptor(certStreamInterceptor),
	)

	srv := grpc.NewServer(opts...)
	for _, fn := range registerFns {
		fn(srv)
	}
	return srv, tlsCfg
}

// RequireCertHTTPMiddleware HTTP 中间件：要求客户端证书 + CRL 检查。
// 豁免 skipPrefixes 中的路径前缀（如 /admin/）不要求证书。
func RequireCertHTTPMiddleware(crl crlGetter, skipPrefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 豁免路径不要求证书（admin 面板等）
			for _, prefix := range skipPrefixes {
				if len(r.URL.Path) >= len(prefix) && r.URL.Path[:len(prefix)] == prefix {
					next.ServeHTTP(w, r)
					return
				}
			}
			var certs []*x509.Certificate
			if r.TLS != nil {
				certs = r.TLS.PeerCertificates
			}
			if len(certs) == 0 {
				http.Error(w, "client certificate required", http.StatusUnauthorized)
				return
			}
			if crl != nil {
				revoked, _, err := peerCertRevoked(crl, certs)
				if err != nil {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				if revoked {
					http.Error(w, "Forbidden: certificate revoked", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// NewMTLSServer 构造 mTLS gRPC server。
// Deprecated: 使用 NewUnifiedServer 替代。
// caPool 用于验证客户端证书（ClientCAs），serverCert 是 server 证书。
// crl 非 nil 时挂 CRL Unary/Stream 拦截器（吊销证书的 RPC 被拒 Unauthenticated）；
// nil 时不做 CRL 校验。registerFns 依次注册所需的 gRPC 服务。
func NewMTLSServer(caPool *x509.CertPool, serverCert tls.Certificate, crl crlGetter, registerFns ...RegisterFunc) *grpc.Server {
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS12,
	}
	opts := []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
	if crl != nil {
		opts = append(opts,
			grpc.ChainUnaryInterceptor(NewCRLUnaryInterceptor(crl)),
			grpc.ChainStreamInterceptor(NewCRLStreamInterceptor(crl)),
		)
	}
	srv := grpc.NewServer(opts...)
	for _, fn := range registerFns {
		fn(srv)
	}
	return srv
}

// NewEnrollServer 构造注册专用 gRPC server（外层 TLS，无客户端证书要求）。
// 出示 CA 签发的 server 证书完整链 [leaf, CA]，node 仅凭 token 里的 ca_hash 即可
// 从握手链按 SPKI 定位 CA 验链（两阶段信任流阶段①）。
// 返回 grpc.Server、leaf 证书（供测试验链参考），以及可能的错误。
func NewEnrollServer(caM *ca.Manager, svc genv1.EnrollServiceServer, extraFns ...RegisterFunc) (*grpc.Server, *x509.Certificate, error) {
	return newEnrollServerImpl(caM, svc, extraFns...)
}

// newEnrollServerImpl 实际构造逻辑（接受 EnrollServiceServer 接口）。
func newEnrollServerImpl(caM *ca.Manager, svc genv1.EnrollServiceServer, extraFns ...RegisterFunc) (*grpc.Server, *x509.Certificate, error) {
	// 用 CA 签发 server 证书并出示完整链（替代旧的独立自签证书）。
	tlsCert, err := BuildServerCert(caM, "")
	if err != nil {
		return nil, nil, fmt.Errorf("server: 构建 enroll server 证书失败: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.NoClientCert, // 注册阶段无需客户端证书
		MinVersion:   tls.VersionTLS12,
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	genv1.RegisterEnrollServiceServer(srv, svc)
	for _, fn := range extraFns {
		fn(srv)
	}
	return srv, tlsCert.Leaf, nil
}
