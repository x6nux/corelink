package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderTable 用 lipgloss 渲染一个简单的文本表格。
// headers 是表头列表，rows 是每行对应的列数据，widths 是各列宽度。
// selectedRow 为当前选中行（-1 表示无选中）。
func RenderTable(headers []string, rows [][]string, widths []int, selectedRow int) string {
	if len(headers) != len(widths) {
		return "表格配置错误"
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	cellStyle := lipgloss.NewStyle()
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	separatorStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	var b strings.Builder

	// Header
	b.WriteString("  ")
	for i, h := range headers {
		b.WriteString(headerStyle.Width(widths[i]).Render(h))
		if i < len(headers)-1 {
			b.WriteString(" ")
		}
	}
	b.WriteByte('\n')

	// Separator
	b.WriteString("  ")
	for i, w := range widths {
		b.WriteString(separatorStyle.Render(strings.Repeat("─", w)))
		if i < len(widths)-1 {
			b.WriteString(" ")
		}
	}
	b.WriteByte('\n')

	// Rows
	for ri, row := range rows {
		b.WriteString("  ")
		for ci, cell := range row {
			if ci >= len(widths) {
				break
			}
			s := cellStyle
			if ri == selectedRow {
				s = selectedStyle
			}
			b.WriteString(s.Width(widths[ci]).Render(Truncate(cell, widths[ci])))
			if ci < len(widths)-1 {
				b.WriteString(" ")
			}
		}
		b.WriteByte('\n')
	}

	if len(rows) == 0 {
		b.WriteString(StyleHelp.Render("  （无数据）\n"))
	}

	return b.String()
}

// RenderDisconnected 渲染未连接守护进程提示（通用）。
func RenderDisconnected(daemonName string) string {
	return StyleHelp.Render(fmt.Sprintf("\n  未连接守护进程。请确认 %s serve 正在运行。", daemonName))
}

// RenderLoading 渲染加载中提示。
func RenderLoading() string {
	return StyleHelp.Render("\n  加载中...")
}

// RenderError 渲染错误提示。
func RenderError(err error) string {
	return "\n  " + StyleError.Render(fmt.Sprintf("错误: %v", err))
}

// RenderSectionHeader 渲染分组标题。
func RenderSectionHeader(title string) string {
	style := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).PaddingLeft(1)
	return style.Render(title)
}
