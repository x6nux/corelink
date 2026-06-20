package node

import (
	"errors"
	"testing"
	"time"
)

// ── helpers.go 包装函数测试 ─────────────────────────────────────────────────

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
				t.Errorf("friendlyRole(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatTopoVersion(t *testing.T) {
	tests := []struct {
		name string
		ver  uint64
		tm   time.Time
		want string
	}{
		{
			name: "零时间只返回版本号",
			ver:  42,
			tm:   time.Time{},
			want: "V42",
		},
		{
			name: "非零时间返回完整格式",
			ver:  3,
			tm:   time.Date(2025, 1, 15, 14, 30, 45, 0, time.UTC),
			want: "V3.25.1.15.14-30-45",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTopoVersion(tt.ver, tt.tm)
			if got != tt.want {
				t.Errorf("formatTopoVersion(%d, %v) = %q, want %q", tt.ver, tt.tm, got, tt.want)
			}
		})
	}
}

func TestRenderDisconnected(t *testing.T) {
	out := renderDisconnected()
	if out == "" {
		t.Fatal("renderDisconnected 不应返回空字符串")
	}
	// 应包含 corelink-node 关键信息
	if !containsSubstr(out, "corelink-node") {
		t.Errorf("renderDisconnected 应包含 'corelink-node'，实际: %q", out)
	}
}

func TestRenderLoading(t *testing.T) {
	out := renderLoading()
	if out == "" {
		t.Fatal("renderLoading 不应返回空字符串")
	}
	if !containsSubstr(out, "加载中") {
		t.Errorf("renderLoading 应包含 '加载中'，实际: %q", out)
	}
}

func TestRenderError(t *testing.T) {
	err := errors.New("测试错误")
	out := renderError(err)
	if out == "" {
		t.Fatal("renderError 不应返回空字符串")
	}
	if !containsSubstr(out, "测试错误") {
		t.Errorf("renderError 应包含错误信息，实际: %q", out)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"短于限制不截断", "hello", 10, "hello"},
		{"等于限制不截断", "hello", 5, "hello"},
		{"超过限制截断", "hello world", 5, "hell…"},
		{"长度为零返回空", "hello", 0, ""},
		{"长度为一返回省略号", "hello", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestRenderTable_EmptyRows(t *testing.T) {
	headers := []string{"A", "B"}
	widths := []int{10, 10}
	out := renderTable(headers, nil, widths, -1)
	if out == "" {
		t.Fatal("renderTable 空行不应返回空字符串")
	}
	if !containsSubstr(out, "无数据") {
		t.Errorf("空行 renderTable 应含 '无数据'，实际: %q", out)
	}
}

func TestRenderTable_WithRows(t *testing.T) {
	headers := []string{"名称", "值"}
	rows := [][]string{
		{"alpha", "100"},
		{"beta", "200"},
	}
	widths := []int{10, 10}
	out := renderTable(headers, rows, widths, -1)
	if out == "" {
		t.Fatal("renderTable 不应返回空字符串")
	}
	// 应包含行数据
	if !containsSubstr(out, "alpha") {
		t.Errorf("renderTable 应含行数据 'alpha'，实际: %q", out)
	}
}

// containsSubstr 检查 s 是否包含 sub。
func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
