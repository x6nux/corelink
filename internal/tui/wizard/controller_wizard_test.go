package wizard

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── ControllerWizardSteps 单元测试 ──────────────────────────────────────────

func TestControllerWizardSteps_StepCount(t *testing.T) {
	steps := ControllerWizardSteps()
	if len(steps) != 4 {
		t.Fatalf("Controller 向导应有 4 步，实际 %d", len(steps))
	}
}

func TestControllerWizardSteps_StepTitles(t *testing.T) {
	steps := ControllerWizardSteps()
	wantTitles := []string{"基础设置", "端口设置", "管理员", "确认"}
	for i, want := range wantTitles {
		if steps[i].Title != want {
			t.Errorf("步骤 %d 标题应为 %q，实际 %q", i+1, want, steps[i].Title)
		}
	}
}

func TestControllerWizardSteps_FieldCounts(t *testing.T) {
	steps := ControllerWizardSteps()
	wantCounts := []int{2, 4, 2, 1}
	for i, want := range wantCounts {
		if len(steps[i].Fields) != want {
			t.Errorf("步骤 %d 应有 %d 字段，实际 %d", i+1, want, len(steps[i].Fields))
		}
	}
}

func TestControllerWizardSteps_Step1Fields(t *testing.T) {
	steps := ControllerWizardSteps()
	step1 := steps[0]

	// 字段 1: 公网地址
	if step1.Fields[0].Key != "SelfSignedHost" {
		t.Errorf("步骤 1 字段 1 Key 应为 SelfSignedHost，实际 %q", step1.Fields[0].Key)
	}
	if step1.Fields[0].Default != "localhost" {
		t.Errorf("SelfSignedHost Default 应为 localhost，实际 %q", step1.Fields[0].Default)
	}

	// 字段 2: TLS 模式（Select）
	if step1.Fields[1].Key != "TLSMode" {
		t.Errorf("步骤 1 字段 2 Key 应为 TLSMode，实际 %q", step1.Fields[1].Key)
	}
	if len(step1.Fields[1].Options) != 2 {
		t.Errorf("TLSMode 应有 2 个选项，实际 %d", len(step1.Fields[1].Options))
	}
}

func TestControllerWizardSteps_Step2Ports(t *testing.T) {
	steps := ControllerWizardSteps()
	step2 := steps[1]

	wantKeys := []string{"GRPCEnrollAddr", "GRPCAddr", "HTTPAddr", "AdminAddr"}
	wantDefaults := []string{":7443", ":7444", ":8080", "127.0.0.1:8090"}

	for i, wantKey := range wantKeys {
		if step2.Fields[i].Key != wantKey {
			t.Errorf("步骤 2 字段 %d Key 应为 %q，实际 %q", i+1, wantKey, step2.Fields[i].Key)
		}
		if step2.Fields[i].Default != wantDefaults[i] {
			t.Errorf("步骤 2 字段 %d Default 应为 %q，实际 %q", i+1, wantDefaults[i], step2.Fields[i].Default)
		}
	}
}

func TestControllerWizardSteps_Step3Admin(t *testing.T) {
	steps := ControllerWizardSteps()
	step3 := steps[2]

	// 用户名字段
	if step3.Fields[0].Key != "AdminUser" {
		t.Errorf("管理员步骤字段 1 Key 应为 AdminUser，实际 %q", step3.Fields[0].Key)
	}
	if step3.Fields[0].Default != "admin" {
		t.Errorf("AdminUser Default 应为 admin，实际 %q", step3.Fields[0].Default)
	}

	// 密码字段
	if step3.Fields[1].Key != "AdminPassword" {
		t.Errorf("管理员步骤字段 2 Key 应为 AdminPassword，实际 %q", step3.Fields[1].Key)
	}
	if !step3.Fields[1].Password {
		t.Error("AdminPassword 应为密码模式")
	}
}

func TestControllerWizardSteps_Step4Confirm(t *testing.T) {
	steps := ControllerWizardSteps()
	step4 := steps[3]

	if step4.Fields[0].Key != "_confirm" {
		t.Errorf("确认步骤字段 Key 应为 _confirm，实际 %q", step4.Fields[0].Key)
	}
	if step4.Fields[0].Default != "yes" {
		t.Errorf("_confirm Default 应为 yes，实际 %q", step4.Fields[0].Default)
	}
}

// ── ControllerConfigJSON 单元测试 ───────────────────────────────────────────

func TestControllerConfigJSON_AllFields(t *testing.T) {
	vals := map[string]string{
		"SelfSignedHost": "my.domain.com",
		"TLSMode":        "acme",
		"GRPCEnrollAddr": ":9443",
		"GRPCAddr":       ":9444",
		"HTTPAddr":       ":9080",
		"AdminAddr":      "0.0.0.0:8090",
		"AdminUser":      "superadmin",
		"AdminPassword":  "p@ssw0rd",
	}

	data, err := ControllerConfigJSON(vals)
	if err != nil {
		t.Fatalf("ControllerConfigJSON 失败: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	// 验证用户输入字段
	for k, want := range vals {
		got, ok := parsed[k]
		if !ok {
			t.Errorf("JSON 缺少字段 %q", k)
			continue
		}
		if got != want {
			t.Errorf("字段 %q：期望 %q，实际 %q", k, want, got)
		}
	}

	// 验证固定字段
	fixedChecks := map[string]string{
		"DBDSN":       "sqlite://corelink.db",
		"VirtualCIDR": "100.64.0.0/10",
		"CASubject":   "CoreLink Root CA",
	}
	for k, want := range fixedChecks {
		got, ok := parsed[k]
		if !ok {
			t.Errorf("JSON 缺少固定字段 %q", k)
			continue
		}
		if got != want {
			t.Errorf("固定字段 %q：期望 %q，实际 %q", k, want, got)
		}
	}
}

func TestControllerConfigJSON_Defaults(t *testing.T) {
	// 空 map 应使用所有默认值
	data, err := ControllerConfigJSON(map[string]string{})
	if err != nil {
		t.Fatalf("ControllerConfigJSON 失败: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}

	defaults := map[string]string{
		"SelfSignedHost": "localhost",
		"TLSMode":        "self-signed",
		"GRPCEnrollAddr": ":7443",
		"GRPCAddr":       ":7444",
		"HTTPAddr":       ":8080",
		"AdminAddr":      "127.0.0.1:8090",
		"AdminUser":      "admin",
	}
	for k, want := range defaults {
		got, ok := parsed[k]
		if !ok {
			t.Errorf("JSON 缺少默认字段 %q", k)
			continue
		}
		if got != want {
			t.Errorf("默认字段 %q：期望 %q，实际 %q", k, want, got)
		}
	}

	// 密码为空时不应包含 AdminPassword
	if _, ok := parsed["AdminPassword"]; ok {
		t.Error("密码为空时不应包含 AdminPassword 字段")
	}
}

func TestControllerConfigJSON_EmptyPassword(t *testing.T) {
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

func TestControllerConfigJSON_ValidJSON(t *testing.T) {
	// 确保输出的是有效且格式化的 JSON
	data, err := ControllerConfigJSON(map[string]string{})
	if err != nil {
		t.Fatalf("ControllerConfigJSON 失败: %v", err)
	}

	// 应缩进（MarshalIndent 的结果）
	if !strings.Contains(string(data), "\n") {
		t.Error("输出应为格式化 JSON（含换行）")
	}
}

// ── ControllerConfigPreview 单元测试 ────────────────────────────────────────

func TestControllerConfigPreview_AllFields(t *testing.T) {
	vals := map[string]string{
		"SelfSignedHost": "example.com",
		"TLSMode":        "acme",
		"GRPCEnrollAddr": ":9443",
		"GRPCAddr":       ":9444",
		"HTTPAddr":       ":9080",
		"AdminAddr":      "0.0.0.0:8090",
		"AdminUser":      "root",
		"AdminPassword":  "secret",
	}

	preview := ControllerConfigPreview(vals)

	checks := []string{
		"配置预览",
		"example.com",
		"acme",
		":9443",
		":9444",
		":9080",
		"0.0.0.0:8090",
		"root",
		"****", // 密码应被遮盖
	}
	for _, want := range checks {
		if !strings.Contains(preview, want) {
			t.Errorf("预览应含 %q", want)
		}
	}
	// 密码不应明文展示
	if strings.Contains(preview, "secret") {
		t.Error("预览中不应包含明文密码")
	}
}

func TestControllerConfigPreview_EmptyPassword(t *testing.T) {
	vals := map[string]string{
		"AdminPassword": "",
	}
	preview := ControllerConfigPreview(vals)
	if !strings.Contains(preview, "(自动生成)") {
		t.Error("密码为空时预览应含 '(自动生成)'")
	}
}

func TestControllerConfigPreview_Defaults(t *testing.T) {
	preview := ControllerConfigPreview(map[string]string{})

	defaults := []string{
		"localhost",
		"self-signed",
		":7443",
		":7444",
		":8080",
		"127.0.0.1:8090",
		"admin",
	}
	for _, want := range defaults {
		if !strings.Contains(preview, want) {
			t.Errorf("空 vals 预览应含默认值 %q", want)
		}
	}
}

// ── valOrDefault 单元测试 ───────────────────────────────────────────────────

func TestValOrDefault(t *testing.T) {
	tests := []struct {
		name string
		vals map[string]string
		key  string
		def  string
		want string
	}{
		{
			name: "存在且非空",
			vals: map[string]string{"k": "v"},
			key:  "k",
			def:  "default",
			want: "v",
		},
		{
			name: "存在但为空字符串",
			vals: map[string]string{"k": ""},
			key:  "k",
			def:  "default",
			want: "default",
		},
		{
			name: "不存在",
			vals: map[string]string{},
			key:  "k",
			def:  "default",
			want: "default",
		},
		{
			name: "nil map",
			vals: nil,
			key:  "k",
			def:  "default",
			want: "default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valOrDefault(tt.vals, tt.key, tt.def)
			if got != tt.want {
				t.Errorf("valOrDefault(%v, %q, %q) = %q, want %q", tt.vals, tt.key, tt.def, got, tt.want)
			}
		})
	}
}
