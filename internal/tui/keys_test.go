package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTabIndexFromKey(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyMsg
		want int
	}{
		// 数字键 1-9 → 0-8
		{name: "按键1→索引0", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}, want: 0},
		{name: "按键5→索引4", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}}, want: 4},
		{name: "按键9→索引8", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}}, want: 8},

		// 非数字字符 → -1
		{name: "按键0→无效", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}}, want: -1},
		{name: "字母a→无效", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, want: -1},
		{name: "特殊字符→无效", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}}, want: -1},

		// 非 rune 按键 → -1
		{name: "Tab键→无效", msg: tea.KeyMsg{Type: tea.KeyTab}, want: -1},
		{name: "Enter键→无效", msg: tea.KeyMsg{Type: tea.KeyEnter}, want: -1},
		{name: "空Runes→无效", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: nil}, want: -1},

		// 多个 rune（理论上不会出现，但防御性测试）
		{name: "多Runes→无效", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1', '2'}}, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TabIndexFromKey(tt.msg)
			if got != tt.want {
				t.Errorf("TabIndexFromKey(%v) = %d, want %d", tt.msg, got, tt.want)
			}
		})
	}
}

func TestDefaultKeyMap_绑定非空(t *testing.T) {
	// 验证默认按键绑定都已配置
	bindings := []struct {
		name string
		keys []string
	}{
		{"Quit", DefaultKeyMap.Quit.Keys()},
		{"Help", DefaultKeyMap.Help.Keys()},
		{"NextTab", DefaultKeyMap.NextTab.Keys()},
		{"PrevTab", DefaultKeyMap.PrevTab.Keys()},
	}

	for _, b := range bindings {
		if len(b.keys) == 0 {
			t.Errorf("DefaultKeyMap.%s 没有绑定任何按键", b.name)
		}
	}
}
