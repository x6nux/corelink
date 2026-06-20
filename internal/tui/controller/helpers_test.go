package controller

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------- formatTopoVersion ----------

// TestFormatTopoVersion_ZeroTime 验证时间为零值时返回 Vx 格式。
func TestFormatTopoVersion_ZeroTime(t *testing.T) {
	got := formatTopoVersion(42, time.Time{})
	if got != "V42" {
		t.Fatalf("零值时间应返回 V42，got: %q", got)
	}
}

// TestFormatTopoVersion_WithTime 验证有时间时返回带时间戳的版本号。
func TestFormatTopoVersion_WithTime(t *testing.T) {
	ts := time.Date(2025, 3, 15, 10, 30, 45, 0, time.UTC)
	got := formatTopoVersion(5, ts)
	if !strings.HasPrefix(got, "V5.") {
		t.Fatalf("应以 V5. 开头，got: %q", got)
	}
}

// ---------- renderDisconnected ----------

// TestRenderDisconnected 验证未连接提示包含 "未连接" 关键字。
func TestRenderDisconnected(t *testing.T) {
	got := renderDisconnected()
	if !strings.Contains(got, "未连接") {
		t.Fatalf("renderDisconnected 应包含 '未连接'，got: %q", got)
	}
}

// ---------- renderLoading ----------

// TestRenderLoading 验证加载中提示包含 "加载中" 关键字。
func TestRenderLoading(t *testing.T) {
	got := renderLoading()
	if !strings.Contains(got, "加载中") {
		t.Fatalf("renderLoading 应包含 '加载中'，got: %q", got)
	}
}

// ---------- renderError ----------

// TestRenderError 验证错误提示包含错误信息。
func TestRenderError(t *testing.T) {
	err := errors.New("连接超时")
	got := renderError(err)
	if !strings.Contains(got, "连接超时") {
		t.Fatalf("renderError 应包含错误文本，got: %q", got)
	}
}

// ---------- truncate ----------

// TestTruncate 验证截断逻辑。
func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "短于限制", input: "abc", maxLen: 5, want: "abc"},
		{name: "等于限制", input: "abcde", maxLen: 5, want: "abcde"},
		{name: "超出限制", input: "abcdef", maxLen: 5, want: "abcd…"},
		{name: "长度为零", input: "abc", maxLen: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Fatalf("truncate(%q, %d) = %q，want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ---------- friendlyRole ----------

// TestFriendlyRole 验证角色名称转换。
func TestFriendlyRole(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"NODE_TOPO_ROLE_TRANSIT", "中转"},
		{"NODE_TOPO_ROLE_LEAF", "叶子"},
		{"NODE_TOPO_ROLE_UNSPECIFIED", "未分配"},
		{"node", "节点"},
		{"", "-"},
		{"unknown_role", "unknown_role"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := friendlyRole(tt.input)
			if got != tt.want {
				t.Fatalf("friendlyRole(%q) = %q，want %q", tt.input, got, tt.want)
			}
		})
	}
}
