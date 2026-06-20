package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── formatDuration 单元测试 ─────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"零", 0, "0m"},
		{"仅分钟", 5 * time.Minute, "5m"},
		{"小时和分钟", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"天数", 25*time.Hour + 15*time.Minute, "1d1h15m"},
		{"多天", 72*time.Hour + 45*time.Minute, "3d0h45m"},
		{"刚好一天", 24 * time.Hour, "1d0h0m"},
		{"59分钟", 59 * time.Minute, "59m"},
		{"1小时整", 1 * time.Hour, "1h0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ── DoctorCheck 单元测试 ────────────────────────────────────────────────────

func TestCommonDoctorChecks_ConfigExists(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")

	// 配置文件不存在时检查应失败
	checks := CommonDoctorChecks(configPath, "/nonexistent.sock", "")
	configCheck := checks[0]
	if configCheck.Name != "配置文件" {
		t.Fatalf("第一个检查项应为 '配置文件'，实际: %q", configCheck.Name)
	}
	ok, detail := configCheck.Check()
	if ok {
		t.Fatal("配置文件不存在时应返回 false")
	}
	if detail == "" {
		t.Fatal("失败时 detail 不应为空")
	}

	// 创建配置文件后检查应通过
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	ok, detail = configCheck.Check()
	if !ok {
		t.Fatalf("配置文件存在时应返回 true，detail: %s", detail)
	}
	if detail != configPath {
		t.Fatalf("成功时 detail 应为路径 %q，实际: %q", configPath, detail)
	}
}

func TestCommonDoctorChecks_DaemonUnavailable(t *testing.T) {
	checks := CommonDoctorChecks("/tmp/nonexist.json", "/nonexistent.sock", "")
	if len(checks) < 2 {
		t.Fatalf("至少应有 2 个检查项，实际: %d", len(checks))
	}
	daemonCheck := checks[1]
	if daemonCheck.Name != "守护进程" {
		t.Fatalf("第二个检查项应为 '守护进程'，实际: %q", daemonCheck.Name)
	}
	ok, _ := daemonCheck.Check()
	if ok {
		t.Fatal("无效 sock 路径时守护进程检查应返回 false")
	}
}

func TestCommonDoctorChecks_WithControllerAddr(t *testing.T) {
	// 带 controller 地址时应有 3 个检查项
	checks := CommonDoctorChecks("/tmp/c.json", "/tmp/c.sock", "127.0.0.1:7443")
	if len(checks) != 3 {
		t.Fatalf("带 controller 地址时应有 3 个检查项，实际: %d", len(checks))
	}
	ctrlCheck := checks[2]
	if ctrlCheck.Name != "Controller 可达" {
		t.Fatalf("第三个检查项应为 'Controller 可达'，实际: %q", ctrlCheck.Name)
	}
}

func TestCommonDoctorChecks_ControllerDefaultPort(t *testing.T) {
	// 无端口时应自动补 7443，使用回环地址 + 不太可能被占用的端口确保不可达
	checks := CommonDoctorChecks("/tmp/c.json", "/tmp/c.sock", "127.0.0.1:19999")
	if len(checks) != 3 {
		t.Fatalf("应有 3 个检查项，实际: %d", len(checks))
	}
	// 检查连接（预期失败，因为是无效地址，但不应 panic）
	ok, detail := checks[2].Check()
	if ok {
		t.Fatal("连接不到的地址应返回 false")
	}
	if detail == "" {
		t.Fatal("失败时 detail 不应为空")
	}
}

func TestCommonDoctorChecks_NoControllerAddr(t *testing.T) {
	// 无 controller 地址时不应有 controller 可达检查
	checks := CommonDoctorChecks("/tmp/c.json", "/tmp/c.sock", "")
	if len(checks) != 2 {
		t.Fatalf("无 controller 地址时应只有 2 个检查项，实际: %d", len(checks))
	}
}

// ── PrintInfo 单元测试 ──────────────────────────────────────────────────────

func TestPrintInfo_NonExistentDataDir(t *testing.T) {
	// 不存在的数据目录不应 panic
	PrintInfo("test-binary", "/nonexistent/dir/for/test")
}

func TestPrintInfo_WithIdentityFile(t *testing.T) {
	tmp := t.TempDir()
	idPath := filepath.Join(tmp, "identity.json")
	info := map[string]string{"node_id": "node-test-123"}
	data, _ := json.Marshal(info)
	if err := os.WriteFile(idPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	// 不应 panic
	PrintInfo("test-binary", tmp)
}

func TestPrintInfo_BadIdentityJSON(t *testing.T) {
	tmp := t.TempDir()
	idPath := filepath.Join(tmp, "identity.json")
	if err := os.WriteFile(idPath, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}

	// 不应 panic
	PrintInfo("test-binary", tmp)
}

// ── UnitContent 单元测试（补充 commands.go 的）──────────────────────────────

func TestUnitContent_ContainsBasicFields(t *testing.T) {
	content := UnitContent(
		"/usr/local/bin/test",
		"/etc/test.json",
		"/var/lib/test",
		"TestService",
	)

	wants := []string{
		"ExecStart=/usr/local/bin/test serve -config /etc/test.json",
		"WorkingDirectory=/var/lib/test",
		"Description=CoreLink TestService",
		"Restart=always",
		"WantedBy=multi-user.target",
	}
	for _, want := range wants {
		found := false
		for i := 0; i <= len(content)-len(want); i++ {
			if content[i:i+len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("UnitContent 应含 %q", want)
		}
	}
}
