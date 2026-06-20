package node

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeStatusResult mirrors nodemethods.systemStatusResult for RPC deserialization.
type nodeStatusResult struct {
	NodeID        string    `json:"node_id"`
	VIP           string    `json:"vip"`
	Role          string    `json:"role"`
	Uptime        float64   `json:"uptime_seconds"`
	TopoVer       uint64    `json:"topo_version"`
	TopoUpdatedAt time.Time `json:"topo_updated_at"`
	Connected     bool      `json:"connected"`

	PeerCount       int  `json:"peer_count"`
	ConnectionCount int  `json:"connection_count"`
	AvgRTTms        int  `json:"avg_rtt_ms"`
	IngressCount    int  `json:"ingress_count"`
	PortmapActive   bool `json:"portmap_active"`
}

// StatusTab 状态 Tab：调 system.status 获取节点概览。
type StatusTab struct {
	client  *tui.RPCClient
	data    *nodeStatusResult
	loading bool
	err     error
}

// NewStatusTab 构造 StatusTab。
func NewStatusTab(client ...*tui.RPCClient) *StatusTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &StatusTab{client: c}
}

// SetClient 设置 RPC client（供 runNodeTUI 延迟注入）。
func (t *StatusTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *StatusTab) Name() string { return "状态" }

// Init 返回初始 Cmd。
func (t *StatusTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *StatusTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "system.status" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*nodeStatusResult); ok {
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
func (t *StatusTab) View() string {
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

	// 连接状态着色
	connStr := "已连接"
	connColor := tui.ColorSuccess
	if !d.Connected {
		connStr = "未连接"
		connColor = tui.ColorError
	}

	// 第一行：身份信息
	row1 := tui.JoinCards(
		tui.RenderCard("节点 ID", tui.Truncate(d.NodeID, 18)),
		tui.RenderCard("虚拟 IP", d.VIP),
		tui.RenderCard("角色", tui.FriendlyRole(d.Role)),
	)

	// 第二行：运行状态
	row2 := tui.JoinCards(
		tui.RenderCard("拓扑版本", tui.FormatTopoVersion(d.TopoVer, d.TopoUpdatedAt)),
		tui.RenderCard("运行时间", tui.FormatUptime(d.Uptime)),
		tui.RenderCard("连接状态",
			lipgloss.NewStyle().Foreground(connColor).Render(connStr),
		),
	)

	// 第三行：网络概览
	rttStr := "-"
	if d.ConnectionCount > 0 {
		rttStr = fmt.Sprintf("%d ms", d.AvgRTTms)
	}
	row3 := tui.JoinCards(
		tui.RenderCard("对端节点", fmt.Sprintf("%d", d.PeerCount)),
		tui.RenderCard("活跃连接", fmt.Sprintf("%d", d.ConnectionCount)),
		tui.RenderCard("平均延迟", rttStr),
	)

	// 第四行：入口 & Portmap
	portmapStr := "未激活"
	portmapColor := tui.ColorMuted
	if d.PortmapActive {
		portmapStr = "已激活"
		portmapColor = tui.ColorSuccess
	}
	row4 := tui.JoinCards(
		tui.RenderCard("入口", fmt.Sprintf("%d 个", d.IngressCount)),
		tui.RenderCard("Portmap",
			lipgloss.NewStyle().Foreground(portmapColor).Render(portmapStr),
		),
	)

	header := tui.RenderSectionHeader("节点状态")

	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	return fmt.Sprintf("\n%s\n\n%s\n%s\n%s\n%s%s", header, row1, row2, row3, row4, errLine)
}

func (t *StatusTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("system.status", nil, func() any { return new(nodeStatusResult) })
}
