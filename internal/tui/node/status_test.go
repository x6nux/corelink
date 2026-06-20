package node

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/tui"
)

// ── StatusTab 单元测试 ──────────────────────────────────────────────────────

func TestStatusTab_Name(t *testing.T) {
	tab := NewStatusTab()
	if got := tab.Name(); got != "状态" {
		t.Fatalf("StatusTab.Name() = %q, want %q", got, "状态")
	}
}

func TestStatusTab_View_NilClient(t *testing.T) {
	// 无 client 时应显示未连接提示
	tab := NewStatusTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestStatusTab_View_Loading(t *testing.T) {
	// 有 client、loading=true、data=nil → 加载中
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{} // 非 nil，模拟已连接
	tab.loading = true
	tab.data = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestStatusTab_View_Error(t *testing.T) {
	// 有 client、err!=nil、data==nil → 错误提示
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("连接超时")

	view := tab.View()
	if !containsSubstr(view, "连接超时") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestStatusTab_View_WithData(t *testing.T) {
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeStatusResult{
		NodeID:          "node-abc-123",
		VIP:             "100.64.0.1",
		Role:            "node",
		Uptime:          3665, // 1h 1m 5s
		TopoVer:         5,
		TopoUpdatedAt:   time.Date(2025, 3, 10, 12, 0, 0, 0, time.UTC),
		Connected:       true,
		PeerCount:       3,
		ConnectionCount: 2,
		AvgRTTms:        42,
		IngressCount:    1,
		PortmapActive:   true,
	}

	view := tab.View()

	// 基本字段应渲染
	checks := []string{
		"节点状态",       // section header
		"100.64.0.1", // VIP
		"节点",         // node → 节点
		"已连接",        // connected
		"已激活",        // portmap active
		"42 ms",      // avg RTT
		"1 个",        // ingress count
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q，实际: %q", want, view)
		}
	}
}

func TestStatusTab_View_Disconnected(t *testing.T) {
	// 节点未连接到 controller 时 Connected=false
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeStatusResult{
		NodeID:    "node-x",
		VIP:       "100.64.0.2",
		Connected: false,
	}

	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Errorf("Connected=false 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestStatusTab_View_NoConnections_RTTDash(t *testing.T) {
	// ConnectionCount=0 时 RTT 显示 "-"
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeStatusResult{
		NodeID:          "node-x",
		ConnectionCount: 0,
		AvgRTTms:        0,
	}

	view := tab.View()
	// 0 个连接时 RTT 应显示 "-" 而非 "0 ms"
	if containsSubstr(view, "0 ms") {
		t.Errorf("ConnectionCount=0 时不应显示 '0 ms'")
	}
}

func TestStatusTab_View_PortmapInactive(t *testing.T) {
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeStatusResult{
		NodeID:        "node-x",
		PortmapActive: false,
	}

	view := tab.View()
	if !containsSubstr(view, "未激活") {
		t.Errorf("PortmapActive=false 时 View 应含 '未激活'")
	}
}

func TestStatusTab_View_ErrorWithStaleData(t *testing.T) {
	// 有 data 但也有 err（刷新失败但有旧数据）→ 应展示数据 + 错误提示
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeStatusResult{
		NodeID: "node-x",
		VIP:    "100.64.0.3",
	}
	tab.err = errors.New("刷新超时")

	view := tab.View()
	// 应同时展示数据和错误
	if !containsSubstr(view, "100.64.0.3") {
		t.Errorf("有旧数据时应继续展示 VIP")
	}
	if !containsSubstr(view, "刷新失败") {
		t.Errorf("有 err 时应展示刷新失败提示")
	}
}

func TestStatusTab_Update_RPCResult(t *testing.T) {
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	// 模拟成功 RPC 结果
	data := &nodeStatusResult{
		NodeID:    "node-rpc",
		VIP:       "100.64.0.5",
		Connected: true,
	}
	msg := tui.RPCResult{
		Method: "system.status",
		Result: data,
	}

	tab.Update(msg)
	if tab.data == nil {
		t.Fatal("成功 RPCResult 后 data 不应为 nil")
	}
	if tab.data.NodeID != "node-rpc" {
		t.Fatalf("data.NodeID = %q, want %q", tab.data.NodeID, "node-rpc")
	}
	if tab.loading {
		t.Fatal("成功 RPCResult 后 loading 应为 false")
	}
	if tab.err != nil {
		t.Fatalf("成功 RPCResult 后 err 应为 nil，实际: %v", tab.err)
	}
}

func TestStatusTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "system.status",
		Err:    fmt.Errorf("RPC 失败"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
	if tab.loading {
		t.Fatal("失败 RPCResult 后 loading 应为 false")
	}
}

func TestStatusTab_Update_IgnoresOtherMethods(t *testing.T) {
	tab := NewStatusTab()
	tab.client = &tui.RPCClient{}

	msg := tui.RPCResult{
		Method: "config.get",
		Err:    fmt.Errorf("不相关的方法"),
	}
	tab.Update(msg)
	// 不应影响 StatusTab 的状态
	if tab.err != nil {
		t.Fatal("不相关方法的 RPCResult 不应设置 err")
	}
}

func TestStatusTab_SetClient(t *testing.T) {
	tab := NewStatusTab()
	if tab.client != nil {
		t.Fatal("初始 client 应为 nil")
	}
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
