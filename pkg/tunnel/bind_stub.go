//go:build !linux && !darwin && !windows

package tunnel

import "syscall"

// SetBindInterface 其他平台空实现。
func SetBindInterface(_ string) {}

// ClearBindInterface 其他平台空实现。
func ClearBindInterface() {}

// SetFwMark 其他平台空实现。
func SetFwMark(_ int) {}

// ClearFwMark 其他平台空实现。
func ClearFwMark() {}

// BindControl 其他平台空实现。
func BindControl(_, _ string, _ syscall.RawConn) error { return nil }
