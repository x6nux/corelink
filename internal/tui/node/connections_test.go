package node

import (
	"errors"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// ── ConnectionsTab 单元测试 ─────────────────────────────────────────────────

func teaKeyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestConnectionsTab_Name(t *testing.T) {
	tab := NewConnectionsTab()
	if got := tab.Name(); got != "连接" {
		t.Fatalf("ConnectionsTab.Name() = %q, want %q", got, "连接")
	}
}

func TestConnectionsTab_View_NilClient(t *testing.T) {
	tab := NewConnectionsTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestConnectionsTab_View_Loading(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true
	tab.items = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestConnectionsTab_View_Error(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("连接列表获取失败")
	tab.items = nil

	view := tab.View()
	if !containsSubstr(view, "连接列表获取失败") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestConnectionsTab_View_EmptyList(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{}

	view := tab.View()
	// 空列表应显示表头 + 无数据
	if !containsSubstr(view, "无数据") {
		t.Errorf("空列表 View 应含 '无数据'，实际: %q", view)
	}
}

func TestConnectionsTab_View_WithData(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{
		{
			PeerID:     "peer-abc",
			VIP:        "100.64.0.2",
			PeerIP:     "203.0.113.10:51820",
			InternalIP: "192.168.1.10:51820",
			LinkType:   "direct",
			RTTms:      15,
			RTTValid:   true,
			Loss:       0,
			LossValid:  true,
			State:      "active",
		},
		{
			PeerID:   "peer-xyz",
			VIP:      "100.64.0.3",
			PeerIP:   "198.51.100.20:51820",
			LinkType: "node",
			State:    "未连接",
		},
	}

	view := tab.View()
	checks := []string{
		"peer-abc",
		"100.64.0.2",
		"203.0.113.10",
		"192.168.1.10",
		"direct",
		"active",
		"15",
		"0",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
	if containsSubstr(view, "peer-xyz") {
		t.Errorf("默认应只显示已连接项，不应含未连接 peer-xyz，实际: %q", view)
	}
}

func TestConnectionsTab_View_MissingMetricsShowsDash(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{
		{
			PeerID:   "peer-no-metric",
			VIP:      "100.64.0.4",
			PeerIP:   "203.0.113.40:51820",
			LinkType: "WireGuard",
			State:    "已连接",
		},
	}

	view := tab.View()
	if !containsSubstr(view, "-") {
		t.Fatalf("无 RTT/丢包数据时应显示 '-'，实际: %q", view)
	}
	if containsSubstr(view, " 0 ") {
		t.Fatalf("无 RTT/丢包数据时不应误显示 0，实际: %q", view)
	}
}

func TestConnectionsTab_Update_StatusFilterShowsDisconnected(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{
		{PeerID: "peer-up", VIP: "100.64.0.2", State: "已连接"},
		{PeerID: "peer-down", VIP: "100.64.0.3", State: "未连接"},
	}

	if view := tab.View(); !containsSubstr(view, "peer-up") || containsSubstr(view, "peer-down") {
		t.Fatalf("默认应只显示已连接项，实际: %q", view)
	}
	tab.Update(teaKeyMsg("s"))
	view := tab.View()
	if !containsSubstr(view, "peer-down") || containsSubstr(view, "peer-up") {
		t.Fatalf("切到未连接筛选后应只显示未连接项，实际: %q", view)
	}
	tab.Update(teaKeyMsg("s"))
	view = tab.View()
	if !containsSubstr(view, "peer-down") || !containsSubstr(view, "peer-up") {
		t.Fatalf("切到全部筛选后应显示全部项，实际: %q", view)
	}
}

func TestConnectionsTab_Update_PeerFilterCombinesWithStatusFilter(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{
		{PeerID: "peer-a", VIP: "100.64.0.2", State: "已连接"},
		{PeerID: "peer-b", VIP: "100.64.0.3", State: "已连接"},
		{PeerID: "peer-c", VIP: "100.64.0.4", State: "未连接"},
	}

	tab.Update(teaKeyMsg("]"))
	view := tab.View()
	if !containsSubstr(view, "peer-b") || containsSubstr(view, "peer-a") || containsSubstr(view, "peer-c") {
		t.Fatalf("节点筛选应只显示 peer-b 且继续叠加已连接筛选，实际: %q", view)
	}

	tab.Update(teaKeyMsg("s"))
	view = tab.View()
	if !containsSubstr(view, "无数据") || containsSubstr(view, "100.64.0.3") || containsSubstr(view, "peer-c") {
		t.Fatalf("peer-b + 未连接筛选应无数据，实际: %q", view)
	}

	tab.Update(teaKeyMsg("a"))
	view = tab.View()
	if !containsSubstr(view, "peer-c") || containsSubstr(view, "peer-a") || containsSubstr(view, "peer-b") {
		t.Fatalf("清除节点筛选后仍应保留未连接筛选，实际: %q", view)
	}
}

func TestConnectionsTab_View_ErrorWithStaleData(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeConnectionDTO{
		{PeerID: "peer-stale", State: "active"},
	}
	tab.err = errors.New("刷新超时")

	view := tab.View()
	if !containsSubstr(view, "peer-stale") {
		t.Errorf("有旧数据时应继续展示")
	}
	if !containsSubstr(view, "刷新失败") {
		t.Errorf("有 err 时应展示刷新失败提示")
	}
}

func TestConnectionsTab_Update_RPCResult(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	items := []nodeConnectionDTO{
		{PeerID: "peer-1", State: "active"},
	}
	msg := tui.RPCResult{
		Method: "connections.list",
		Result: &items,
	}

	tab.Update(msg)
	if len(tab.items) != 1 {
		t.Fatalf("成功 RPCResult 后 items 应有 1 项，实际: %d", len(tab.items))
	}
	if tab.loading {
		t.Fatal("成功 RPCResult 后 loading 应为 false")
	}
}

func TestConnectionsTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "connections.list",
		Err:    fmt.Errorf("RPC 失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
}

func TestConnectionsTab_Update_IgnoresOtherMethods(t *testing.T) {
	tab := NewConnectionsTab()
	tab.client = &tui.RPCClient{}

	msg := tui.RPCResult{
		Method: "ingress.list",
		Err:    errors.New("无关"),
	}
	tab.Update(msg)
	if tab.err != nil {
		t.Fatal("不相关方法的 RPCResult 不应设置 err")
	}
}

func TestConnectionsTab_SetClient(t *testing.T) {
	tab := NewConnectionsTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
