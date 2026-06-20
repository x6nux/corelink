package controller

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// topoStatusResult mirrors ctrlmethods.topoStatusResult for RPC deserialization.
type topoStatusResult struct {
	Version       uint64    `json:"version"`
	TransitCount  int       `json:"transit_count"`
	LeafCount     int       `json:"leaf_count"`
	LastRecompute time.Time `json:"last_recompute"`
}

type topoGraphNode struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	VIP      string `json:"vip"`
	Role     string `json:"role"`
	Online   bool   `json:"online"`
}

type topoGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type topoGraphResult struct {
	Nodes []topoGraphNode `json:"nodes"`
	Edges []topoGraphEdge `json:"edges"`
}

// TopoTab 拓扑 Tab。
type TopoTab struct {
	client  *tui.RPCClient
	status  *topoStatusResult
	graph   *topoGraphResult
	loading bool
	err     error
	cursor  int
}

// NewTopoTab 构造 TopoTab。
func NewTopoTab(client *tui.RPCClient) *TopoTab {
	return &TopoTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *TopoTab) Name() string { return "拓扑" }

// Init 返回初始 Cmd。
func (t *TopoTab) Init() tea.Cmd {
	return tea.Batch(t.fetchStatus(), t.fetchGraph())
}

// Update 处理消息。
func (t *TopoTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		switch msg.Method {
		case "topo.status":
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*topoStatusResult); ok {
				t.status = r
				t.err = nil
			}
		case "topo.graph":
			if msg.Err == nil {
				if r, ok := msg.Result.(*topoGraphResult); ok {
					t.graph = r
				}
			}
		}
	case tui.TickMsg:
		return t, tea.Batch(t.fetchStatus(), t.fetchGraph())
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *TopoTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if t.graph == nil {
		return t, nil
	}
	switch msg.String() {
	case "j", "down":
		if t.cursor < len(t.graph.Nodes)-1 {
			t.cursor++
		}
	case "k", "up":
		if t.cursor > 0 {
			t.cursor--
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *TopoTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && t.status == nil {
		return renderLoading()
	}
	if t.err != nil && t.status == nil {
		return renderError(t.err)
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)
	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.ColorMuted).
		Padding(1, 3).
		Width(22)
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)

	card := func(label, value string) string {
		return cardStyle.Render(fmt.Sprintf("%s\n%s", labelStyle.Render(label), value))
	}

	d := t.status
	lastRecompute := "无"
	if !d.LastRecompute.IsZero() {
		lastRecompute = d.LastRecompute.Format("2006-01-02 15:04:05")
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top,
		card("版本", formatTopoVersion(d.Version, d.LastRecompute)),
		" ",
		card("中转节点", fmt.Sprintf("%d", d.TransitCount)),
		" ",
		card("叶子节点", fmt.Sprintf("%d", d.LeafCount)),
		" ",
		card("上次重算", lastRecompute),
	)

	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n\n%s%s", titleStyle.Render("拓扑统计"), row, errLine)

	// ASCII 拓扑图
	if t.graph != nil && len(t.graph.Nodes) > 0 {
		fmt.Fprintf(&b, "\n\n%s\n", titleStyle.Render("网络拓扑"))
		b.WriteString(t.renderGraph())
		b.WriteString("\n")
		b.WriteString(tui.StyleHelp.Render("  j/k:选择节点"))
	}

	return b.String()
}

// renderGraph 渲染 ASCII 拓扑图。
func (t *TopoTab) renderGraph() string {
	g := t.graph
	if g == nil || len(g.Nodes) == 0 {
		return ""
	}

	// 建立邻接表
	adj := make(map[string][]string)
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}

	onlineStyle := lipgloss.NewStyle().Foreground(tui.ColorSuccess)
	offlineStyle := lipgloss.NewStyle().Foreground(tui.ColorError)
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)

	var b strings.Builder
	for i, n := range g.Nodes {
		prefix := "  "
		if i == t.cursor {
			prefix = "▸ "
		}

		statusDot := offlineStyle.Render("○")
		if n.Online {
			statusDot = onlineStyle.Render("●")
		}

		name := n.Hostname
		if name == "" {
			name = truncate(n.ID, 12)
		}

		role := n.Role
		if role == "" {
			role = "?"
		}

		line := fmt.Sprintf("%s %s [%s] %s", statusDot, name, role, n.VIP)
		if i == t.cursor {
			line = selectedStyle.Render(line)
		}

		// 连接线
		neighbors := adj[n.ID]
		connStr := ""
		if len(neighbors) > 0 {
			names := make([]string, 0, len(neighbors))
			for _, nid := range neighbors {
				for _, nn := range g.Nodes {
					if nn.ID == nid {
						hn := nn.Hostname
						if hn == "" {
							hn = truncate(nn.ID, 8)
						}
						names = append(names, hn)
						break
					}
				}
			}
			if len(names) > 0 {
				connStr = tui.StyleHelp.Render(fmt.Sprintf(" ── %s", strings.Join(names, ", ")))
			}
		}

		fmt.Fprintf(&b, "%s%s%s\n", prefix, line, connStr)
	}

	return b.String()
}

func (t *TopoTab) fetchStatus() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("topo.status", nil, func() any { return new(topoStatusResult) })
}

func (t *TopoTab) fetchGraph() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.client.Call("topo.graph", nil, func() any { return new(topoGraphResult) })
}
