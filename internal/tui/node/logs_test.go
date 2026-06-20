package node

import (
	"errors"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// ── LogsTab 单元测试 ────────────────────────────────────────────────────────

func TestLogsTab_Name(t *testing.T) {
	tab := NewLogsTab()
	if got := tab.Name(); got != "日志" {
		t.Fatalf("LogsTab.Name() = %q, want %q", got, "日志")
	}
}

func TestLogsTab_View_NilClient(t *testing.T) {
	tab := NewLogsTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestLogsTab_View_Loading(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true
	tab.entries = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestLogsTab_View_Error(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("日志获取失败")
	tab.entries = nil

	view := tab.View()
	if !containsSubstr(view, "日志获取失败") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestLogsTab_View_EmptyEntries(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = []logEntry{}

	view := tab.View()
	if !containsSubstr(view, "暂无日志") {
		t.Errorf("空日志 View 应含 '暂无日志'，实际: %q", view)
	}
}

func TestLogsTab_View_WithEntries(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = []logEntry{
		{
			Time:    time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC),
			Level:   "INFO",
			Message: "节点已启动",
		},
		{
			Time:    time.Date(2025, 6, 15, 14, 30, 5, 0, time.UTC),
			Level:   "ERROR",
			Message: "连接失败",
			Attrs:   "addr=10.0.0.1",
		},
	}
	tab.scroll = 0

	view := tab.View()
	checks := []string{
		"实时日志",
		"节点已启动",
		"连接失败",
		"addr=10.0.0.1",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
}

func TestLogsTab_View_Paused(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = []logEntry{
		{Time: time.Now(), Level: "INFO", Message: "test"},
	}
	tab.paused = true

	view := tab.View()
	if !containsSubstr(view, "已暂停") {
		t.Errorf("paused 时 View 应含 '已暂停'")
	}
}

func TestLogsTab_HandleKey_ScrollDown(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = make([]logEntry, 50)
	tab.scroll = 0

	// 按 j 滚动一行
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.scroll != 1 {
		t.Fatalf("按 j 后 scroll 应为 1，实际: %d", tab.scroll)
	}

	// 按 down 滚动一行
	tab.Update(tea.KeyMsg{Type: tea.KeyDown})
	if tab.scroll != 2 {
		t.Fatalf("按 down 后 scroll 应为 2，实际: %d", tab.scroll)
	}
}

func TestLogsTab_HandleKey_ScrollUp(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = make([]logEntry, 50)
	tab.scroll = 5

	// 按 k 向上滚动
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 4 {
		t.Fatalf("按 k 后 scroll 应为 4，实际: %d", tab.scroll)
	}

	// 按 up 向上滚动
	tab.Update(tea.KeyMsg{Type: tea.KeyUp})
	if tab.scroll != 3 {
		t.Fatalf("按 up 后 scroll 应为 3，实际: %d", tab.scroll)
	}
}

func TestLogsTab_HandleKey_ScrollBounds(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = make([]logEntry, 5)
	tab.scroll = 0

	// 在最顶部按 k 不应越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("最顶部按 k 后 scroll 应保持 0，实际: %d", tab.scroll)
	}

	// 移到最底部
	tab.scroll = len(tab.entries) - 1
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.scroll != len(tab.entries)-1 {
		t.Fatalf("最底部按 j 后 scroll 不应越界，实际: %d", tab.scroll)
	}
}

func TestLogsTab_HandleKey_GoToTop(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = make([]logEntry, 50)
	tab.scroll = 20

	// 按 g 跳到顶部
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if tab.scroll != 0 {
		t.Fatalf("按 g 后 scroll 应为 0，实际: %d", tab.scroll)
	}
}

func TestLogsTab_HandleKey_GoToBottom(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.entries = make([]logEntry, 50)
	tab.scroll = 0

	// 按 G 跳到底部（最多显示 30 行）
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	want := max(len(tab.entries)-30, 0)
	if tab.scroll != want {
		t.Fatalf("按 G 后 scroll 应为 %d，实际: %d", want, tab.scroll)
	}
}

func TestLogsTab_HandleKey_PauseResume(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.paused = false

	// 按空格暂停
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !tab.paused {
		t.Fatal("按空格后 paused 应为 true")
	}

	// 再按 p 恢复
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if tab.paused {
		t.Fatal("按 p 后 paused 应为 false")
	}
}

func TestLogsTab_TickMsg_NotPaused(t *testing.T) {
	tab := NewLogsTab()
	// 无 client → fetch 返回 nil
	tab.paused = false
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatal("无 client 时 TickMsg 返回 cmd 应为 nil")
	}
}

func TestLogsTab_TickMsg_Paused(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.paused = true

	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatal("暂停时 TickMsg 不应触发 fetch")
	}
}

func TestLogsTab_Update_RPCResult(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	data := &logsDataResult{
		Entries: []logEntry{
			{Time: time.Now(), Level: "INFO", Message: "test-msg"},
		},
	}
	msg := tui.RPCResult{
		Method: "system.logs",
		Result: data,
	}

	tab.Update(msg)
	if len(tab.entries) != 1 {
		t.Fatalf("成功 RPCResult 后 entries 应有 1 项，实际: %d", len(tab.entries))
	}
	if tab.loading {
		t.Fatal("成功 RPCResult 后 loading 应为 false")
	}
}

func TestLogsTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewLogsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "system.logs",
		Err:    fmt.Errorf("RPC 失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
}

func TestLogsTab_SetClient(t *testing.T) {
	tab := NewLogsTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}

func TestLevelStyle(t *testing.T) {
	// 验证 levelStyle 不会 panic，且返回非空 Style
	levels := []string{"ERROR", "WARN", "INFO", "DEBUG", "TRACE", ""}
	for _, lvl := range levels {
		s := levelStyle(lvl)
		// 使用 Render 确认不 panic
		_ = s.Render("test")
	}
}
