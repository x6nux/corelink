package ca

import (
	"crypto/x509"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/pki"
	"github.com/x6nux/corelink/pkg/tunnel"
)

func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func testEncKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// TestEnsureCAIdempotent: 两次 EnsureCA 返回同一 CA 证书（幂等）。
func TestEnsureCAIdempotent(t *testing.T) {
	s := newMemStore(t)
	encKey := testEncKey()

	mgr1, err := EnsureCA(s, "Test CA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA #1: %v", err)
	}
	cert1 := mgr1.Cert()

	mgr2, err := EnsureCA(s, "Test CA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA #2: %v", err)
	}
	cert2 := mgr2.Cert()

	if !cert1.Equal(cert2) {
		t.Fatal("EnsureCA 不幂等：两次获取的 CA 证书不同")
	}
}

// TestAESGCMRoundTrip: AES-GCM 加密→解密往返正确。
func TestAESGCMRoundTrip(t *testing.T) {
	key := testEncKey()
	plaintext := []byte("this is my secret private key PEM data")

	ciphertext, err := aesGCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("aesGCMEncrypt: %v", err)
	}
	if string(ciphertext) == string(plaintext) {
		t.Fatal("加密后数据不应与明文相同")
	}

	decrypted, err := aesGCMDecrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("aesGCMDecrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("解密结果 = %q, want %q", decrypted, plaintext)
	}
}

// TestAESGCMWrongKey: 错误密钥解密应报错。
func TestAESGCMWrongKey(t *testing.T) {
	key := testEncKey()
	plaintext := []byte("secret")
	ct, _ := aesGCMEncrypt(key, plaintext)

	wrongKey := make([]byte, 32) // 全零
	_, err := aesGCMDecrypt(wrongKey, ct)
	if err == nil {
		t.Fatal("错误密钥解密应报错，但未报错")
	}
}

// TestIssueAndVerify: 签发的证书可被 CA 验证。
func TestIssueAndVerify(t *testing.T) {
	s := newMemStore(t)
	mgr, err := EnsureCA(s, "Test CA", testEncKey())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	csrDER, _, err := pki.GenerateCSR("node-1")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	certDER, err := mgr.Issue(csrDER, "node-1", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(mgr.Cert())
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("证书验证失败: %v", err)
	}
}

// TestRevokeAndCRL: 吊销证书后 CRL 命中该序列号。
func TestRevokeAndCRL(t *testing.T) {
	s := newMemStore(t)
	mgr, err := EnsureCA(s, "Test CA", testEncKey())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	csrDER, _, err := pki.GenerateCSR("node-2")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	certDER, err := mgr.Issue(csrDER, "node-2", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cert, _ := x509.ParseCertificate(certDER)
	serial := cert.SerialNumber.Text(10)

	if err := mgr.Revoke(serial); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	crlDER, err := mgr.CurrentCRL(time.Hour)
	if err != nil {
		t.Fatalf("CurrentCRL: %v", err)
	}

	revoked, err := pki.IsRevoked(crlDER, cert.SerialNumber)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("吊销后 CRL 未命中")
	}
}

// TestCurrentCRLStringToBigInt: string→big.Int 转换正确，非法序列号跳过。
func TestCurrentCRLStringToBigInt(t *testing.T) {
	cases := []struct {
		input   string
		wantNil bool
	}{
		{"12345678901234567890", false},
		{"not-a-number", true},
		{"", true},
		{"0", false},
	}
	for _, c := range cases {
		n, ok := new(big.Int).SetString(c.input, 10)
		if c.wantNil {
			if ok {
				t.Errorf("input=%q: 期望解析失败，但成功得到 %v", c.input, n)
			}
		} else {
			if !ok {
				t.Errorf("input=%q: 期望解析成功，但失败", c.input)
			}
		}
	}
}

// TestIssueSavesSerial: Issue 后序列号记录到 store。
func TestIssueSavesSerial(t *testing.T) {
	s := newMemStore(t)
	mgr, _ := EnsureCA(s, "Test CA", testEncKey())
	csrDER, _, _ := pki.GenerateCSR("n3")
	certDER, err := mgr.Issue(csrDER, "n3", "node", 24*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cert, _ := x509.ParseCertificate(certDER)
	serial := cert.SerialNumber.Text(10)

	// 吊销并检查 store 有记录
	if err := mgr.Revoke(serial); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	serials, err := s.RevokedSerials()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range serials {
		if s == serial {
			found = true
		}
	}
	if !found {
		t.Fatalf("吊销序列号 %s 未在 store 中找到", serial)
	}
}

// TestIssueRecordsFingerprint: 签发后 store 中记录的指纹与 tunnel.CertFingerprint 一致。
func TestIssueRecordsFingerprint(t *testing.T) {
	s := newMemStore(t)
	mgr, err := EnsureCA(s, "Test CA", testEncKey())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	csrDER, _, err := pki.GenerateCSR("nodeA")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	certDER, err := mgr.Issue(csrDER, "nodeA", "node", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	want := tunnel.CertFingerprint(cert)
	got, ok, err := s.GetCertFingerprint("nodeA")
	if err != nil {
		t.Fatalf("GetCertFingerprint: %v", err)
	}
	if !ok || got != want {
		t.Fatalf("store 指纹 %q != tunnel.CertFingerprint %q (ok=%v)", got, want, ok)
	}
}

// TestEnsureCAWithWrongDecryptKey: 用错误密钥加载应报错。
func TestEnsureCAWithWrongDecryptKey(t *testing.T) {
	s := newMemStore(t)
	encKey := testEncKey()
	_, err := EnsureCA(s, "Test CA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA first: %v", err)
	}

	wrongKey := make([]byte, 32)
	_, err = EnsureCA(s, "Test CA", wrongKey)
	if err == nil {
		t.Fatal("错误解密密钥应报错")
	}
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("err = %v, want ErrDecryptFailed (wrapped)", err)
	}
}
