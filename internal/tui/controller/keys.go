package controller

import (
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/tui"
)

// keyDTO mirrors ctrlmethods.keyDTO for RPC deserialization.
type keyDTO struct {
	Key       string     `json:"key"`
	Reusable  bool       `json:"reusable"`
	Tag       string     `json:"tag"`
	Revoked   bool       `json:"revoked"`  // 管理员吊销
	Consumed  bool       `json:"consumed"` // 一次性 key 已被消费
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type keysView int

const (
	keysViewList keysView = iota
	keysViewCreate
	keysViewConfirmRevoke
)

// keysCreateForm 简易创建表单状态。
type keysCreateForm struct {
	tag      string
	reusable bool
	ttl      string // 字符串输入的 TTL（秒）
	field    int    // 0=tag, 1=reusable, 2=ttl
}

// KeysTab 密钥 Tab。
type KeysTab struct {
	client  *tui.RPCClient
	keys    []keyDTO
	loading bool
	err     error
	cursor  int
	view    keysView
	form    keysCreateForm
}

// NewKeysTab 构造 KeysTab。
func NewKeysTab(client *tui.RPCClient) *KeysTab {
	return &KeysTab{client: client}
}

// Name 返回 Tab 显示名。
func (t *KeysTab) Name() string { return "密钥" }

// InputFocused 创建表单活跃时有输入焦点。
func (t *KeysTab) InputFocused() bool { return t.view == keysViewCreate }

// Init 返回初始 Cmd。
func (t *KeysTab) Init() tea.Cmd {
	return t.fetchList()
}

// Update 处理消息。
func (t *KeysTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tui.RPCResult:
		return t.handleRPC(msg)
	case tui.TickMsg:
		if t.view == keysViewList {
			return t, t.fetchList()
		}
		return t, nil
	case tea.KeyMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *KeysTab) handleRPC(msg tui.RPCResult) (tea.Model, tea.Cmd) {
	t.loading = false
	switch msg.Method {
	case "keys.list":
		if msg.Err != nil {
			t.err = msg.Err
			return t, nil
		}
		if r, ok := msg.Result.(*[]keyDTO); ok {
			t.keys = *r
			t.err = nil
			if t.cursor >= len(t.keys) {
				t.cursor = max(len(t.keys)-1, 0)
			}
		}
	case "keys.create":
		if msg.Err != nil {
			t.err = msg.Err
		}
		t.view = keysViewList
		return t, t.fetchList()
	case "keys.revoke":
		if msg.Err != nil {
			t.err = msg.Err
		}
		t.view = keysViewList
		return t, t.fetchList()
	}
	return t, nil
}

func (t *KeysTab) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch t.view {
	case keysViewConfirmRevoke:
		switch msg.String() {
		case "y", "Y":
			if t.cursor >= 0 && t.cursor < len(t.keys) {
				return t, t.revokeKey(t.keys[t.cursor].Key)
			}
			t.view = keysViewList
		case "n", "N", "esc":
			t.view = keysViewList
		}
		return t, nil
	case keysViewCreate:
		return t.handleCreateKey(msg)
	default: // list
		switch msg.String() {
		case "j", "down":
			if t.cursor < len(t.keys)-1 {
				t.cursor++
			}
		case "k", "up":
			if t.cursor > 0 {
				t.cursor--
			}
		case "c":
			t.view = keysViewCreate
			t.form = keysCreateForm{}
		case "d":
			if len(t.keys) > 0 && t.cursor < len(t.keys) {
				t.view = keysViewConfirmRevoke
			}
		}
	}
	return t, nil
}

func (t *KeysTab) handleCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	switch k {
	case "esc":
		t.view = keysViewList
		return t, nil
	case "tab", "down":
		t.form.field = (t.form.field + 1) % 3
		return t, nil
	case "shift+tab", "up":
		t.form.field = (t.form.field + 2) % 3
		return t, nil
	case "enter":
		if t.form.field == 2 || k == "enter" {
			return t, t.createKey()
		}
	case " ":
		if t.form.field == 1 {
			t.form.reusable = !t.form.reusable
			return t, nil
		}
	case "backspace":
		switch t.form.field {
		case 0:
			if len(t.form.tag) > 0 {
				t.form.tag = t.form.tag[:len(t.form.tag)-1]
			}
		case 2:
			if len(t.form.ttl) > 0 {
				t.form.ttl = t.form.ttl[:len(t.form.ttl)-1]
			}
		}
		return t, nil
	default:
		if len(k) == 1 {
			switch t.form.field {
			case 0:
				t.form.tag += k
			case 2:
				if k >= "0" && k <= "9" {
					t.form.ttl += k
				}
			}
		}
	}
	return t, nil
}

// View 渲染界面。
func (t *KeysTab) View() string {
	if t.client == nil {
		return renderDisconnected()
	}
	if t.loading && len(t.keys) == 0 {
		return renderLoading()
	}
	if t.err != nil && len(t.keys) == 0 {
		return renderError(t.err)
	}

	switch t.view {
	case keysViewCreate:
		return t.renderCreate()
	case keysViewConfirmRevoke:
		return t.renderConfirmRevoke()
	default:
		return t.renderList()
	}
}

func (t *KeysTab) renderList() string {
	headers := []string{"密钥", "可复用", "标签", "状态", "创建时间"}
	widths := []int{20, 8, 16, 8, 20}
	rows := make([][]string, len(t.keys))
	for i, k := range t.keys {
		keyStr := k.Key
		if len(keyStr) > 16 {
			keyStr = keyStr[:16] + "…"
		}
		reusable := "否"
		if k.Reusable {
			reusable = "是"
		}
		status := "有效"
		switch {
		case k.Revoked:
			status = "已吊销"
		case k.Consumed:
			status = "已使用"
		}
		rows[i] = []string{keyStr, reusable, k.Tag, status, k.CreatedAt.Format("2006-01-02 15:04")}
	}
	table := renderTable(headers, rows, widths, t.cursor)
	help := tui.StyleHelp.Render("  j/k:移动  c:创建  d:吊销  ")
	errLine := ""
	if t.err != nil {
		errLine = "\n" + tui.StyleError.Render(fmt.Sprintf("  错误: %v", t.err))
	}
	return fmt.Sprintf("\n%s\n%s%s", table, help, errLine)
}

func (t *KeysTab) renderCreate() string {
	indicator := func(field int) string {
		if t.form.field == field {
			return "▸ "
		}
		return "  "
	}
	reusableStr := "[ ] 否"
	if t.form.reusable {
		reusableStr = "[x] 是"
	}
	return fmt.Sprintf(
		"\n  %s\n\n%s标签:     %s\n%s可复用:   %s\n%sTTL(秒): %s\n\n  %s",
		tui.StyleTitle.Render("创建注册密钥"),
		indicator(0), t.form.tag+"_",
		indicator(1), reusableStr,
		indicator(2), t.form.ttl+"_",
		tui.StyleHelp.Render("Tab:切换字段  Space:切换复用  Enter:确认  Esc:取消"),
	)
}

func (t *KeysTab) renderConfirmRevoke() string {
	if t.cursor < 0 || t.cursor >= len(t.keys) {
		return t.renderList()
	}
	k := t.keys[t.cursor]
	keyStr := k.Key
	if len(keyStr) > 20 {
		keyStr = keyStr[:20] + "…"
	}
	return fmt.Sprintf(
		"\n  %s\n\n  密钥: %s\n  标签: %s\n\n  %s",
		tui.StyleError.Render("确认吊销密钥？"),
		keyStr, k.Tag,
		tui.StyleHelp.Render("y:确认  n/Esc:取消"),
	)
}

func (t *KeysTab) fetchList() tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("keys.list", nil, func() any { return new([]keyDTO) })
}

func (t *KeysTab) createKey() tea.Cmd {
	// 先解析并校验 TTL：用 strconv.ParseInt 处理 err（含 ErrRange 溢出），
	// 避免手工累加导致超长数字串静默溢出 int64。
	var ttlSec int64
	if t.form.ttl != "" {
		v, err := strconv.ParseInt(t.form.ttl, 10, 64)
		if err != nil || v < 0 {
			t.err = fmt.Errorf("TTL 无效：请输入 0 ~ %d 之间的秒数", int64(^uint64(0)>>1))
			return nil
		}
		ttlSec = v
	}

	if t.client == nil {
		return nil
	}
	t.err = nil
	t.loading = true
	params := map[string]any{
		"reusable":    t.form.reusable,
		"tag":         t.form.tag,
		"ttl_seconds": ttlSec,
	}
	return t.client.Call("keys.create", params, func() any { return new(keyDTO) })
}

func (t *KeysTab) revokeKey(key string) tea.Cmd {
	if t.client == nil {
		return nil
	}
	t.loading = true
	return t.client.Call("keys.revoke", map[string]string{"key": key}, func() any { return new(map[string]string) })
}
