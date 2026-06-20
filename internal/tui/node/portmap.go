package node

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeMappingDTO mirrors nodemethods.MappingInfo for RPC deserialization.
type nodeMappingDTO struct {
	Protocol     string `json:"protocol"`
	ExternalIP   string `json:"external_ip"`
	ExternalPort uint16 `json:"external_port"`
	InternalPort uint16 `json:"internal_port"`
	Transport    string `json:"transport"`
	TTL          string `json:"ttl"`
	RenewIn      string `json:"renew_in"`
}

// nodePortmapStatusDTO mirrors nodemethods.PortmapStatusInfo for RPC deserialization.
type nodePortmapStatusDTO struct {
	Active       bool `json:"active"`
	ManagedCount int  `json:"managed_count"`
}

// PortmapTab Portmap Tab：调 portmap.list + portmap.status 展示端口映射。
type PortmapTab struct {
	client   *tui.RPCClient
	mappings []nodeMappingDTO
	status   *nodePortmapStatusDTO
	loading  bool
	err      error
}

// NewPortmapTab 构造 PortmapTab。
func NewPortmapTab(client ...*tui.RPCClient) *PortmapTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &PortmapTab{client: c}
}

// SetClient 设置 RPC client。
func (t *PortmapTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *PortmapTab) Name() string { return "Portmap" }

// Init 返回初始 Cmd。
func (t *PortmapTab) Init() tea.Cmd {
	return tea.Batch(t.fetchList(), t.fetchStatus())
}

// Update 处理消息。
func (t *PortmapTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		switch msg.Method {
		case "portmap.list":
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*[]nodeMappingDTO); ok {
				t.mappings = *r
				t.err = nil
			}
		case "portmap.status":
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*nodePortmapStatusDTO); ok {
				t.status = r
				t.err = nil
			}
		}
	case tui.TickMsg:
		return t, tea.Batch(t.fetchList(), t.fetchStatus())
	}
	return t, nil
}

// View 渲染界面。
func (t *PortmapTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.mappings) == 0 && t.status == nil {
		return renderLoading()
	}
	if t.err != nil && len(t.mappings) == 0 && t.status == nil {
		return renderError(t.err)
	}

	var out string

	// Status card
	if t.status != nil {
		cardStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tui.ColorMuted).
			Padding(0, 3).
			Width(40)

		activeStr := "未激活"
		activeColor := tui.ColorError
		if t.status.Active {
			activeStr = "已激活"
			activeColor = tui.ColorSuccess
		}

		card := cardStyle.Render(fmt.Sprintf(
			"%s: %s  |  管理映射数: %d",
			lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).Render("Portmap"),
			lipgloss.NewStyle().Foreground(activeColor).Render(activeStr),
			t.status.ManagedCount,
		))
		out += "\n" + card + "\n\n"
	}

	// Mapping table
	headers := []string{"协议", "外部IP", "外部端口", "内部端口", "传输", "TTL", "续期倒计时"}
	widths := []int{10, 16, 10, 10, 6, 10, 12}
	rows := make([][]string, len(t.mappings))
	for i, m := range t.mappings {
		rows[i] = []string{
			m.Protocol,
			m.ExternalIP,
			fmt.Sprintf("%d", m.ExternalPort),
			fmt.Sprintf("%d", m.InternalPort),
			m.Transport,
			m.TTL,
			m.RenewIn,
		}
	}
	out += renderTable(headers, rows, widths, -1)

	if t.err != nil {
		out += "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	return out
}

func (t *PortmapTab) fetchList() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("portmap.list", nil, func() any { return new([]nodeMappingDTO) })
}

func (t *PortmapTab) fetchStatus() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.client.Call("portmap.status", nil, func() any { return new(nodePortmapStatusDTO) })
}
