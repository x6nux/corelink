package pki

import (
	"crypto/x509"
	"net"
	"testing"
)

// TestGenerateCSR_TableDriven 表驱动测试覆盖各种 CSR 生成场景。
func TestGenerateCSR_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		commonName string
		dnsSANs    []string
		wantDNS    []string
		wantIP     []string // 期望的 IP SAN（字符串形式）
	}{
		{
			name:       "普通节点名称_无SAN",
			commonName: "node-001",
			dnsSANs:    nil,
			wantDNS:    nil,
			wantIP:     nil,
		},
		{
			name:       "带DNS_SAN",
			commonName: "relay-server",
			dnsSANs:    []string{"relay.example.com", "relay.local"},
			wantDNS:    []string{"relay.example.com", "relay.local"},
			wantIP:     nil,
		},
		{
			name:       "IP地址作为CN",
			commonName: "192.168.1.1",
			dnsSANs:    nil,
			wantDNS:    nil,
			wantIP:     []string{"192.168.1.1"},
		},
		{
			name:       "IPv6地址作为CN",
			commonName: "::1",
			dnsSANs:    nil,
			wantDNS:    nil,
			wantIP:     []string{"::1"},
		},
		{
			name:       "IP地址CN_加DNS_SAN",
			commonName: "10.0.0.1",
			dnsSANs:    []string{"server.example.com"},
			wantDNS:    []string{"server.example.com"},
			wantIP:     []string{"10.0.0.1"},
		},
		{
			name:       "空CN",
			commonName: "",
			dnsSANs:    nil,
			wantDNS:    nil,
			wantIP:     nil,
		},
		{
			name:       "多个DNS_SAN",
			commonName: "multi-san",
			dnsSANs:    []string{"a.example.com", "b.example.com", "c.example.com"},
			wantDNS:    []string{"a.example.com", "b.example.com", "c.example.com"},
			wantIP:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrDER, key, err := GenerateCSR(tt.commonName, tt.dnsSANs...)
			if err != nil {
				t.Fatalf("GenerateCSR(%q) 失败: %v", tt.commonName, err)
			}
			if key == nil {
				t.Fatal("生成的私钥不应为 nil")
			}
			if len(csrDER) == 0 {
				t.Fatal("CSR DER 不应为空")
			}

			// 解析 CSR 并验证
			csr, err := x509.ParseCertificateRequest(csrDER)
			if err != nil {
				t.Fatalf("解析 CSR 失败: %v", err)
			}
			if err := csr.CheckSignature(); err != nil {
				t.Fatalf("CSR 签名校验失败: %v", err)
			}

			// 检查 Subject.CN
			if csr.Subject.CommonName != tt.commonName {
				t.Errorf("CSR CN = %q, 期望 %q", csr.Subject.CommonName, tt.commonName)
			}

			// 检查 DNS SAN
			if len(csr.DNSNames) != len(tt.wantDNS) {
				t.Errorf("DNS SAN 数量 = %d, 期望 %d", len(csr.DNSNames), len(tt.wantDNS))
			} else {
				for i, want := range tt.wantDNS {
					if csr.DNSNames[i] != want {
						t.Errorf("DNS SAN[%d] = %q, 期望 %q", i, csr.DNSNames[i], want)
					}
				}
			}

			// 检查 IP SAN
			if len(csr.IPAddresses) != len(tt.wantIP) {
				t.Errorf("IP SAN 数量 = %d, 期望 %d", len(csr.IPAddresses), len(tt.wantIP))
			} else {
				for i, want := range tt.wantIP {
					wantIP := net.ParseIP(want)
					if !csr.IPAddresses[i].Equal(wantIP) {
						t.Errorf("IP SAN[%d] = %v, 期望 %v", i, csr.IPAddresses[i], wantIP)
					}
				}
			}
		})
	}
}

// TestGenerateCSR_UniqueKeys 每次调用生成不同的密钥对。
func TestGenerateCSR_UniqueKeys(t *testing.T) {
	_, key1, err := GenerateCSR("node-a")
	if err != nil {
		t.Fatal(err)
	}
	_, key2, err := GenerateCSR("node-b")
	if err != nil {
		t.Fatal(err)
	}
	if key1.D.Cmp(key2.D) == 0 {
		t.Error("两次调用 GenerateCSR 应生成不同私钥")
	}
}

// TestGenerateCSR_SignatureValid 验证生成的 CSR 签名合法。
func TestGenerateCSR_SignatureValid(t *testing.T) {
	csrDER, _, err := GenerateCSR("sig-test", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR 签名校验失败: %v", err)
	}
}

// TestGenerateCSR_KeyMatchesCSR 验证返回的私钥与 CSR 中公钥匹配。
func TestGenerateCSR_KeyMatchesCSR(t *testing.T) {
	csrDER, key, err := GenerateCSR("key-match")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	// CSR 的公钥应与返回的私钥的公钥部分一致
	if !key.PublicKey.Equal(csr.PublicKey) {
		t.Error("CSR 公钥与返回的私钥公钥不匹配")
	}
}

// TestGenerateCSR_CanBeIssuedByCA 验证 GenerateCSR 产出的 CSR 可被 CA 签发。
func TestGenerateCSR_CanBeIssuedByCA(t *testing.T) {
	ca, err := NewCA("test-root")
	if err != nil {
		t.Fatal(err)
	}
	csrDER, _, err := GenerateCSR("issuable-node", "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := ca.IssueFromCSR(csrDER, "issuable-node", NodeRoleNode, 3600e9) // 1 hour
	if err != nil {
		t.Fatalf("CA 无法签发 GenerateCSR 产出的 CSR: %v", err)
	}
	if len(certDER) == 0 {
		t.Fatal("签发的证书 DER 不应为空")
	}
}

// TestGenerateCSR_NonIPCommonName 非 IP 格式的 CN 不应加入 IP SAN。
func TestGenerateCSR_NonIPCommonName(t *testing.T) {
	csrDER, _, err := GenerateCSR("not-an-ip")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	if len(csr.IPAddresses) != 0 {
		t.Errorf("非 IP CN 不应有 IP SAN, 实际: %v", csr.IPAddresses)
	}
}
