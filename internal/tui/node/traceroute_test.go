package node

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// ── TracerouteTab 单元测试 ──────────────────────────────────────────────────

func TestTracerouteTab_Name(t *testing.T) {
	tab := NewTracerouteTab()
	if got := tab.Name(); got != "路由追踪" {
		t.Fatalf("TracerouteTab.Name() = %q, want %q", got, "路由追踪")
	}
}

func TestTracerouteTab_View_NilClient(t *testing.T) {
	tab := NewTracerouteTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestTracerouteTab_InputFocused(t *testing.T) {
	tab := NewTracerouteTab()
	if !tab.InputFocused() {
		t.Fatal("TracerouteTab.InputFocused 应始终返回 true")
	}
}

func TestTracerouteTab_Init(t *testing.T) {
	tab := NewTracerouteTab()
	cmd := tab.Init()
	if cmd != nil {
		t.Fatal("TracerouteTab.Init 应返回 nil")
	}
}

func TestTracerouteTab_View_EmptyInput(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}

	view := tab.View()
	// 应显示路由追踪标题和输入框
	if !containsSubstr(view, "路由追踪") {
		t.Errorf("View 应含 '路由追踪'")
	}
	if !containsSubstr(view, "目标节点 ID") {
		t.Errorf("View 应含 '目标节点 ID' 输入提示")
	}
}

func TestTracerouteTab_View_Loading(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	view := tab.View()
	if !containsSubstr(view, "查询中") {
		t.Errorf("loading 时 View 应含 '查询中'")
	}
}

func TestTracerouteTab_View_Error(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("路由查询失败")

	view := tab.View()
	if !containsSubstr(view, "路由查询失败") {
		t.Errorf("error 时 View 应含错误信息")
	}
}

func TestTracerouteTab_View_NoPaths(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.result = &nodeRouteResult{Paths: nil}

	view := tab.View()
	if !containsSubstr(view, "未找到路径") {
		t.Errorf("无路径时 View 应含 '未找到路径'")
	}
}

func TestTracerouteTab_View_WithPaths(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.result = &nodeRouteResult{
		Paths: []nodeRoutePath{
			{
				Hops: []nodeRouteHop{
					{NodeID: "relay-0", IngressID: "ing-1", Host: "1.2.3.4", Port: 7443},
					{NodeID: "node-dst", IngressID: "ing-2", Host: "5.6.7.8", Port: 7444},
				},
				TotalHops: 2,
				Active:    true,
			},
			{
				Hops: []nodeRouteHop{
					{NodeID: "node-dst", IngressID: "ing-3"},
				},
				TotalHops: 1,
				Active:    false,
			},
		},
	}
	tab.pathIndex = 0

	view := tab.View()
	checks := []string{
		"找到 2 条路径",
		"relay-0",
		"1.2.3.4",
		"活跃",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
}

func TestTracerouteTab_HandleKey_TypeChars(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}

	// 输入字符
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})

	if tab.dst != "abc" {
		t.Fatalf("输入 abc 后 dst 应为 'abc'，实际: %q", tab.dst)
	}
}

func TestTracerouteTab_HandleKey_Backspace(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.dst = "abc"

	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.dst != "ab" {
		t.Fatalf("backspace 后 dst 应为 'ab'，实际: %q", tab.dst)
	}

	// 空字符串时 backspace 不应 panic
	tab.dst = ""
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.dst != "" {
		t.Fatalf("空字符串 backspace 后 dst 应为空")
	}
}

func TestTracerouteTab_HandleKey_PathNavigation(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.result = &nodeRouteResult{
		Paths: []nodeRoutePath{
			{TotalHops: 1},
			{TotalHops: 2},
			{TotalHops: 3},
		},
	}
	tab.pathIndex = 0

	// j 切到下一条路径
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.pathIndex != 1 {
		t.Fatalf("按 j 后 pathIndex 应为 1，实际: %d", tab.pathIndex)
	}

	// k 切到上一条路径
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.pathIndex != 0 {
		t.Fatalf("按 k 后 pathIndex 应为 0，实际: %d", tab.pathIndex)
	}

	// k 在最顶不越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.pathIndex != 0 {
		t.Fatalf("最顶按 k 后 pathIndex 应保持 0")
	}

	// j 到最底
	tab.pathIndex = 2
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.pathIndex != 2 {
		t.Fatalf("最底按 j 后 pathIndex 应保持 2")
	}
}

func TestTracerouteTab_HandleKey_EnterEmpty(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.dst = ""

	// 空输入 Enter 不应触发 fetch
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("空 dst Enter 不应返回 cmd")
	}
}

func TestTracerouteTab_Update_RPCResult(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	result := &nodeRouteResult{
		Paths: []nodeRoutePath{
			{TotalHops: 2, Active: true},
		},
	}
	msg := tui.RPCResult{
		Method: "route.trace",
		Result: result,
	}

	tab.Update(msg)
	if tab.result == nil {
		t.Fatal("成功 RPCResult 后 result 不应为 nil")
	}
	if len(tab.result.Paths) != 1 {
		t.Fatalf("result.Paths 应有 1 项，实际: %d", len(tab.result.Paths))
	}
	if tab.loading {
		t.Fatal("成功 RPCResult 后 loading 应为 false")
	}
	if tab.pathIndex != 0 {
		t.Fatal("成功 RPCResult 后 pathIndex 应重置为 0")
	}
}

func TestTracerouteTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewTracerouteTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "route.trace",
		Err:    errors.New("路由查询失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
	if tab.loading {
		t.Fatal("失败 RPCResult 后 loading 应为 false")
	}
}

func TestTracerouteTab_SetClient(t *testing.T) {
	tab := NewTracerouteTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
