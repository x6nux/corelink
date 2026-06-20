package node

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeIngressDTO mirrors nodemethods.IngressInfo for RPC deserialization.
type nodeIngressDTO struct {
	Host       string `json:"host"`
	Port       uint32 `json:"port"`
	Source     string `json:"source"`
	Confidence uint32 `json:"confidence"`
	NATType    string `json:"nat_type"`
}

// IngressTab 入口 Tab：调 ingress.list 展示入口列表。
type IngressTab struct {
	client  *tui.RPCClient
	items   []nodeIngressDTO
	loading bool
	err     error
}

// NewIngressTab 构造 IngressTab。
func NewIngressTab(client ...*tui.RPCClient) *IngressTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &IngressTab{client: c}
}

// SetClient 设置 RPC client。
func (t *IngressTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *IngressTab) Name() string { return "入口" }

// Init 返回初始 Cmd。
func (t *IngressTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *IngressTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "ingress.list" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*[]nodeIngressDTO); ok {
				t.items = *r
				t.err = nil
			}
		}
	case tui.TickMsg:
		return t, t.fetch()
	}
	return t, nil
}

// View 渲染界面。
func (t *IngressTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.items) == 0 {
		return renderLoading()
	}
	if t.err != nil && len(t.items) == 0 {
		return renderError(t.err)
	}

	headers := []string{"来源", "地址", "置信度", "NAT 类型"}
	widths := []int{10, 24, 8, 16}
	rows := make([][]string, len(t.items))
	for i, ing := range t.items {
		rows[i] = []string{
			ing.Source,
			fmt.Sprintf("%s:%d", ing.Host, ing.Port),
			fmt.Sprintf("%d", ing.Confidence),
			ing.NATType,
		}
	}
	table := renderTable(headers, rows, widths, -1)

	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	return fmt.Sprintf("\n%s%s", table, errLine)
}

func (t *IngressTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("ingress.list", nil, func() any { return new([]nodeIngressDTO) })
}
