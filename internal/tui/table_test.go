package tui

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// RenderTable
// ---------------------------------------------------------------------------

func TestRenderTable_配置错误(t *testing.T) {
	// headers 和 widths 长度不匹配应返回错误提示
	got := RenderTable([]string{"A", "B"}, nil, []int{10}, -1)
	if got != "表格配置错误" {
		t.Errorf("headers/widths 长度不一致时应返回错误提示, got %q", got)
	}
}

func TestRenderTable_空数据(t *testing.T) {
	got := RenderTable([]string{"名称", "状态"}, nil, []int{10, 10}, -1)
	// 空行时应显示「无数据」提示
	if !strings.Contains(got, "无数据") {
		t.Error("空 rows 应显示 '无数据' 提示")
	}
	// 表头仍应渲染
	if !strings.Contains(got, "名称") {
		t.Error("空 rows 时表头仍应渲染")
	}
}

func TestRenderTable_正常渲染(t *testing.T) {
	headers := []string{"节点", "延迟"}
	rows := [][]string{
		{"node-1", "10ms"},
		{"node-2", "20ms"},
	}
	widths := []int{12, 8}

	got := RenderTable(headers, rows, widths, -1)

	// 验证表头
	if !strings.Contains(got, "节点") {
		t.Error("输出应包含表头 '节点'")
	}
	if !strings.Contains(got, "延迟") {
		t.Error("输出应包含表头 '延迟'")
	}

	// 验证分隔线
	if !strings.Contains(got, "─") {
		t.Error("输出应包含分隔线")
	}

	// 验证数据行
	if !strings.Contains(got, "node-1") {
		t.Error("输出应包含 'node-1'")
	}
	if !strings.Contains(got, "20ms") {
		t.Error("输出应包含 '20ms'")
	}
}

func TestRenderTable_选中行(t *testing.T) {
	headers := []string{"名称"}
	rows := [][]string{{"aaa"}, {"bbb"}, {"ccc"}}
	widths := []int{10}

	// 选中第二行（index=1）
	got := RenderTable(headers, rows, widths, 1)

	// 所有行内容都应存在
	if !strings.Contains(got, "aaa") || !strings.Contains(got, "bbb") || !strings.Contains(got, "ccc") {
		t.Error("所有行内容都应渲染")
	}
}

func TestRenderTable_列数超出宽度定义(t *testing.T) {
	headers := []string{"A"}
	rows := [][]string{{"val1", "extra_col"}} // 第二列超出 widths 定义
	widths := []int{10}

	// 不应 panic
	got := RenderTable(headers, rows, widths, -1)
	if !strings.Contains(got, "val1") {
		t.Error("输出应包含 'val1'")
	}
	// 超出宽度定义的列应被忽略
	if strings.Contains(got, "extra_col") {
		t.Error("超出宽度定义的列不应渲染")
	}
}

func TestRenderTable_长文本截断(t *testing.T) {
	headers := []string{"名称"}
	rows := [][]string{{strings.Repeat("x", 50)}}
	widths := []int{10}

	got := RenderTable(headers, rows, widths, -1)
	if !strings.Contains(got, "…") {
		t.Error("超长单元格文本应被截断并附加省略号")
	}
}

// ---------------------------------------------------------------------------
// RenderDisconnected
// ---------------------------------------------------------------------------

func TestRenderDisconnected(t *testing.T) {
	got := RenderDisconnected("corelink")
	if !strings.Contains(got, "corelink") {
		t.Error("输出应包含守护进程名称")
	}
	if !strings.Contains(got, "未连接") {
		t.Error("输出应包含 '未连接' 提示")
	}
}

// ---------------------------------------------------------------------------
// RenderLoading
// ---------------------------------------------------------------------------

func TestRenderLoading(t *testing.T) {
	got := RenderLoading()
	if !strings.Contains(got, "加载中") {
		t.Error("输出应包含 '加载中' 提示")
	}
}

// ---------------------------------------------------------------------------
// RenderError
// ---------------------------------------------------------------------------

func TestRenderError(t *testing.T) {
	err := errors.New("连接超时")
	got := RenderError(err)
	if !strings.Contains(got, "连接超时") {
		t.Error("输出应包含错误消息")
	}
	if !strings.Contains(got, "错误") {
		t.Error("输出应包含 '错误' 前缀")
	}
}

func TestRenderError_nil错误(t *testing.T) {
	// error 接口传 nil 也不应 panic
	got := RenderError((*testErr)(nil))
	if len(got) == 0 {
		t.Error("即使 err 值表示为 nil，输出也不应为空")
	}
}

// testErr 实现 error 接口，用于测试非 nil 接口但底层值为 nil 的情况
type testErr struct{}

func (e *testErr) Error() string { return "<nil>" }

// ---------------------------------------------------------------------------
// RenderSectionHeader
// ---------------------------------------------------------------------------

func TestRenderSectionHeader(t *testing.T) {
	got := RenderSectionHeader("节点列表")
	if !strings.Contains(got, "节点列表") {
		t.Error("输出应包含标题文本")
	}
	if len(got) == 0 {
		t.Error("输出不应为空")
	}
}
