package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/x6nux/corelink/internal/tui"
)

// ---------- ConfigTab ----------

// TestConfigTab_Name 验证 Tab 显示名。
func TestConfigTab_Name(t *testing.T) {
	tab := NewConfigTab(nil)
	if got := tab.Name(); got != "配置" {
		t.Fatalf("Name() = %q，want '配置'", got)
	}
}

// TestConfigTab_ViewNilClient 验证无 client 时显示未连接。
func TestConfigTab_ViewNilClient(t *testing.T) {
	tab := NewConfigTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestConfigTab_ViewLoading 验证 loading 状态。
func TestConfigTab_ViewLoading(t *testing.T) {
	tab := &ConfigTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestConfigTab_ViewError 验证错误状态。
func TestConfigTab_ViewError(t *testing.T) {
	tab := &ConfigTab{
		client: &tui.RPCClient{},
		err:    errors.New("配置加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "配置加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestConfigTab_ViewWithData 验证有数据时正常渲染配置信息。
func TestConfigTab_ViewWithData(t *testing.T) {
	tab := &ConfigTab{
		client: &tui.RPCClient{},
		data: &configStatusResult{
			DBDSN:       "file:corelink.db",
			ListenAddr:  ":9090",
			AdminAddr:   ":9092",
			VirtualCIDR: "10.0.0.0/24",
			TLSMode:     "mtls",
			CASubject:   "CN=CoreLink CA",
			CAHash:      "abc123",
		},
	}
	view := tab.View()
	checks := []string{"运行配置", "file:corelink.db", ":9090", "10.0.0.0/24", "mtls", "abc123"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestConfigTab_UpdateRPCResult 验证收到 RPCResult 后更新数据。
func TestConfigTab_UpdateRPCResult(t *testing.T) {
	tab := &ConfigTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	data := &configStatusResult{VirtualCIDR: "10.1.0.0/16"}
	tab.Update(tui.RPCResult{
		Method: "config.status",
		Result: data,
	})
	if tab.data == nil || tab.data.VirtualCIDR != "10.1.0.0/16" {
		t.Fatalf("收到 RPCResult 后 data 应更新")
	}
	if tab.loading {
		t.Fatalf("收到 RPCResult 后 loading 应为 false")
	}
}

// TestConfigTab_UpdateRPCError 验证收到 RPC 错误。
func TestConfigTab_UpdateRPCError(t *testing.T) {
	tab := &ConfigTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "config.status",
		Err:    errors.New("rpc 断开"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestConfigTab_UpdateIgnoresOtherMethod 验证忽略不相关方法。
func TestConfigTab_UpdateIgnoresOtherMethod(t *testing.T) {
	tab := &ConfigTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "system.status",
		Result: &configStatusResult{VirtualCIDR: "ignored"},
	})
	if tab.data != nil {
		t.Fatalf("不应处理 system.status 的结果")
	}
}

// TestConfigTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestConfigTab_InitNilClient(t *testing.T) {
	tab := NewConfigTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// TestConfigTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestConfigTab_TickNilClient(t *testing.T) {
	tab := NewConfigTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}
