package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/tui"
)

// ---------- KeysTab 基础 ----------

// TestKeysTab_Name 验证 Tab 显示名。
func TestKeysTab_Name(t *testing.T) {
	tab := NewKeysTab(nil)
	if got := tab.Name(); got != "密钥" {
		t.Fatalf("Name() = %q，want '密钥'", got)
	}
}

// TestKeysTab_ViewNilClient 验证无 client 时显示未连接。
func TestKeysTab_ViewNilClient(t *testing.T) {
	tab := NewKeysTab(nil)
	view := tab.View()
	if !strings.Contains(view, "未连接") {
		t.Fatalf("nil client 时 View 应包含 '未连接'，got: %q", view)
	}
}

// TestKeysTab_ViewLoading 验证 loading 状态。
func TestKeysTab_ViewLoading(t *testing.T) {
	tab := &KeysTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	view := tab.View()
	if !strings.Contains(view, "加载中") {
		t.Fatalf("loading 状态应包含 '加载中'，got: %q", view)
	}
}

// TestKeysTab_ViewError 验证 error 无数据时显示错误。
func TestKeysTab_ViewError(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		err:    errors.New("密钥加载失败"),
	}
	view := tab.View()
	if !strings.Contains(view, "密钥加载失败") {
		t.Fatalf("error 状态应包含错误文本，got: %q", view)
	}
}

// TestKeysTab_InitNilClient 验证 nil client 时 Init 返回 nil。
func TestKeysTab_InitNilClient(t *testing.T) {
	tab := NewKeysTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("nil client 时 Init 应返回 nil cmd")
	}
}

// ---------- KeysTab 列表视图 ----------

// TestKeysTab_ViewList 验证有数据时渲染列表。
func TestKeysTab_ViewList(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys: []keyDTO{
			{Key: "key-abc123", Reusable: true, Tag: "dev", CreatedAt: time.Now()},
			{Key: "key-def456", Reusable: false, Tag: "prod", Consumed: true, CreatedAt: time.Now()},
		},
	}
	view := tab.View()
	if !strings.Contains(view, "dev") || !strings.Contains(view, "prod") {
		t.Fatalf("列表 View 应包含密钥标签，got: %q", view)
	}
}

// ---------- KeysTab 按键处理 ----------

// TestKeysTab_KeyJK 验证 j/k 移动光标。
func TestKeysTab_KeyJK(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "k1"}, {Key: "k2"}, {Key: "k3"}},
		cursor: 0,
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if tab.cursor != 1 {
		t.Fatalf("按 j 后 cursor 应为 1，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // 到底
	if tab.cursor != 2 {
		t.Fatalf("按 j 到底后 cursor 应保持 2，got %d", tab.cursor)
	}

	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if tab.cursor != 1 {
		t.Fatalf("按 k 后 cursor 应为 1，got %d", tab.cursor)
	}
}

// TestKeysTab_KeyC 验证 c 键切换到创建视图。
func TestKeysTab_KeyC(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "k1"}},
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if tab.view != keysViewCreate {
		t.Fatalf("按 c 后应切换到创建视图，got view=%d", tab.view)
	}
}

// TestKeysTab_KeyD 验证 d 键切换到吊销确认视图。
func TestKeysTab_KeyD(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "k1"}},
		cursor: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if tab.view != keysViewConfirmRevoke {
		t.Fatalf("按 d 后应切换到吊销确认视图，got view=%d", tab.view)
	}
}

// TestKeysTab_InputFocused 验证创建表单活跃时 InputFocused 返回 true。
func TestKeysTab_InputFocused(t *testing.T) {
	tab := &KeysTab{view: keysViewList}
	if tab.InputFocused() {
		t.Fatalf("列表视图时 InputFocused 应为 false")
	}
	tab.view = keysViewCreate
	if !tab.InputFocused() {
		t.Fatalf("创建视图时 InputFocused 应为 true")
	}
}

// ---------- KeysTab 创建表单 ----------

// TestKeysTab_CreateFormTagInput 验证创建表单标签输入。
func TestKeysTab_CreateFormTagInput(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
		form:   keysCreateForm{field: 0}, // tag 字段
	}
	// 输入 "abc"
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if tab.form.tag != "abc" {
		t.Fatalf("输入后 tag 应为 'abc'，got %q", tab.form.tag)
	}

	// 退格
	tab.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if tab.form.tag != "ab" {
		t.Fatalf("退格后 tag 应为 'ab'，got %q", tab.form.tag)
	}
}

// TestKeysTab_CreateFormReusableToggle 验证创建表单可复用切换。
func TestKeysTab_CreateFormReusableToggle(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
		form:   keysCreateForm{field: 1}, // reusable 字段
	}
	if tab.form.reusable {
		t.Fatalf("初始应为不可复用")
	}
	tab.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !tab.form.reusable {
		t.Fatalf("空格后应为可复用")
	}
	tab.Update(tea.KeyMsg{Type: tea.KeySpace})
	if tab.form.reusable {
		t.Fatalf("再次空格后应切换回不可复用")
	}
}

// TestKeysTab_CreateFormTTLInput 验证创建表单 TTL 只接受数字。
func TestKeysTab_CreateFormTTLInput(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
		form:   keysCreateForm{field: 2}, // TTL 字段
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6")})
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}) // 非数字应被忽略
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("0")})
	if tab.form.ttl != "360" {
		t.Fatalf("TTL 应为 '360'（忽略非数字），got %q", tab.form.ttl)
	}
}

// TestKeysTab_CreateFormFieldSwitch 验证 Tab/Shift+Tab 切换字段。
func TestKeysTab_CreateFormFieldSwitch(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
		form:   keysCreateForm{field: 0},
	}
	// Tab 向下
	tab.Update(tea.KeyMsg{Type: tea.KeyTab})
	if tab.form.field != 1 {
		t.Fatalf("Tab 后 field 应为 1，got %d", tab.form.field)
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyTab})
	if tab.form.field != 2 {
		t.Fatalf("Tab 后 field 应为 2，got %d", tab.form.field)
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyTab})
	if tab.form.field != 0 {
		t.Fatalf("Tab 循环后 field 应回到 0，got %d", tab.form.field)
	}
}

// TestKeysTab_CreateFormEsc 验证 Esc 退出创建表单。
func TestKeysTab_CreateFormEsc(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != keysViewList {
		t.Fatalf("Esc 后应返回列表视图，got view=%d", tab.view)
	}
}

// TestKeysTab_RenderCreate 验证创建视图渲染。
func TestKeysTab_RenderCreate(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		view:   keysViewCreate,
		form:   keysCreateForm{tag: "test", field: 0},
	}
	view := tab.View()
	if !strings.Contains(view, "创建注册密钥") {
		t.Fatalf("创建视图应包含标题，got: %q", view)
	}
	if !strings.Contains(view, "test") {
		t.Fatalf("创建视图应包含已输入的 tag")
	}
}

// ---------- KeysTab 吊销确认 ----------

// TestKeysTab_ConfirmRevokeN 验证吊销确认时按 n 取消。
func TestKeysTab_ConfirmRevokeN(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "k1"}},
		view:   keysViewConfirmRevoke,
		cursor: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if tab.view != keysViewList {
		t.Fatalf("按 n 后应返回列表视图")
	}
}

// TestKeysTab_ConfirmRevokeEsc 验证吊销确认时按 esc 取消。
func TestKeysTab_ConfirmRevokeEsc(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "k1"}},
		view:   keysViewConfirmRevoke,
		cursor: 0,
	}
	tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if tab.view != keysViewList {
		t.Fatalf("Esc 后应返回列表视图")
	}
}

// TestKeysTab_RenderConfirmRevoke 验证吊销确认视图渲染。
func TestKeysTab_RenderConfirmRevoke(t *testing.T) {
	tab := &KeysTab{
		client: &tui.RPCClient{},
		keys:   []keyDTO{{Key: "key-abcdef123456", Tag: "dev-key"}},
		view:   keysViewConfirmRevoke,
		cursor: 0,
	}
	view := tab.View()
	if !strings.Contains(view, "确认吊销") {
		t.Fatalf("吊销确认视图应包含 '确认吊销'")
	}
}

// ---------- KeysTab RPC ----------

// TestKeysTab_HandleRPCKeysList 验证收到 keys.list 结果更新列表。
func TestKeysTab_HandleRPCKeysList(t *testing.T) {
	tab := &KeysTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	keys := []keyDTO{{Key: "k1", Tag: "t1"}, {Key: "k2", Tag: "t2"}}
	tab.Update(tui.RPCResult{
		Method: "keys.list",
		Result: &keys,
	})
	if len(tab.keys) != 2 {
		t.Fatalf("收到 keys.list 后应有 2 个密钥，got %d", len(tab.keys))
	}
}

// TestKeysTab_HandleRPCKeysListError 验证 RPC 错误。
func TestKeysTab_HandleRPCKeysListError(t *testing.T) {
	tab := &KeysTab{
		client:  &tui.RPCClient{},
		loading: true,
	}
	tab.Update(tui.RPCResult{
		Method: "keys.list",
		Err:    errors.New("超时"),
	})
	if tab.err == nil {
		t.Fatalf("收到 RPC 错误后 err 不应为 nil")
	}
}

// TestKeysTab_TickNilClient 验证 nil client 时 TickMsg 返回 nil cmd。
func TestKeysTab_TickNilClient(t *testing.T) {
	tab := NewKeysTab(nil)
	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("nil client 时 TickMsg 应返回 nil cmd")
	}
}
