package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── TestInstall_UnitContent ──────────────────────────────────────────────────

func TestInstall_UnitContent_Controller(t *testing.T) {
	content := UnitContent(
		"/usr/local/bin/corelink-controller",
		"/etc/corelink-controller.json",
		"/var/lib/corelink-controller",
		"Controller",
	)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/corelink-controller serve -config /etc/corelink-controller.json",
		"Restart=always",
		"RestartSec=5",
		"WorkingDirectory=/var/lib/corelink-controller",
		"LimitNOFILE=65535",
		"After=network-online.target",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("unit content 缺少 %q", want)
		}
	}
}

func TestInstall_UnitContent_Node(t *testing.T) {
	content := UnitContent(
		"/usr/local/bin/corelink-node",
		"/etc/corelink-node.json",
		"/var/lib/corelink",
		"Node",
		"AmbientCapabilities=CAP_NET_ADMIN",
	)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/corelink-node serve -config /etc/corelink-node.json",
		"WorkingDirectory=/var/lib/corelink",
		"AmbientCapabilities=CAP_NET_ADMIN",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("unit content 缺少 %q", want)
		}
	}
	// Node 版必须含 CAP_NET_ADMIN
	if !strings.Contains(content, "CAP_NET_ADMIN") {
		t.Error("Node unit 缺少 AmbientCapabilities=CAP_NET_ADMIN")
	}
}

// ── TestInstall_CheckRoot ────────────────────────────────────────────────────

func TestInstall_CheckRoot_NonRoot(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 1000 } // 非 root

	err := Run(cfg)
	if err == nil {
		t.Fatal("非 root 应返回错误")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("错误消息应提示 sudo，got: %v", err)
	}
}

func TestInstall_CheckRoot_Root(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 "config file" 使其跳过向导
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	err := Run(cfg)
	if err != nil {
		t.Fatalf("root 用户不应失败: %v", err)
	}
}

// ── TestInstall_CopyBinary ───────────────────────────────────────────────────

func TestInstall_CopyBinary(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 config file
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// 创建 "源二进制"
	srcBin := filepath.Join(t.TempDir(), "src-bin")
	payload := []byte("binary-content-12345")
	if err := os.WriteFile(srcBin, payload, 0755); err != nil {
		t.Fatal(err)
	}
	cfg.Executable = func() (string, error) { return srcBin, nil }

	err := Run(cfg)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	// 检查目标文件已复制
	got, err := os.ReadFile(cfg.BinaryDest)
	if err != nil {
		t.Fatalf("目标二进制不存在: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("目标二进制内容不匹配: got %q, want %q", got, payload)
	}
}

func TestInstall_CopyBinary_AlreadyInPlace(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 config file
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// Executable 返回与 BinaryDest 相同的路径 → 跳过复制
	cfg.Executable = func() (string, error) { return cfg.BinaryDest, nil }

	var output strings.Builder
	cfg.Printf = func(format string, a ...any) {
		fmt.Fprintf(&output, format, a...)
	}

	err := Run(cfg)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(output.String(), "跳过") {
		t.Error("已在目标路径时应输出跳过提示")
	}
}

// ── TestInstall_ConfigExists ─────────────────────────────────────────────────

func TestInstall_ConfigExists(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 config file
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	configFnCalled := false
	cfg.ConfigFn = func(_ []string) error {
		configFnCalled = true
		return nil
	}

	err := Run(cfg)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if configFnCalled {
		t.Error("配置文件已存在时 ConfigFn 不应被调用")
	}
}

// ── TestInstall_ConfigMissing ────────────────────────────────────────────────

func TestInstall_ConfigMissing(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 不创建 config file → ConfigFn 应被调用

	configFnCalled := false
	var configFnArgs []string
	cfg.ConfigFn = func(args []string) error {
		configFnCalled = true
		configFnArgs = args
		// 模拟向导创建配置文件
		return os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600)
	}

	err := Run(cfg)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !configFnCalled {
		t.Error("配置文件不存在时 ConfigFn 应被调用")
	}
	// 检查传入参数
	if len(configFnArgs) != 2 || configFnArgs[0] != "--output" || configFnArgs[1] != cfg.ConfigPath {
		t.Errorf("ConfigFn 参数不正确: %v", configFnArgs)
	}
}

// ── TestInstall_SystemctlCalls ───────────────────────────────────────────────

func TestInstall_SystemctlCalls(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 config file
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	type call struct {
		name string
		args []string
	}
	var mu sync.Mutex
	var calls []call
	cfg.ExecCmd = func(name string, args ...string) error {
		mu.Lock()
		calls = append(calls, call{name: name, args: args})
		mu.Unlock()
		return nil
	}

	err := Run(cfg)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// 预期调用顺序：daemon-reload → enable → start → is-active（至少一次）
	if len(calls) < 4 {
		t.Fatalf("systemctl 调用次数不足: got %d, want >= 4", len(calls))
	}

	wantSequence := []struct {
		name string
		args []string
	}{
		{"systemctl", []string{"daemon-reload"}},
		{"systemctl", []string{"enable", cfg.ServiceName}},
		{"systemctl", []string{"start", cfg.ServiceName}},
		{"systemctl", []string{"is-active", cfg.ServiceName}},
	}

	for i, want := range wantSequence {
		if i >= len(calls) {
			t.Fatalf("缺少调用 #%d: %s %v", i, want.name, want.args)
		}
		got := calls[i]
		if got.name != want.name {
			t.Errorf("调用 #%d: 命令 got %q, want %q", i, got.name, want.name)
		}
		if len(got.args) != len(want.args) {
			t.Errorf("调用 #%d: 参数数量 got %d, want %d", i, len(got.args), len(want.args))
			continue
		}
		for j := range want.args {
			if got.args[j] != want.args[j] {
				t.Errorf("调用 #%d 参数 #%d: got %q, want %q", i, j, got.args[j], want.args[j])
			}
		}
	}
}

func TestInstall_SystemctlCalls_StartupTimeout(t *testing.T) {
	cfg := testConfig(t)
	cfg.GetUID = func() int { return 0 }

	// 创建 config file
	if err := os.WriteFile(cfg.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg.ExecCmd = func(name string, args ...string) error {
		if len(args) > 0 && args[0] == "is-active" {
			return fmt.Errorf("inactive")
		}
		return nil
	}
	cfg.Sleep = func(_ time.Duration) {} // 不真正 sleep

	err := Run(cfg)
	if err == nil {
		t.Fatal("is-active 始终失败时应返回超时错误")
	}
	if !strings.Contains(err.Error(), "启动超时") {
		t.Errorf("错误消息应含 '启动超时'，got: %v", err)
	}
}

// ── 测试辅助 ─────────────────────────────────────────────────────────────────

// testConfig 返回一个完全 mock 化的 InstallConfig，所有路径指向临时目录。
func testConfig(t *testing.T) InstallConfig {
	t.Helper()
	tmp := t.TempDir()

	binaryDest := filepath.Join(tmp, "bin", "test-binary")
	configPath := filepath.Join(tmp, "etc", "test.json")
	dataDir := filepath.Join(tmp, "data")
	unitDir := filepath.Join(tmp, "systemd")

	// 预创建必要的父目录
	for _, d := range []string{filepath.Dir(binaryDest), filepath.Dir(configPath), unitDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// 创建一个假的源二进制
	srcBin := filepath.Join(tmp, "src-bin")
	if err := os.WriteFile(srcBin, []byte("fake-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	return InstallConfig{
		BinaryName:  "test-binary",
		BinaryDest:  binaryDest,
		DataDir:     dataDir,
		ConfigPath:  configPath,
		ServiceName: "test-service",
		UnitContent: "[Unit]\nDescription=Test\n",
		ConfigFn:    nil,
		ExecCmd:     func(string, ...string) error { return nil },
		GetUID:      func() int { return 1000 }, // 默认非 root
		Executable:  func() (string, error) { return srcBin, nil },
		MkdirAll:    os.MkdirAll,
		WriteFile: func(name string, data []byte, perm os.FileMode) error {
			// 将 systemd unit 写入临时目录（避免写真实 /etc）
			if strings.HasPrefix(name, "/etc/systemd/") {
				name = filepath.Join(unitDir, filepath.Base(name))
			}
			return os.WriteFile(name, data, perm)
		},
		CopyFile: defaultCopyFile,
		Stat:     os.Stat,
		Sleep:    func(_ time.Duration) {},
		Printf:   func(string, ...any) {},
	}
}
