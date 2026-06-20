package pki

import (
	"math/big"
	"testing"
	"time"
)

// TestBuildCRL_TableDriven 表驱动测试覆盖各种 CRL 构建场景。
func TestBuildCRL_TableDriven(t *testing.T) {
	ca, err := NewCA("crl-test-root")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		serials  []*big.Int
		validFor time.Duration
	}{
		{
			name:     "空吊销列表",
			serials:  nil,
			validFor: time.Hour,
		},
		{
			name:     "单个序列号",
			serials:  []*big.Int{big.NewInt(42)},
			validFor: 24 * time.Hour,
		},
		{
			name:     "多个序列号",
			serials:  []*big.Int{big.NewInt(1), big.NewInt(100), big.NewInt(9999)},
			validFor: 7 * 24 * time.Hour,
		},
		{
			name:     "大序列号",
			serials:  []*big.Int{new(big.Int).SetBytes([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF})},
			validFor: time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crlDER, err := ca.BuildCRL(tt.serials, tt.validFor)
			if err != nil {
				t.Fatalf("BuildCRL 失败: %v", err)
			}
			if len(crlDER) == 0 {
				t.Fatal("CRL DER 不应为空")
			}
			// 验证每个序列号是否在 CRL 中
			for _, s := range tt.serials {
				revoked, err := IsRevoked(crlDER, s)
				if err != nil {
					t.Fatalf("IsRevoked(%s) 失败: %v", s, err)
				}
				if !revoked {
					t.Errorf("序列号 %s 应在 CRL 中被标记为已吊销", s)
				}
			}
			// 不在列表中的序列号不应命中
			absent := big.NewInt(777777)
			revoked, err := IsRevoked(crlDER, absent)
			if err != nil {
				t.Fatalf("IsRevoked(absent) 失败: %v", err)
			}
			if revoked {
				t.Error("不在吊销列表中的序列号不应被标记")
			}
		})
	}
}

// TestIsRevoked_InvalidCRL 验证无效 CRL 数据时返回错误。
func TestIsRevoked_InvalidCRL(t *testing.T) {
	_, err := IsRevoked([]byte("garbage"), big.NewInt(1))
	if err == nil {
		t.Fatal("无效 CRL 应返回错误")
	}
}

// TestIsRevoked_EmptyCRL 空 CRL 中任何序列号都不应被标记。
func TestIsRevoked_EmptyCRL(t *testing.T) {
	ca, _ := NewCA("root")
	crlDER, err := ca.BuildCRL(nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := IsRevoked(crlDER, big.NewInt(12345))
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("空 CRL 中不应有任何已吊销序列号")
	}
}

// TestBuildCRL_WithRealCerts 使用实际签发的证书序列号构建 CRL 并校验。
func TestBuildCRL_WithRealCerts(t *testing.T) {
	ca, _ := NewCA("root")

	// 签发 3 张证书
	var serials []*big.Int
	for i := 0; i < 3; i++ {
		csrDER, _, _ := GenerateCSR("node")
		certDER, err := ca.IssueFromCSR(csrDER, "node", NodeRoleNode, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := parseTestCert(t, certDER)
		serials = append(serials, cert.SerialNumber)
	}

	// 只吊销前两张
	crlDER, err := ca.BuildCRL(serials[:2], time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// 前两张应被吊销
	for i := 0; i < 2; i++ {
		revoked, _ := IsRevoked(crlDER, serials[i])
		if !revoked {
			t.Errorf("证书 %d（序列号 %s）应被吊销", i, serials[i])
		}
	}
	// 第三张不应被吊销
	revoked, _ := IsRevoked(crlDER, serials[2])
	if revoked {
		t.Errorf("证书 2（序列号 %s）不应被吊销", serials[2])
	}
}

// TestBuildCRL_DifferentCA 不同 CA 生成的 CRL 格式均合法。
func TestBuildCRL_DifferentCA(t *testing.T) {
	ca1, _ := NewCA("CA-1")
	ca2, _ := NewCA("CA-2")

	crl1, err := ca1.BuildCRL([]*big.Int{big.NewInt(10)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	crl2, err := ca2.BuildCRL([]*big.Int{big.NewInt(10)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// 两个 CRL 都应该可以被正常解析和查询
	r1, _ := IsRevoked(crl1, big.NewInt(10))
	r2, _ := IsRevoked(crl2, big.NewInt(10))
	if !r1 || !r2 {
		t.Error("两个 CA 的 CRL 都应能标记相同序列号")
	}
}

// TestIsRevoked_ZeroSerial 验证序列号 0 的处理。
func TestIsRevoked_ZeroSerial(t *testing.T) {
	ca, _ := NewCA("root")
	zero := big.NewInt(0)
	crlDER, err := ca.BuildCRL([]*big.Int{zero}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := IsRevoked(crlDER, zero)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("序列号 0 也应被正确标记为已吊销")
	}
}
