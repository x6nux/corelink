package ingress

import (
	"net"
	"strings"
	"testing"
)

// TestDefaultStunServers_非空 验证列表不为空。
func TestDefaultStunServers_非空(t *testing.T) {
	if len(DefaultStunServers) == 0 {
		t.Fatal("DefaultStunServers 不应为空")
	}
}

// TestDefaultStunServers_格式合法 验证每个条目都是合法的 host:port 格式。
func TestDefaultStunServers_格式合法(t *testing.T) {
	for i, s := range DefaultStunServers {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			t.Errorf("[%d] %q 不是合法的 host:port: %v", i, s, err)
			continue
		}
		if host == "" {
			t.Errorf("[%d] %q 主机名为空", i, s)
		}
		if port == "" {
			t.Errorf("[%d] %q 端口为空", i, s)
		}
	}
}

// TestDefaultStunServers_无重复 验证列表中无重复条目。
func TestDefaultStunServers_无重复(t *testing.T) {
	seen := make(map[string]bool, len(DefaultStunServers))
	for i, s := range DefaultStunServers {
		if seen[s] {
			t.Errorf("[%d] %q 重复出现", i, s)
		}
		seen[s] = true
	}
}

// TestDefaultStunServers_多数使用标准端口 验证大部分服务器使用 STUN 标准端口 3478。
func TestDefaultStunServers_多数使用标准端口(t *testing.T) {
	standardCount := 0
	for _, s := range DefaultStunServers {
		if strings.HasSuffix(s, ":3478") {
			standardCount++
		}
	}
	// 至少 80% 应使用标准端口
	threshold := len(DefaultStunServers) * 80 / 100
	if standardCount < threshold {
		t.Errorf("仅 %d/%d 服务器使用标准端口 3478，期望至少 %d",
			standardCount, len(DefaultStunServers), threshold)
	}
}

// TestDefaultStunServers_足够随机采样 验证列表长度足够大（至少 10 个）以支持随机采样。
func TestDefaultStunServers_足够随机采样(t *testing.T) {
	if len(DefaultStunServers) < 10 {
		t.Fatalf("DefaultStunServers 长度 %d 不够大（需至少 10 个以支持随机采样）",
			len(DefaultStunServers))
	}
}

// TestDefaultStunServers_主机名含stun前缀 验证每个条目的主机名以 "stun." 开头
// （符合 STUN 服务器命名惯例）。
func TestDefaultStunServers_主机名含stun前缀(t *testing.T) {
	for i, s := range DefaultStunServers {
		host, _, _ := net.SplitHostPort(s)
		if !strings.HasPrefix(host, "stun.") {
			t.Errorf("[%d] %q 主机名不以 stun. 开头", i, s)
		}
	}
}
