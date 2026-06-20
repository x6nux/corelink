//go:build darwin

package splittunnel

import (
	"net"
	"strings"
	"syscall"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// DetectPhysicalInterface 通过 BSD route socket 检测默认路由的出口网卡名。
// 排除 corelink TUN 接口和 lo 回环。
func DetectPhysicalInterface() string {
	rib, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return fallbackInterface()
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return fallbackInterface()
	}

	for _, msg := range msgs {
		rm, ok := msg.(*route.RouteMessage)
		if !ok {
			continue
		}
		if rm.Flags&unix.RTF_UP == 0 || rm.Flags&unix.RTF_GATEWAY == 0 {
			continue
		}
		if len(rm.Addrs) <= syscall.RTAX_NETMASK {
			continue
		}
		// 检查是否为 0.0.0.0/0 默认路由
		dstAddr, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok || dstAddr.IP != [4]byte{0, 0, 0, 0} {
			continue
		}
		maskAddr, ok := rm.Addrs[syscall.RTAX_NETMASK].(*route.Inet4Addr)
		if !ok {
			continue
		}
		ones, _ := net.IPMask(maskAddr.IP[:]).Size()
		if ones != 0 {
			continue
		}
		iface, err := net.InterfaceByIndex(rm.Index)
		if err != nil {
			continue
		}
		name := iface.Name
		if strings.HasPrefix(name, "corelink") || strings.HasPrefix(name, "lo") {
			continue
		}
		return name
	}
	return fallbackInterface()
}

// fallbackInterface 在 route socket 失败时回退到遍历接口列表。
func fallbackInterface() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 &&
			!strings.HasPrefix(iface.Name, "lo") &&
			!strings.HasPrefix(iface.Name, "corelink") {
			return iface.Name
		}
	}
	return ""
}

// DetectGateway 通过 BSD route socket 检测默认网关 IP。
// 排除 corelink TUN 接口和 lo 回环（与 DetectPhysicalInterface 一致）。
func DetectGateway() string {
	rib, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return ""
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return ""
	}

	for _, msg := range msgs {
		rm, ok := msg.(*route.RouteMessage)
		if !ok {
			continue
		}
		if rm.Flags&unix.RTF_UP == 0 || rm.Flags&unix.RTF_GATEWAY == 0 {
			continue
		}
		if len(rm.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		dstAddr, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok || dstAddr.IP != [4]byte{0, 0, 0, 0} {
			continue
		}
		// 排除 corelink TUN 接口和 lo 回环
		if iface, err := net.InterfaceByIndex(rm.Index); err == nil {
			if strings.HasPrefix(iface.Name, "corelink") || strings.HasPrefix(iface.Name, "lo") {
				continue
			}
		}
		gwAddr, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
		if !ok {
			continue
		}
		ip := net.IP(gwAddr.IP[:])
		return ip.String()
	}
	return ""
}

// DetectLocalSubnet 检测物理网卡的 IPv4 子网（如 "192.168.1.0/24"），
// 用于策略路由排除本地流量。
func DetectLocalSubnet(physIfce string) string {
	if physIfce == "" {
		return ""
	}
	iface, err := net.InterfaceByName(physIfce)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil {
			continue
		}
		// 取网络前缀
		network := ipNet.IP.Mask(ipNet.Mask)
		ones, bits := ipNet.Mask.Size()
		if bits == 0 {
			continue
		}
		result := &net.IPNet{IP: network, Mask: net.CIDRMask(ones, bits)}
		return result.String()
	}
	return ""
}
