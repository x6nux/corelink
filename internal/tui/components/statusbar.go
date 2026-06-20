package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/x6nux/corelink/internal/tui"
)

// StatusBar 渲染底部状态栏。
func StatusBar(connected bool, help string, width int) string {
	connStr := "● 已连接"
	if !connected {
		connStr = "○ 未连接"
	}
	left := tui.StyleStatusBar.Render(connStr)
	right := tui.StyleStatusBar.Render(help)
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 0)
	mid := tui.StyleStatusBar.Render(fmt.Sprintf("%*s", gap, ""))
	return left + mid + right
}
