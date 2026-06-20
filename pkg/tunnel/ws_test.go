package tunnel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ---- 测试辅助：轻量 PKI ----

// tunnelTestCA 是 tunnel 包内测试用的轻量 CA，不依赖 internal/pki。
type tunnelTestCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	tlsCert tls.Certificate
	pool    *x509.CertPool
}

func newTunnelTestCA(t *testing.T) *tunnelTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tunnelTestCA{
		cert:    cert,
		key:     key,
		tlsCert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert},
		pool:    pool,
	}
}

// issueServer 签发 server 证书（ExtKeyUsageServerAuth）。
func (ca *tunnelTestCA) issueServer(t *testing.T, host string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

// issueClient 签发客户端证书（ExtKeyUsageClientAuth）。
func (ca *tunnelTestCA) issueClient(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// ---- 测试 ----

// TestWSDialer_mTLS 验证 wsDialer 在 WSS 模式下支持 mTLS（CA 哈希钉扎）。
// 依赖 A6：server 出示 CA 签链，由后续 section 实现。
func TestWSDialer_mTLS(t *testing.T) {
	t.Skip("依赖 A6：server 出示 CA 签链，见后续 section")
	ca := newTunnelTestCA(t)
	srvTLS, _ := ca.issueServer(t, "127.0.0.1")
	clientTLS := ca.issueClient(t, "ws-test-node")

	// ---- 服务端 ----
	srvTLSConf := &tls.Config{
		Certificates: []tls.Certificate{srvTLS},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.pool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	tlsLn := tls.NewListener(rawLn, srvTLSConf)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
		defer nc.Close()
		io.Copy(nc, nc) // echo
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(tlsLn) //nolint:errcheck
	defer srv.Close()

	// ---- 客户端（CA 哈希钉扎）----
	caHash := CASPKIHash(ca.cert)

	d, err := newWSDialer(&Config{
		Protocol: WSS,
		WSPath:   "/ws",
		TLS: &TLSOptions{
			Mode:         TLSModePinned,
			ServerName:   "127.0.0.1",
			PinnedCAHash: caHash,
		},
	})
	if err != nil {
		t.Fatalf("newWSDialer: %v", err)
	}

	// 注入客户端证书（上层负责将签发的节点证书注入 TLS config）。
	wd := d.(*wsDialer)
	wd.tlsConf.Certificates = []tls.Certificate{clientTLS}

	// 拨号 + 回声验证。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx, rawLn.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-mtls-ws")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// TestWSDialer_ALPN_http11 验证 wsDialer 构造的 tlsConf.NextProtos 包含 "http/1.1"。
// 依赖 A6：CA 签链钉扎需 server 出示完整链，由后续 section 实现。
func TestWSDialer_ALPN_http11(t *testing.T) {
	t.Skip("依赖 A6：CA 钉扎需 server 出示完整链，见后续 section")
	ca := newTunnelTestCA(t)
	caHash := CASPKIHash(ca.cert)

	d, err := newWSDialer(&Config{
		Protocol: WSS,
		WSPath:   "/",
		TLS: &TLSOptions{
			Mode:         TLSModePinned,
			ServerName:   "127.0.0.1",
			PinnedCAHash: caHash,
		},
	})
	if err != nil {
		t.Fatalf("newWSDialer: %v", err)
	}

	wd := d.(*wsDialer)
	if wd.tlsConf == nil {
		t.Fatal("WSS dialer 应有 tlsConf")
	}

	if !slices.Contains(wd.tlsConf.NextProtos, "http/1.1") {
		t.Fatalf("NextProtos = %v, 缺少 \"http/1.1\"", wd.tlsConf.NextProtos)
	}
}
