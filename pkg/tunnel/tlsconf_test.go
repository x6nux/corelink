package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func TestClientTLSConfigPinnedSkipsDefaultVerifyButPins(t *testing.T) {
	ca := newTestCA(t, "corelink-ca")
	cfg, err := ClientTLSConfig(&TLSOptions{Mode: TLSModePinned, PinnedCAHash: CASPKIHash(ca.cert)})
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("CA 钉扎模式应跳过默认链校验（含 hostname），改用 caPinnedVerifier")
	}
	if cfg.VerifyPeerCertificate == nil {
		t.Fatal("CA 钉扎模式必须设置 VerifyPeerCertificate")
	}
}

func TestClientTLSConfigPinnedRejectsBadCAHash(t *testing.T) {
	// 非法 CA 哈希 fail-fast，不拖到握手期
	_, err := ClientTLSConfig(&TLSOptions{Mode: TLSModePinned, PinnedCAHash: "not-a-hash"})
	if err == nil {
		t.Fatal("非法 PinnedCAHash 应在构造时即返回 error")
	}
	_, err = ClientTLSConfig(&TLSOptions{Mode: TLSModePinned, PinnedCAHash: ""})
	if err == nil {
		t.Fatal("空 PinnedCAHash 应返回 error")
	}
}

var _ = tls.Config{}

// testCA 在测试内联生成一对 CA（自签）。
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, cn string) testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("建 CA: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert: cert, key: key}
}

// issueLeaf 用 CA 签发一张 server leaf，返回 [leafDER, caDER] 完整链。
func (ca testCA) issueLeaf(t *testing.T, cn string) [][]byte {
	t.Helper()
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &leafKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("签发 leaf: %v", err)
	}
	return [][]byte{der, ca.cert.Raw}
}

func TestCAPinnedVerifierAcceptsMatchingCA(t *testing.T) {
	ca := newTestCA(t, "corelink-ca")
	chain := ca.issueLeaf(t, "controller")
	verify := caPinnedVerifier(CASPKIHash(ca.cert))
	if err := verify(chain, nil); err != nil {
		t.Fatalf("CA 哈希匹配应通过: %v", err)
	}
}

func TestCAPinnedVerifierRejectsWrongCA(t *testing.T) {
	caGood := newTestCA(t, "good-ca")
	caEvil := newTestCA(t, "evil-ca")
	// 出示 evil CA 签的链，但 verifier 期望 good CA 哈希 → 拒绝
	chain := caEvil.issueLeaf(t, "controller")
	verify := caPinnedVerifier(CASPKIHash(caGood.cert))
	if err := verify(chain, nil); err == nil {
		t.Fatal("链中无期望 CA → 必须拒绝（防 MITM）")
	}
}

func TestCAPinnedVerifierAcceptsRotatedLeafSameCA(t *testing.T) {
	// 同 CA 签的不同 leaf（服务端证书轮换）仍通过——这是钉扎 CA 而非 leaf 的核心收益
	ca := newTestCA(t, "corelink-ca")
	verify := caPinnedVerifier(CASPKIHash(ca.cert))
	if err := verify(ca.issueLeaf(t, "controller-v1"), nil); err != nil {
		t.Fatalf("第一张 leaf 应通过: %v", err)
	}
	if err := verify(ca.issueLeaf(t, "controller-v2"), nil); err != nil {
		t.Fatalf("轮换后的 leaf（同 CA）应仍通过: %v", err)
	}
}

func TestCAPinnedVerifierRejectsInvalidChain(t *testing.T) {
	ca := newTestCA(t, "corelink-ca")
	// 一张与 CA 无关的自签 leaf 冒充，但链里塞入真 CA 的 DER（SPKI 命中）
	// → x509.Verify 验签必败（leaf 非该 CA 所签）
	bogusKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "bogus"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
	}
	bogusDER, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &bogusKey.PublicKey, bogusKey)
	chain := [][]byte{bogusDER, ca.cert.Raw}
	verify := caPinnedVerifier(CASPKIHash(ca.cert))
	if err := verify(chain, nil); err == nil {
		t.Fatal("leaf 非该 CA 所签 → 验签失败必须拒绝")
	}
}

func TestCAPinnedVerifierRejectsEmptyChain(t *testing.T) {
	verify := caPinnedVerifier("sha256:" + repeatHex64ForTLS())
	if err := verify(nil, nil); err == nil {
		t.Fatal("空链必须拒绝")
	}
}

// TestCAPinnedVerifier 验证导出包装 CAPinnedVerifier 与私有实现行为一致：
// CA 签的 leaf 链按匹配/不匹配 wantHash 分别通过/拒绝（供 cmd mTLS 层复用）。
func TestCAPinnedVerifier(t *testing.T) {
	ca := newTestCA(t, "VerifierTestCA")
	// CA 签一张 leaf，构造 rawCerts=[leaf, ca]。
	rawCerts := ca.issueLeaf(t, "leaf")

	wantHash := CASPKIHash(ca.cert)
	if err := CAPinnedVerifier(wantHash)(rawCerts, nil); err != nil {
		t.Errorf("匹配哈希应通过: %v", err)
	}
	if err := CAPinnedVerifier("sha256:deadbeef")(rawCerts, nil); err == nil {
		t.Error("不匹配哈希应拒绝")
	}
}

func repeatHex64ForTLS() string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

func TestServerTLSConfigACMEBuilds(t *testing.T) {
	// Mode=acme：仅验证装配，不真正签发证书
	cfg, err := ServerTLSConfig(&TLSOptions{
		Mode:         TLSModeACME,
		ACMEDomains:  []string{"example.com"},
		ACMECacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ServerTLSConfig(acme): %v", err)
	}
	if cfg == nil {
		t.Fatal("acme 模式应返回非空 config")
	}
	if cfg.GetCertificate == nil {
		t.Fatal("acme 模式必须设置 GetCertificate（由 autocert.Manager 提供）")
	}
}

func TestServerTLSConfigPinnedHasCertificate(t *testing.T) {
	cfg, err := ServerTLSConfig(&TLSOptions{
		Mode:       TLSModePinned,
		ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("ServerTLSConfig(pinned): %v", err)
	}
	if cfg == nil {
		t.Fatal("pinned 模式应返回非空 config")
	}
	if len(cfg.Certificates) == 0 {
		t.Fatal("pinned 模式必须包含自签证书")
	}
}
