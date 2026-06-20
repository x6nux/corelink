package pki

import (
	"crypto/x509"
	"math/big"
	"testing"
	"time"
)

func TestRenewIssuesNewSerial(t *testing.T) {
	ca, _ := NewCA("root")
	csr1, _, _ := GenerateCSR("n1")
	c1DER, _ := ca.IssueFromCSR(csr1, "n1", NodeRoleNode, time.Hour)
	csr2, _, _ := GenerateCSR("n1")
	c2DER, err := ca.Renew(csr2, "n1", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	c1, _ := x509.ParseCertificate(c1DER)
	c2, _ := x509.ParseCertificate(c2DER)
	if c1.SerialNumber.Cmp(c2.SerialNumber) == 0 {
		t.Fatal("续签必须使用新序列号")
	}
	if c2.Subject.CommonName != "n1" {
		t.Fatal("续签应保持身份 CN")
	}
}

func TestCAMarshalUnmarshalRoundTrip(t *testing.T) {
	ca, _ := NewCA("CoreLink Root")
	certPEM, keyPEM, err := ca.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	loaded, err := LoadCA(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	// 加载后仍能签发且可验证
	csr, _, _ := GenerateCSR("n2")
	if _, err := loaded.IssueFromCSR(csr, "n2", NodeRoleNode, time.Hour); err != nil {
		t.Fatalf("加载后签发失败: %v", err)
	}
}

func TestNewCAAndIssueNodeCert(t *testing.T) {
	ca, err := NewCA("CoreLink Root")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	// 用节点自带密钥生成 CSR
	csrDER, _, err := GenerateCSR("node-n1")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	certDER, err := ca.IssueFromCSR(csrDER, "node-n1", NodeRoleNode, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueFromCSR: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	// 签发证书应能被 CA 验证（节点证书为 client 证书，仅 ClientAuth）
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("签发证书无法被 CA 验证: %v", err)
	}
	if cert.Subject.CommonName != "node-n1" {
		t.Fatalf("CN=%s, want node-n1", cert.Subject.CommonName)
	}
}

// TestIssueClientCert_NoSANNoServerAuth 默认（client）签发：忽略 CSR SAN、仅 ClientAuth。
func TestIssueClientCert_DropsSANAndServerAuth(t *testing.T) {
	ca, _ := NewCA("root")
	// CSR 携带攻击者塞入的 DNS/IP SAN
	csr, _, err := GenerateCSR("node-x", "evil.example.com", "1.2.3.4")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	certDER, err := ca.IssueFromCSR(csr, "node-x", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatalf("IssueFromCSR: %v", err)
	}
	cert, _ := x509.ParseCertificate(certDER)

	if len(cert.DNSNames) != 0 {
		t.Errorf("client 证书不应复制 CSR DNS SAN，实际 %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 0 {
		t.Errorf("client 证书不应复制 CSR IP SAN，实际 %v", cert.IPAddresses)
	}
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			t.Error("client 证书不应含 ServerAuth")
		}
	}
	hasClient := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasClient {
		t.Error("client 证书应含 ClientAuth")
	}
}

// TestIssueServerCert_KeepsSANAndServerAuth WithServerAuth：复制 CSR SAN 且含 ServerAuth。
func TestIssueServerCert_KeepsSANAndServerAuth(t *testing.T) {
	ca, _ := NewCA("root")
	csr, _, err := GenerateCSR("controller-server", "controller-server")
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	certDER, err := ca.IssueFromCSR(csr, "controller-server", NodeRoleNode, time.Hour, WithServerAuth())
	if err != nil {
		t.Fatalf("IssueFromCSR(server): %v", err)
	}
	cert, _ := x509.ParseCertificate(certDER)

	foundSAN := false
	for _, d := range cert.DNSNames {
		if d == "controller-server" {
			foundSAN = true
		}
	}
	if !foundSAN {
		t.Errorf("server 证书应保留 DNS SAN controller-server，实际 %v", cert.DNSNames)
	}
	hasServer := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
	}
	if !hasServer {
		t.Error("server 证书应含 ServerAuth")
	}
}

// parseTestCert 解析 DER 编码的证书，失败时终止测试。
func parseTestCert(t *testing.T, der []byte) (*x509.Certificate, error) {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate 失败: %v", err)
	}
	return cert, nil
}

func TestCRLRevokeAndCheck(t *testing.T) {
	ca, _ := NewCA("root")
	csr, _, _ := GenerateCSR("n1")
	certDER, _ := ca.IssueFromCSR(csr, "n1", NodeRoleNode, time.Hour)
	cert, _ := x509.ParseCertificate(certDER)

	crlDER, err := ca.BuildCRL([]*big.Int{cert.SerialNumber}, time.Hour)
	if err != nil {
		t.Fatalf("BuildCRL: %v", err)
	}
	revoked, err := IsRevoked(crlDER, cert.SerialNumber)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("已吊销证书应被 CRL 命中")
	}
	// 未吊销的序列号不命中
	other := big.NewInt(999999)
	hit, _ := IsRevoked(crlDER, other)
	if hit {
		t.Fatal("未吊销序列号不应命中")
	}
}
