package wizard

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/x6nux/corelink/internal/nodecore/jointoken"
)

// TokenToConfigJSON 从入网 token 解析结果 + role/hostname 生成 node 配置 JSON。
// 三地址按 token.H 用 net.JoinHostPort 拼接（IPv6 自动加方括号，消化 #31）。
func TokenToConfigJSON(jt jointoken.JoinToken, role, hostname string) ([]byte, error) {
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	if role == "" {
		role = "relay"
	}
	cfg := map[string]interface{}{
		"controller_enroll_addr": net.JoinHostPort(jt.H, "7443"),
		"controller_mtls_addr":   net.JoinHostPort(jt.H, "7444"),
		"controller_http_addr":   net.JoinHostPort(jt.H, "8080"),
		"enrollment_key":         jt.K,
		"controller_ca_hash":     jt.C,
		"role":                   role,
		"hostname":               hostname,
		"data_dir":               "/var/lib/corelink",
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// NodeWizardSteps 返回 Node 配置向导的 3 步定义。
func NodeWizardSteps() []Step {
	hostname, _ := os.Hostname()
	return []Step{
		{
			Title: "粘贴入网 token",
			Fields: []Field{
				{
					Label:       "入网 Token",
					Key:         "join_token",
					Required:    true,
					CharLimit:   512,
					Description: "由 corelink key create 生成（含 controller 地址 + 密钥 + CA 哈希）",
				},
			},
		},
		{
			Title: "节点信息",
			Fields: []Field{
				{
					Label:   "角色",
					Key:     "role",
					Default: "relay",
					Options: []string{"relay", "agent"},
				},
				{
					Label:       "主机名",
					Key:         "hostname",
					Default:     hostname,
					Description: "留空用 os.Hostname()",
				},
			},
		},
		{
			Title: "确认 + 保存",
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

// PreviewToken 解析入网 token 并返回供向导展示的人类可读摘要。
func PreviewToken(token string) (string, error) {
	jt, err := jointoken.Decode(token)
	if err != nil {
		return "", fmt.Errorf("解析入网 token 失败: %w", err)
	}
	caShort := jt.C
	if len(caShort) > 20 {
		caShort = caShort[:20] + "…"
	}
	return fmt.Sprintf("controller=%s  CA=%s", jt.H, caShort), nil
}

// NodeConfigJSON 从 Wizard Values 生成 node 配置 JSON。
// 入网信息从 join_token 解析（controller 地址 + enrollment_key + CA 哈希）。
func NodeConfigJSON(vals map[string]string) ([]byte, error) {
	jt, err := jointoken.Decode(vals["join_token"])
	if err != nil {
		return nil, fmt.Errorf("解析入网 token 失败: %w", err)
	}
	return TokenToConfigJSON(jt, valOrDefault(vals, "role", "relay"), vals["hostname"])
}
