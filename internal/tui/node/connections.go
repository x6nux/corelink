package node

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// nodeConnectionDTO mirrors nodemethods.ConnectionInfo for RPC deserialization.
type nodeConnectionDTO struct {
	PeerID     string `json:"peer_id"`
	VIP        string `json:"vip"`
	PeerIP     string `json:"peer_ip"`
	InternalIP string `json:"internal_ip"`
	LinkType   string `json:"link_type"`
	RTTms      uint32 `json:"rtt_ms"`
	RTTValid   bool   `json:"rtt_valid"`
	Loss       uint32 `json:"loss_permille"`
	LossValid  bool   `json:"loss_valid"`
	State      string `json:"state"`
}

type connectionStatusFilter int

const (
	connectionStatusConnected connectionStatusFilter = iota
	connectionStatusDisconnected
	connectionStatusAll
)

// ConnectionsTab 连接 Tab：调 connections.list 展示连接列表。
type ConnectionsTab struct {
	client       *tui.RPCClient
	items        []nodeConnectionDTO
	loading      bool
	err          error
	statusFilter connectionStatusFilter
	selectedPeer string
}

// NewConnectionsTab 构造 ConnectionsTab。
func NewConnectionsTab(client ...*tui.RPCClient) *ConnectionsTab {
	var c *tui.RPCClient
	if len(client) > 0 {
		c = client[0]
	}
	return &ConnectionsTab{client: c}
}

// SetClient 设置 RPC client。
func (t *ConnectionsTab) SetClient(c *tui.RPCClient) { t.client = c }

// Name 返回 Tab 显示名。
func (t *ConnectionsTab) Name() string { return "连接" }

// Init 返回初始 Cmd。
func (t *ConnectionsTab) Init() tea.Cmd {
	return t.fetch()
}

// Update 处理消息。
func (t *ConnectionsTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		if msg.Method == "connections.list" {
			t.loading = false
			if msg.Err != nil {
				t.err = msg.Err
				return t, nil
			}
			if r, ok := msg.Result.(*[]nodeConnectionDTO); ok {
				t.items = *r
				t.err = nil
			}
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "s":
			t.statusFilter = (t.statusFilter + 1) % 3
		case "]":
			t.selectPeer(1)
		case "[":
			t.selectPeer(-1)
		case "a":
			t.selectedPeer = ""
		}
	case tui.TickMsg:
		return t, t.fetch()
	}
	return t, nil
}

// View 渲染界面。
func (t *ConnectionsTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.items) == 0 {
		return renderLoading()
	}
	if t.err != nil && len(t.items) == 0 {
		return renderError(t.err)
	}

	items := t.filteredItems()
	headers := []string{"对端", "VIP", "对端IP", "内网IP", "链路", "RTT", "丢包", "状态"}
	widths := []int{14, 14, 22, 20, 12, 8, 8, 12}
	rows := make([][]string, len(items))
	for i, c := range items {
		rows[i] = []string{
			c.PeerID,
			c.VIP,
			c.PeerIP,
			c.InternalIP,
			c.LinkType,
			formatMetric(c.RTTms, c.RTTValid),
			formatMetric(c.Loss, c.LossValid),
			c.State,
		}
	}
	table := renderTable(headers, rows, widths, -1)

	filterLine := fmt.Sprintf("  状态: %s  节点: %s", t.statusFilterLabel(), t.peerFilterLabel())
	var errLine string
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  刷新失败: %v", t.err))
	}

	return fmt.Sprintf("\n%s\n%s%s", filterLine, table, errLine)
}

func (t *ConnectionsTab) fetch() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("connections.list", nil, func() any { return new([]nodeConnectionDTO) })
}

func (t *ConnectionsTab) filteredItems() []nodeConnectionDTO {
	out := make([]nodeConnectionDTO, 0, len(t.items))
	for _, item := range t.items {
		if t.selectedPeer != "" && item.PeerID != t.selectedPeer {
			continue
		}
		connected := isConnectionConnected(item.State)
		switch t.statusFilter {
		case connectionStatusConnected:
			if !connected {
				continue
			}
		case connectionStatusDisconnected:
			if connected {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func (t *ConnectionsTab) selectPeer(delta int) {
	peers := uniqueConnectionPeers(t.items)
	if len(peers) == 0 {
		t.selectedPeer = ""
		return
	}
	idx := 0
	if t.selectedPeer != "" {
		for i, peer := range peers {
			if peer == t.selectedPeer {
				idx = i
				break
			}
		}
	}
	idx = (idx + delta + len(peers)) % len(peers)
	t.selectedPeer = peers[idx]
}

func uniqueConnectionPeers(items []nodeConnectionDTO) []string {
	seen := make(map[string]struct{})
	peers := make([]string, 0, len(items))
	for _, item := range items {
		if item.PeerID == "" {
			continue
		}
		if _, ok := seen[item.PeerID]; ok {
			continue
		}
		seen[item.PeerID] = struct{}{}
		peers = append(peers, item.PeerID)
	}
	sort.Strings(peers)
	return peers
}

func isConnectionConnected(state string) bool {
	s := strings.ToLower(strings.TrimSpace(state))
	switch s {
	case "active", "connected", "established", "up", "已连接":
		return true
	default:
		return false
	}
}

func (t *ConnectionsTab) statusFilterLabel() string {
	switch t.statusFilter {
	case connectionStatusDisconnected:
		return "未连接"
	case connectionStatusAll:
		return "全部"
	default:
		return "已连接"
	}
}

func (t *ConnectionsTab) peerFilterLabel() string {
	if t.selectedPeer == "" {
		return "全部"
	}
	return t.selectedPeer
}

func formatMetric(v uint32, valid bool) string {
	if !valid {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}
