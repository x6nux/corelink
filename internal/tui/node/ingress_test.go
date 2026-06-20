package node

import (
	"errors"
	"fmt"
	"testing"

	"github.com/x6nux/corelink/internal/tui"
)

// ── IngressTab 单元测试 ─────────────────────────────────────────────────────

func TestIngressTab_Name(t *testing.T) {
	tab := NewIngressTab()
	if got := tab.Name(); got != "入口" {
		t.Fatalf("IngressTab.Name() = %q, want %q", got, "入口")
	}
}

func TestIngressTab_View_NilClient(t *testing.T) {
	tab := NewIngressTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestIngressTab_View_Loading(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true
	tab.items = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestIngressTab_View_Error(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("入口获取失败")
	tab.items = nil

	view := tab.View()
	if !containsSubstr(view, "入口获取失败") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestIngressTab_View_EmptyList(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeIngressDTO{}

	view := tab.View()
	if !containsSubstr(view, "无数据") {
		t.Errorf("空列表 View 应含 '无数据'")
	}
}

func TestIngressTab_View_WithData(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeIngressDTO{
		{
			Host:       "203.0.113.1",
			Port:       7443,
			Source:     "stun",
			Confidence: 80,
			NATType:    "Full Cone",
		},
		{
			Host:       "192.168.1.1",
			Port:       51820,
			Source:     "upnp",
			Confidence: 95,
			NATType:    "None",
		},
	}

	view := tab.View()
	checks := []string{
		"203.0.113.1:7443",
		"stun",
		"80",
		"Full Cone",
		"192.168.1.1:51820",
		"upnp",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
}

func TestIngressTab_View_ErrorWithStaleData(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.items = []nodeIngressDTO{
		{Host: "10.0.0.1", Port: 1234, Source: "stale"},
	}
	tab.err = errors.New("刷新超时")

	view := tab.View()
	if !containsSubstr(view, "10.0.0.1:1234") {
		t.Errorf("有旧数据时应继续展示")
	}
	if !containsSubstr(view, "刷新失败") {
		t.Errorf("有 err 时应展示刷新失败提示")
	}
}

func TestIngressTab_Update_RPCResult(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	items := []nodeIngressDTO{
		{Host: "1.2.3.4", Port: 7443, Source: "stun"},
	}
	msg := tui.RPCResult{
		Method: "ingress.list",
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

func TestIngressTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewIngressTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "ingress.list",
		Err:    fmt.Errorf("RPC 失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
}

func TestIngressTab_SetClient(t *testing.T) {
	tab := NewIngressTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
