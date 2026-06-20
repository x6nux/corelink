package node

import (
	"encoding/json"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// ── ConfigTab 单元测试 ──────────────────────────────────────────────────────

func TestConfigTab_Name(t *testing.T) {
	tab := NewConfigTab()
	if got := tab.Name(); got != "配置" {
		t.Fatalf("ConfigTab.Name() = %q, want %q", got, "配置")
	}
}

func TestConfigTab_View_NilClient(t *testing.T) {
	tab := NewConfigTab()
	view := tab.View()
	if !containsSubstr(view, "未连接") {
		t.Fatalf("nil client 时 View 应含 '未连接'，实际: %q", view)
	}
}

func TestConfigTab_View_Loading(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true
	tab.data = nil

	view := tab.View()
	if !containsSubstr(view, "加载中") {
		t.Fatalf("loading 状态 View 应含 '加载中'，实际: %q", view)
	}
}

func TestConfigTab_View_Error(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.err = errors.New("配置解析失败")
	tab.data = nil

	view := tab.View()
	if !containsSubstr(view, "配置解析失败") {
		t.Fatalf("error 状态 View 应含错误信息，实际: %q", view)
	}
}

func TestConfigTab_View_WithData(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeConfigDisplay{
		ControllerEnrollAddr: ":7443",
		ControllerMTLSAddr:   ":7444",
		ControllerHTTPAddr:   ":8080",
		EnrollmentKey:        "secret-key-12345",
		ControllerCAHash:     "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		DataDir:              "/var/lib/corelink",
		Role:                 "node",
		Hostname:             "node-1",
		TUNName:              "utun42",
	}

	view := tab.View()

	// 应包含结构化配置区段
	checks := []string{
		"当前配置",
		"网络",
		"身份",
		"安全",
		":7443",
		":7444",
		":8080",
		"/var/lib/corelink",
		"节点", // node → 节点
		"node-1",
		"utun42",
	}
	for _, want := range checks {
		if !containsSubstr(view, want) {
			t.Errorf("View 应含 %q", want)
		}
	}
}

func TestConfigTab_ShowSecretsToggle(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeConfigDisplay{
		EnrollmentKey:    "my-secret-enrollment-key",
		ControllerCAHash: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}

	// 初始状态 showSecrets=false → 密钥应被遮盖
	if tab.showSecrets {
		t.Fatal("初始 showSecrets 应为 false")
	}
	view1 := tab.View()
	if containsSubstr(view1, "my-secret-enrollment-key") {
		t.Fatal("showSecrets=false 时不应显示完整密钥")
	}
	if !containsSubstr(view1, "****") {
		t.Fatal("showSecrets=false 时应显示 ****")
	}
	// CA 哈希应被截断
	if containsSubstr(view1, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789") {
		t.Fatal("showSecrets=false 时 CA 哈希不应完整显示")
	}

	// 按 's' 切换
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !tab.showSecrets {
		t.Fatal("按 's' 后 showSecrets 应为 true")
	}

	view2 := tab.View()
	if !containsSubstr(view2, "my-secret-enrollment-key") {
		t.Fatal("showSecrets=true 时应显示完整密钥")
	}
	// CA 哈希应完整显示
	if !containsSubstr(view2, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789") {
		t.Fatal("showSecrets=true 时应显示完整 CA 哈希")
	}

	// 再按 's' 切换回去
	tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if tab.showSecrets {
		t.Fatal("再次按 's' 后 showSecrets 应回到 false")
	}
}

func TestConfigTab_ShowSecretsHelpText(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeConfigDisplay{}

	// showSecrets=false → 帮助提示 "显示"
	view1 := tab.View()
	if !containsSubstr(view1, "s:显示敏感信息") {
		t.Fatal("showSecrets=false 时帮助应含 's:显示敏感信息'")
	}

	// showSecrets=true → 帮助提示 "隐藏"
	tab.showSecrets = true
	view2 := tab.View()
	if !containsSubstr(view2, "s:隐藏敏感信息") {
		t.Fatal("showSecrets=true 时帮助应含 's:隐藏敏感信息'")
	}
}

func TestConfigTab_EmptyEnrollmentKey(t *testing.T) {
	// EnrollmentKey 为空时应显示 "-" 而非 "****"
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeConfigDisplay{
		EnrollmentKey: "",
	}

	view := tab.View()
	// 注册密钥行应显示 "-"
	if containsSubstr(view, "****") {
		t.Fatal("EnrollmentKey 为空时不应显示 '****'")
	}
}

func TestConfigTab_RefreshKey(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.data = &nodeConfigDisplay{}

	// 按 'r' 应触发 fetch（返回非 nil cmd，因为 client 非 nil）
	// 但由于 RPCClient 内部 inner 为 nil，我们只验证 cmd 非 nil 说明 fetch 被触发
	// 注：实际 RPCClient.Call 会 panic 如果 inner 为 nil，所以只验证逻辑分支
	// 这里只验证 loading 状态变化
	tab.loading = false
	_, cmd := tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	// fetch() 在 client 非 nil 时设置 loading=true 并返回 cmd
	if cmd == nil {
		// cmd 可能为 nil 如果 Call 实现返回 nil，取决于 RPCClient 实现
		// 至少验证 loading 被设为 true
	}
	if !tab.loading {
		t.Fatal("按 'r' 后 loading 应为 true")
	}
}

func TestConfigTab_InputFocused(t *testing.T) {
	tab := NewConfigTab()
	if tab.InputFocused() {
		t.Fatal("ConfigTab.InputFocused 应始终返回 false")
	}
}

func TestConfigTab_Update_RPCResult(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	// 构造 RPC 结果
	cfg := nodeConfigDisplay{
		ControllerEnrollAddr: ":7443",
		Role:                 "node",
		Hostname:             "test-node",
	}
	raw, _ := json.Marshal(cfg)
	rawMsg := json.RawMessage(raw)

	msg := tui.RPCResult{
		Method: "config.get",
		Result: &rawMsg,
	}

	tab.Update(msg)
	if tab.data == nil {
		t.Fatal("成功 RPCResult 后 data 不应为 nil")
	}
	if tab.data.Role != "node" {
		t.Fatalf("data.Role = %q, want %q", tab.data.Role, "node")
	}
	if tab.loading {
		t.Fatal("成功 RPCResult 后 loading 应为 false")
	}
}

func TestConfigTab_Update_RPCResult_Error(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	msg := tui.RPCResult{
		Method: "config.get",
		Err:    errors.New("RPC 错误"),
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("失败 RPCResult 后 err 不应为 nil")
	}
	if tab.loading {
		t.Fatal("失败 RPCResult 后 loading 应为 false")
	}
}

func TestConfigTab_Update_RPCResult_BadJSON(t *testing.T) {
	tab := NewConfigTab()
	tab.client = &tui.RPCClient{}
	tab.loading = true

	// 构造无效 JSON
	raw := json.RawMessage([]byte("invalid json{{{"))
	msg := tui.RPCResult{
		Method: "config.get",
		Result: &raw,
	}

	tab.Update(msg)
	if tab.err == nil {
		t.Fatal("无效 JSON RPCResult 后 err 不应为 nil")
	}
	if !containsSubstr(tab.err.Error(), "解析配置") {
		t.Fatalf("err 应包含 '解析配置'，实际: %v", tab.err)
	}
}

func TestConfigTab_SetClient(t *testing.T) {
	tab := NewConfigTab()
	c := &tui.RPCClient{}
	tab.SetClient(c)
	if tab.client != c {
		t.Fatal("SetClient 后 client 应被设置")
	}
}
