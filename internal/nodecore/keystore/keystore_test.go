package keystore_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/keystore"
)

// newTestKS 在临时目录中创建 KeyStore。
func newTestKS(t *testing.T) *keystore.KeyStore {
	t.Helper()
	return keystore.New(t.TempDir())
}

// buildSelfSignedCert 生成一张自签 ECDSA 证书 DER，供测试用。
func buildSelfSignedCert(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-node"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// ──────────────────── WG 密钥 ────────────────────

func TestEnsureWGKey_CreatesFiles(t *testing.T) {
	ks := newTestKS(t)
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatalf("EnsureWGKey: %v", err)
	}
	pub, err := ks.WGPublicKey()
	if err != nil {
		t.Fatalf("WGPublicKey: %v", err)
	}
	if pub == "" {
		t.Fatal("公钥不应为空")
	}
	// 公钥应为合法 base64，且解码后为 32 字节
	decoded, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("公钥不是合法 base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("公钥长度应为 32，得到 %d", len(decoded))
	}
}

func TestEnsureWGKey_Idempotent(t *testing.T) {
	ks := newTestKS(t)
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatal(err)
	}
	pub1, _ := ks.WGPublicKey()

	// 再次调用 EnsureWGKey，不应改变密钥
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatal(err)
	}
	pub2, _ := ks.WGPublicKey()

	if pub1 != pub2 {
		t.Fatalf("EnsureWGKey 不幂等：第一次 %q，第二次 %q", pub1, pub2)
	}
}

func TestWGPublicKey_ErrorIfNoKey(t *testing.T) {
	ks := newTestKS(t)
	_, err := ks.WGPublicKey()
	if err == nil {
		t.Fatal("未生成密钥时 WGPublicKey 应返回错误")
	}
}

// ──────────────────── 身份 ────────────────────

func TestHasIdentity_FalseWhenEmpty(t *testing.T) {
	ks := newTestKS(t)
	if ks.HasIdentity() {
		t.Fatal("新 KeyStore 不应有身份")
	}
}

func TestSaveLoadIdentity_RoundTrip(t *testing.T) {
	ks := newTestKS(t)

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nodeCertDER := buildSelfSignedCert(t, nodeKey)
	caCertDER := buildSelfSignedCert(t, nodeKey) // 简化：用同一密钥做 CA 证书
	virtualIP := "10.0.1.5/32"
	nodeID := "testnode001"

	if err := ks.SaveIdentity(nodeCertDER, nodeKey, caCertDER, virtualIP, nodeID); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	if !ks.HasIdentity() {
		t.Fatal("保存后 HasIdentity 应为 true")
	}

	id, err := ks.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if id.NodeID != nodeID {
		t.Errorf("NodeID 不一致: got %q, want %q", id.NodeID, nodeID)
	}
	if id.VirtualIP != virtualIP {
		t.Errorf("VirtualIP 不一致: got %q, want %q", id.VirtualIP, virtualIP)
	}
	if len(id.NodeCertPEM) == 0 {
		t.Error("NodeCertPEM 不应为空")
	}
	if len(id.NodeKeyPEM) == 0 {
		t.Error("NodeKeyPEM 不应为空")
	}
	if len(id.CACertPEM) == 0 {
		t.Error("CACertPEM 不应为空")
	}
}

func TestLoadIdentity_ErrorWhenEmpty(t *testing.T) {
	ks := newTestKS(t)
	_, err := ks.LoadIdentity()
	if err == nil {
		t.Fatal("无身份时 LoadIdentity 应返回错误")
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	ks := keystore.New(dir)

	// 生成 WG 密钥
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatal(err)
	}

	checkMode := func(name string) {
		t.Helper()
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %q: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("%q 权限应为 0600，得到 %04o", name, perm)
		}
	}
	checkMode("wg_private.key")
	checkMode("wg_public.key")

	// 保存身份
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	certDER := buildSelfSignedCert(t, nodeKey)
	if err := ks.SaveIdentity(certDER, nodeKey, certDER, "10.0.0.1/32", "id1"); err != nil {
		t.Fatal(err)
	}
	checkMode("node_key.pem")
	checkMode("node_cert.pem")
	checkMode("ca_cert.pem")
	checkMode("identity.json")
}

// ──────────────────── NodeCertKeyPair ────────────────────

func TestNodeCertKeyPair_ValidPEM(t *testing.T) {
	dir := t.TempDir()
	ks := keystore.New(dir)

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certDER := buildSelfSignedCert(t, nodeKey)

	if err := ks.SaveIdentity(certDER, nodeKey, certDER, "10.0.0.2/32", "test-id"); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	pair, err := ks.NodeCertKeyPair()
	if err != nil {
		t.Fatalf("NodeCertKeyPair: %v", err)
	}
	if pair.Certificate == nil {
		t.Fatal("返回的 tls.Certificate 中证书为空")
	}
}

// 模拟「私钥写完、公钥未写就崩溃」后重启：EnsureWGKey 应由私钥重算补写公钥并自愈。
func TestEnsureWGKey_RecoversMissingPublicKey(t *testing.T) {
	dir := t.TempDir()
	ks := keystore.New(dir)

	// 第一次正常生成私钥+公钥
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatalf("首次 EnsureWGKey: %v", err)
	}
	wantPub, err := ks.WGPublicKey()
	if err != nil {
		t.Fatalf("WGPublicKey: %v", err)
	}

	// 模拟公钥部分写入后损坏：删除公钥文件，仅保留私钥
	if err := os.Remove(filepath.Join(dir, "wg_public.key")); err != nil {
		t.Fatalf("删除公钥文件: %v", err)
	}
	if _, err := ks.WGPublicKey(); err == nil {
		t.Fatal("前置条件：公钥应已缺失")
	}

	// 重启后再次 EnsureWGKey：应识别「私钥在公钥缺」并由私钥重算补写公钥
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatalf("自愈 EnsureWGKey: %v", err)
	}
	gotPub, err := ks.WGPublicKey()
	if err != nil {
		t.Fatalf("自愈后 WGPublicKey: %v", err)
	}
	if gotPub != wantPub {
		t.Fatalf("重算公钥应与原公钥一致：want %q got %q", wantPub, gotPub)
	}

	// 私钥不应被改动（重算公钥不能重新生成私钥）
	gotPriv, err := ks.WGPrivateKey()
	if err != nil {
		t.Fatalf("WGPrivateKey: %v", err)
	}
	if gotPriv == "" {
		t.Fatal("私钥不应为空")
	}
}

// HasWGKey 仅当私钥与公钥都存在时返回 true。
func TestHasWGKey(t *testing.T) {
	dir := t.TempDir()
	ks := keystore.New(dir)

	if ks.HasWGKey() {
		t.Fatal("空目录 HasWGKey 应为 false")
	}
	if err := ks.EnsureWGKey(); err != nil {
		t.Fatalf("EnsureWGKey: %v", err)
	}
	if !ks.HasWGKey() {
		t.Fatal("生成后 HasWGKey 应为 true")
	}
	// 仅缺公钥时也应为 false
	if err := os.Remove(filepath.Join(dir, "wg_public.key")); err != nil {
		t.Fatalf("删除公钥文件: %v", err)
	}
	if ks.HasWGKey() {
		t.Fatal("公钥缺失时 HasWGKey 应为 false")
	}
}
