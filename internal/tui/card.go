package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// CardWidth 默认卡片宽度。
const CardWidth = 24

// CardOpts 卡片渲染选项。
type CardOpts struct {
	Width      int            // 卡片宽度，0 则取 CardWidth
	ValueColor lipgloss.Color // 值文本颜色，零值使用默认前景
}

// RenderCard 渲染一张固定高度的信息卡片。
func RenderCard(label, value string, opts ...CardOpts) string {
	width := CardWidth
	var valColor lipgloss.Color
	if len(opts) > 0 {
		if opts[0].Width > 0 {
			width = opts[0].Width
		}
		valColor = opts[0].ValueColor
	}

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorMuted).
		Padding(0, 2).
		Width(width).
		Height(3)

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	valueStyle := lipgloss.NewStyle()
	if valColor != "" {
		valueStyle = valueStyle.Foreground(valColor)
	}

	innerWidth := width - 6 // border(2) + padding(4)
	return cardStyle.Render(fmt.Sprintf(
		"%s\n%s",
		labelStyle.Render(label),
		valueStyle.Render(Truncate(value, innerWidth)),
	))
}

// JoinCards 横向拼接一组卡片，间距 2 字符。
func JoinCards(cards ...string) string {
	if len(cards) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cards)*2-1)
	for i, c := range cards {
		if i > 0 {
			parts = append(parts, "  ")
		}
		parts = append(parts, c)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}
