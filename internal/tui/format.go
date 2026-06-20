package tui

import (
	"fmt"
	"time"
)

// FormatUptime 将秒数格式化为人类可读的时长（支持天级）。
func FormatUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// FormatTopoVersion 格式化拓扑版本为 Vx.YY.M.DD.HH-MM-SS。
func FormatTopoVersion(ver uint64, t time.Time) string {
	if t.IsZero() {
		return fmt.Sprintf("V%d", ver)
	}
	return fmt.Sprintf("V%d.%s", ver, t.Format("06.1.02.15-04-05"))
}

// FriendlyRole 将 proto enum 字符串转成中文显示名。
func FriendlyRole(role string) string {
	switch role {
	case "NODE_TOPO_ROLE_TRANSIT":
		return "中转"
	case "NODE_TOPO_ROLE_LEAF":
		return "叶子"
	case "NODE_TOPO_ROLE_UNSPECIFIED":
		return "未分配"
	case "node":
		return "节点"
	case "":
		return "-"
	default:
		return role
	}
}

// Truncate 将字符串截断到指定长度，超长则后缀 "…"。
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}
