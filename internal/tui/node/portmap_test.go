package node

import (
	"errors"
	"fmt"
	"testing"

	"github.com/x6nux/corelink/internal/tui"
)

// ── PortmapTab 单元测试 ─────────────────────────────────────────────────────

func TestPortmapTab_Name(t *testing.T) {
	tab := NewPortmapTab()
	if got := tab.Name(); got != "Portmap" {
		t.Fatalf("PortmapTab.Name() = %q, want %q", got, "Portmap")
	}
}

func TestPortmapTab_View_NilClient(t *testing.T) {
	tab := NewPortmapTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestPortmapTab_View_Loading(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true
	tab.mappings = nil
	tab.status = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestPortmapTab_View_Error(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("portmap 获取失败")
	tab.mappings = nil
	tab.status = nil

	view := tab.View()
	if !containsSubstr(view, "portmap 获取失败") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestPortmapTab_View_WithStatusAndMappings(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.status = &nodePortmapStatusDTO{
		Active:       true,
		ManagedCount: 3,
	}
	tab.mappings = []nodeMappingDTO{
		{
			Protocol:     "UDP",
			ExternalIP:   "203.0.113.1",
			ExternalPort: 51820,
			InternalPort: 51820,
			Transport:    "UPnP",
			TTL:          "7200s",
			RenewIn:      "3500s",
		},
	}

	view := tab.View()
	checks := []string{
		"已激活",
		"UDP",
		"203.0.113.1",
		"51820",
		"UPnP",
		"7200s",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
}

func TestPortmapTab_View_Inactive(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.status = &nodePortmapStatusDTO{
		Active:       false,
		ManagedCount: 0,
	}
	tab.mappings = []nodeMappingDTO{}

	view := tab.View()
	if !containsSubstr(view, "未激活") {
		t.Errorf("Active=false 时 View 应含 '未激活'")
	}
}

func TestPortmapTab_View_NoStatus(t *testing.T) {
	// 只有 mappings、无 status → 不应 panic
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.status = nil
	tab.mappings = []nodeMappingDTO{
		{Protocol: "TCP", ExternalIP: "1.2.3.4", ExternalPort: 80, InternalPort: 8080},
	}

	view := tab.View()
	if !containsSubstr(view, "TCP") {
		t.Errorf("无 status 时 mapping 数据仍应展示")
	}
}

func TestPortmapTab_View_ErrorWithStaleData(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.status = &nodePortmapStatusDTO{Active: true, ManagedCount: 1}
	tab.mappings = []nodeMappingDTO{
		{Protocol: "UDP", ExternalIP: "1.2.3.4"},
	}
	tab.err = errors.New("刷新超时")

	view := tab.View()
	if !containsSubstr(view, "1.2.3.4") {
		t.Errorf("有旧数据时应继续展示")
	}
	if !containsSubstr(view, "刷新失败") {
		t.Errorf("有 err 时应展示刷新失败提示")
	}
}

func TestPortmapTab_Update_RPCResult_List(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	items := []nodeMappingDTO{
		{Protocol: "UDP", ExternalPort: 51820},
	}
	msg := tui.RPCResult{
		Method: "portmap.list",
		Result: &items,
	}

	tab.Update(msg)
	if len(tab.mappings) != 1 {
		t.Fatalf("成功 portmap.list 后 mappings 应有 1 项，实际: %d", len(tab.mappings))
	}
	if tab.loading {
		t.Fatal("成功 portmap.list 后 loading 应为 false")
	}
}

func TestPortmapTab_Update_RPCResult_Status(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}

	status := &nodePortmapStatusDTO{Active: true, ManagedCount: 2}
	msg := tui.RPCResult{
		Method: "portmap.status",
		Result: status,
	}

	tab.Update(msg)
	if tab.status == nil {
		t.Fatal("成功 portmap.status 后 status 不应为 nil")
	}
	if !tab.status.Active {
		t.Fatal("status.Active 应为 true")
	}
}

func TestPortmapTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewPortmapTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "portmap.list",
		Err:    fmt.Errorf("RPC 失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
}

func TestPortmapTab_SetClient(t *testing.T) {
	tab := NewPortmapTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
