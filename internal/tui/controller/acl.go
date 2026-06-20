package controller

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// aclDTO mirrors ctrlmethods.aclDTO for RPC deserialization.
type aclDTO struct {
	Version   uint      `json:"version"`
	Document  string    `json:"document"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type aclView int

const (
	aclViewDisplay aclView = iota
	aclViewEdit
	aclViewHistory
	aclViewConfirmSave
)

// ACLTab ACL 策略 Tab。
type ACLTab struct {
	client  *tui.RPCClient
	policy  *aclDTO
	history []aclDTO
	loading bool
	err     error
	view    aclView
	editBuf string // 编辑缓冲区
	scroll  int    // 文本滚动偏移
}

// NewACLTab 构造 ACLTab。
func NewACLTab(client *tui.RPCClient) *ACLTab {
	return &ACLTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *ACLTab) Name() string { return "ACL" }

// InputFocused 编辑模式时有输入焦点。
func (t *ACLTab) InputFocused() bool { return t.view == aclViewEdit }

// Init 返回初始 Cmd。
func (t *ACLTab) Init() tea.Cmd {
	return t.fetchPolicy()
}

// Update 处理消息。
func (t *ACLTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		return t.handleRPC(msg)
	case tui.TickMsg:
		if t.view == aclViewDisplay {
			return t, t.fetchPolicy()
		}
		return t, nil
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *ACLTab) handleRPC(msg tui.RPCResult) (tea.Model, tea.Cmd) {
	t.loading = false
	switch msg.Method {
	case "acl.get":
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*aclDTO); ok {
			t.policy = r
			t.err = nil
		}
	case "acl.set":
		if msg.Err != nil {
			t.err = msg.Err
			t.view = aclViewDisplay
			return t, nil
		}
		if r, ok := msg.Result.(*aclDTO); ok {
			t.policy = r
			t.err = nil
		}
		t.view = aclViewDisplay
	case "acl.history":
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*[]aclDTO); ok {
			t.history = *r
			t.view = aclViewHistory
			t.err = nil
		}
	}
	return t, nil
}

func (t *ACLTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch t.view {
	case aclViewEdit:
		return t.handleEditKey(msg)
	case aclViewConfirmSave:
		switch msg.String() {
		case "y", "Y":
			return t, t.savePolicy()
		case "n", "N", "esc":
			t.view = aclViewEdit
		}
		return t, nil
	case aclViewHistory:
		switch msg.String() {
		case "esc":
			t.view = aclViewDisplay
		case "j", "down":
			t.scroll++
		case "k", "up":
			if t.scroll > 0 {
				t.scroll--
			}
		}
		return t, nil
	default: // display
		switch msg.String() {
		case "e":
			t.view = aclViewEdit
			if t.policy != nil {
				t.editBuf = t.policy.Document
			}
		case "h":
			return t, t.fetchHistory()
		case "j", "down":
			t.scroll++
		case "k", "up":
			if t.scroll > 0 {
				t.scroll--
			}
		}
	}
	return t, nil
}

func (t *ACLTab) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc":
		t.view = aclViewDisplay
	case "ctrl+s":
		t.view = aclViewConfirmSave
	case "enter":
		t.editBuf += "\n"
	case "backspace":
		if len(t.editBuf) > 0 {
			t.editBuf = t.editBuf[:len(t.editBuf)-1]
		}
	default:
		if len(k) == 1 || k == " " {
			t.editBuf += k
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *ACLTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && t.policy == nil {
		return renderLoading()
	}
	if t.err != nil && t.policy == nil {
		return renderError(t.err)
	}

	switch t.view {
	case aclViewEdit:
		return t.renderEdit()
	case aclViewConfirmSave:
		return t.renderConfirmSave()
	case aclViewHistory:
		return t.renderHistory()
	default:
		return t.renderDisplay()
	}
}

func (t *ACLTab) renderDisplay() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)
	var b strings.Builder
	b.WriteString("\n")
	if t.policy != nil {
		b.WriteString(titleStyle.Render(fmt.Sprintf("ACL 策略 (版本 %d)", t.policy.Version)))
		b.WriteString("\n\n")

		// Scrollable viewport
		lines := strings.Split(t.policy.Document, "\n")
		maxVisible := 20
		start := t.scroll
		if start > len(lines) {
			start = len(lines)
		}
		end := start + maxVisible
		if end > len(lines) {
			end = len(lines)
		}
		for _, line := range lines[start:end] {
			b.WriteString("  " + line + "\n")
		}
		if len(lines) > maxVisible {
			b.WriteString(tui.StyleHelp.Render(fmt.Sprintf("\n  [%d-%d / %d 行]", start+1, end, len(lines))))
		}
	} else {
		b.WriteString(tui.StyleHelp.Render("  （暂无策略）"))
	}
	b.WriteString("\n")
	b.WriteString(tui.StyleHelp.Render("  e:编辑  h:历史  j/k:滚动  "))
	if t.err != nil {
		b.WriteString("\n" + tui.StyleError.Render(fmt.Sprintf("  错误: %v", t.err)))
	}
	return b.String()
}

func (t *ACLTab) renderEdit() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorWarning).PaddingLeft(1)
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("编辑 ACL 策略"))
	b.WriteString("\n\n")
	b.WriteString("  " + strings.ReplaceAll(t.editBuf, "\n", "\n  "))
	b.WriteString("█\n\n")
	b.WriteString(tui.StyleHelp.Render("  Ctrl+S:保存  Esc:取消"))
	return b.String()
}

func (t *ACLTab) renderConfirmSave() string {
	return fmt.Sprintf(
		"\n  %s\n\n  %s",
		tui.StyleError.Render("确认保存 ACL 策略？"),
		tui.StyleHelp.Render("y:确认  n/Esc:取消"),
	)
}

func (t *ACLTab) renderHistory() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("ACL 策略历史"))
	b.WriteString("\n\n")

	headers := []string{"版本", "作者", "创建时间"}
	widths := []int{8, 16, 20}
	rows := make([][]string, len(t.history))
	for i, h := range t.history {
		rows[i] = []string{
			fmt.Sprintf("v%d", h.Version),
			h.Author,
			h.CreatedAt.Format("2006-01-02 15:04"),
		}
	}
	b.WriteString(renderTable(headers, rows, widths, -1))
	b.WriteString(tui.StyleHelp.Render("  Esc:返回  "))
	return b.String()
}

func (t *ACLTab) fetchPolicy() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("acl.get", nil, func() any { return new(aclDTO) })
}

func (t *ACLTab) fetchHistory() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("acl.history", nil, func() any { return new([]aclDTO) })
}

func (t *ACLTab) savePolicy() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	params := map[string]string{
		"document": t.editBuf,
		"author":   "tui",
	}
	return t.client.Call("acl.set", params, func() any { return new(aclDTO) })
}
