package controller

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- NodesTab 基础 ----------

// TestNodesTab_Name 验证 Tab 显示名。
func TestNodesTab_Name(t *testing.T) {
	tab := NewNodesTab(nil)
	if got := tab.Name(); got != "节点" {
		t.Fatalf("Name() = %q，want '节点'", got)
	}
}

// TestNodesTab_ViewNilClient 验证无 client 时显示未连接。
func TestNodesTab_ViewNilClient(t *testing.T) {
	tab := NewNodesTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestNodesTab_ViewLoading 验证 loading 状态。
func TestNodesTab_ViewLoading(t *testing.T) {
	tab := &NodesTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestNodesTab_ViewError 验证 error 无数据时显示错误。
func TestNodesTab_ViewError(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		err:    errors.New("获取节点失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "获取节点失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestNodesTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestNodesTab_InitNilClient(t *testing.T) {
	tab := NewNodesTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// ---------- NodesTab 列表视图 ----------

// TestNodesTab_ViewList 验证有数据时渲染列表。
func TestNodesTab_ViewList(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		nodes: []nodeDTO{
			{ID: "n1", Name: "host-1", VIP: "10.0.0.1", Role: "node", Online: true},
			{ID: "n2", Name: "host-2", VIP: "10.0.0.2", Role: "node", Online: false},
		},
	}
	view := tab.View()
	checks := []string{"host-1", "host-2", "10.0.0.1", "10.0.0.2"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("列表 View 应包含 %q，got: %q", c, view)
		}
	}
}

// ---------- NodesTab 按键处理 ----------

// TestNodesTab_KeyJK 验证 j/k 移动光标。
func TestNodesTab_KeyJK(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		nodes: []nodeDTO{
			{ID: "n1"}, {ID: "n2"}, {ID: "n3"},
		},
		cursor: 0,
	}

	// j 向下
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 1 {
		t.Fatalf("按 j 后 cursor 应为 1，got %d", tab.cursor)
	}

	// j 继续向下
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 2 {
		t.Fatalf("按 j 后 cursor 应为 2，got %d", tab.cursor)
	}

	// j 到底不越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 2 {
		t.Fatalf("按 j 到底后 cursor 应保持 2，got %d", tab.cursor)
	}

	// k 向上
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 1 {
		t.Fatalf("按 k 后 cursor 应为 1，got %d", tab.cursor)
	}

	// k 继续向上
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 0 {
		t.Fatalf("按 k 后 cursor 应为 0，got %d", tab.cursor)
	}

	// k 到顶不越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 0 {
		t.Fatalf("按 k 到顶后 cursor 应保持 0，got %d", tab.cursor)
	}
}

// TestNodesTab_KeyEnter 验证 enter 切换到详情视图（nil client 不发 RPC）。
func TestNodesTab_KeyEnter(t *testing.T) {
	tab := &NodesTab{
		client: nil, // nil client → fetchDetail 返回 nil
		nodes:  []nodeDTO{{ID: "n1"}},
		cursor: 0,
	}
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// nil client 时 fetchDetail 返回 nil
	if cmd != nil {
		t.Fatalf("nil client 时 enter 应返回 nil cmd")
	}
}

// TestNodesTab_KeyD 验证 d 键切换到删除确认视图。
func TestNodesTab_KeyD(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		nodes:  []nodeDTO{{ID: "n1", Name: "host-1"}},
		cursor: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if tab.view != nodesViewConfirmDelete {
		t.Fatalf("按 d 后应切换到确认删除视图，got view=%d", tab.view)
	}
	if tab.deleteIdx != 0 {
		t.Fatalf("deleteIdx 应为当前 cursor 0，got %d", tab.deleteIdx)
	}
}

// TestNodesTab_ConfirmDeleteN 验证删除确认时按 n 取消。
func TestNodesTab_ConfirmDeleteN(t *testing.T) {
	tab := &NodesTab{
		client:    &tui.RPCClient{},
		nodes:     []nodeDTO{{ID: "n1"}},
		view:      nodesViewConfirmDelete,
		deleteIdx: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if tab.view != nodesViewList {
		t.Fatalf("按 n 后应返回列表视图，got view=%d", tab.view)
	}
}

// TestNodesTab_ConfirmDeleteEsc 验证删除确认时按 esc 取消。
func TestNodesTab_ConfirmDeleteEsc(t *testing.T) {
	tab := &NodesTab{
		client:    &tui.RPCClient{},
		nodes:     []nodeDTO{{ID: "n1"}},
		view:      nodesViewConfirmDelete,
		deleteIdx: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != nodesViewList {
		t.Fatalf("按 esc 后应返回列表视图，got view=%d", tab.view)
	}
}

// TestNodesTab_DetailEsc 验证详情视图按 esc 返回列表。
func TestNodesTab_DetailEsc(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		nodes:  []nodeDTO{{ID: "n1"}},
		view:   nodesViewDetail,
		detail: &nodeDetailDTO{ID: "n1", Name: "host-1"},
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != nodesViewList {
		t.Fatalf("详情视图按 esc 后应返回列表，got view=%d", tab.view)
	}
	if tab.detail != nil {
		t.Fatalf("返回列表后 detail 应清空")
	}
}

// ---------- NodesTab RPC ----------

// TestNodesTab_HandleRPCNodesList 验证收到 nodes.list 结果更新列表。
func TestNodesTab_HandleRPCNodesList(t *testing.T) {
	tab := &NodesTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	nodes := []nodeDTO{
		{ID: "n1", Name: "host-1"},
		{ID: "n2", Name: "host-2"},
	}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Result: &nodes,
	})
	if len(tab.nodes) != 2 {
		t.Fatalf("收到 nodes.list 后应有 2 个节点，got %d", len(tab.nodes))
	}
}

// TestNodesTab_HandleRPCNodesGet 验证收到 nodes.get 结果切换到详情。
func TestNodesTab_HandleRPCNodesGet(t *testing.T) {
	tab := &NodesTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	detail := &nodeDetailDTO{ID: "n1", Name: "host-1", VIP: "10.0.0.1"}
	tab.Update(tui.RPCResult{
		Method: "nodes.get",
		Result: detail,
	})
	if tab.view != nodesViewDetail {
		t.Fatalf("收到 nodes.get 后应切换到详情视图")
	}
	if tab.detail == nil || tab.detail.ID != "n1" {
		t.Fatalf("detail 应更新为返回值")
	}
}

// TestNodesTab_HandleRPCError 验证 RPC 错误处理。
func TestNodesTab_HandleRPCError(t *testing.T) {
	tab := &NodesTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Err:    errors.New("网络错误"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestNodesTab_CursorClampOnShrink 验证列表缩短时 cursor 自动修正。
func TestNodesTab_CursorClampOnShrink(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		cursor: 5,
	}
	// 返回只有 2 个节点的列表
	nodes := []nodeDTO{{ID: "n1"}, {ID: "n2"}}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Result: &nodes,
	})
	if tab.cursor >= len(tab.nodes) {
		t.Fatalf("cursor 应修正到合法范围，got cursor=%d, len=%d", tab.cursor, len(tab.nodes))
	}
}

// TestNodesTab_RenderDetail 验证详情视图渲染。
func TestNodesTab_RenderDetail(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		view:   nodesViewDetail,
		detail: &nodeDetailDTO{
			ID:       "n1",
			Name: "host-1",
			VIP:      "10.0.0.1",
			Role:     "node",
			Online:   true,
			Ingresses: []ingressDTO{
				{Host: "1.2.3.4", Port: 51820, Source: "stun", Confidence: 90, NatType: "cone"},
			},
		},
	}
	view := tab.View()
	checks := []string{"节点详情", "host-1", "10.0.0.1", "1.2.3.4:51820"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("详情 View 应包含 %q", c)
		}
	}
}

// TestNodesTab_RenderConfirmDelete 验证删除确认视图渲染。
func TestNodesTab_RenderConfirmDelete(t *testing.T) {
	tab := &NodesTab{
		client:    &tui.RPCClient{},
		nodes:     []nodeDTO{{ID: "n1", Name: "host-1"}},
		view:      nodesViewConfirmDelete,
		deleteIdx: 0,
	}
	view := tab.View()
	if !strings.Contains(view, "确认删除") {
		t.Fatalf("确认删除视图应包含 '确认删除'")
	}
	if !strings.Contains(view, "n1") {
		t.Fatalf("确认删除视图应包含节点 ID")
	}
}

// TestNodesTab_EmptyListView 验证空列表视图包含无数据提示。
func TestNodesTab_EmptyListView(t *testing.T) {
	tab := &NodesTab{
		client: &tui.RPCClient{},
		nodes:  []nodeDTO{},
	}
	view := tab.View()
	if !strings.Contains(view, "无数据") {
		t.Fatalf("空列表应包含 '无数据' 提示，got: %q", view)
	}
}
