package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/x6nux/corelink/internal/tui"
)

// TabBar 渲染 Tab 栏。
func TabBar(names []string, active int, width int) string {
	var tabs []string
	for i, name := range names {
		label := name
		if i == active {
			tabs = append(tabs, tui.StyleActiveTab.Render(label))
		} else {
			tabs = append(tabs, tui.StyleInactiveTab.Render(label))
		}
	}
	bar := strings.Join(tabs, " ")
	return lipgloss.NewStyle().Width(width).Render(bar)
}
