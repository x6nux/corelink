package controller

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeDTO mirrors ctrlmethods.nodeDTO for RPC deserialization.
type nodeDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Remark     string `json:"remark,omitempty"`
	Hostname   string `json:"hostname"`
	VIP        string `json:"vip"`
	Role       string `json:"role"`
	Online     bool   `json:"online"`
	Generation uint64 `json:"generation"`
}

// ingressDTO mirrors ctrlmethods.ingressDTO for RPC deserialization.
type ingressDTO struct {
	Host       string `json:"host"`
	Port       uint32 `json:"port"`
	Source     string `json:"source"`
	Confidence uint32 `json:"confidence"`
	NatType    string `json:"nat_type"`
}

// nodeDetailDTO mirrors ctrlmethods.nodeDetailDTO for RPC deserialization.
type nodeDetailDTO struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Remark     string       `json:"remark,omitempty"`
	Hostname   string       `json:"hostname"`
	VIP        string       `json:"vip"`
	Role       string       `json:"role"`
	Online     bool         `json:"online"`
	Generation uint64       `json:"generation"`
	Ingresses  []ingressDTO `json:"ingresses"`
}

type nodesView int

const (
	nodesViewList nodesView = iota
	nodesViewDetail
	nodesViewConfirmDelete
)

// NodesTab 节点 Tab：列表 / 详情 / 删除确认。
type NodesTab struct {
	client    *tui.RPCClient
	nodes     []nodeDTO
	detail    *nodeDetailDTO
	loading   bool
	err       error
	cursor    int
	view      nodesView
	deleteIdx int // 待删除的节点索引
}

// NewNodesTab 构造 NodesTab。
func NewNodesTab(client *tui.RPCClient) *NodesTab {
	return &NodesTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *NodesTab) Name() string { return "节点" }

// Init 返回初始 Cmd。
func (t *NodesTab) Init() tea.Cmd {
	return t.fetchList()
}

// Update 处理消息。
func (t *NodesTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		return t.handleRPC(msg)
	case tui.TickMsg:
		if t.view == nodesViewList {
			return t, t.fetchList()
		}
		return t, nil
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *NodesTab) handleRPC(msg tui.RPCResult) (tea.Model, tea.Cmd) {
	t.loading = false
	switch msg.Method {
	case "nodes.list":
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*[]nodeDTO); ok {
			t.nodes = *r
			t.err = nil
			if t.cursor >= len(t.nodes) {
				t.cursor = max(len(t.nodes)-1, 0)
			}
		}
	case "nodes.get":
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*nodeDetailDTO); ok {
			t.detail = r
			t.view = nodesViewDetail
			t.err = nil
		}
	case "nodes.delete":
		if msg.Err != nil {
			t.err = msg.Err
		}
		t.view = nodesViewList
		return t, t.fetchList()
	}
	return t, nil
}

func (t *NodesTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch t.view {
	case nodesViewConfirmDelete:
		switch msg.String() {
		case "y", "Y":
			if t.deleteIdx >= 0 && t.deleteIdx < len(t.nodes) {
				id := t.nodes[t.deleteIdx].ID
				return t, t.deleteNode(id)
			}
			t.view = nodesViewList
		case "n", "N", "esc":
			t.view = nodesViewList
		}
		return t, nil
	case nodesViewDetail:
		if msg.String() == "esc" {
			t.view = nodesViewList
			t.detail = nil
		}
		return t, nil
	default: // list
		switch msg.String() {
		case "j", "down":
			if t.cursor < len(t.nodes)-1 {
				t.cursor++
			}
		case "k", "up":
			if t.cursor > 0 {
				t.cursor--
			}
		case "enter":
			if len(t.nodes) > 0 && t.cursor < len(t.nodes) {
				return t, t.fetchDetail(t.nodes[t.cursor].ID)
			}
		case "d":
			if len(t.nodes) > 0 && t.cursor < len(t.nodes) {
				t.deleteIdx = t.cursor
				t.view = nodesViewConfirmDelete
			}
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *NodesTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.nodes) == 0 && t.detail == nil {
		return renderLoading()
	}
	if t.err != nil && len(t.nodes) == 0 && t.detail == nil {
		return renderError(t.err)
	}

	switch t.view {
	case nodesViewDetail:
		return t.renderDetail()
	case nodesViewConfirmDelete:
		return t.renderConfirmDelete()
	default:
		return t.renderList()
	}
}

func (t *NodesTab) renderList() string {
	headers := []string{"ID", "名称", "虚拟IP", "角色", "状态"}
	widths := []int{8, 18, 18, 10, 8}
	rows := make([][]string, len(t.nodes))
	for i, n := range t.nodes {
		status := "离线"
		if n.Online {
			status = "在线"
		}
		rows[i] = []string{n.ID, n.Name, n.VIP, n.Role, status}
	}
	table := renderTable(headers, rows, widths, t.cursor)
	help := tui.StyleHelp.Render("  j/k:移动  Enter:详情  d:删除  ")
	errLine := ""
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}
	return fmt.Sprintf("\n%s\n%s%s", table, help, errLine)
}

func (t *NodesTab) renderDetail() string {
	if t.detail == nil {
		return renderLoading()
	}
	d := t.detail
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary).PaddingLeft(1)

	status := tui.StyleOffline.String()
	if d.Online {
		status = tui.StyleOnline.String()
	}

	remarkLine := ""
	if d.Remark != "" {
		remarkLine = fmt.Sprintf("  备注:     %s\n", d.Remark)
	}

	info := fmt.Sprintf(
		"\n%s\n\n  名称:     %s\n%s  ID:       %s\n  主机名:   %s\n  虚拟IP:   %s\n  角色:     %s\n  状态:     %s\n  世代:     %d\n",
		titleStyle.Render("节点详情"),
		d.Name, remarkLine, d.ID, d.Hostname, d.VIP, d.Role, status, d.Generation,
	)

	// Ingress sub-table
	if len(d.Ingresses) > 0 {
		info += fmt.Sprintf("\n%s\n", titleStyle.Render("入口列表"))
		iHeaders := []string{"地址", "来源", "置信度", "NAT类型"}
		iWidths := []int{24, 12, 8, 14}
		iRows := make([][]string, len(d.Ingresses))
		for i, ing := range d.Ingresses {
			iRows[i] = []string{
				fmt.Sprintf("%s:%d", ing.Host, ing.Port),
				ing.Source,
				fmt.Sprintf("%d", ing.Confidence),
				ing.NatType,
			}
		}
		info += renderTable(iHeaders, iRows, iWidths, -1)
	}

	info += tui.StyleHelp.Render("\n  Esc:返回列表")
	return info
}

func (t *NodesTab) renderConfirmDelete() string {
	if t.deleteIdx < 0 || t.deleteIdx >= len(t.nodes) {
		return t.renderList()
	}
	n := t.nodes[t.deleteIdx]
	return fmt.Sprintf(
		"\n  %s\n\n  节点: %s (%s)\n\n  %s",
		tui.StyleError.Render("确认删除节点？"),
		n.Name, n.ID,
		tui.StyleHelp.Render("y:确认  n/Esc:取消"),
	)
}

func (t *NodesTab) fetchList() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("nodes.list", nil, func() any { return new([]nodeDTO) })
}

func (t *NodesTab) fetchDetail(id string) tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("nodes.get", map[string]string{"id": id}, func() any { return new(nodeDetailDTO) })
}

func (t *NodesTab) deleteNode(id string) tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("nodes.delete", map[string]string{"id": id}, func() any { return new(map[string]string) })
}
