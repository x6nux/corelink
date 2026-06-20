package components

import (
	"strings"
	"testing"
)

// TestStatusBar_NegativeGap 验证帮助文本超长时不会产生负数填充
func TestStatusBar_NegativeGap(t *testing.T) {
	longHelp := strings.Repeat("帮助信息", 50)
	out := StatusBar(true, longHelp, 40)
	if out == "" {
		t.Fatal("帮助文本超过宽度时不应返回空字符串")
	}
	if !strings.Contains(out, "已连接") {
		t.Error("输出应仍包含连接状态")
	}
}

// TestStatusBar_ConnectedDisconnectedToggle 验证切换连接状态输出不同
func TestStatusBar_ConnectedDisconnectedToggle(t *testing.T) {
	connected := StatusBar(true, "help", 80)
	disconnected := StatusBar(false, "help", 80)
	if connected == disconnected {
		t.Error("已连接和未连接输出不应完全相同")
	}
}

// TestStatusBar_EmptyHelp 验证空帮助文本不崩溃
func TestStatusBar_EmptyHelp(t *testing.T) {
	out := StatusBar(true, "", 80)
	if !strings.Contains(out, "已连接") {
		t.Error("空帮助文本时仍应包含连接状态")
	}
}

// TestStatusBar_LargeWidth 验证大宽度时正常渲染
func TestStatusBar_LargeWidth(t *testing.T) {
	out := StatusBar(false, "F1:帮助", 500)
	if !strings.Contains(out, "未连接") {
		t.Error("大宽度时仍应包含连接状态")
	}
	if !strings.Contains(out, "F1:帮助") {
		t.Error("大宽度时仍应包含帮助文本")
	}
}
