package tui

import "github.com/charmbracelet/lipgloss"

// 全局样式主题（终端 256 色兼容）。
var (
	ColorPrimary   = lipgloss.Color("39")  // 蓝
	ColorSuccess   = lipgloss.Color("42")  // 绿
	ColorWarning   = lipgloss.Color("214") // 橙
	ColorError     = lipgloss.Color("196") // 红
	ColorMuted     = lipgloss.Color("245") // 灰
	ColorHighlight = lipgloss.Color("229") // 黄

	StyleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			PaddingLeft(1)

	StyleActiveTab = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("0")).
			Background(ColorPrimary).
			Padding(0, 1)

	StyleInactiveTab = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Padding(0, 1)

	StyleStatusBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	StyleOnline = lipgloss.NewStyle().
			Foreground(ColorSuccess).
			SetString("● 在线")

	StyleOffline = lipgloss.NewStyle().
			Foreground(ColorError).
			SetString("○ 离线")

	StyleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorMuted)

	StyleHelp = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true)

	StyleError = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)
)
