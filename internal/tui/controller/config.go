package controller

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

type configStatusResult struct {
	DBDSN      string `json:"db_dsn"`
	ListenAddr string `json:"listen_addr"`
	AdminAddr  string `json:"admin_addr"`
	VirtualCIDR    string `json:"virtual_cidr"`
	TLSMode        string `json:"tls_mode"`
	CASubject      string `json:"ca_subject"`
	CAHash         string `json:"ca_hash"`
}

// ConfigTab 配置 Tab。
type ConfigTab struct {
	client  *tui.RPCClient
	data    *configStatusResult
	loading bool
	err     error
}

// NewConfigTab 构造 ConfigTab。
func NewConfigTab(client *tui.RPCClient) *ConfigTab {
	return &ConfigTab{client: client}
}

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
		if msg.Method == "config.status" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*configStatusResult); ok {
				t.data = r
				t.err = nil
			}
		}
	case tui.TickMsg:
		return t, t.fetch()
	}
	return t, nil
}

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
	fmt.Fprintf(&b, "\n%s\n\n", tui.RenderSectionHeader("Controller 运行配置"))

	renderKV := func(key, val string) {
		if val == "" {
			val = "-"
		}
		fmt.Fprintf(&b, "%s%s\n", keyStyle.Render(key), valStyle.Render(val))
	}

	// 数据库
	b.WriteString(sectionStyle.Render("── 数据库 ──") + "\n")
	renderKV("数据库", d.DBDSN)
	b.WriteByte('\n')

	// 网络
	b.WriteString(sectionStyle.Render("── 网络 ──") + "\n")
	renderKV("监听地址", d.ListenAddr)
	renderKV("管理端口", d.AdminAddr)
	renderKV("虚拟网段", d.VirtualCIDR)
	b.WriteByte('\n')

	// 安全
	b.WriteString(sectionStyle.Render("── 安全 ──") + "\n")
	renderKV("TLS 模式", d.TLSMode)
	renderKV("CA 主体", d.CASubject)
	renderKV("CA 哈希", d.CAHash)

	return b.String()
}

func (t *ConfigTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("config.status", nil, func() any { return new(configStatusResult) })
}
