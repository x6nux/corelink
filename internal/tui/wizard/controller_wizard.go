package wizard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ControllerWizardSteps 返回 Controller 配置向导的 4 步定义。
func ControllerWizardSteps() []Step {
	return []Step{
		{
			Title: "基础设置",
			Fields: []Field{
				{
					Label:       "公网地址/域名",
					Key:         "SelfSignedHost",
					Default:     "localhost",
					Description: "Controller 对外可达的地址或域名",
				},
				{
					Label:   "TLS 模式",
					Key:     "TLSMode",
					Default: "self-signed",
					Options: []string{"self-signed", "acme"},
				},
			},
		},
		{
			Title: "端口设置",
			Fields: []Field{
				{
					Label:       "监听地址",
					Key:         "ListenAddr",
					Default:     ":7443",
					Description: "统一监听端口（gRPC + HTTP 共享）",
				},
				{
					Label:       "管理面地址",
					Key:         "AdminAddr",
					Default:     "127.0.0.1:8090",
					Description: "管理面 HTTP 监听地址",
				},
			},
		},
		{
			Title: "管理员",
			Fields: []Field{
				{
					Label:   "用户名",
					Key:     "AdminUser",
					Default: "admin",
				},
				{
					Label:       "密码",
					Key:         "AdminPassword",
					Password:    true,
					Description: "留空自动生成",
				},
			},
		},
		{
			Title: "确认",
			Fields: []Field{
				{
					Label:       "确认保存",
					Key:         "_confirm",
					Default:     "yes",
					Description: "按 Enter 保存配置 / Esc 取消",
				},
			},
		},
	}
}

// ControllerConfigJSON 从 Wizard Values 生成 controller 配置 JSON。
func ControllerConfigJSON(vals map[string]string) ([]byte, error) {
	cfg := map[string]interface{}{
		"SelfSignedHost": valOrDefault(vals, "SelfSignedHost", "localhost"),
		"TLSMode":        valOrDefault(vals, "TLSMode", "self-signed"),
		"ListenAddr":     valOrDefault(vals, "ListenAddr", ":7443"),
		"AdminAddr":      valOrDefault(vals, "AdminAddr", "127.0.0.1:8090"),
		"AdminUser":      valOrDefault(vals, "AdminUser", "admin"),
		"DBDSN":          "sqlite://corelink.db",
		"VirtualCIDR":    "100.64.0.0/10",
		"CASubject":      "CoreLink Root CA",
	}
	// AdminPassword 非空才写入。
	if pw := vals["AdminPassword"]; pw != "" {
		cfg["AdminPassword"] = pw
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// ControllerConfigPreview 生成向导确认步骤的预览文本。
func ControllerConfigPreview(vals map[string]string) string {
	var b strings.Builder
	b.WriteString("── 配置预览 ──\n")
	b.WriteString(fmt.Sprintf("  公网地址:    %s\n", valOrDefault(vals, "SelfSignedHost", "localhost")))
	b.WriteString(fmt.Sprintf("  TLS 模式:    %s\n", valOrDefault(vals, "TLSMode", "self-signed")))
	b.WriteString(fmt.Sprintf("  监听地址:    %s\n", valOrDefault(vals, "ListenAddr", ":7443")))
	b.WriteString(fmt.Sprintf("  管理面地址:  %s\n", valOrDefault(vals, "AdminAddr", "127.0.0.1:8090")))
	b.WriteString(fmt.Sprintf("  管理员:      %s\n", valOrDefault(vals, "AdminUser", "admin")))
	pw := vals["AdminPassword"]
	if pw == "" {
		pw = "(自动生成)"
	} else {
		pw = "****"
	}
	b.WriteString(fmt.Sprintf("  密码:        %s\n", pw))
	return b.String()
}

func valOrDefault(vals map[string]string, key, def string) string {
	if v, ok := vals[key]; ok && v != "" {
		return v
	}
	return def
}
