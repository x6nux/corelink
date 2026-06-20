package controller

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// certDTO mirrors ctrlmethods.certDTO for RPC deserialization.
type certDTO struct {
	Serial    string     `json:"serial"`
	NodeID    string     `json:"node_id"`
	NotAfter  time.Time  `json:"not_after"`
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// caInfoResult mirrors ctrlmethods.caInfoResult for RPC deserialization.
type caInfoResult struct {
	CACertPEM string `json:"ca_cert_pem"`
	CAHash    string `json:"ca_hash"`
}

// CertsTab 证书 Tab。
type CertsTab struct {
	client  *tui.RPCClient
	certs   []certDTO
	caInfo  *caInfoResult
	loading bool
	err     error
	cursor  int
}

// NewCertsTab 构造 CertsTab。
func NewCertsTab(client *tui.RPCClient) *CertsTab {
	return &CertsTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *CertsTab) Name() string { return "证书" }

// Init 返回初始 Cmd。
func (t *CertsTab) Init() tea.Cmd {
	return tea.Batch(t.fetchCerts(), t.fetchCAInfo())
}

// Update 处理消息。
func (t *CertsTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		return t.handleRPC(msg)
	case tui.TickMsg:
		return t, tea.Batch(t.fetchCerts(), t.fetchCAInfo())
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if t.cursor < len(t.certs)-1 {
				t.cursor++
			}
		case "k", "up":
			if t.cursor > 0 {
				t.cursor--
			}
		}
	}
	return t, nil
}

func (t *CertsTab) handleRPC(msg tui.RPCResult) (tea.Model, tea.Cmd) {
	switch msg.Method {
	case "certs.list":
		t.loading = false
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*[]certDTO); ok {
			t.certs = *r
			t.err = nil
			if t.cursor >= len(t.certs) {
				t.cursor = max(len(t.certs)-1, 0)
			}
		}
	case "ca.info":
		if msg.Err != nil {
			// CA info failure is non-fatal
			return t, nil
		}
		if r, ok := msg.Result.(*caInfoResult); ok {
			t.caInfo = r
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *CertsTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.certs) == 0 && t.caInfo == nil {
		return renderLoading()
	}
	if t.err != nil && len(t.certs) == 0 {
		return renderError(t.err)
	}

	var out string
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)

	// CA info card
	if t.caInfo != nil {
		fp := t.caInfo.CAHash
		if len(fp) > 40 {
			fp = fp[:40] + "…"
		}
		cardStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tui.ColorMuted).
			Padding(0, 2).
			MarginLeft(1)
		card := cardStyle.Render(fmt.Sprintf(
			"%s\nCA 哈希: %s",
			titleStyle.Render("CA 信息"),
			fp,
		))
		out += "\n" + card + "\n\n"
	}

	// Certs table
	out += titleStyle.Render("证书列表") + "\n"
	headers := []string{"序列号", "节点", "过期时间", "状态"}
	widths := []int{16, 16, 20, 10}
	rows := make([][]string, len(t.certs))
	for i, c := range t.certs {
		status := "有效"
		if c.Revoked {
			status = "已吊销"
		}
		rows[i] = []string{
			c.Serial,
			c.NodeID,
			c.NotAfter.Format("2006-01-02 15:04"),
			status,
		}
	}
	out += renderTable(headers, rows, widths, t.cursor)
	out += tui.StyleHelp.Render("  j/k:移动  ")
	return out
}

func (t *CertsTab) fetchCerts() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("certs.list", nil, func() any { return new([]certDTO) })
}

func (t *CertsTab) fetchCAInfo() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.client.Call("ca.info", nil, func() any { return new(caInfoResult) })
}
