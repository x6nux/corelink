package controller

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- ACLTab 基础 ----------

// TestACLTab_Name 验证 Tab 显示名。
func TestACLTab_Name(t *testing.T) {
	tab := NewACLTab(nil)
	if got := tab.Name(); got != "ACL" {
		t.Fatalf("Name() = %q，want 'ACL'", got)
	}
}

// TestACLTab_ViewNilClient 验证无 client 时显示未连接。
func TestACLTab_ViewNilClient(t *testing.T) {
	tab := NewACLTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestACLTab_ViewLoading 验证 loading 状态。
func TestACLTab_ViewLoading(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestACLTab_ViewError 验证 error 无数据时显示错误。
func TestACLTab_ViewError(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		err:    errors.New("ACL 加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "ACL 加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestACLTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestACLTab_InitNilClient(t *testing.T) {
	tab := NewACLTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// TestACLTab_InputFocused 验证编辑模式时 InputFocused 返回 true。
func TestACLTab_InputFocused(t *testing.T) {
	tab := &ACLTab{view: aclViewDisplay}
	if tab.InputFocused() {
		t.Fatalf("展示视图时 InputFocused 应为 false")
	}
	tab.view = aclViewEdit
	if !tab.InputFocused() {
		t.Fatalf("编辑视图时 InputFocused 应为 true")
	}
}

// ---------- ACLTab 展示视图 ----------

// TestACLTab_ViewDisplay 验证有策略数据时正常渲染。
func TestACLTab_ViewDisplay(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		policy: &aclDTO{
			Version:  3,
			Document: "allow all\ndeny 10.0.0.0/8",
		},
	}
	view := tab.View()
	if !strings.Contains(view, "ACL 策略") {
		t.Fatalf("展示视图应包含 'ACL 策略'")
	}
	if !strings.Contains(view, "版本 3") {
		t.Fatalf("展示视图应包含版本号")
	}
}

// TestACLTab_ViewDisplayNoPolicy 验证无策略时显示提示。
func TestACLTab_ViewDisplayNoPolicy(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		policy: nil,
	}
	// 需绕过 loading 检查
	tab.loading = false
	tab.err = nil
	// 直接调用 renderDisplay
	view := tab.renderDisplay()
	if !strings.Contains(view, "暂无策略") {
		t.Fatalf("无策略时应显示提示，got: %q", view)
	}
}

// ---------- ACLTab 按键处理 ----------

// TestACLTab_KeyE 验证 e 键切换到编辑视图。
func TestACLTab_KeyE(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		policy: &aclDTO{Document: "original"},
		view:   aclViewDisplay,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if tab.view != aclViewEdit {
		t.Fatalf("按 e 后应切换到编辑视图，got view=%d", tab.view)
	}
	if tab.editBuf != "original" {
		t.Fatalf("编辑缓冲区应初始化为当前策略，got %q", tab.editBuf)
	}
}

// TestACLTab_KeyH 验证 h 键请求历史（nil client 不发 RPC）。
func TestACLTab_KeyH(t *testing.T) {
	tab := &ACLTab{
		client: nil,
		view:   aclViewDisplay,
	}
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if cmd != nil {
		t.Fatalf("nil client 时按 h 应返回 nil cmd")
	}
}

// TestACLTab_ScrollJK 验证展示视图 j/k 滚动。
func TestACLTab_ScrollJK(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		policy: &aclDTO{Document: "line1\nline2\nline3"},
		view:   aclViewDisplay,
		scroll: 0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.scroll != 1 {
		t.Fatalf("按 j 后 scroll 应为 1，got %d", tab.scroll)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("按 k 后 scroll 应为 0，got %d", tab.scroll)
	}

	// k 到顶不越界
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("scroll 不应为负，got %d", tab.scroll)
	}
}

// ---------- ACLTab 编辑视图 ----------

// TestACLTab_EditInput 验证编辑模式文本输入。
func TestACLTab_EditInput(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		view:    aclViewEdit,
		editBuf: "",
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if tab.editBuf != "xy" {
		t.Fatalf("输入后 editBuf 应为 'xy'，got %q", tab.editBuf)
	}

	// 回车插入换行
	tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if tab.editBuf != "xy\n" {
		t.Fatalf("Enter 后 editBuf 应包含换行，got %q", tab.editBuf)
	}

	// 退格删除
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.editBuf != "xy" {
		t.Fatalf("Backspace 后 editBuf 应为 'xy'，got %q", tab.editBuf)
	}
}

// TestACLTab_EditEsc 验证编辑模式 Esc 返回展示。
func TestACLTab_EditEsc(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewEdit,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != aclViewDisplay {
		t.Fatalf("Esc 后应返回展示视图，got view=%d", tab.view)
	}
}

// TestACLTab_EditCtrlS 验证 Ctrl+S 切换到保存确认。
func TestACLTab_EditCtrlS(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		view:    aclViewEdit,
		editBuf: "new policy",
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if tab.view != aclViewConfirmSave {
		t.Fatalf("Ctrl+S 后应切换到保存确认视图，got view=%d", tab.view)
	}
}

// TestACLTab_ConfirmSaveN 验证保存确认按 n 取消。
func TestACLTab_ConfirmSaveN(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewConfirmSave,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if tab.view != aclViewEdit {
		t.Fatalf("按 n 后应返回编辑视图，got view=%d", tab.view)
	}
}

// TestACLTab_ConfirmSaveEsc 验证保存确认按 Esc 取消。
func TestACLTab_ConfirmSaveEsc(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewConfirmSave,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != aclViewEdit {
		t.Fatalf("Esc 后应返回编辑视图，got view=%d", tab.view)
	}
}

// ---------- ACLTab 历史视图 ----------

// TestACLTab_HistoryEsc 验证历史视图 Esc 返回展示。
func TestACLTab_HistoryEsc(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewHistory,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != aclViewDisplay {
		t.Fatalf("历史视图 Esc 后应返回展示视图，got view=%d", tab.view)
	}
}

// TestACLTab_HistoryScrollJK 验证历史视图 j/k 滚动。
func TestACLTab_HistoryScrollJK(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewHistory,
		scroll: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.scroll != 1 {
		t.Fatalf("按 j 后 scroll 应为 1")
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.scroll != 0 {
		t.Fatalf("按 k 后 scroll 应为 0")
	}
}

// ---------- ACLTab RPC ----------

// TestACLTab_HandleRPCACLGet 验证收到 acl.get 结果更新策略。
func TestACLTab_HandleRPCACLGet(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	policy := &aclDTO{Version: 5, Document: "allow *"}
	tab.Update(tui.RPCResult{
		Method: "acl.get",
		Result: policy,
	})
	if tab.policy == nil || tab.policy.Version != 5 {
		t.Fatalf("收到 acl.get 后 policy 应更新")
	}
}

// TestACLTab_HandleRPCACLSet 验证收到 acl.set 结果更新并返回展示视图。
func TestACLTab_HandleRPCACLSet(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewConfirmSave,
	}
	policy := &aclDTO{Version: 6, Document: "deny all"}
	tab.Update(tui.RPCResult{
		Method: "acl.set",
		Result: policy,
	})
	if tab.view != aclViewDisplay {
		t.Fatalf("收到 acl.set 后应返回展示视图")
	}
	if tab.policy == nil || tab.policy.Version != 6 {
		t.Fatalf("收到 acl.set 后 policy 应更新")
	}
}

// TestACLTab_HandleRPCACLHistory 验证收到 acl.history 结果切换到历史视图。
func TestACLTab_HandleRPCACLHistory(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	history := []aclDTO{
		{Version: 1, Author: "admin"},
		{Version: 2, Author: "admin"},
	}
	tab.Update(tui.RPCResult{
		Method: "acl.history",
		Result: &history,
	})
	if tab.view != aclViewHistory {
		t.Fatalf("收到 acl.history 后应切换到历史视图")
	}
	if len(tab.history) != 2 {
		t.Fatalf("history 应有 2 条记录，got %d", len(tab.history))
	}
}

// TestACLTab_HandleRPCError 验证 RPC 错误。
func TestACLTab_HandleRPCError(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "acl.get",
		Err:    errors.New("超时"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestACLTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestACLTab_TickNilClient(t *testing.T) {
	tab := NewACLTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}

// TestACLTab_RenderEdit 验证编辑视图渲染。
func TestACLTab_RenderEdit(t *testing.T) {
	tab := &ACLTab{
		client:  &tui.RPCClient{},
		view:    aclViewEdit,
		editBuf: "test policy content",
	}
	view := tab.View()
	if !strings.Contains(view, "编辑 ACL") {
		t.Fatalf("编辑视图应包含标题")
	}
	if !strings.Contains(view, "test policy content") {
		t.Fatalf("编辑视图应包含编辑内容")
	}
}

// TestACLTab_RenderConfirmSave 验证保存确认视图渲染。
func TestACLTab_RenderConfirmSave(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewConfirmSave,
	}
	view := tab.View()
	if !strings.Contains(view, "确认保存") {
		t.Fatalf("保存确认视图应包含 '确认保存'")
	}
}

// TestACLTab_RenderHistory 验证历史视图渲染。
func TestACLTab_RenderHistory(t *testing.T) {
	tab := &ACLTab{
		client: &tui.RPCClient{},
		view:   aclViewHistory,
		history: []aclDTO{
			{Version: 1, Author: "admin"},
		},
	}
	view := tab.View()
	if !strings.Contains(view, "策略历史") {
		t.Fatalf("历史视图应包含 '策略历史'")
	}
}
