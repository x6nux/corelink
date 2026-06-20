package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// KeyMap 全局按键绑定。
type KeyMap struct {
	Quit    key.Binding
	Help    key.Binding
	NextTab key.Binding
	PrevTab key.Binding
}

// DefaultKeyMap 默认按键。
var DefaultKeyMap = KeyMap{
	Quit:    key.NewBinding(key.WithKeys("f10", "ctrl+c"), key.WithHelp("F10", "退出")),
	Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "帮助")),
	NextTab: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "下一个")),
	PrevTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("S-tab", "上一个")),
}

// TabIndexFromKey 从按键消息提取 Tab 索引（0-based）。数字键 1-9 → 0-8，其它 → -1。
func TabIndexFromKey(msg tea.KeyMsg) int {
	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			return int(r - '1')
		}
	}
	return -1
}
