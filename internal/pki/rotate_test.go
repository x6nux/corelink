package pki

import (
	"crypto/x509"
	"testing"
	"time"
)

// TestRenew_TableDriven 表驱动测试覆盖各种续签场景。
func TestRenew_TableDriven(t *testing.T) {
	ca, err := NewCA("renew-root")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		nodeID     string
		role       NodeRole
		ttl        time.Duration
		opts       []IssueOption
		wantServer bool
	}{
		{
			name:       "agent续签_客户端证书",
			nodeID:     "agent-renew",
			role:       NodeRoleNode,
			ttl:        24 * time.Hour,
			wantServer: false,
		},
		{
			name:       "relay续签_客户端证书",
			nodeID:     "relay-renew",
			role:       NodeRoleNode,
			ttl:        48 * time.Hour,
			wantServer: false,
		},
		{
			name:       "server续签_含ServerAuth",
			nodeID:     "server-renew",
			role:       NodeRoleNode,
			ttl:        time.Hour,
			opts:       []IssueOption{WithServerAuth()},
			wantServer: true,
		},
		{
			name:       "短有效期续签",
			nodeID:     "short-renew",
			role:       NodeRoleNode,
			ttl:        5 * time.Minute,
			wantServer: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrDER, _, err := GenerateCSR(tt.nodeID)
			if err != nil {
				t.Fatal(err)
			}
			certDER, err := ca.Renew(csrDER, tt.nodeID, tt.role, tt.ttl, tt.opts...)
			if err != nil {
				t.Fatalf("Renew 失败: %v", err)
			}
			cert, err := x509.ParseCertificate(certDER)
			if err != nil {
				t.Fatalf("解析续签证书失败: %v", err)
			}
			// 检查 CN
			if cert.Subject.CommonName != tt.nodeID {
				t.Errorf("CN = %q, 期望 %q", cert.Subject.CommonName, tt.nodeID)
			}
			// 检查角色
			if len(cert.Subject.OrganizationalUnit) != 1 ||
				cert.Subject.OrganizationalUnit[0] != string(tt.role) {
				t.Errorf("OU = %v, 期望 [%s]", cert.Subject.OrganizationalUnit, tt.role)
			}
			// 检查 ServerAuth
			hasServer := false
			for _, eku := range cert.ExtKeyUsage {
				if eku == x509.ExtKeyUsageServerAuth {
					hasServer = true
				}
			}
			if hasServer != tt.wantServer {
				t.Errorf("ServerAuth = %v, 期望 %v", hasServer, tt.wantServer)
			}
			// 验证证书链
			pool := x509.NewCertPool()
			pool.AddCert(ca.Cert())
			if _, err := cert.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}); err != nil {
				t.Errorf("续签证书验证失败: %v", err)
			}
		})
	}
}

// TestRenew_NewSerial 验证续签生成新序列号。
func TestRenew_NewSerial(t *testing.T) {
	ca, _ := NewCA("root")

	// 首次签发
	csrDER1, _, _ := GenerateCSR("node-1")
	cert1DER, err := ca.IssueFromCSR(csrDER1, "node-1", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert1, _ := parseTestCert(t, cert1DER)

	// 续签（用新 CSR）
	csrDER2, _, _ := GenerateCSR("node-1")
	cert2DER, err := ca.Renew(csrDER2, "node-1", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert2, _ := parseTestCert(t, cert2DER)

	if cert1.SerialNumber.Cmp(cert2.SerialNumber) == 0 {
		t.Fatal("续签必须使用新序列号")
	}
}

// TestRenew_PreservesIdentity 验证续签保留身份信息。
func TestRenew_PreservesIdentity(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, _ := GenerateCSR("persistent-node")
	certDER, err := ca.Renew(csrDER, "persistent-node", NodeRoleNode, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := parseTestCert(t, certDER)

	if cert.Subject.CommonName != "persistent-node" {
		t.Errorf("续签应保持 CN = persistent-node, 实际 %q", cert.Subject.CommonName)
	}
	if cert.Subject.OrganizationalUnit[0] != "node" {
		t.Errorf("续签应保持 OU = relay, 实际 %v", cert.Subject.OrganizationalUnit)
	}
}

// TestRenew_InvalidCSR 验证无效 CSR 续签失败。
func TestRenew_InvalidCSR(t *testing.T) {
	ca, _ := NewCA("root")
	_, err := ca.Renew([]byte("bad-csr"), "node", NodeRoleNode, time.Hour)
	if err == nil {
		t.Fatal("无效 CSR 续签应返回错误")
	}
}

// TestRenew_WithServerAuth_CopiesSAN 验证 server 证书续签保留 SAN。
func TestRenew_WithServerAuth_CopiesSAN(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, err := GenerateCSR("server-node", "server.example.com")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := ca.Renew(csrDER, "server-node", NodeRoleNode, time.Hour, WithServerAuth())
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := parseTestCert(t, certDER)
	foundDNS := false
	for _, d := range cert.DNSNames {
		if d == "server.example.com" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Errorf("server 续签应保留 DNS SAN, 实际: %v", cert.DNSNames)
	}
}

// TestRenew_ClientAuth_DropsSAN 验证 client 证书续签忽略 SAN。
func TestRenew_ClientAuth_DropsSAN(t *testing.T) {
	ca, _ := NewCA("root")
	csrDER, _, err := GenerateCSR("client-node", "evil.example.com")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := ca.Renew(csrDER, "client-node", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := parseTestCert(t, certDER)
	if len(cert.DNSNames) != 0 {
		t.Errorf("client 续签不应含 DNS SAN, 实际: %v", cert.DNSNames)
	}
}

// TestRenew_ConsecutiveRenewals 验证连续多次续签均成功。
func TestRenew_ConsecutiveRenewals(t *testing.T) {
	ca, _ := NewCA("root")
	for i := 0; i < 5; i++ {
		csrDER, _, err := GenerateCSR("renew-loop")
		if err != nil {
			t.Fatalf("第 %d 次 GenerateCSR 失败: %v", i, err)
		}
		certDER, err := ca.Renew(csrDER, "renew-loop", NodeRoleNode, time.Hour)
		if err != nil {
			t.Fatalf("第 %d 次 Renew 失败: %v", i, err)
		}
		cert, _ := parseTestCert(t, certDER)
		pool := x509.NewCertPool()
		pool.AddCert(ca.Cert())
		if _, err := cert.Verify(x509.VerifyOptions{
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); err != nil {
			t.Fatalf("第 %d 次续签证书验证失败: %v", i, err)
		}
	}
}

// TestRenew_OldAndNewBothValid 验证续签后旧证书和新证书在有效期内都可验证。
func TestRenew_OldAndNewBothValid(t *testing.T) {
	ca, _ := NewCA("root")
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())

	// 首次签发
	csrDER1, _, _ := GenerateCSR("node")
	cert1DER, _ := ca.IssueFromCSR(csrDER1, "node", NodeRoleNode, time.Hour)
	cert1, _ := parseTestCert(t, cert1DER)

	// 续签
	csrDER2, _, _ := GenerateCSR("node")
	cert2DER, _ := ca.Renew(csrDER2, "node", NodeRoleNode, time.Hour)
	cert2, _ := parseTestCert(t, cert2DER)

	// 两张证书都应在有效期内可验证
	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert1.Verify(opts); err != nil {
		t.Errorf("旧证书验证失败: %v", err)
	}
	if _, err := cert2.Verify(opts); err != nil {
		t.Errorf("新证书验证失败: %v", err)
	}
}
