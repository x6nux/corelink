//go:build windows

package tunnel

import (
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
)

var globalBindInterface atomic.Value // stores string
var globalFwMark atomic.Int32        // Windows 无 SO_MARK，保留 API 兼容

// SetBindInterface 设置全局绑定网卡名。
func SetBindInterface(ifce string) { globalBindInterface.Store(ifce) }

// ClearBindInterface 清除绑定。
func ClearBindInterface() { globalBindInterface.Store("") }

// SetFwMark Windows 不支持 SO_MARK，空实现保持 API 兼容。
func SetFwMark(_ int) {}

// ClearFwMark Windows 不支持 SO_MARK，空实现保持 API 兼容。
func ClearFwMark() {}

// BindControl 是 net.Dialer/net.ListenConfig 的 Control 回调，
// 在 Windows 上通过绑定到接口的 IP 地址实现接口绑定。
// Windows 不支持 SO_BINDTODEVICE 或 IP_BOUND_IF，使用 bind() 到具体 IP 作为替代。
func BindControl(network, address string, c syscall.RawConn) error {
	ifce, _ := globalBindInterface.Load().(string)
	if ifce == "" {
		return nil
	}

	// 获取接口信息
	iface, err := net.InterfaceByName(ifce)
	if err != nil {
		return err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("tunnel: 接口 %s 无任何地址", ifce)
	}

	// 查找第一个 IPv4 地址并绑定
	// 注：当前仅支持 IPv4 绑定，IPv6 暂未实现（Windows IPv6 绑定需使用 SockaddrInet6）
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil {
			continue
		}
		var sockErr error
		err = c.Control(func(fd uintptr) {
			sa := &syscall.SockaddrInet4{}
			copy(sa.Addr[:], ipNet.IP.To4())
			sockErr = syscall.Bind(syscall.Handle(fd), sa)
		})
		if err != nil {
			return err
		}
		// 绑定成功后立即返回，避免继续遍历导致重复绑定
		return sockErr
	}
	return fmt.Errorf("tunnel: 接口 %s 无可用 IPv4 地址", ifce)
}
