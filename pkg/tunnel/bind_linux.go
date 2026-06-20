//go:build linux

package tunnel

import (
	"sync/atomic"
	"syscall"
)

var globalBindInterface atomic.Value // stores string
var globalFwMark atomic.Int32        // stores fwmark (0 = disabled)

// SetBindInterface 设置全局 SO_BINDTODEVICE 网卡名。
func SetBindInterface(ifce string) { globalBindInterface.Store(ifce) }

// ClearBindInterface 清除绑定。
func ClearBindInterface() { globalBindInterface.Store("") }

// SetFwMark 设置全局 SO_MARK，使出站连接打上 fwmark 绕过 TUN 策略路由。
func SetFwMark(mark int) { globalFwMark.Store(int32(mark)) }

// ClearFwMark 清除 fwmark。
func ClearFwMark() { globalFwMark.Store(0) }

// BindControl 是 net.Dialer/net.ListenConfig 的 Control 回调，
// 在 socket 创建后、connect 前设置 SO_BINDTODEVICE + SO_MARK。
func BindControl(network, address string, c syscall.RawConn) error {
	ifce, _ := globalBindInterface.Load().(string)
	mark := int(globalFwMark.Load())
	if ifce == "" && mark == 0 {
		return nil
	}
	var sockErr error
	ctlErr := c.Control(func(fd uintptr) {
		if ifce != "" {
			sockErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET,
				syscall.SO_BINDTODEVICE, ifce)
			if sockErr != nil {
				return
			}
		}
		if mark != 0 {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET,
				syscall.SO_MARK, mark)
		}
	})
	if ctlErr != nil {
		return ctlErr
	}
	return sockErr
}
