package wizard

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/nodecore/jointoken"
)

// sendKey 发送一个按键到 Wizard 并返回更新后的 Wizard。
func sendKey(t *testing.T, w *Wizard, key string) *Wizard {
	t.Helper()
	m, _ := w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return m.(*Wizard)
}

// sendSpecialKey 发送特殊按键。
func sendSpecialKey(t *testing.T, w *Wizard, keyType tea.KeyType) *Wizard {
	t.Helper()
	m, _ := w.Update(tea.KeyMsg{Type: keyType})
	return m.(*Wizard)
}

func TestNewAppliesFieldCharLimit(t *testing.T) {
	w := New([]Step{{Title: "t", Fields: []Field{
		{Label: "tok", Key: "join_token", CharLimit: 512},
		{Label: "x", Key: "x"},
	}}})
	if got := w.fields[0][0].CharLimit; got != 512 {
		t.Fatalf("CharLimit=%d, want 512", got)
	}
	if got := w.fields[0][1].CharLimit; got != 256 {
		t.Fatalf("默认 CharLimit=%d, want 256", got)
	}
}

func TestWizard_StepNavigation(t *testing.T) {
	steps := []Step{
		{
			Title: "Step 1",
			Fields: []Field{
				{Label: "Field A", Key: "a", Default: "default_a"},
				{Label: "Field B", Key: "b", Default: "default_b"},
			},
		},
		{
			Title: "Step 2",
			Fields: []Field{
				{Label: "Field C", Key: "c", Default: "default_c"},
			},
		},
	}

	w := New(steps)
	w.Init()

	// 初始在 Step 0。
	if w.current != 0 {
		t.Fatalf("初始步骤应为 0，实际 %d", w.current)
	}
	if w.activeField != 0 {
		t.Fatalf("初始字段应为 0，实际 %d", w.activeField)
	}

	// Tab → 下一字段。
	w = sendSpecialKey(t, w, tea.KeyTab)
	if w.activeField != 1 {
		t.Fatalf("Tab 后字段应为 1，实际 %d", w.activeField)
	}

	// Enter 在最后一个字段 → 下一步。
	w = sendSpecialKey(t, w, tea.KeyEnter)
	if w.current != 1 {
		t.Fatalf("Enter 后应在步骤 1，实际 %d", w.current)
	}
	if w.activeField != 0 {
		t.Fatalf("新步骤活跃字段应为 0，实际 %d", w.activeField)
	}

	// Enter 在最后一步最后一个字段 → 完成。
	w = sendSpecialKey(t, w, tea.KeyEnter)
	if !w.Done() {
		t.Fatal("应标记完成")
	}
}

func TestWizard_Values(t *testing.T) {
	steps := []Step{
		{
			Title: "Step 1",
			Fields: []Field{
				{Label: "Name", Key: "name", Default: ""},
				{Label: "Port", Key: "port", Default: "8080"},
			},
		},
		{
			Title: "Step 2",
			Fields: []Field{
				{Label: "Role", Key: "role", Default: "node", Options: []string{"node", "node"}},
			},
		},
	}

	w := New(steps)
	w.Init()

	// 手动设置值（模拟输入）。
	w.fields[0][0].SetValue("myhost")

	// 第二步 Select 字段已有默认值 "node"。
	vals := w.Values()
	if vals["name"] != "myhost" {
		t.Fatalf("name 应为 myhost，实际 %q", vals["name"])
	}
	if vals["port"] != "8080" {
		t.Fatalf("port 应回落 Default 8080，实际 %q", vals["port"])
	}
	if vals["role"] != "node" {
		t.Fatalf("role 应为 relay，实际 %q", vals["role"])
	}
}

func TestWizard_Cancel(t *testing.T) {
	steps := []Step{
		{
			Title:  "Step 1",
			Fields: []Field{{Label: "F", Key: "f"}},
		},
	}
	w := New(steps)
	w.Init()

	w = sendSpecialKey(t, w, tea.KeyEsc)
	if !w.Cancelled() {
		t.Fatal("Esc 后应标记取消")
	}
	if w.Done() {
		t.Fatal("取消后不应标记完成")
	}
}

func TestWizard_SelectMode(t *testing.T) {
	steps := []Step{
		{
			Title: "Step 1",
			Fields: []Field{
				{Label: "TLS", Key: "tls", Default: "self-signed", Options: []string{"self-signed", "acme"}},
			},
		},
	}
	w := New(steps)
	w.Init()

	// 初始选中 "self-signed"（索引 0）。
	if v := w.fields[0][0].Value(); v != "self-signed" {
		t.Fatalf("初始应为 self-signed，实际 %q", v)
	}

	// 按 right → "acme"。
	w = sendKey(t, w, "right")
	if v := w.fields[0][0].Value(); v != "acme" {
		t.Fatalf("right 后应为 acme，实际 %q", v)
	}

	// 按 left → 回到 "self-signed"。
	w = sendKey(t, w, "left")
	if v := w.fields[0][0].Value(); v != "self-signed" {
		t.Fatalf("left 后应为 self-signed，实际 %q", v)
	}
}

func TestWizard_RequiredField(t *testing.T) {
	steps := []Step{
		{
			Title: "Step 1",
			Fields: []Field{
				{Label: "Required", Key: "req", Required: true},
			},
		},
		{
			Title:  "Step 2",
			Fields: []Field{{Label: "F", Key: "f"}},
		},
	}
	w := New(steps)
	w.Init()

	// 空值 Enter 不应前进。
	w = sendSpecialKey(t, w, tea.KeyEnter)
	if w.current != 0 {
		t.Fatalf("Required 空值 Enter 不应前进，实际步骤 %d", w.current)
	}

	// 填写后 Enter 应前进。
	w.fields[0][0].SetValue("filled")
	w = sendSpecialKey(t, w, tea.KeyEnter)
	if w.current != 1 {
		t.Fatalf("填写后 Enter 应前进到步骤 1，实际 %d", w.current)
	}
}

func TestWizard_View(t *testing.T) {
	steps := []Step{
		{
			Title: "测试步骤",
			Fields: []Field{
				{Label: "字段A", Key: "a", Default: "val", Description: "帮助信息"},
			},
		},
	}
	w := New(steps)
	w.Init()
	view := w.View()
	if view == "" {
		t.Fatal("View 不应为空")
	}
	// 基本检查：包含步骤标题和提示。
	if !containsStr(view, "测试步骤") {
		t.Fatal("View 应包含步骤标题")
	}
	if !containsStr(view, "Esc:取消") {
		t.Fatal("View 应包含底部提示")
	}
}

func TestWizard_DownUpNavigation(t *testing.T) {
	steps := []Step{
		{
			Title: "Step",
			Fields: []Field{
				{Label: "A", Key: "a"},
				{Label: "B", Key: "b"},
				{Label: "C", Key: "c"},
			},
		},
	}
	w := New(steps)
	w.Init()

	// down → field 1。
	w = sendKey(t, w, "down")
	if w.activeField != 1 {
		t.Fatalf("down 后应为字段 1，实际 %d", w.activeField)
	}

	// up → field 0。
	w = sendKey(t, w, "up")
	if w.activeField != 0 {
		t.Fatalf("up 后应为字段 0，实际 %d", w.activeField)
	}
}

func TestControllerWizardSteps(t *testing.T) {
	steps := ControllerWizardSteps()
	if len(steps) != 4 {
		t.Fatalf("Controller 向导应有 4 步，实际 %d", len(steps))
	}
	// Step 1: 2 字段。
	if len(steps[0].Fields) != 2 {
		t.Fatalf("Step 1 应有 2 字段，实际 %d", len(steps[0].Fields))
	}
	// Step 2: 4 字段。
	if len(steps[1].Fields) != 4 {
		t.Fatalf("Step 2 应有 4 字段，实际 %d", len(steps[1].Fields))
	}
	// Step 3: 2 字段。
	if len(steps[2].Fields) != 2 {
		t.Fatalf("Step 3 应有 2 字段，实际 %d", len(steps[2].Fields))
	}
	// Step 4: 1 字段（确认）。
	if len(steps[3].Fields) != 1 {
		t.Fatalf("Step 4 应有 1 字段，实际 %d", len(steps[3].Fields))
	}
}

func TestNodeWizardSteps(t *testing.T) {
	steps := NodeWizardSteps()
	if len(steps) != 3 {
		t.Fatalf("Node 向导应有 3 步，实际 %d", len(steps))
	}
	// Step 1: 1 字段（join_token）。
	if len(steps[0].Fields) != 1 {
		t.Fatalf("Step 1 应有 1 字段，实际 %d", len(steps[0].Fields))
	}
	// Step 2: 2 字段（role/hostname，enrollment_key 与 server_fingerprint 已并入 token）。
	if len(steps[1].Fields) != 2 {
		t.Fatalf("Step 2 应有 2 字段，实际 %d", len(steps[1].Fields))
	}
	// Step 3: 1 字段（确认）。
	if len(steps[2].Fields) != 1 {
		t.Fatalf("Step 3 应有 1 字段，实际 %d", len(steps[2].Fields))
	}
}

func TestControllerConfigJSON(t *testing.T) {
	vals := map[string]string{
		"SelfSignedHost": "example.com",
		"TLSMode":        "acme",
		"GRPCEnrollAddr": ":9443",
		"GRPCAddr":       ":9444",
		"HTTPAddr":       ":9080",
		"AdminAddr":      "0.0.0.0:8090",
		"AdminUser":      "root",
		"AdminPassword":  "secret123",
	}
	data, err := ControllerConfigJSON(vals)
	if err != nil {
		t.Fatalf("ControllerConfigJSON 失败: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	checks := map[string]string{
		"SelfSignedHost": "example.com",
		"TLSMode":        "acme",
		"GRPCEnrollAddr": ":9443",
		"GRPCAddr":       ":9444",
		"HTTPAddr":       ":9080",
		"AdminAddr":      "0.0.0.0:8090",
		"AdminUser":      "root",
		"AdminPassword":  "secret123",
		"DBDSN":          "sqlite://corelink.db",
		"VirtualCIDR":    "100.64.0.0/10",
		"CASubject":      "CoreLink Root CA",
	}
	for k, want := range checks {
		got, ok := parsed[k]
		if !ok {
			t.Errorf("JSON 缺少字段 %q", k)
			continue
		}
		if got != want {
			t.Errorf("字段 %q：期望 %q，实际 %q", k, want, got)
		}
	}
}

func TestControllerConfigJSON_NoPassword(t *testing.T) {
	vals := map[string]string{
		"AdminPassword": "",
	}
	data, err := ControllerConfigJSON(vals)
	if err != nil {
		t.Fatalf("ControllerConfigJSON 失败: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if _, ok := parsed["AdminPassword"]; ok {
		t.Fatal("密码为空时不应包含 AdminPassword 字段")
	}
}

func TestNodeConfigJSON(t *testing.T) {
	tok, _ := jointoken.Encode(jointoken.JoinToken{
		V: 1, H: "ctrl.example.com", K: "key-abc-123",
		C: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	})
	vals := map[string]string{
		"join_token": tok,
		"role":       "node",
		"hostname":   "node-1",
	}
	data, err := NodeConfigJSON(vals)
	if err != nil {
		t.Fatalf("NodeConfigJSON 失败: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	// 验证 token.H 正确拆分为 3 个地址。
	checks := map[string]string{
		"controller_enroll_addr": "ctrl.example.com:7443",
		"controller_mtls_addr":   "ctrl.example.com:7444",
		"controller_http_addr":   "ctrl.example.com:8080",
		"enrollment_key":         "key-abc-123",
		"controller_ca_hash":     "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"role":                   "node",
		"hostname":               "node-1",
		"data_dir":               "/var/lib/corelink",
	}
	for k, want := range checks {
		got, ok := parsed[k]
		if !ok {
			t.Errorf("JSON 缺少字段 %q", k)
			continue
		}
		if got != want {
			t.Errorf("字段 %q：期望 %q，实际 %q", k, want, got)
		}
	}
}

func TestNodeConfigJSON_BadToken(t *testing.T) {
	if _, err := NodeConfigJSON(map[string]string{"join_token": "garbage"}); err == nil {
		t.Fatal("非法 token 应报错")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
