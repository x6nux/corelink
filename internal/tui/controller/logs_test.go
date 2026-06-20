package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- LogsTab 基础 ----------

// TestLogsTab_Name 验证 Tab 显示名。
func TestLogsTab_Name(t *testing.T) {
	tab := NewLogsTab(nil)
	if got := tab.Name(); got != "日志" {
		t.Fatalf("Name() = %q，want '日志'", got)
	}
}

// TestLogsTab_ViewNilClient 验证无 client 时显示未连接。
func TestLogsTab_ViewNilClient(t *testing.T) {
	tab := NewLogsTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestLogsTab_ViewLoading 验证 loading 状态。
func TestLogsTab_ViewLoading(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestLogsTab_ViewError 验证 error 无数据时显示错误。
func TestLogsTab_ViewError(t *testing.T) {
	tab := &LogsTab{
		client: &tui.RPCClient{},
		err:    errors.New("日志加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "日志加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestLogsTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestLogsTab_InitNilClient(t *testing.T) {
	tab := NewLogsTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// ---------- LogsTab 数据渲染 ----------

// TestLogsTab_ViewWithEntries 验证有日志条目时正常渲染。
func TestLogsTab_ViewWithEntries(t *testing.T) {
	now := time.Now()
	tab := &LogsTab{
		client: &tui.RPCClient{},
		entries: []logEntry{
			{Time: now, Level: "INFO", Message: "服务启动成功"},
			{Time: now, Level: "ERROR", Message: "连接失败", Attrs: "host=10.0.0.1"},
		},
	}
	view := tab.View()
	checks := []string{"实时日志", "服务启动成功", "连接失败"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestLogsTab_ViewNoEntries 验证无日志条目时显示提示。
func TestLogsTab_ViewNoEntries(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		entries: []logEntry{},
	}
	view := tab.View()
	if !strings.Contains(view, "暂无日志") {
		t.Fatalf("无日志时应显示提示，got: %q", view)
	}
}

// TestLogsTab_ViewPaused 验证暂停状态标题包含 "已暂停"。
func TestLogsTab_ViewPaused(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		entries: []logEntry{},
		paused:  true,
	}
	view := tab.View()
	if !strings.Contains(view, "已暂停") {
		t.Fatalf("暂停状态应包含 '已暂停'，got: %q", view)
	}
}

// ---------- LogsTab 按键处理 ----------

// TestLogsTab_KeyJK 验证 j/k 滚动。
func TestLogsTab_KeyJK(t *testing.T) {
	now := time.Now()
	entries := make([]logEntry, 50)
	for i := range entries {
		entries[i] = logEntry{Time: now, Level: "INFO", Message: "msg"}
	}
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		entries: entries,
		scroll:  0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.scroll != 1 {
		t.Fatalf("按 j 后 scroll 应为 1，got %d", tab.scroll)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("按 k 后 scroll 应为 0，got %d", tab.scroll)
	}

	// k 到顶不越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("scroll 不应为负，got %d", tab.scroll)
	}
}

// TestLogsTab_KeyG 验证 g 跳转到顶部。
func TestLogsTab_KeyG(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		entries: make([]logEntry, 50),
		scroll:  20,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if tab.scroll != 0 {
		t.Fatalf("按 g 后 scroll 应为 0，got %d", tab.scroll)
	}
}

// TestLogsTab_KeyShiftG 验证 G 跳转到底部。
func TestLogsTab_KeyShiftG(t *testing.T) {
	entries := make([]logEntry, 50)
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		entries: entries,
		scroll:  0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	// G 应跳到 max(len-30, 0)
	expected := max(len(entries)-30, 0)
	if tab.scroll != expected {
		t.Fatalf("按 G 后 scroll 应为 %d，got %d", expected, tab.scroll)
	}
}

// TestLogsTab_KeyPause 验证空格/p 切换暂停。
func TestLogsTab_KeyPause(t *testing.T) {
	tab := &LogsTab{
		client: &tui.RPCClient{},
		paused: false,
	}

	// 空格暂停
	tab.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !tab.paused {
		t.Fatalf("空格后应暂停")
	}

	// 再按空格继续
	tab.Update(tea.KeyMsg{Type: tea.KeySpace})
	if tab.paused {
		t.Fatalf("再按空格后应继续")
	}

	// p 键暂停
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if !tab.paused {
		t.Fatalf("按 p 后应暂停")
	}
}

// ---------- LogsTab RPC ----------

// TestLogsTab_HandleRPCSystemLogs 验证收到 system.logs 结果更新日志。
func TestLogsTab_HandleRPCSystemLogs(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	result := &logsDataResult{
		Entries: []logEntry{
			{Time: time.Now(), Level: "INFO", Message: "启动"},
		},
	}
	tab.Update(tui.RPCResult{
		Method: "system.logs",
		Result: result,
	})
	if len(tab.entries) != 1 {
		t.Fatalf("收到 system.logs 后应有 1 条日志，got %d", len(tab.entries))
	}
}

// TestLogsTab_HandleRPCError 验证 RPC 错误。
func TestLogsTab_HandleRPCError(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "system.logs",
		Err:    errors.New("日志获取失败"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestLogsTab_HandleRPCIgnoresOtherMethod 验证忽略不相关方法。
func TestLogsTab_HandleRPCIgnoresOtherMethod(t *testing.T) {
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Result: &logsDataResult{},
	})
	// loading 状态不应被其他方法改变
	if tab.loading != true {
		t.Fatalf("不应处理 nodes.list 的结果")
	}
}

// TestLogsTab_TickPaused 验证暂停时 TickMsg 不触发刷新。
func TestLogsTab_TickPaused(t *testing.T) {
	tab := &LogsTab{
		client: nil,
		paused: true,
	}
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("暂停时 TickMsg 应返回 nil cmd")
	}
}

// TestLogsTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestLogsTab_TickNilClient(t *testing.T) {
	tab := NewLogsTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}

// TestLogsTab_AutoScrollOnNewData 验证非暂停时收到数据自动滚动到底部。
func TestLogsTab_AutoScrollOnNewData(t *testing.T) {
	entries := make([]logEntry, 50)
	for i := range entries {
		entries[i] = logEntry{Time: time.Now(), Level: "INFO", Message: "msg"}
	}
	tab := &LogsTab{
		client:  &tui.RPCClient{},
		loading: true,
		paused:  false,
		scroll:  0,
	}
	tab.Update(tui.RPCResult{
		Method: "system.logs",
		Result: &logsDataResult{Entries: entries},
	})
	expected := max(len(entries)-30, 0)
	if tab.scroll != expected {
		t.Fatalf("自动滚动后 scroll 应为 %d，got %d", expected, tab.scroll)
	}
}
