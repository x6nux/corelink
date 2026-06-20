package routepolicy

import (
	"strings"
	"testing"
)

func TestValidateAlias(t *testing.T) {
	tests := []struct {
		name, fqdn, kind string
		wantErr          string
	}{
		{"db", "db.corelink.internal", "internal", ""},
		{"web", "web.example.com", "external", ""},
		{"", "db.corelink.internal", "internal", "name 不能为空"},
		{"db", "", "internal", "fqdn 不能为空"},
		{"db", "db.corelink.internal", "unknown", "kind 必须是"},
		{"db", "invalid", "internal", "至少需要两个标签"},
		{"db", "-bad.example.com", "internal", "不能以连字符开头"},
		{"db", strings.Repeat("a", 64) + ".com", "internal", "标签超过 63 字符"},
	}
	for _, tt := range tests {
		err := ValidateAlias(tt.name, tt.fqdn, tt.kind)
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("ValidateAlias(%q, %q, %q) = %v, want nil", tt.name, tt.fqdn, tt.kind, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateAlias(%q, %q, %q) = %v, want 包含 %q", tt.name, tt.fqdn, tt.kind, err, tt.wantErr)
			}
		}
	}
}

func TestValidatePublishedRoute(t *testing.T) {
	tests := []struct {
		kind, route, vip, target string
		wantErr                  string
	}{
		{"direct", "10.0.0.0/16", "", "", ""},
		{"direct", "", "", "", "必须指定 route_cidr"},
		{"direct", "invalid", "", "", "route_cidr 无效"},
		{"static_mapping", "", "100.64.2.0/24", "10.0.2.0/24", ""},
		{"static_mapping", "", "100.64.2.0/24", "", "必须指定 vip_cidr 和 target_cidr"},
		{"static_mapping", "", "100.64.2.0/24", "10.0.2.0/25", "前缀长度不一致"},
		{"static_mapping", "", "100.64.2.0/24", "fe80::/24", "地址族不一致"},
		{"discovered_mapping", "", "100.64.0.0/16", "10.1.0.0/16", ""},
		{"discovered_mapping", "", "", "10.1.0.0/16", "必须指定 vip_cidr 和 target_cidr"},
		{"unknown_kind", "", "", "", "未知 route kind"},
	}
	for _, tt := range tests {
		err := ValidatePublishedRoute(tt.kind, tt.route, tt.vip, tt.target)
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tt.kind, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate(%q) = %v, want 包含 %q", tt.kind, err, tt.wantErr)
			}
		}
	}
}

func TestCheckStaticMappingOverlap(t *testing.T) {
	if err := CheckStaticMappingOverlap("100.64.2.0/24", nil, ""); err != nil {
		t.Fatalf("无冲突时不应报错: %v", err)
	}

	if err := CheckStaticMappingOverlap("100.64.2.0/24", nil, "100.64.0.0/16"); err == nil {
		t.Fatal("与 VIP 池重叠应报错")
	}

	if err := CheckStaticMappingOverlap("100.64.2.0/24", []string{"100.64.2.128/25"}, ""); err == nil {
		t.Fatal("与现有路由重叠应报错")
	}

	if err := CheckStaticMappingOverlap("100.64.3.0/24", []string{"100.64.2.0/24"}, ""); err != nil {
		t.Fatalf("不重叠时不应报错: %v", err)
	}
}

func TestCheckDirectRouteOverlap(t *testing.T) {
	if err := CheckDirectRouteOverlap("10.0.0.0/16", nil); err != nil {
		t.Fatalf("无冲突: %v", err)
	}

	if err := CheckDirectRouteOverlap("10.0.0.0/16", []string{"10.0.1.0/24"}); err == nil {
		t.Fatal("重叠应报错")
	}

	if err := CheckDirectRouteOverlap("10.0.0.0/16", []string{"10.1.0.0/16"}); err != nil {
		t.Fatalf("不重叠: %v", err)
	}
}
