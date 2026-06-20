package controller

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

type logEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs,omitempty"`
}

type logsDataResult struct {
	Entries []logEntry `json:"entries"`
}

// LogsTab 日志 Tab：轮询 system.logs 展示最近日志。
type LogsTab struct {
	client  *tui.RPCClient
	entries []logEntry
	loading bool
	err     error
	scroll  int
	paused  bool
}

// NewLogsTab 构造 LogsTab。
func NewLogsTab(client *tui.RPCClient) *LogsTab {
	return &LogsTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *LogsTab) Name() string { return "日志" }

// Init 返回初始 Cmd。
func (t *LogsTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *LogsTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "system.logs" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*logsDataResult); ok {
				t.entries = r.Entries
				t.err = nil
				if !t.paused {
					// 自动滚动到底部
					t.scroll = max(len(t.entries)-30, 0)
				}
			}
		}
	case tui.TickMsg:
		if !t.paused {
			return t, t.fetch()
		}
		return t, nil
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *LogsTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if t.scroll < len(t.entries)-1 {
			t.scroll++
		}
	case "k", "up":
		if t.scroll > 0 {
			t.scroll--
		}
	case "G":
		t.scroll = max(len(t.entries)-30, 0)
	case "g":
		t.scroll = 0
	case " ", "p":
		t.paused = !t.paused
		if !t.paused {
			return t, t.fetch()
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *LogsTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.entries) == 0 {
		return renderLoading()
	}
	if t.err != nil && len(t.entries) == 0 {
		return renderError(t.err)
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)
	timeStyle := lipgloss.NewStyle().Foreground(tui.ColorMuted)
	msgStyle := lipgloss.NewStyle()
	attrStyle := lipgloss.NewStyle().Foreground(tui.ColorMuted)

	var b strings.Builder
	title := "实时日志"
	if t.paused {
		title += " [已暂停]"
	}
	fmt.Fprintf(&b, "\n%s\n\n", titleStyle.Render(title))

	if len(t.entries) == 0 {
		b.WriteString(tui.StyleHelp.Render("  暂无日志\n"))
	} else {
		visible := 30
		start := t.scroll
		end := start + visible
		if end > len(t.entries) {
			end = len(t.entries)
		}

		for i := start; i < end; i++ {
			e := t.entries[i]
			ts := e.Time.Format("15:04:05")
			level := levelStyle(e.Level).Render(fmt.Sprintf("%-5s", e.Level))
			line := fmt.Sprintf("  %s %s %s",
				timeStyle.Render(ts),
				level,
				msgStyle.Render(e.Message),
			)
			if e.Attrs != "" {
				line += " " + attrStyle.Render(e.Attrs)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}

		fmt.Fprintf(&b, "\n  %s",
			tui.StyleHelp.Render(fmt.Sprintf("[%d/%d] ", end, len(t.entries))))
	}

	b.WriteString("\n")
	b.WriteString(tui.StyleHelp.Render("  j/k:滚动  g/G:首/尾  空格:暂停/继续"))
	return b.String()
}

func (t *LogsTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("system.logs", map[string]int{"count": 200}, func() any { return new(logsDataResult) })
}

func levelStyle(level string) lipgloss.Style {
	switch level {
	case "ERROR":
		return lipgloss.NewStyle().Foreground(tui.ColorError).Bold(true)
	case "WARN":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case "INFO":
		return lipgloss.NewStyle().Foreground(tui.ColorSuccess)
	default:
		return lipgloss.NewStyle().Foreground(tui.ColorMuted)
	}
}
