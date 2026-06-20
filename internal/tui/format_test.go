package tui

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FormatUptime
// ---------------------------------------------------------------------------

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name    string
		seconds float64
		want    string
	}{
		// 纯秒
		{name: "零秒", seconds: 0, want: "0s"},
		{name: "1秒", seconds: 1, want: "1s"},
		{name: "59秒", seconds: 59, want: "59s"},

		// 分+秒
		{name: "1分0秒", seconds: 60, want: "1m 0s"},
		{name: "1分30秒", seconds: 90, want: "1m 30s"},
		{name: "59分59秒", seconds: 3599, want: "59m 59s"},

		// 时+分+秒
		{name: "1小时整", seconds: 3600, want: "1h 0m 0s"},
		{name: "2小时30分15秒", seconds: 9015, want: "2h 30m 15s"},
		{name: "23小时59分59秒", seconds: 86399, want: "23h 59m 59s"},

		// 天+时+分
		{name: "1天整", seconds: 86400, want: "1d 0h 0m"},
		{name: "1天1小时30分", seconds: 91800, want: "1d 1h 30m"},
		{name: "7天", seconds: 604800, want: "7d 0h 0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatUptime(tt.seconds)
			if got != tt.want {
				t.Errorf("FormatUptime(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatTopoVersion
// ---------------------------------------------------------------------------

func TestFormatTopoVersion(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 14, 30, 5, 0, time.UTC)

	tests := []struct {
		name string
		ver  uint64
		t    time.Time
		want string
	}{
		{name: "零值时间只显示版本号", ver: 3, t: time.Time{}, want: "V3"},
		{name: "正常时间格式化", ver: 42, t: fixed, want: "V42.26.6.15.14-30-05"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTopoVersion(tt.ver, tt.t)
			if got != tt.want {
				t.Errorf("FormatTopoVersion(%d, %v) = %q, want %q", tt.ver, tt.t, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FriendlyRole
// ---------------------------------------------------------------------------

func TestFriendlyRole(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"NODE_TOPO_ROLE_TRANSIT", "中转"},
		{"NODE_TOPO_ROLE_LEAF", "叶子"},
		{"NODE_TOPO_ROLE_UNSPECIFIED", "未分配"},
		{"node", "节点"},
		{"", "-"},
		{"unknown_role", "unknown_role"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := FriendlyRole(tt.role)
			if got != tt.want {
				t.Errorf("FriendlyRole(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Truncate
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		// 边界情况
		{name: "maxLen=0返回空", s: "hello", maxLen: 0, want: ""},
		{name: "负数maxLen返回空", s: "hello", maxLen: -1, want: ""},
		{name: "maxLen=1超长返回省略号", s: "hello", maxLen: 1, want: "…"},

		// 不截断
		{name: "短于限制不截断", s: "hi", maxLen: 5, want: "hi"},
		{name: "等长不截断", s: "hello", maxLen: 5, want: "hello"},
		{name: "空字符串", s: "", maxLen: 5, want: ""},

		// 截断
		{name: "超长截断加省略号", s: "hello world", maxLen: 5, want: "hell…"},
		{name: "maxLen=2截断", s: "abc", maxLen: 2, want: "a…"},

		// 中文字符
		{name: "中文不截断", s: "你好", maxLen: 3, want: "你好"},
		{name: "中文截断", s: "你好世界", maxLen: 3, want: "你好…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}
