//go:build darwin

package tunnel

import (
	"net"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

var globalBindInterface atomic.Value // stores string
var globalFwMark atomic.Int32        // macOS 无 SO_MARK，保留 API 兼容

// SetBindInterface 设置全局绑定网卡名（macOS 使用 IP_BOUND_IF 实现）。
func SetBindInterface(ifce string) { globalBindInterface.Store(ifce) }

// ClearBindInterface 清除绑定。
func ClearBindInterface() { globalBindInterface.Store("") }

// SetFwMark macOS 不支持 SO_MARK，空实现保持 API 兼容。
func SetFwMark(_ int) {}

// ClearFwMark macOS 不支持 SO_MARK，空实现保持 API 兼容。
func ClearFwMark() {}

// BindControl 是 net.Dialer/net.ListenConfig 的 Control 回调，
// 在 socket 创建后、connect 前通过 IP_BOUND_IF / IPV6_BOUND_IF 绑定到指定接口。
// macOS 不支持 SO_BINDTODEVICE，使用 IP_BOUND_IF 作为等效替代。
func BindControl(network, address string, c syscall.RawConn) error {
	ifce, _ := globalBindInterface.Load().(string)
	if ifce == "" {
		return nil
	}

	// 获取接口索引
	iface, err := net.InterfaceByName(ifce)
	if err != nil {
		return err
	}

	var sockErr error
	err = c.Control(func(fd uintptr) {
		// IPv4: IP_BOUND_IF — 将 socket 绑定到指定接口
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, iface.Index)
		if sockErr != nil {
			return
		}
		// IPv6: IPV6_BOUND_IF — 同时设置 IPv6（双栈 socket 需要）
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, iface.Index)
	})
	if err != nil {
		return err
	}
	return sockErr
}
