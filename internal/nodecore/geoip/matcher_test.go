package geoip

import (
	"net/netip"
	"testing"
)

func TestMatcherLookupCIDRs(t *testing.T) {
	m, err := LoadBytes(BuildTestDat(t))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	cn := m.LookupCIDRs("cn")
	if len(cn) != 1 || cn[0].String() != "1.0.0.0/8" {
		t.Fatalf("期望 cn=[1.0.0.0/8], got %v", cn)
	}

	// 测试大小写不敏感
	cnUpper := m.LookupCIDRs("CN")
	if len(cnUpper) != 1 || cnUpper[0].String() != "1.0.0.0/8" {
		t.Fatalf("期望 CN=[1.0.0.0/8], got %v", cnUpper)
	}

	unknown := m.LookupCIDRs("zz")
	if len(unknown) != 0 {
		t.Fatalf("期望 zz=空, got %v", unknown)
	}
}

func TestMatcherLookupIP(t *testing.T) {
	m, err := LoadBytes(BuildTestDat(t))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	tests := []struct {
		ip   string
		want string
	}{
		{"1.2.3.4", "cn"},
		{"8.8.8.8", "us"},
		{"2.2.2.2", ""},
	}
	for _, tt := range tests {
		got := m.LookupIP(netip.MustParseAddr(tt.ip))
		if got != tt.want {
			t.Errorf("LookupIP(%s) = %q, want %q", tt.ip, got, tt.want)
		}
	}
}

func TestMatcherCodes(t *testing.T) {
	m, err := LoadBytes(BuildTestDat(t))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	codes := m.Codes()
	if len(codes) != 2 {
		t.Fatalf("期望 2 个国家代码, got %d: %v", len(codes), codes)
	}
	// codes 应该已排序
	if codes[0] != "cn" || codes[1] != "us" {
		t.Fatalf("期望 [cn, us], got %v", codes)
	}
}

func TestParseEmptyData(t *testing.T) {
	m, err := LoadBytes([]byte{})
	if err != nil {
		t.Fatalf("解析空数据不应报错: %v", err)
	}
	if len(m.Codes()) != 0 {
		t.Fatalf("空数据应返回 0 个国家代码, got %d", len(m.Codes()))
	}
}
