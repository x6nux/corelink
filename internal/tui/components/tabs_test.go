package components

import (
	"strings"
	"testing"
)

// TestTabBar_SingleTab 验证单个 tab 正确渲染
func TestTabBar_SingleTab(t *testing.T) {
	out := TabBar([]string{"唯一标签"}, 0, 80)
	if !strings.Contains(out, "唯一标签") {
		t.Fatalf("输出应包含标签名，got: %q", out)
	}
}

// TestTabBar_OutOfBoundsActive 验证 active 超出范围时不崩溃
func TestTabBar_OutOfBoundsActive(t *testing.T) {
	names := []string{"A", "B"}
	// active=5 超出范围，不应 panic
	out := TabBar(names, 5, 80)
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Fatalf("超出范围的 active 索引时仍应包含所有标签，got: %q", out)
	}
}

// TestTabBar_NegativeActive 验证负数 active 不崩溃
func TestTabBar_NegativeActive(t *testing.T) {
	out := TabBar([]string{"X", "Y"}, -1, 80)
	if !strings.Contains(out, "X") || !strings.Contains(out, "Y") {
		t.Fatalf("负数 active 时仍应包含所有标签，got: %q", out)
	}
}

// TestTabBar_LargeWidth 验证大宽度不崩溃
func TestTabBar_LargeWidth(t *testing.T) {
	out := TabBar([]string{"Tab1", "Tab2", "Tab3"}, 1, 1000)
	for _, name := range []string{"Tab1", "Tab2", "Tab3"} {
		if !strings.Contains(out, name) {
			t.Fatalf("大宽度时应包含 %q，got: %q", name, out)
		}
	}
}

// TestTabBar_ManyTabs 验证多标签不崩溃且全部出现
func TestTabBar_ManyTabs(t *testing.T) {
	names := make([]string, 20)
	for i := range names {
		names[i] = string(rune('A' + i))
	}
	out := TabBar(names, 10, 200)
	for _, name := range names {
		if !strings.Contains(out, name) {
			t.Errorf("输出应包含 %q", name)
		}
	}
}
