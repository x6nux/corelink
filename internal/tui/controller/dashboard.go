package controller

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// systemStatusResult mirrors ctrlmethods.systemStatusResult for RPC deserialization.
type systemStatusResult struct {
	UptimeSeconds float64           `json:"uptime_seconds"`
	Version       string            `json:"version"`
	NodeCount     int               `json:"node_count"`
	OnlineCount   int               `json:"online_count"`
	TopoVersion   uint64            `json:"topo_version"`
	TopoRecompute time.Time         `json:"topo_recompute"`
	TransitCount  int               `json:"transit_count"`
	LeafCount     int               `json:"leaf_count"`
	CertCount     int               `json:"cert_count"`
	KeyCount      int               `json:"key_count"`
	Nodes         []nodeStatusEntry `json:"nodes,omitempty"`
}

type nodeStatusEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	VIP    string `json:"vip"`
	Role   string `json:"role"`
	Online bool   `json:"online"`
}

// DashboardTab 仪表盘 Tab：调 system.status 获取概览数据。
type DashboardTab struct {
	client  *tui.RPCClient
	data    *systemStatusResult
	loading bool
	err     error
}

// NewDashboardTab 构造 DashboardTab。
func NewDashboardTab(client *tui.RPCClient) *DashboardTab {
	return &DashboardTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *DashboardTab) Name() string { return "仪表盘" }

// Init 返回初始 Cmd。
func (t *DashboardTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *DashboardTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "system.status" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*systemStatusResult); ok {
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
func (t *DashboardTab) View() string {
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

	onlineColor := tui.ColorSuccess
	if d.OnlineCount == 0 {
		onlineColor = tui.ColorError
	}
	offlineCount := d.NodeCount - d.OnlineCount

	// 第一行：核心指标
	row1 := tui.JoinCards(
		tui.RenderCard("节点",
			fmt.Sprintf("%s / %d 总计",
				lipgloss.NewStyle().Foreground(onlineColor).Render(fmt.Sprintf("%d 在线", d.OnlineCount)),
				d.NodeCount)),
		tui.RenderCard("拓扑版本", tui.FormatTopoVersion(d.TopoVersion, d.TopoRecompute)),
		tui.RenderCard("运行时间", tui.FormatUptime(d.UptimeSeconds)),
		tui.RenderCard("版本", d.Version),
	)

	// 第二行：拓扑分布 / 资源
	row2 := tui.JoinCards(
		tui.RenderCard("中转节点", fmt.Sprintf("%d", d.TransitCount)),
		tui.RenderCard("叶子节点", fmt.Sprintf("%d", d.LeafCount)),
		tui.RenderCard("离线节点", fmt.Sprintf("%d", offlineCount), tui.CardOpts{
			ValueColor: func() lipgloss.Color {
				if offlineCount > 0 {
					return tui.ColorWarning
				}
				return tui.ColorSuccess
			}(),
		}),
		tui.RenderCard("证书 / 密钥", fmt.Sprintf("%d 有效 / %d 可用", d.CertCount, d.KeyCount)),
	)

	header := tui.RenderSectionHeader("系统概览")

	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	result := fmt.Sprintf("\n%s\n\n%s\n%s%s", header, row1, row2, errLine)

	// 节点列表
	if len(d.Nodes) > 0 {
		nodeHeader := tui.RenderSectionHeader("节点列表")

		headers := []string{"ID", "名称", "VIP", "角色", "状态"}
		widths := []int{8, 18, 16, 8, 8}
		var rows [][]string
		for _, n := range d.Nodes {
			status := tui.StyleOffline.String()
			if n.Online {
				status = tui.StyleOnline.String()
			}
			rows = append(rows, []string{
				n.ID,
				tui.Truncate(n.Name, 16),
				n.VIP,
				tui.FriendlyRole(n.Role),
				status,
			})
		}
		table := tui.RenderTable(headers, rows, widths, -1)
		result += fmt.Sprintf("\n\n%s\n%s", nodeHeader, table)
	}

	return result
}

func (t *DashboardTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("system.status", nil, func() any { return new(systemStatusResult) })
}
