package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/tui"
)

// ---------- DashboardTab ----------

// TestDashboardTab_Name 验证 Tab 显示名。
func TestDashboardTab_Name(t *testing.T) {
	tab := NewDashboardTab(nil)
	if got := tab.Name(); got != "仪表盘" {
		t.Fatalf("Name() = %q，want '仪表盘'", got)
	}
}

// TestDashboardTab_ViewNilClient 验证无 client 时显示未连接提示。
func TestDashboardTab_ViewNilClient(t *testing.T) {
	tab := NewDashboardTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestDashboardTab_ViewLoading 验证 loading 状态显示加载中。
func TestDashboardTab_ViewLoading(t *testing.T) {
	tab := &DashboardTab{
		client:  &tui.RPCClient{}, // 非 nil 但不会实际调用
		loading: true,
		data:    nil,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态 View 应包含 '加载中'，got: %q", view)
	}
}

// TestDashboardTab_ViewError 验证 error 且无数据时显示错误。
func TestDashboardTab_ViewError(t *testing.T) {
	tab := &DashboardTab{
		client: &tui.RPCClient{},
		err:    errors.New("测试错误"),
		data:   nil,
	}
	view := tab.View()
	if !strings.Contains(view, "测试错误") {
		t.Fatalf("error 状态 View 应包含错误文本，got: %q", view)
	}
}

// TestDashboardTab_ViewWithData 验证有数据时正常渲染，包含关键指标。
func TestDashboardTab_ViewWithData(t *testing.T) {
	tab := &DashboardTab{
		client: &tui.RPCClient{},
		data: &systemStatusResult{
			UptimeSeconds: 3661,
			Version:       "v1.0.0",
			NodeCount:     5,
			OnlineCount:   3,
			TopoVersion:   10,
			TopoRecompute: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			TransitCount:  2,
			LeafCount:     3,
			CertCount:     4,
			KeyCount:      2,
		},
	}
	view := tab.View()
	checks := []string{"系统概览", "v1.0.0", "3 在线", "中转节点", "叶子节点"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestDashboardTab_UpdateRPCResult 验证收到 RPCResult 后更新数据。
func TestDashboardTab_UpdateRPCResult(t *testing.T) {
	tab := &DashboardTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	data := &systemStatusResult{
		Version:     "v2.0.0",
		NodeCount:   10,
		OnlineCount: 8,
	}
	tab.Update(tui.RPCResult{
		Method: "system.status",
		Result: data,
	})
	if tab.data == nil {
		t.Fatalf("收到 RPCResult 后 data 不应为 nil")
	}
	if tab.data.Version != "v2.0.0" {
		t.Fatalf("data.Version = %q，want 'v2.0.0'", tab.data.Version)
	}
	if tab.loading {
		t.Fatalf("收到 RPCResult 后 loading 应为 false")
	}
}

// TestDashboardTab_UpdateRPCError 验证收到 RPC 错误后设置 err。
func TestDashboardTab_UpdateRPCError(t *testing.T) {
	tab := &DashboardTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "system.status",
		Err:    errors.New("rpc 失败"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestDashboardTab_UpdateIgnoresOtherMethod 验证忽略不相关的 RPC 方法。
func TestDashboardTab_UpdateIgnoresOtherMethod(t *testing.T) {
	tab := &DashboardTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "nodes.list",
		Result: &systemStatusResult{Version: "ignore"},
	})
	// loading 状态不应被其他方法改变
	if tab.data != nil {
		t.Fatalf("不应处理 nodes.list 的结果")
	}
}

// TestDashboardTab_ViewWithNodes 验证节点列表渲染。
func TestDashboardTab_ViewWithNodes(t *testing.T) {
	tab := &DashboardTab{
		client: &tui.RPCClient{},
		data: &systemStatusResult{
			NodeCount:   2,
			OnlineCount: 1,
			Nodes: []nodeStatusEntry{
				{ID: "node-1", Name: "host-a", VIP: "10.0.0.1", Role: "node", Online: true},
				{ID: "node-2", Name: "host-b", VIP: "10.0.0.2", Role: "node", Online: false},
			},
		},
	}
	view := tab.View()
	if !strings.Contains(view, "节点列表") {
		t.Fatalf("有节点数据时应显示节点列表，got: %q", view)
	}
	if !strings.Contains(view, "host-a") {
		t.Fatalf("View 应包含节点主机名 host-a")
	}
}

// TestDashboardTab_ViewErrorWithData 验证有数据但刷新失败时显示数据 + 错误提示。
func TestDashboardTab_ViewErrorWithData(t *testing.T) {
	tab := &DashboardTab{
		client: &tui.RPCClient{},
		data: &systemStatusResult{
			Version: "v1.0.0",
		},
		err: errors.New("刷新超时"),
	}
	view := tab.View()
	// 应同时显示数据和错误
	if !strings.Contains(view, "v1.0.0") {
		t.Fatalf("有数据时应仍然显示数据")
	}
	if !strings.Contains(view, "刷新失败") {
		t.Fatalf("有 err 时应显示刷新失败提示")
	}
}

// TestDashboardTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestDashboardTab_InitNilClient(t *testing.T) {
	tab := NewDashboardTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}
