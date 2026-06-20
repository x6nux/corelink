package node

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeConfigDisplay 从 config.get RPC 返回的 JSON 解析。
type nodeConfigDisplay struct {
	ControllerEnrollAddr string `json:"controller_enroll_addr"`
	ControllerMTLSAddr   string `json:"controller_mtls_addr"`
	ControllerHTTPAddr   string `json:"controller_http_addr"`
	EnrollmentKey        string `json:"enrollment_key"`
	ControllerCAHash     string `json:"controller_ca_hash"`
	DataDir              string `json:"data_dir"`
	Role                 string `json:"role"`
	Hostname             string `json:"hostname"`
	TUNName              string `json:"tun_name"`
}

// ConfigTab 配置 Tab：调 config.get 展示结构化配置。
type ConfigTab struct {
	client      *tui.RPCClient
	data        *nodeConfigDisplay
	loading     bool
	err         error
	showSecrets bool
}

// NewConfigTab 构造 ConfigTab。
func NewConfigTab(client ...*tui.RPCClient) *ConfigTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &ConfigTab{client: c}
}

// SetClient 设置 RPC client。
func (t *ConfigTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *ConfigTab) Name() string { return "配置" }

// Init 返回初始 Cmd。
func (t *ConfigTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *ConfigTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "config.get" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*json.RawMessage); ok {
				var cfg nodeConfigDisplay
				if err := json.Unmarshal(*r, &cfg); err == nil {
					t.data = &cfg
					t.err = nil
				} else {
					t.err = fmt.Errorf("解析配置: %w", err)
				}
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "s":
			t.showSecrets = !t.showSecrets
		case "r":
			return t, t.fetch()
		}
	}
	return t, nil
}

// InputFocused 配置页无输入焦点。
func (t *ConfigTab) InputFocused() bool { return false }

// View 渲染界面。
func (t *ConfigTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && t.data == nil {
		return renderLoading()
	}
	if t.err != nil && t.data == nil {
		return renderError(t.err)
	}

	d := t.data
	keyStyle := lipgloss.NewStyle().Bold(true).Width(22).PaddingLeft(2)
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorMuted).PaddingLeft(2)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n%s\n\n", tui.RenderSectionHeader("当前配置")))

	renderKV := func(key, val string) {
		if val == "" {
			val = "-"
		}
		b.WriteString(fmt.Sprintf("%s%s\n", keyStyle.Render(key), valStyle.Render(val)))
	}

	// 网络
	b.WriteString(sectionStyle.Render("── 网络 ──") + "\n")
	renderKV("注册端口 (enroll)", d.ControllerEnrollAddr)
	renderKV("业务端口 (mTLS)", d.ControllerMTLSAddr)
	renderKV("HTTP 端口", d.ControllerHTTPAddr)
	b.WriteByte('\n')

	// 身份
	b.WriteString(sectionStyle.Render("── 身份 ──") + "\n")
	renderKV("节点角色", tui.FriendlyRole(string(d.Role)))
	renderKV("主机名", d.Hostname)
	renderKV("数据目录", d.DataDir)
	renderKV("TUN 设备名", d.TUNName)
	b.WriteByte('\n')

	// 安全
	b.WriteString(sectionStyle.Render("── 安全 ──") + "\n")
	enrollKey := "****"
	if t.showSecrets && d.EnrollmentKey != "" {
		enrollKey = d.EnrollmentKey
	} else if d.EnrollmentKey == "" {
		enrollKey = "-"
	}
	renderKV("注册密钥", enrollKey)
	caHash := d.ControllerCAHash
	if len(caHash) > 24 && !t.showSecrets {
		caHash = caHash[:24] + "…"
	}
	renderKV("CA 哈希", caHash)

	// 帮助
	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}
	secretToggle := "s:显示敏感信息"
	if t.showSecrets {
		secretToggle = "s:隐藏敏感信息"
	}
	help := tui.StyleHelp.Render(fmt.Sprintf("\n  %s  r:刷新配置", secretToggle))
	b.WriteString(errLine)
	b.WriteString(help)

	return b.String()
}

func (t *ConfigTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("config.get", nil, func() any { return new(json.RawMessage) })
}
