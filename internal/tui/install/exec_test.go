package install

import (
	"testing"
)

// ── exec.go 单元测试 ────────────────────────────────────────────────────────

func TestDefaultExecCmd_NonExistentCommand(t *testing.T) {
	// 执行不存在的命令应返回错误
	err := defaultExecCmd("__nonexistent_command_12345__")
	if err == nil {
		t.Fatal("执行不存在的命令应返回错误")
	}
}

func TestDefaultExecCmd_TrueCommand(t *testing.T) {
	// true 命令应成功执行
	err := defaultExecCmd("true")
	if err != nil {
		t.Fatalf("执行 true 命令不应报错: %v", err)
	}
}

func TestDefaultExecCmd_FalseCommand(t *testing.T) {
	// false 命令应返回非零退出码错误
	err := defaultExecCmd("false")
	if err == nil {
		t.Fatal("执行 false 命令应返回错误")
	}
}

func TestDefaultExecCmd_WithArgs(t *testing.T) {
	// 带参数的命令
	err := defaultExecCmd("echo", "hello", "world")
	if err != nil {
		t.Fatalf("echo 命令不应报错: %v", err)
	}
}
