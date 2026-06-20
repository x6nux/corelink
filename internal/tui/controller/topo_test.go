package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- TopoTab 基础 ----------

// TestTopoTab_Name 验证 Tab 显示名。
func TestTopoTab_Name(t *testing.T) {
	tab := NewTopoTab(nil)
	if got := tab.Name(); got != "拓扑" {
		t.Fatalf("Name() = %q，want '拓扑'", got)
	}
}

// TestTopoTab_ViewNilClient 验证无 client 时显示未连接。
func TestTopoTab_ViewNilClient(t *testing.T) {
	tab := NewTopoTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestTopoTab_ViewLoading 验证 loading 状态。
func TestTopoTab_ViewLoading(t *testing.T) {
	tab := &TopoTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestTopoTab_ViewError 验证 error 无数据时显示错误。
func TestTopoTab_ViewError(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		err:    errors.New("拓扑加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "拓扑加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestTopoTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestTopoTab_InitNilClient(t *testing.T) {
	tab := NewTopoTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// ---------- TopoTab 数据渲染 ----------

// TestTopoTab_ViewWithData 验证有数据时正常渲染统计信息。
func TestTopoTab_ViewWithData(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{
			Version:       12,
			TransitCount:  3,
			LeafCount:     7,
			LastRecompute: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	view := tab.View()
	checks := []string{"拓扑统计", "中转节点", "叶子节点"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestTopoTab_ViewWithGraph 验证有图数据时渲染拓扑图。
func TestTopoTab_ViewWithGraph(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{
			Version:      1,
			TransitCount: 1,
			LeafCount:    1,
		},
		graph: &topoGraphResult{
			Nodes: []topoGraphNode{
				{ID: "n1", Hostname: "relay-1", VIP: "10.0.0.1", Role: "node", Online: true},
				{ID: "n2", Hostname: "agent-1", VIP: "10.0.0.2", Role: "node", Online: false},
			},
			Edges: []topoGraphEdge{
				{From: "n1", To: "n2"},
			},
		},
	}
	view := tab.View()
	checks := []string{"网络拓扑", "relay-1", "agent-1"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("拓扑图 View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestTopoTab_ViewZeroLastRecompute 验证上次重算时间为零值时显示 "无"。
func TestTopoTab_ViewZeroLastRecompute(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{
			Version: 1,
		},
	}
	view := tab.View()
	if !strings.Contains(view, "无") {
		t.Fatalf("零值 LastRecompute 应显示 '无'，got: %q", view)
	}
}

// ---------- TopoTab 按键处理 ----------

// TestTopoTab_KeyJK 验证 j/k 在图中移动光标。
func TestTopoTab_KeyJK(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{Version: 1},
		graph: &topoGraphResult{
			Nodes: []topoGraphNode{
				{ID: "n1"}, {ID: "n2"}, {ID: "n3"},
			},
		},
		cursor: 0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 1 {
		t.Fatalf("按 j 后 cursor 应为 1，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // 到底
	if tab.cursor != 2 {
		t.Fatalf("j 到底后 cursor 应保持 2，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 1 {
		t.Fatalf("按 k 后 cursor 应为 1，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}) // 到顶
	if tab.cursor != 0 {
		t.Fatalf("k 到顶后 cursor 应保持 0，got %d", tab.cursor)
	}
}

// TestTopoTab_KeyJKNoGraph 验证无图数据时按键不崩溃。
func TestTopoTab_KeyJKNoGraph(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{Version: 1},
		graph:  nil,
	}
	// 无 graph 时 handleKey 应直接返回
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	// 不崩溃即可
}

// ---------- TopoTab RPC ----------

// TestTopoTab_HandleRPCTopoStatus 验证收到 topo.status 结果更新。
func TestTopoTab_HandleRPCTopoStatus(t *testing.T) {
	tab := &TopoTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	status := &topoStatusResult{Version: 42, TransitCount: 5}
	tab.Update(tui.RPCResult{
		Method: "topo.status",
		Result: status,
	})
	if tab.status == nil || tab.status.Version != 42 {
		t.Fatalf("收到 topo.status 后 status 应更新")
	}
}

// TestTopoTab_HandleRPCTopoGraph 验证收到 topo.graph 结果更新。
func TestTopoTab_HandleRPCTopoGraph(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
	}
	graph := &topoGraphResult{
		Nodes: []topoGraphNode{{ID: "n1"}},
	}
	tab.Update(tui.RPCResult{
		Method: "topo.graph",
		Result: graph,
	})
	if tab.graph == nil || len(tab.graph.Nodes) != 1 {
		t.Fatalf("收到 topo.graph 后 graph 应更新")
	}
}

// TestTopoTab_HandleRPCError 验证 RPC 错误处理。
func TestTopoTab_HandleRPCError(t *testing.T) {
	tab := &TopoTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "topo.status",
		Err:    errors.New("超时"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestTopoTab_HandleRPCGraphError 验证 topo.graph 错误不覆盖 status。
func TestTopoTab_HandleRPCGraphError(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{Version: 10},
	}
	tab.Update(tui.RPCResult{
		Method: "topo.graph",
		Err:    errors.New("图数据不可用"),
	})
	// graph 错误不应影响 status
	if tab.status == nil || tab.status.Version != 10 {
		t.Fatalf("topo.graph 错误不应影响 status")
	}
}

// TestTopoTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestTopoTab_TickNilClient(t *testing.T) {
	tab := NewTopoTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}

// TestTopoTab_ViewErrorWithData 验证有数据但刷新失败时显示数据 + 错误。
func TestTopoTab_ViewErrorWithData(t *testing.T) {
	tab := &TopoTab{
		client: &tui.RPCClient{},
		status: &topoStatusResult{
			Version:      5,
			TransitCount: 2,
		},
		err: errors.New("刷新超时"),
	}
	view := tab.View()
	if !strings.Contains(view, "刷新失败") {
		t.Fatalf("有 err 时应显示刷新失败提示")
	}
	if !strings.Contains(view, "拓扑统计") {
		t.Fatalf("有数据时应仍然显示拓扑统计")
	}
}
