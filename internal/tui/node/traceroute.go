package node

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeRouteHop mirrors nodemethods.HopInfo for RPC deserialization.
type nodeRouteHop struct {
	NodeID    string `json:"node_id"`
	IngressID string `json:"ingress_id"`
	Host      string `json:"host"`
	Port      uint32 `json:"port"`
}

// nodeRoutePath mirrors nodemethods.RouteInfo for RPC deserialization.
type nodeRoutePath struct {
	Hops      []nodeRouteHop `json:"hops"`
	TotalHops int            `json:"total_hops"`
	Active    bool           `json:"active"`
}

// nodeRouteResult mirrors nodemethods.routeTraceResult for RPC deserialization.
type nodeRouteResult struct {
	Paths []nodeRoutePath `json:"paths"`
}

// TracerouteTab 路由追踪 Tab：输入目标节点 ID → 调 route.trace。
type TracerouteTab struct {
	client    *tui.RPCClient
	dst       string
	result    *nodeRouteResult
	loading   bool
	err       error
	pathIndex int
}

// NewTracerouteTab 构造 TracerouteTab。
func NewTracerouteTab(client ...*tui.RPCClient) *TracerouteTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &TracerouteTab{client: c}
}

// SetClient 设置 RPC client。
func (t *TracerouteTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *TracerouteTab) Name() string { return "路由追踪" }

// InputFocused 始终有输入框活跃。
func (t *TracerouteTab) InputFocused() bool { return true }

// Init 返回初始 Cmd。
func (t *TracerouteTab) Init() tea.Cmd { return nil }

// Update 处理消息。
func (t *TracerouteTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "route.trace" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*nodeRouteResult); ok {
				t.result = r
				t.pathIndex = 0
				t.err = nil
			}
		}
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *TracerouteTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "enter":
		if t.dst != "" {
			return t, t.fetch()
		}
		return t, nil
	case "backspace":
		if len(t.dst) > 0 {
			t.dst = t.dst[:len(t.dst)-1]
		}
		return t, nil
	case "j", "down":
		if t.result != nil && t.pathIndex < len(t.result.Paths)-1 {
			t.pathIndex++
		}
		return t, nil
	case "k", "up":
		if t.pathIndex > 0 {
			t.pathIndex--
		}
		return t, nil
	default:
		if len(k) == 1 {
			t.dst += k
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *TracerouteTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("路由追踪"))
	b.WriteString("\n\n")

	// Input field
	b.WriteString(fmt.Sprintf("▸ 目标节点 ID: %s█\n", t.dst))
	b.WriteString("\n")

	if t.loading {
		b.WriteString(tui.StyleHelp.Render("  查询中...\n"))
	} else if t.err != nil {
		b.WriteString(tui.StyleError.Render(fmt.Sprintf("  错误: %v\n", t.err)))
	} else if t.result != nil {
		if len(t.result.Paths) == 0 {
			b.WriteString(tui.StyleHelp.Render("  未找到路径\n"))
		} else {
			b.WriteString(fmt.Sprintf("  找到 %d 条路径\n\n", len(t.result.Paths)))
			for pi, p := range t.result.Paths {
				marker := "  "
				if pi == t.pathIndex {
					marker = "▸ "
				}
				activeStr := ""
				if p.Active {
					activeStr = lipgloss.NewStyle().Foreground(tui.ColorSuccess).Render(" [活跃]")
				}
				b.WriteString(fmt.Sprintf("%s路径 %d (%d 跳)%s\n",
					marker, pi+1, p.TotalHops, activeStr))

				if pi == t.pathIndex {
					for hi, h := range p.Hops {
						addr := ""
						if h.Host != "" {
							addr = fmt.Sprintf(" → %s:%d", h.Host, h.Port)
						}
						prefix := "    ├─ "
						if hi == len(p.Hops)-1 {
							prefix = "    └─ "
						}
						b.WriteString(fmt.Sprintf("%s%s:%s%s\n",
							prefix, h.NodeID, h.IngressID, addr))
					}
				}
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(tui.StyleHelp.Render("  Enter:查询  j/k:切换路径  "))
	return b.String()
}

func (t *TracerouteTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	t.result = nil
	params := map[string]string{"dst": t.dst}
	return t.client.Call("route.trace", params, func() any { return new(nodeRouteResult) })
}
