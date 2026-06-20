package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

// throwawayCert 在测试内联生成一张自签证书，避免依赖 tlsconf.go（Task 3.3）。
func throwawayCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("生成测试证书: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert
}

func TestCertFingerprintMatchesManualSHA256(t *testing.T) {
	cert := throwawayCert(t)

	got := CertFingerprint(cert)

	sum := sha256.Sum256(cert.Raw)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("指纹 = %s, want %s", got, want)
	}
}

func TestCASPKIHashMatchesManualSPKISHA256(t *testing.T) {
	cert := throwawayCert(t)

	got := CASPKIHash(cert)

	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("SPKI 哈希 = %s, want %s", got, want)
	}
}

func TestCASPKIHashStableAcrossReParse(t *testing.T) {
	// 同一证书重新解析（模拟续期前后重编码）SPKI 不变 → 哈希稳定
	cert := throwawayCert(t)
	reparsed, err := x509.ParseCertificate(cert.Raw)
	if err != nil {
		t.Fatalf("重新解析: %v", err)
	}
	if CASPKIHash(cert) != CASPKIHash(reparsed) {
		t.Fatal("同一公钥的 SPKI 哈希必须稳定")
	}
}

func TestParseCAHashAcceptsValid(t *testing.T) {
	cert := throwawayCert(t)
	h := CASPKIHash(cert)
	got, err := ParseCAHash(h)
	if err != nil {
		t.Fatalf("ParseCAHash: %v", err)
	}
	if got != h {
		t.Fatalf("归一化 = %s, want %s", got, h)
	}
}

func TestParseCAHashRejectsInvalid(t *testing.T) {
	bad := []string{
		"",                                  // 空
		"abcd",                              // 缺前缀
		"sha1:" + repeatHex64(),             // 前缀错误
		"sha256:abcd",                       // 长度不足
		"sha256:" + repeatHex64() + "ab",    // 长度超出
		"sha256:" + "g" + repeatHex64()[1:], // 非 hex
	}
	for _, in := range bad {
		if _, err := ParseCAHash(in); err == nil {
			t.Fatalf("非法 CA 哈希 %q 应返回 error", in)
		}
	}
}

// repeatHex64 返回 64 个 'a'（合法 SHA-256 hex 长度）。
func repeatHex64() string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}
