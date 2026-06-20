package firewall

import (
	"strings"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestGenerateCleanup(t *testing.T) {
	cmds := GenerateCleanup()
	if len(cmds) == 0 {
		t.Fatal("cleanup 应生成命令")
	}
	for _, cmd := range cmds {
		if !strings.Contains(cmd.String(), ChainPrefix) {
			t.Errorf("cleanup 命令应只影响 CoreLink 链: %s", cmd)
		}
	}
}

func TestGenerateDNSRedirectLocal(t *testing.T) {
	cfg := &genv1.DNSConfig{
		Enabled:       true,
		InterceptMode: "local",
		ListenAddr:    "127.0.0.1",
		ListenPort:    5353,
	}
	cmds := GenerateDNSRedirect(cfg)
	// 2 PREROUTING + 0 upstream RETURN + 1 loopback RETURN + 2 OUTPUT = 5
	if len(cmds) < 4 {
		t.Fatalf("local 模式应生成至少 4 条规则，got %d", len(cmds))
	}
	hasPrerouting, hasOutput := false, false
	for _, cmd := range cmds {
		s := cmd.String()
		if strings.Contains(s, DNSChain) && strings.Contains(s, "REDIRECT") {
			hasPrerouting = true
		}
		if strings.Contains(s, DNSOutChain) && strings.Contains(s, "REDIRECT") {
			hasOutput = true
		}
	}
	if !hasPrerouting {
		t.Error("应包含 PREROUTING REDIRECT")
	}
	if !hasOutput {
		t.Error("应包含 OUTPUT REDIRECT")
	}
}

func TestGenerateDNSRedirectLAN(t *testing.T) {
	cfg := &genv1.DNSConfig{
		Enabled:       true,
		InterceptMode: "lan",
		ListenAddr:    "10.0.0.1",
		ListenPort:    53,
		LanInterfaces: []string{"eth0"},
		LanCidrs:      []string{"192.168.1.0/24"},
	}
	cmds := GenerateDNSRedirect(cfg)
	if len(cmds) != 2 {
		t.Fatalf("lan 模式应生成 2 条规则（iface+cidr），got %d", len(cmds))
	}
	found := strings.Join([]string{cmds[0].String(), cmds[1].String()}, "\n")
	if !strings.Contains(found, "eth0") {
		t.Error("应包含 eth0 接口")
	}
	if !strings.Contains(found, "192.168.1.0/24") {
		t.Error("应包含 LAN CIDR")
	}
}

func TestGenerateDNSRedirectOff(t *testing.T) {
	cfg := &genv1.DNSConfig{
		Enabled:       true,
		InterceptMode: "off",
	}
	cmds := GenerateDNSRedirect(cfg)
	if len(cmds) != 0 {
		t.Fatalf("off 模式不应生成规则，got %d", len(cmds))
	}
}

func TestGenerateEgressRules(t *testing.T) {
	rules := []*genv1.EgressRule{
		{Kind: "direct", VipPrefix: "10.0.0.0/16", TargetPrefix: "10.0.0.0/16", Snat: true},
		{Kind: "static_mapping", VipPrefix: "100.64.2.0/24", TargetPrefix: "10.0.2.0/24", Snat: true},
		{Kind: "discovered_mapping", VipPrefix: "100.64.0.8/32", TargetPrefix: "10.1.0.8/32", Snat: false},
	}
	cmds := GenerateEgressRules(rules)
	if len(cmds) == 0 {
		t.Fatal("应生成出口规则")
	}

	// 验证包含 CoreLink 链名
	for _, cmd := range cmds {
		s := cmd.String()
		if !strings.Contains(s, ChainPrefix) {
			t.Errorf("应使用 CoreLink 链: %s", s)
		}
	}

	// 验证 direct 包含 MASQUERADE
	var hasMasq bool
	for _, cmd := range cmds {
		if strings.Contains(cmd.String(), "MASQUERADE") && strings.Contains(cmd.String(), "10.0.0.0/16") {
			hasMasq = true
		}
	}
	if !hasMasq {
		t.Error("direct+SNAT 应包含 MASQUERADE")
	}

	// 验证 discovered_mapping 无 SNAT 时不生成 -s MASQUERADE（源 NAT），
	// 但 -d MASQUERADE（conntrack 回程）仍需存在。
	for _, cmd := range cmds {
		s := cmd.String()
		if strings.Contains(s, "-s 10.1.0.8/32") && strings.Contains(s, "MASQUERADE") {
			t.Error("snat=false 时不应生成 -s MASQUERADE")
		}
	}
}

func TestGenerateDNSRedirectNilConfig(t *testing.T) {
	cmds := GenerateDNSRedirect(nil)
	if cmds != nil {
		t.Fatalf("nil config 应返回 nil，got %v", cmds)
	}
}

func TestGenerateEgressRulesEmpty(t *testing.T) {
	cmds := GenerateEgressRules([]*genv1.EgressRule{})
	if len(cmds) != 0 {
		t.Fatalf("空 rules 应返回空，got %d 条", len(cmds))
	}
}

func TestGenerateDNSRedirectLANNoInterfaces(t *testing.T) {
	cfg := &genv1.DNSConfig{
		Enabled:       true,
		InterceptMode: "lan",
		ListenAddr:    "10.0.0.1",
		ListenPort:    53,
	}
	cmds := GenerateDNSRedirect(cfg)
	if len(cmds) != 0 {
		t.Fatalf("lan 模式无接口和 CIDR 应生成 0 条规则，got %d", len(cmds))
	}
}
