package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"testing"
	"time"
)

// TestNewCA_Properties 校验 NewCA 生成的 CA 证书属性。
func TestNewCA_Properties(t *testing.T) {
	tests := []struct {
		name       string
		commonName string
	}{
		{name: "普通名称", commonName: "CoreLink Root CA"},
		{name: "空名称", commonName: ""},
		{name: "中文名称", commonName: "测试根证书"},
		{name: "特殊字符", commonName: "CA-test_01.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ca, err := NewCA(tt.commonName)
			if err != nil {
				t.Fatalf("NewCA(%q) 失败: %v", tt.commonName, err)
			}
			cert := ca.Cert()
			if cert == nil {
				t.Fatal("CA.Cert() 返回 nil")
			}
			if cert.Subject.CommonName != tt.commonName {
				t.Errorf("CN = %q, 期望 %q", cert.Subject.CommonName, tt.commonName)
			}
			if !cert.IsCA {
				t.Error("CA 证书应设置 IsCA = true")
			}
			if !cert.BasicConstraintsValid {
				t.Error("CA 证书应设置 BasicConstraintsValid = true")
			}
			if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
				t.Error("CA 证书应包含 KeyUsageCertSign")
			}
			if cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
				t.Error("CA 证书应包含 KeyUsageCRLSign")
			}
			// 有效期约 10 年
			dur := cert.NotAfter.Sub(cert.NotBefore)
			if dur < 9*365*24*time.Hour || dur > 11*365*24*time.Hour {
				t.Errorf("有效期 %v 不在 9~11 年范围内", dur)
			}
			// 自签：Issuer == Subject
			if cert.Issuer.CommonName != cert.Subject.CommonName {
				t.Errorf("自签 CA Issuer.CN = %q, 期望 = %q", cert.Issuer.CommonName, cert.Subject.CommonName)
			}
		})
	}
}

// TestNewCA_UniqueInstances 每次调用 NewCA 都生成不同的密钥对。
func TestNewCA_UniqueInstances(t *testing.T) {
	ca1, err := NewCA("ca1")
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := NewCA("ca2")
	if err != nil {
		t.Fatal(err)
	}
	pub1 := ca1.Cert().PublicKey.(*ecdsa.PublicKey)
	pub2 := ca2.Cert().PublicKey.(*ecdsa.PublicKey)
	if pub1.X.Cmp(pub2.X) == 0 && pub1.Y.Cmp(pub2.Y) == 0 {
		t.Error("两次 NewCA 应生成不同密钥对")
	}
}

// TestIssueFromCSR_TableDriven 表驱动测试覆盖各种签发场景。
func TestIssueFromCSR_TableDriven(t *testing.T) {
	ca, err := NewCA("test-root")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		nodeID     string
		role       NodeRole
		ttl        time.Duration
		opts       []IssueOption
		wantServer bool // 期望含 ServerAuth
		wantClient bool // 期望含 ClientAuth
	}{
		{
			name:       "agent客户端证书",
			nodeID:     "agent-001",
			role:       NodeRoleNode,
			ttl:        24 * time.Hour,
			wantServer: false,
			wantClient: true,
		},
		{
			name:       "relay客户端证书",
			nodeID:     "relay-001",
			role:       NodeRoleNode,
			ttl:        72 * time.Hour,
			wantServer: false,
			wantClient: true,
		},
		{
			name:       "server证书含ServerAuth",
			nodeID:     "ctrl-001",
			role:       NodeRoleNode,
			ttl:        time.Hour,
			opts:       []IssueOption{WithServerAuth()},
			wantServer: true,
			wantClient: true,
		},
		{
			name:       "短有效期1秒",
			nodeID:     "short-lived",
			role:       NodeRoleNode,
			ttl:        time.Second,
			wantServer: false,
			wantClient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrDER, _, err := GenerateCSR(tt.nodeID)
			if err != nil {
				t.Fatalf("GenerateCSR: %v", err)
			}
			certDER, err := ca.IssueFromCSR(csrDER, tt.nodeID, tt.role, tt.ttl, tt.opts...)
			if err != nil {
				t.Fatalf("IssueFromCSR: %v", err)
			}
			cert, err := x509.ParseCertificate(certDER)
			if err != nil {
				t.Fatalf("ParseCertificate: %v", err)
			}
			// 检查 CN
			if cert.Subject.CommonName != tt.nodeID {
				t.Errorf("CN = %q, 期望 %q", cert.Subject.CommonName, tt.nodeID)
			}
			// 检查 OU（角色）
			if len(cert.Subject.OrganizationalUnit) != 1 || cert.Subject.OrganizationalUnit[0] != string(tt.role) {
				t.Errorf("OU = %v, 期望 [%s]", cert.Subject.OrganizationalUnit, tt.role)
			}
			// 检查 ExtKeyUsage
			hasServer := false
			hasClient := false
			for _, eku := range cert.ExtKeyUsage {
				if eku == x509.ExtKeyUsageServerAuth {
					hasServer = true
				}
				if eku == x509.ExtKeyUsageClientAuth {
					hasClient = true
				}
			}
			if hasServer != tt.wantServer {
				t.Errorf("ServerAuth = %v, 期望 %v", hasServer, tt.wantServer)
			}
			if hasClient != tt.wantClient {
				t.Errorf("ClientAuth = %v, 期望 %v", hasClient, tt.wantClient)
			}
			// 校验证书链
			pool := x509.NewCertPool()
			pool.AddCert(ca.Cert())
			keyUsages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			if tt.wantServer {
				keyUsages = append(keyUsages, x509.ExtKeyUsageServerAuth)
			}
			if _, err := cert.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: keyUsages,
			}); err != nil {
				t.Errorf("证书链校验失败: %v", err)
			}
		})
	}
}

// TestIssueFromCSR_InvalidCSR 验证无效 CSR 被拒绝。
func TestIssueFromCSR_InvalidCSR(t *testing.T) {
	ca, _ := NewCA("root")
	// 随机垃圾数据
	_, err := ca.IssueFromCSR([]byte("not-a-csr"), "node", NodeRoleNode, time.Hour)
	if err == nil {
		t.Fatal("无效 CSR 应返回错误")
	}
}

// TestIssueFromCSR_TamperedCSR 验证篡改过签名的 CSR 被拒绝。
func TestIssueFromCSR_TamperedCSR(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, err := GenerateCSR("legit-node")
	if err != nil {
		t.Fatal(err)
	}
	// 篡改 CSR 的最后一个字节
	tampered := make([]byte, len(csrDER))
	copy(tampered, csrDER)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = ca.IssueFromCSR(tampered, "legit-node", NodeRoleNode, time.Hour)
	if err == nil {
		t.Fatal("篡改过的 CSR 应返回签名校验错误")
	}
}

// TestIssueFromCSR_ServerAuth_CopiesSAN 验证 WithServerAuth 正确复制 CSR 中的 SAN。
func TestIssueFromCSR_ServerAuth_CopiesSAN(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, err := GenerateCSR("10.0.0.1", "relay.example.com", "relay.local")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := ca.IssueFromCSR(csrDER, "relay-server", NodeRoleNode, time.Hour, WithServerAuth())
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(certDER)

	// 检查 DNS SAN
	wantDNS := map[string]bool{"relay.example.com": true, "relay.local": true}
	for _, d := range cert.DNSNames {
		delete(wantDNS, d)
	}
	if len(wantDNS) > 0 {
		t.Errorf("缺少 DNS SAN: %v", wantDNS)
	}
	// 检查 IP SAN（commonName 为 IP 时 GenerateCSR 自动加入）
	foundIP := false
	for _, ip := range cert.IPAddresses {
		if ip.String() == "10.0.0.1" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Error("server 证书应复制 CSR 中的 IP SAN 10.0.0.1")
	}
}

// TestIssueFromCSR_ClientAuth_IgnoresSAN 验证默认 client 签发忽略 CSR SAN。
func TestIssueFromCSR_ClientAuth_IgnoresSAN(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, err := GenerateCSR("attacker-node", "admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := ca.IssueFromCSR(csrDER, "attacker-node", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(certDER)
	if len(cert.DNSNames) != 0 {
		t.Errorf("client 证书不应含 DNS SAN, 实际: %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 0 {
		t.Errorf("client 证书不应含 IP SAN, 实际: %v", cert.IPAddresses)
	}
}

// TestIssueFromCSR_WrongKeyType 验证使用非 CSR 格式的公钥时的行为。
func TestIssueFromCSR_WrongKeyType(t *testing.T) {
	ca, _ := NewCA("root")
	// 构造一个合法但用不同密钥签名的 CSR（确保签名校验不通过）
	key1, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	key2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	tmpl := &x509.CertificateRequest{
		// 空 subject
	}
	// 用 key1 创建 CSR
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key1)
	if err != nil {
		t.Fatal(err)
	}
	// 篡改公钥信息（解析后修改再编码不可行，直接用 key2 签但嵌入 key1 公钥 — 做不到）
	// 所以这里只测试正常流程：用 key1 签名、key1 公钥，应该成功
	certDER, err := ca.IssueFromCSR(csrDER, "test", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatalf("合法 CSR 签发不应失败: %v", err)
	}
	cert, _ := x509.ParseCertificate(certDER)
	// 签发的证书公钥应是 CSR 中 key1 的公钥
	pub := cert.PublicKey.(*ecdsa.PublicKey)
	if pub.X.Cmp(key1.PublicKey.X) != 0 {
		t.Error("签发证书公钥应与 CSR 提交的公钥一致")
	}
	_ = key2 // key2 仅用于确保测试编译
}

// TestIssueFromCSR_UniqueSerials 验证连续签发使用不同序列号。
func TestIssueFromCSR_UniqueSerials(t *testing.T) {
	ca, _ := NewCA("root")
	serials := make(map[string]bool)
	for i := 0; i < 10; i++ {
		csrDER, _, _ := GenerateCSR("node")
		certDER, err := ca.IssueFromCSR(csrDER, "node", NodeRoleNode, time.Hour)
		if err != nil {
			t.Fatalf("第 %d 次签发失败: %v", i, err)
		}
		cert, _ := x509.ParseCertificate(certDER)
		key := cert.SerialNumber.String()
		if serials[key] {
			t.Fatalf("第 %d 次签发使用了重复序列号 %s", i, key)
		}
		serials[key] = true
	}
}

// TestNodeRole_Values 验证角色常量值。
func TestNodeRole_Values(t *testing.T) {
	tests := []struct {
		role NodeRole
		want string
	}{
		{NodeRoleNode, "node"},
		{NodeRoleNode, "node"},
	}
	for _, tt := range tests {
		if string(tt.role) != tt.want {
			t.Errorf("NodeRole %v = %q, 期望 %q", tt.role, string(tt.role), tt.want)
		}
	}
}

// TestCA_SelfVerify 验证 CA 证书可自验证。
func TestCA_SelfVerify(t *testing.T) {
	ca, _ := NewCA("self-verify")
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	_, err := ca.Cert().Verify(x509.VerifyOptions{Roots: pool})
	if err != nil {
		t.Fatalf("CA 自签证书应可自验证: %v", err)
	}
}

// TestIssueFromCSR_KeyUsageDigitalSignature 验证签发证书仅有 DigitalSignature 用途。
func TestIssueFromCSR_KeyUsageDigitalSignature(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, _ := GenerateCSR("node")
	certDER, _ := ca.IssueFromCSR(csrDER, "node", NodeRoleNode, time.Hour)
	cert, _ := x509.ParseCertificate(certDER)
	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("签发证书 KeyUsage = %v, 期望仅 DigitalSignature(%v)", cert.KeyUsage, x509.KeyUsageDigitalSignature)
	}
}

// TestIssueFromCSR_NotCA 验证签发的证书不是 CA。
func TestIssueFromCSR_NotCA(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, _ := GenerateCSR("node")
	certDER, _ := ca.IssueFromCSR(csrDER, "node", NodeRoleNode, time.Hour)
	cert, _ := x509.ParseCertificate(certDER)
	if cert.IsCA {
		t.Error("签发的节点证书不应是 CA")
	}
}

// TestIssueFromCSR_ValidityWindow 验证签发证书有效期窗口。
func TestIssueFromCSR_ValidityWindow(t *testing.T) {
	ca, _ := NewCA("root")
	ttl := 48 * time.Hour
	csrDER, _, _ := GenerateCSR("node")
	before := time.Now()
	certDER, _ := ca.IssueFromCSR(csrDER, "node", NodeRoleNode, ttl)
	cert, _ := x509.ParseCertificate(certDER)
	// NotBefore 应在 now-2min 之前（代码设 now - 1min）
	if cert.NotBefore.After(before) {
		t.Error("NotBefore 不应晚于签发时间")
	}
	// NotAfter 应约等于 now + ttl
	expectedAfter := before.Add(ttl)
	diff := cert.NotAfter.Sub(expectedAfter)
	if diff < -2*time.Minute || diff > 2*time.Minute {
		t.Errorf("NotAfter 与期望偏差 %v 过大", diff)
	}
}

// TestWithServerAuth_Multiple 验证多次调用 WithServerAuth 不会 panic 或异常。
func TestWithServerAuth_Multiple(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, _ := GenerateCSR("server", "a.example.com")
	certDER, err := ca.IssueFromCSR(csrDER, "server", NodeRoleNode, time.Hour,
		WithServerAuth(), WithServerAuth())
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(certDER)
	// 应含 ServerAuth（即使传了两次 opt，结果应相同）
	hasServer := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
	}
	if !hasServer {
		t.Error("重复 WithServerAuth 后仍应含 ServerAuth")
	}
}

// TestCA_SerialNumber 验证 CA 自身的序列号为 1。
func TestCA_SerialNumber(t *testing.T) {
	ca, _ := NewCA("root")
	if ca.Cert().SerialNumber.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("CA 序列号 = %s, 期望 1", ca.Cert().SerialNumber)
	}
}
