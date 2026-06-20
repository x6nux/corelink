package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestThemeColors_非空(t *testing.T) {
	// 验证所有颜色常量都已定义（非零值）
	colors := []struct {
		name  string
		color lipgloss.Color
	}{
		{"ColorPrimary", ColorPrimary},
		{"ColorSuccess", ColorSuccess},
		{"ColorWarning", ColorWarning},
		{"ColorError", ColorError},
		{"ColorMuted", ColorMuted},
		{"ColorHighlight", ColorHighlight},
	}

	for _, c := range colors {
		if c.color == "" {
			t.Errorf("%s 不应为零值", c.name)
		}
	}
}

func TestThemeStyles_可渲染(t *testing.T) {
	// 验证所有样式常量都能正常渲染（不 panic），且产出非空字符串
	styles := []struct {
		name  string
		style lipgloss.Style
	}{
		{"StyleTitle", StyleTitle},
		{"StyleActiveTab", StyleActiveTab},
		{"StyleInactiveTab", StyleInactiveTab},
		{"StyleStatusBar", StyleStatusBar},
		{"StyleOnline", StyleOnline},
		{"StyleOffline", StyleOffline},
		{"StyleBorder", StyleBorder},
		{"StyleHelp", StyleHelp},
		{"StyleError", StyleError},
	}

	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			out := s.style.Render("test")
			if len(out) == 0 {
				t.Errorf("%s.Render(\"test\") 返回空字符串", s.name)
			}
		})
	}
}

func TestThemeStyles_预设文本(t *testing.T) {
	// StyleOnline 和 StyleOffline 使用 SetString 预设了文本
	onlineOut := StyleOnline.String()
	if len(onlineOut) == 0 {
		t.Error("StyleOnline 应有预设文本 '● 在线'")
	}

	offlineOut := StyleOffline.String()
	if len(offlineOut) == 0 {
		t.Error("StyleOffline 应有预设文本 '○ 离线'")
	}
}
