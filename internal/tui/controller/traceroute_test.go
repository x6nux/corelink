package controller

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- TracerouteTab 基础 ----------

// TestTracerouteTab_Name 验证 Tab 显示名。
func TestTracerouteTab_Name(t *testing.T) {
	tab := NewTracerouteTab(nil)
	if got := tab.Name(); got != "路由追踪" {
		t.Fatalf("Name() = %q，want '路由追踪'", got)
	}
}

// TestTracerouteTab_ViewNilClient 验证无 client 时显示未连接。
func TestTracerouteTab_ViewNilClient(t *testing.T) {
	tab := NewTracerouteTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestTracerouteTab_InitReturnsNil 验证 Init 返回 nil（路由追踪不自动发请求）。
func TestTracerouteTab_InitReturnsNil(t *testing.T) {
	tab := NewTracerouteTab(&tui.RPCClient{})
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("Init 应返回 nil cmd（路由追踪不自动发请求）")
	}
}

// TestTracerouteTab_InputFocused 验证始终有输入焦点。
func TestTracerouteTab_InputFocused(t *testing.T) {
	tab := NewTracerouteTab(nil)
	if !tab.InputFocused() {
		t.Fatalf("TracerouteTab 应始终返回 InputFocused=true")
	}
}

// ---------- TracerouteTab 输入 ----------

// TestTracerouteTab_InputSrc 验证源节点 ID 输入。
func TestTracerouteTab_InputSrc(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldSrc,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if tab.src != "abc" {
		t.Fatalf("输入后 src 应为 'abc'，got %q", tab.src)
	}
}

// TestTracerouteTab_InputDst 验证目标节点 ID 输入。
func TestTracerouteTab_InputDst(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldDst,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if tab.dst != "xy" {
		t.Fatalf("输入后 dst 应为 'xy'，got %q", tab.dst)
	}
}

// TestTracerouteTab_BackspaceSrc 验证源字段退格。
func TestTracerouteTab_BackspaceSrc(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldSrc,
		src:    "abc",
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.src != "ab" {
		t.Fatalf("退格后 src 应为 'ab'，got %q", tab.src)
	}
}

// TestTracerouteTab_BackspaceDst 验证目标字段退格。
func TestTracerouteTab_BackspaceDst(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldDst,
		dst:    "xy",
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.dst != "x" {
		t.Fatalf("退格后 dst 应为 'x'，got %q", tab.dst)
	}
}

// TestTracerouteTab_BackspaceEmpty 验证空字段退格不崩溃。
func TestTracerouteTab_BackspaceEmpty(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldSrc,
		src:    "",
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.src != "" {
		t.Fatalf("空字段退格后应仍为空")
	}
}

// TestTracerouteTab_FieldSwitchNoResult 验证无结果时 up/down 切换字段。
func TestTracerouteTab_FieldSwitchNoResult(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		field:  traceFieldSrc,
		result: nil,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyDown})
	if tab.field != traceFieldDst {
		t.Fatalf("无结果时 down 应切换到 dst 字段，got field=%d", tab.field)
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyUp})
	if tab.field != traceFieldSrc {
		t.Fatalf("无结果时 up 应切换回 src 字段，got field=%d", tab.field)
	}
}

// TestTracerouteTab_EnterEmptyFields 验证源或目标为空时 enter 不发请求。
func TestTracerouteTab_EnterEmptyFields(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		src:    "",
		dst:    "n2",
	}
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("src 为空时 enter 应返回 nil cmd")
	}

	tab.src = "n1"
	tab.dst = ""
	_, cmd = tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("dst 为空时 enter 应返回 nil cmd")
	}
}

// TestTracerouteTab_EnterNilClient 验证 nil client 时 enter 不崩溃。
func TestTracerouteTab_EnterNilClient(t *testing.T) {
	tab := &TracerouteTab{
		client: nil,
		src:    "n1",
		dst:    "n2",
	}
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("nil client 时 enter 应返回 nil cmd")
	}
}

// ---------- TracerouteTab 路径切换 ----------

// TestTracerouteTab_PathSwitch 验证有结果时 up/down 切换路径。
func TestTracerouteTab_PathSwitch(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		result: &tracerouteResult{
			Paths: []traceroutePath{
				{Hops: []tracerouteHop{{NodeID: "n1"}}, TotalHops: 1},
				{Hops: []tracerouteHop{{NodeID: "n2"}}, TotalHops: 1},
				{Hops: []tracerouteHop{{NodeID: "n3"}}, TotalHops: 1},
			},
		},
		pathIndex: 0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyDown})
	if tab.pathIndex != 1 {
		t.Fatalf("down 后 pathIndex 应为 1，got %d", tab.pathIndex)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyDown})
	tab.Update(tea.KeyMsg{Type: tea.KeyDown}) // 到底
	if tab.pathIndex != 2 {
		t.Fatalf("down 到底后 pathIndex 应保持 2，got %d", tab.pathIndex)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyUp})
	if tab.pathIndex != 1 {
		t.Fatalf("up 后 pathIndex 应为 1，got %d", tab.pathIndex)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyUp})
	tab.Update(tea.KeyMsg{Type: tea.KeyUp}) // 到顶
	if tab.pathIndex != 0 {
		t.Fatalf("up 到顶后 pathIndex 应保持 0，got %d", tab.pathIndex)
	}
}

// ---------- TracerouteTab 渲染 ----------

// TestTracerouteTab_ViewInputForm 验证输入表单渲染。
func TestTracerouteTab_ViewInputForm(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		src:    "node-a",
		dst:    "node-b",
		field:  traceFieldSrc,
	}
	view := tab.View()
	checks := []string{"路由追踪", "源节点", "目标节点", "node-a", "node-b"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestTracerouteTab_ViewLoading 验证查询中状态。
func TestTracerouteTab_ViewLoading(t *testing.T) {
	tab := &TracerouteTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "查询中") {
		t.Fatalf("loading 状态应包含 '查询中'，got: %q", view)
	}
}

// TestTracerouteTab_ViewError 验证查询错误渲染。
func TestTracerouteTab_ViewError(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		err:    errors.New("节点不存在"),
	}
	view := tab.View()
	if !strings.Contains(view, "节点不存在") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestTracerouteTab_ViewNoPaths 验证无路径时的提示。
func TestTracerouteTab_ViewNoPaths(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		result: &tracerouteResult{Paths: nil},
	}
	view := tab.View()
	if !strings.Contains(view, "未找到路径") {
		t.Fatalf("无路径时应显示提示，got: %q", view)
	}
}

// TestTracerouteTab_ViewWithPaths 验证有路径时渲染。
func TestTracerouteTab_ViewWithPaths(t *testing.T) {
	tab := &TracerouteTab{
		client: &tui.RPCClient{},
		result: &tracerouteResult{
			Paths: []traceroutePath{
				{
					Hops: []tracerouteHop{
						{NodeID: "relay-1", IngressID: "ing-1", Host: "1.2.3.4", Port: 51820},
						{NodeID: "agent-2", IngressID: "ing-2", Host: "5.6.7.8", Port: 51821},
					},
					TotalHops: 2,
					Active:    true,
				},
			},
		},
		pathIndex: 0,
	}
	view := tab.View()
	checks := []string{"1 条路径", "relay-1", "agent-2", "1.2.3.4:51820", "活跃"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("路径 View 应包含 %q，got: %q", c, view)
		}
	}
}

// ---------- TracerouteTab RPC ----------

// TestTracerouteTab_HandleRPCResult 验证收到追踪结果更新。
func TestTracerouteTab_HandleRPCResult(t *testing.T) {
	tab := &TracerouteTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	result := &tracerouteResult{
		Paths: []traceroutePath{
			{TotalHops: 3},
		},
	}
	tab.Update(tui.RPCResult{
		Method: "topo.traceroute",
		Result: result,
	})
	if tab.result == nil || len(tab.result.Paths) != 1 {
		t.Fatalf("收到 topo.traceroute 后 result 应更新")
	}
	if tab.pathIndex != 0 {
		t.Fatalf("收到结果后 pathIndex 应重置为 0，got %d", tab.pathIndex)
	}
	if tab.loading {
		t.Fatalf("收到结果后 loading 应为 false")
	}
}

// TestTracerouteTab_HandleRPCError 验证 RPC 错误。
func TestTracerouteTab_HandleRPCError(t *testing.T) {
	tab := &TracerouteTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "topo.traceroute",
		Err:    errors.New("追踪失败"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestTracerouteTab_HandleRPCIgnoresOtherMethod 验证忽略不相关方法。
func TestTracerouteTab_HandleRPCIgnoresOtherMethod(t *testing.T) {
	tab := &TracerouteTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Result: &tracerouteResult{},
	})
	if tab.result != nil {
		t.Fatalf("不应处理 nodes.list 的结果")
	}
}
