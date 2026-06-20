package components

import (
	"strings"
	"testing"
)

// ---------- StatusBar ----------

// TestStatusBar_Connected 验证已连接时包含 "已连接" 关键字。
func TestStatusBar_Connected(t *testing.T) {
	out := StatusBar(true, "帮助", 80)
	if !strings.Contains(out, "已连接") {
		t.Fatalf("已连接状态应包含 '已连接'，got: %q", out)
	}
}

// TestStatusBar_Disconnected 验证未连接时包含 "未连接" 关键字。
func TestStatusBar_Disconnected(t *testing.T) {
	out := StatusBar(false, "帮助", 80)
	if !strings.Contains(out, "未连接") {
		t.Fatalf("未连接状态应包含 '未连接'，got: %q", out)
	}
}

// TestStatusBar_HelpText 验证帮助文本出现在输出中。
func TestStatusBar_HelpText(t *testing.T) {
	help := "F10:退出"
	out := StatusBar(true, help, 80)
	if !strings.Contains(out, help) {
		t.Fatalf("输出应包含帮助文本 %q，got: %q", help, out)
	}
}

// TestStatusBar_ZeroWidth 验证宽度为 0 不崩溃。
func TestStatusBar_ZeroWidth(t *testing.T) {
	out := StatusBar(true, "帮助", 0)
	if out == "" {
		t.Fatalf("宽度为 0 时不应返回空字符串")
	}
}

// ---------- TabBar ----------

// TestTabBar_AllNamesPresent 验证所有 Tab 名称出现在输出中。
func TestTabBar_AllNamesPresent(t *testing.T) {
	names := []string{"仪表盘", "节点", "配置"}
	out := TabBar(names, 0, 80)
	for _, name := range names {
		if !strings.Contains(out, name) {
			t.Fatalf("TabBar 输出应包含 %q，got: %q", name, out)
		}
	}
}

// TestTabBar_EmptyNames 验证空 names 不崩溃。
func TestTabBar_EmptyNames(t *testing.T) {
	out := TabBar(nil, 0, 80)
	// 不崩溃即可
	_ = out
}

// TestTabBar_ActiveIndex 验证各 active 索引不崩溃且输出非空。
func TestTabBar_ActiveIndex(t *testing.T) {
	names := []string{"A", "B", "C"}
	for i := range names {
		out := TabBar(names, i, 80)
		if out == "" {
			t.Fatalf("active=%d 时不应返回空字符串", i)
		}
	}
}

// TestTabBar_ZeroWidth 验证宽度为 0 不崩溃。
func TestTabBar_ZeroWidth(t *testing.T) {
	out := TabBar([]string{"A"}, 0, 0)
	_ = out
}
