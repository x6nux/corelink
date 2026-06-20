//go:build !linux

package splittunnel

import "syscall"

// DialControlFwMark 非 Linux 平台空实现。
func DialControlFwMark(_, _ string, _ syscall.RawConn) error {
	return nil
}
