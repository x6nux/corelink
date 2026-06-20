package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tab 是一个可嵌入 App 的 Tab 页。
type Tab interface {
	tea.Model
	Name() string // Tab 显示名
}

// InputFocusable 可选接口：Tab 有文本输入焦点时返回 true，
// App 层据此跳过数字键/Tab 键的全局拦截，避免输入冲突。
type InputFocusable interface {
	InputFocused() bool
}

// AppConfig 构造 App 的参数。
type AppConfig struct {
	Title  string
	Tabs   []Tab
	Client *RPCClient // 封装的 RPC client
}

// App 顶层 TUI Model：Tab 导航 + 状态栏 + 委派活跃 Tab 的 Update/View。
type App struct {
	title       string
	tabs        []Tab
	activeTab   int
	client      *RPCClient
	width       int
	height      int
	connected   bool
	tickSeconds int // 全局 Tick 刷新周期（秒）
}

// NewApp 构造 App。
func NewApp(cfg AppConfig) *App {
	return &App{
		title:       cfg.Title,
		tabs:        cfg.Tabs,
		activeTab:   0,
		client:      cfg.Client,
		connected:   cfg.Client != nil,
		tickSeconds: 2, // 全局每 2s 刷新一次
	}
}

// Init 返回初始 Cmd：App 层统一装弹 TickCmd（#13）+ 收集所有 Tab 的 Init()（#14a）。
// - TickCmd 始终装弹：Tick 链由 App 层独占驱动，无 Tab 时也返回非 nil（不破坏 #13 断言）。
// - 遍历所有 Tab 的 Init：确保非首个 Tab 切换后首屏也已加载数据（修复仅 tabs[0] 加载）。
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{TickCmd(a.tickSeconds)}
	for _, t := range a.tabs {
		if c := t.Init(); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

// Update 处理消息：全局按键 + 委派活跃 Tab。
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 退出
		if key.Matches(msg, DefaultKeyMap.Quit) {
			return a, tea.Quit
		}

		// 活跃 Tab 有输入焦点时，跳过全局导航键拦截，直接委派给 Tab 处理。
		focused := false
		if len(a.tabs) > 0 {
			if f, ok := a.tabs[a.activeTab].(InputFocusable); ok {
				focused = f.InputFocused()
			}
		}

		// Esc 退出输入焦点（始终可用）——委派给 Tab 处理后返回，
		// 下次按键 focused 变 false 即可恢复全局导航。
		// tab / shift+tab 始终可用于切换标签页，不受输入焦点限制。
		if key.Matches(msg, DefaultKeyMap.NextTab) {
			a.activeTab = (a.activeTab + 1) % len(a.tabs)
			return a, a.tabs[a.activeTab].Init()
		}
		if key.Matches(msg, DefaultKeyMap.PrevTab) {
			a.activeTab = (a.activeTab - 1 + len(a.tabs)) % len(a.tabs)
			return a, a.tabs[a.activeTab].Init()
		}

		// 数字键切 Tab——仅在无输入焦点时拦截
		if !focused {
			if idx := TabIndexFromKey(msg); idx >= 0 && idx < len(a.tabs) {
				a.activeTab = idx
				return a, a.tabs[a.activeTab].Init()
			}
		}

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		// 委派给活跃 Tab 处理窗口大小变化

	case TickMsg:
		// Tick 链由 App 层独占续约：无条件重装弹，再委派活跃 Tab。
		// 活跃 Tab 收到 TickMsg 只做 fetch，不再自行装弹（见各 Tab）。
		rearm := TickCmd(a.tickSeconds)
		if len(a.tabs) > 0 {
			updated, cmd := a.tabs[a.activeTab].Update(msg)
			if tab, ok := updated.(Tab); ok {
				a.tabs[a.activeTab] = tab
			}
			return a, tea.Batch(rearm, cmd)
		}
		return a, rearm
	}

	// 委派给活跃 Tab
	if len(a.tabs) > 0 {
		updated, cmd := a.tabs[a.activeTab].Update(msg)
		if tab, ok := updated.(Tab); ok {
			a.tabs[a.activeTab] = tab
		}
		return a, cmd
	}
	return a, nil
}

// View 渲染界面。
func (a *App) View() string {
	var b strings.Builder

	// Title
	b.WriteString(StyleTitle.Render(a.title))
	b.WriteByte('\n')

	// TabBar — 内联渲染，避免 tui → tui/components 循环依赖
	b.WriteString(renderTabBar(a.tabs, a.activeTab, a.width))
	b.WriteByte('\n')

	// 活跃 Tab View
	if len(a.tabs) > 0 {
		b.WriteString(a.tabs[a.activeTab].View())
	}
	b.WriteByte('\n')

	// StatusBar — 内联渲染
	b.WriteString(renderStatusBar(a.connected, "F10:退出  ?:帮助", a.width))

	return b.String()
}

// renderTabBar 渲染 Tab 栏（同 components.TabBar 逻辑，使用本包样式）。
func renderTabBar(tabs []Tab, active, width int) string {
	var parts []string
	for i, t := range tabs {
		if i == active {
			parts = append(parts, StyleActiveTab.Render(t.Name()))
		} else {
			parts = append(parts, StyleInactiveTab.Render(t.Name()))
		}
	}
	bar := strings.Join(parts, " ")
	return lipgloss.NewStyle().Width(width).Render(bar)
}

// renderStatusBar 渲染底部状态栏（同 components.StatusBar 逻辑，使用本包样式）。
func renderStatusBar(connected bool, help string, width int) string {
	connStr := "● 已连接"
	if !connected {
		connStr = "○ 未连接"
	}
	left := StyleStatusBar.Render(connStr)
	right := StyleStatusBar.Render(help)
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	mid := StyleStatusBar.Render(fmt.Sprintf("%*s", gap, ""))
	return left + mid + right
}
