package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- CertsTab 基础 ----------

// TestCertsTab_Name 验证 Tab 显示名。
func TestCertsTab_Name(t *testing.T) {
	tab := NewCertsTab(nil)
	if got := tab.Name(); got != "证书" {
		t.Fatalf("Name() = %q，want '证书'", got)
	}
}

// TestCertsTab_ViewNilClient 验证无 client 时显示未连接。
func TestCertsTab_ViewNilClient(t *testing.T) {
	tab := NewCertsTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestCertsTab_ViewLoading 验证 loading 状态。
func TestCertsTab_ViewLoading(t *testing.T) {
	tab := &CertsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestCertsTab_ViewError 验证 error 无数据时显示错误。
func TestCertsTab_ViewError(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
		err:    errors.New("证书加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "证书加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestCertsTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestCertsTab_InitNilClient(t *testing.T) {
	tab := NewCertsTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// ---------- CertsTab 数据渲染 ----------

// TestCertsTab_ViewWithData 验证有数据时正常渲染。
func TestCertsTab_ViewWithData(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
		certs: []certDTO{
			{Serial: "001", NodeID: "n1", NotAfter: time.Now().Add(24 * time.Hour)},
			{Serial: "002", NodeID: "n2", NotAfter: time.Now(), Revoked: true},
		},
		caInfo: &caInfoResult{
			CAHash: "sha256:abcdef1234567890abcdef1234567890abcdef12",
		},
	}
	view := tab.View()
	checks := []string{"证书列表", "001", "002", "CA 信息"}
	for _, c := range checks {
		if !strings.Contains(view, c) {
			t.Fatalf("View 应包含 %q，got: %q", c, view)
		}
	}
}

// TestCertsTab_ViewWithoutCAInfo 验证无 CA 信息时仍能渲染。
func TestCertsTab_ViewWithoutCAInfo(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
		certs:  []certDTO{{Serial: "001", NodeID: "n1", NotAfter: time.Now()}},
	}
	view := tab.View()
	if !strings.Contains(view, "证书列表") {
		t.Fatalf("无 CA 信息时仍应渲染证书列表")
	}
}

// ---------- CertsTab 按键 ----------

// TestCertsTab_KeyJK 验证 j/k 移动光标。
func TestCertsTab_KeyJK(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
		certs:  []certDTO{{Serial: "001"}, {Serial: "002"}, {Serial: "003"}},
		cursor: 0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 1 {
		t.Fatalf("按 j 后 cursor 应为 1，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 0 {
		t.Fatalf("按 k 后 cursor 应为 0，got %d", tab.cursor)
	}

	// 边界检查
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 0 {
		t.Fatalf("k 到顶后 cursor 应保持 0，got %d", tab.cursor)
	}
}

// ---------- CertsTab RPC ----------

// TestCertsTab_HandleRPCCertsList 验证收到 certs.list 结果更新列表。
func TestCertsTab_HandleRPCCertsList(t *testing.T) {
	tab := &CertsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	certs := []certDTO{
		{Serial: "001", NodeID: "n1", NotAfter: time.Now()},
	}
	tab.Update(tui.RPCResult{
		Method: "certs.list",
		Result: &certs,
	})
	if len(tab.certs) != 1 {
		t.Fatalf("收到 certs.list 后应有 1 个证书，got %d", len(tab.certs))
	}
}

// TestCertsTab_HandleRPCCAInfo 验证收到 ca.info 结果更新 CA 信息。
func TestCertsTab_HandleRPCCAInfo(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
	}
	caInfo := &caInfoResult{CAHash: "sha256:abc"}
	tab.Update(tui.RPCResult{
		Method: "ca.info",
		Result: caInfo,
	})
	if tab.caInfo == nil || tab.caInfo.CAHash != "sha256:abc" {
		t.Fatalf("收到 ca.info 后 caInfo 应更新")
	}
}

// TestCertsTab_HandleRPCCAInfoError 验证 ca.info 错误不影响正常显示。
func TestCertsTab_HandleRPCCAInfoError(t *testing.T) {
	tab := &CertsTab{
		client: &tui.RPCClient{},
		certs:  []certDTO{{Serial: "001"}},
	}
	tab.Update(tui.RPCResult{
		Method: "ca.info",
		Err:    errors.New("CA 不可用"),
	})
	// ca.info 错误是非致命的，不应设置 tab.err
	if tab.err != nil {
		t.Fatalf("ca.info 错误不应设置 tab.err，got: %v", tab.err)
	}
}

// TestCertsTab_HandleRPCCertsListError 验证 certs.list 错误。
func TestCertsTab_HandleRPCCertsListError(t *testing.T) {
	tab := &CertsTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "certs.list",
		Err:    errors.New("证书列表失败"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestCertsTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestCertsTab_TickNilClient(t *testing.T) {
	tab := NewCertsTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}
