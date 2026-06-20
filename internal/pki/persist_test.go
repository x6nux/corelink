package pki

import (
	"crypto/x509"
	"testing"
	"time"
)

// TestMarshal_LoadCA_RoundTrip 表驱动测试验证 Marshal/LoadCA 往返一致性。
func TestMarshal_LoadCA_RoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		commonName string
	}{
		{name: "普通名称", commonName: "CoreLink Root"},
		{name: "空名称", commonName: ""},
		{name: "中文名称", commonName: "测试CA"},
		{name: "长名称", commonName: "Very Long Common Name For Testing Purposes That Goes On And On"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ca, err := NewCA(tt.commonName)
			if err != nil {
				t.Fatal(err)
			}

			certPEM, keyPEM, err := ca.Marshal()
			if err != nil {
				t.Fatalf("Marshal 失败: %v", err)
			}
			if len(certPEM) == 0 {
				t.Fatal("证书 PEM 不应为空")
			}
			if len(keyPEM) == 0 {
				t.Fatal("私钥 PEM 不应为空")
			}

			loaded, err := LoadCA(certPEM, keyPEM)
			if err != nil {
				t.Fatalf("LoadCA 失败: %v", err)
			}

			// 验证 CN 一致
			if loaded.Cert().Subject.CommonName != tt.commonName {
				t.Errorf("加载后 CN = %q, 期望 %q",
					loaded.Cert().Subject.CommonName, tt.commonName)
			}
			// 验证 IsCA 标记
			if !loaded.Cert().IsCA {
				t.Error("加载后应保持 IsCA = true")
			}
		})
	}
}

// TestMarshal_PEMFormat 验证 Marshal 输出的 PEM 格式正确。
func TestMarshal_PEMFormat(t *testing.T) {
	ca, _ := NewCA("format-test")
	certPEM, keyPEM, err := ca.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// 检查 PEM 头
	certHeader := "-----BEGIN CERTIFICATE-----"
	keyHeader := "-----BEGIN EC PRIVATE KEY-----"
	if string(certPEM[:len(certHeader)]) != certHeader {
		t.Errorf("证书 PEM 应以 %q 开头", certHeader)
	}
	if string(keyPEM[:len(keyHeader)]) != keyHeader {
		t.Errorf("私钥 PEM 应以 %q 开头", keyHeader)
	}
}

// TestLoadCA_InvalidCertPEM 验证无效证书 PEM 时返回错误。
func TestLoadCA_InvalidCertPEM(t *testing.T) {
	ca, _ := NewCA("root")
	_, keyPEM, _ := ca.Marshal()

	_, err := LoadCA([]byte("not-pem-data"), keyPEM)
	if err == nil {
		t.Fatal("无效证书 PEM 应返回错误")
	}
}

// TestLoadCA_InvalidKeyPEM 验证无效私钥 PEM 时返回错误。
func TestLoadCA_InvalidKeyPEM(t *testing.T) {
	ca, _ := NewCA("root")
	certPEM, _, _ := ca.Marshal()

	_, err := LoadCA(certPEM, []byte("not-pem-data"))
	if err == nil {
		t.Fatal("无效私钥 PEM 应返回错误")
	}
}

// TestLoadCA_MismatchedKeyType 验证非 EC 私钥内容时的处理。
func TestLoadCA_MismatchedKeyType(t *testing.T) {
	ca, _ := NewCA("root")
	certPEM, _, _ := ca.Marshal()

	// 用证书 PEM 充当密钥 PEM（格式不对应）
	_, err := LoadCA(certPEM, certPEM)
	if err == nil {
		t.Fatal("用证书数据充当密钥应返回错误")
	}
}

// TestLoadCA_EmptyInputs 验证空输入时返回错误。
func TestLoadCA_EmptyInputs(t *testing.T) {
	tests := []struct {
		name    string
		certPEM []byte
		keyPEM  []byte
	}{
		{name: "空证书", certPEM: nil, keyPEM: []byte("something")},
		{name: "空私钥", certPEM: []byte("something"), keyPEM: nil},
		{name: "全空", certPEM: nil, keyPEM: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadCA(tt.certPEM, tt.keyPEM)
			if err == nil {
				t.Fatal("空输入应返回错误")
			}
		})
	}
}

// TestMarshal_LoadCA_CanStillSign 验证 Marshal/LoadCA 后 CA 仍能正常签发证书。
func TestMarshal_LoadCA_CanStillSign(t *testing.T) {
	ca, _ := NewCA("round-trip-sign")
	certPEM, keyPEM, err := ca.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCA(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	csrDER, _, err := GenerateCSR("node-after-reload")
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := loaded.IssueFromCSR(csrDER, "node-after-reload", NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatalf("加载后签发失败: %v", err)
	}

	// 验证签发的证书可被原 CA 和加载后的 CA 都验证
	cert, _ := x509.ParseCertificate(certDER)
	pool := x509.NewCertPool()
	pool.AddCert(loaded.Cert())
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("加载后签发的证书验证失败: %v", err)
	}
}

// TestMarshal_LoadCA_MultipleTimes 验证可多次序列化/反序列化。
func TestMarshal_LoadCA_MultipleTimes(t *testing.T) {
	ca, _ := NewCA("multi-trip")
	for i := 0; i < 3; i++ {
		certPEM, keyPEM, err := ca.Marshal()
		if err != nil {
			t.Fatalf("第 %d 次 Marshal 失败: %v", i, err)
		}
		loaded, err := LoadCA(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("第 %d 次 LoadCA 失败: %v", i, err)
		}
		ca = loaded
	}
	// 最终仍能签发
	csrDER, _, _ := GenerateCSR("final")
	if _, err := ca.IssueFromCSR(csrDER, "final", NodeRoleNode, time.Hour); err != nil {
		t.Fatalf("多次往返后签发失败: %v", err)
	}
}

// TestLoadCA_SwappedPEM 验证证书和密钥 PEM 交换时返回错误。
func TestLoadCA_SwappedPEM(t *testing.T) {
	ca, _ := NewCA("root")
	certPEM, keyPEM, _ := ca.Marshal()
	// 交换证书和密钥
	_, err := LoadCA(keyPEM, certPEM)
	if err == nil {
		t.Fatal("交换证书和密钥 PEM 应返回错误")
	}
}

// TestMarshal_Idempotent 验证同一 CA 多次 Marshal 的证书 PEM 一致。
func TestMarshal_Idempotent(t *testing.T) {
	ca, _ := NewCA("idem")
	cert1, key1, _ := ca.Marshal()
	cert2, key2, _ := ca.Marshal()
	if string(cert1) != string(cert2) {
		t.Error("同一 CA 两次 Marshal 的证书 PEM 应一致")
	}
	if string(key1) != string(key2) {
		t.Error("同一 CA 两次 Marshal 的密钥 PEM 应一致")
	}
}
