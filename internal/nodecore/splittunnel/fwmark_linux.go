//go:build linux

package splittunnel

import "syscall"

// DialControlFwMark 是 net.Dialer.Control 回调：为 socket 设置 SO_MARK 绕过分流策略路由。
func DialControlFwMark(network, address string, c syscall.RawConn) error {
	var sErr error
	if err := c.Control(func(fd uintptr) {
		sErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, FwMarkBypass)
	}); err != nil {
		return err
	}
	return sErr
}
