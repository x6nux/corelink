package controller

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// tracerouteHop mirrors ctrlmethods.tracerouteHop for RPC deserialization.
type tracerouteHop struct {
	NodeID    string `json:"node_id"`
	IngressID string `json:"ingress_id"`
	Host      string `json:"host"`
	Port      uint32 `json:"port"`
}

// traceroutePath mirrors ctrlmethods.traceroutePath for RPC deserialization.
type traceroutePath struct {
	Hops      []tracerouteHop `json:"hops"`
	TotalHops int             `json:"total_hops"`
	Active    bool            `json:"active"`
}

// tracerouteResult mirrors ctrlmethods.tracerouteResult for RPC deserialization.
type tracerouteResult struct {
	Paths []traceroutePath `json:"paths"`
}

type traceField int

const (
	traceFieldSrc traceField = iota
	traceFieldDst
)

// TracerouteTab 路由追踪 Tab。
type TracerouteTab struct {
	client    *tui.RPCClient
	src       string
	dst       string
	field     traceField
	result    *tracerouteResult
	loading   bool
	err       error
	pathIndex int // 当前查看的路径索引
}

// NewTracerouteTab 构造 TracerouteTab。
func NewTracerouteTab(client *tui.RPCClient) *TracerouteTab {
	return &TracerouteTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *TracerouteTab) Name() string { return "路由追踪" }

// InputFocused 路由追踪始终有输入框活跃。
func (t *TracerouteTab) InputFocused() bool { return true }

// Init 返回初始 Cmd。
func (t *TracerouteTab) Init() tea.Cmd { return nil }

// Update 处理消息。
func (t *TracerouteTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "topo.traceroute" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*tracerouteResult); ok {
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
	case "up", "down":
		if t.result == nil {
			// 无结果时切换输入字段
			if t.field == traceFieldSrc {
				t.field = traceFieldDst
			} else {
				t.field = traceFieldSrc
			}
			return t, nil
		}
		// 有结果时切换路径
		if k == "down" && t.pathIndex < len(t.result.Paths)-1 {
			t.pathIndex++
		} else if k == "up" && t.pathIndex > 0 {
			t.pathIndex--
		}
		return t, nil
	case "enter":
		if t.src != "" && t.dst != "" {
			return t, t.fetch()
		}
		return t, nil
	case "backspace":
		switch t.field {
		case traceFieldSrc:
			if len(t.src) > 0 {
				t.src = t.src[:len(t.src)-1]
			}
		case traceFieldDst:
			if len(t.dst) > 0 {
				t.dst = t.dst[:len(t.dst)-1]
			}
		}
		return t, nil
	default:
		if len(k) == 1 {
			switch t.field {
			case traceFieldSrc:
				t.src += k
			case traceFieldDst:
				t.dst += k
			}
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

	// Input fields
	srcIndicator := "  "
	dstIndicator := "  "
	if t.field == traceFieldSrc {
		srcIndicator = "▸ "
	} else {
		dstIndicator = "▸ "
	}
	b.WriteString(fmt.Sprintf("%s源节点 ID:   %s█\n", srcIndicator, t.src))
	b.WriteString(fmt.Sprintf("%s目标节点 ID: %s█\n", dstIndicator, t.dst))
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
					// Show hops for selected path
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
	b.WriteString(tui.StyleHelp.Render("  ↑/↓:切换字段/路径  Enter:查询  "))
	return b.String()
}

func (t *TracerouteTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	t.result = nil
	params := map[string]string{
		"src": t.src,
		"dst": t.dst,
	}
	return t.client.Call("topo.traceroute", params, func() any { return new(tracerouteResult) })
}
