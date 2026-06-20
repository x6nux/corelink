package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/config"
)

func TestDefaultValues(t *testing.T) {
	// Load 一个含所有必填字段的配置，验证默认值被填充
	cfg := writeAndLoad(t, map[string]any{
		"controller_enroll_addr": "ctrl:7443",
		"controller_mtls_addr":   "ctrl:7444",
		"controller_http_addr":   "ctrl:8080",
		"enrollment_key":         "key123",
		"controller_ca_hash":     "sha256:aabbccdd",
	})
	// 根据当前平台判断期望的默认值
	var wantDataDir, wantTUNName string
	switch runtime.GOOS {
	case "darwin":
		wantDataDir = "/Library/Application Support/CoreLink"
		wantTUNName = "utun"
	case "windows":
		wantDataDir = filepath.Join(os.Getenv("ProgramData"), "CoreLink")
		wantTUNName = "CoreLink"
	default:
		wantDataDir = "/var/lib/corelink"
		wantTUNName = "corelink%d"
	}
	if cfg.DataDir != wantDataDir {
		t.Errorf("默认 DataDir 错误: got %q, want %q", cfg.DataDir, wantDataDir)
	}
	if cfg.TUNName != wantTUNName {
		t.Errorf("默认 TUNName 错误: got %q, want %q", cfg.TUNName, wantTUNName)
	}
	if cfg.Role != config.RoleNode {
		t.Errorf("默认 Role 错误: got %q", cfg.Role)
	}
}

func TestLoadFields(t *testing.T) {
	cfg := writeAndLoad(t, map[string]any{
		"controller_enroll_addr": "ctrl:7443",
		"controller_mtls_addr":   "ctrl:7444",
		"controller_http_addr":   "ctrl:7080",
		"enrollment_key":         "k1",
		"controller_ca_hash":     "sha256:aabbccdd",
		"data_dir":               "/tmp/node",
		"role":                   "node",
		"hostname":               "node1",
	})
	if cfg.ControllerEnrollAddr != "ctrl:7443" {
		t.Error("ControllerEnrollAddr 错误")
	}
	if cfg.ControllerMTLSAddr != "ctrl:7444" {
		t.Error("ControllerMTLSAddr 错误")
	}
	if cfg.ControllerHTTPAddr != "ctrl:7080" {
		t.Error("ControllerHTTPAddr 错误")
	}
	if cfg.EnrollmentKey != "k1" {
		t.Error("EnrollmentKey 错误")
	}
	if cfg.ControllerCAHash != "sha256:aabbccdd" {
		t.Error("ControllerCAHash 错误")
	}
	if cfg.DataDir != "/tmp/node" {
		t.Error("DataDir 错误")
	}
	if cfg.Role != config.RoleNode {
		t.Errorf("Role 错误: got %q", cfg.Role)
	}
	if cfg.Hostname != "node1" {
		t.Error("Hostname 错误")
	}
}

func TestRoleValidation(t *testing.T) {
	cases := []struct {
		role    string
		wantErr bool
	}{
		{"node", false},
		{"", false},
		{"anything", false},
	}
	for _, tc := range cases {
		cfg := &config.Config{
			ControllerEnrollAddr: "ctrl:7443",
			ControllerMTLSAddr:   "ctrl:7444",
			ControllerHTTPAddr:   "ctrl:8080",
			EnrollmentKey:        "k",
			ControllerCAHash:     "sha256:aabb",
			Role:                 config.Role(tc.role),
			DataDir:              "/tmp/x",
		}
		err := cfg.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("role=%q: wantErr=%v got %v", tc.role, tc.wantErr, err)
		}
	}
}

func TestControllerCAHashRequired(t *testing.T) {
	cases := []struct {
		name    string
		caHash  string
		wantErr bool
	}{
		{"非空通过", "sha256:aabb", false},
		{"空值拒绝", "", true},
	}
	for _, tc := range cases {
		cfg := &config.Config{
			ControllerEnrollAddr: "ctrl:7443",
			ControllerMTLSAddr:   "ctrl:7444",
			ControllerHTTPAddr:   "ctrl:8080",
			EnrollmentKey:        "k",
			ControllerCAHash:     tc.caHash,
			Role:                 config.RoleNode,
			DataDir:              "/tmp/x",
		}
		err := cfg.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: wantErr=%v got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("期望加载不存在文件时报错")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("期望 JSON 解析失败时报错")
	}
}

// writeAndLoad 是辅助函数：将 map 序列化为 JSON 写临时文件，调用 Load 后返回 Config。
func writeAndLoad(t *testing.T, m map[string]any) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	return cfg
}
