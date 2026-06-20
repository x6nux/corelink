package controller

import (
	"time"

	"github.com/x6nux/corelink/internal/tui"
)

func formatTopoVersion(ver uint64, t time.Time) string { return tui.FormatTopoVersion(ver, t) }
func renderDisconnected() string                       { return tui.RenderDisconnected("corelink-controller") }
func renderLoading() string                            { return tui.RenderLoading() }
func renderError(err error) string                     { return tui.RenderError(err) }
func renderTable(headers []string, rows [][]string, widths []int, selectedRow int) string {
	return tui.RenderTable(headers, rows, widths, selectedRow)
}
func truncate(s string, maxLen int) string { return tui.Truncate(s, maxLen) }
func friendlyRole(role string) string      { return tui.FriendlyRole(role) }
