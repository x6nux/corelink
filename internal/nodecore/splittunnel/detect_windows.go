//go:build windows

package splittunnel

import (
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

// DetectPhysicalInterface 通过 route print 解析默认路由的出口网卡名。
// 排除 corelink TUN 接口和 lo 回环。
func DetectPhysicalInterface() string {
	out, err := exec.Command("route", "print", "0.0.0.0").Output()
	if err != nil {
		return fallbackInterfaceWin()
	}
	// 解析 route print 输出找默认路由行。
	// 典型格式: "0.0.0.0          0.0.0.0     192.168.1.1   192.168.1.100      25"
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			ifAddr, err := netip.ParseAddr(fields[3])
			if err != nil {
				continue
			}
			name := findInterfaceByAddrWin(ifAddr)
			if name != "" && !strings.HasPrefix(name, "corelink") {
				return name
			}
		}
	}
	return fallbackInterfaceWin()
}

// findInterfaceByAddrWin 通过 IP 地址查找对应的网络接口名。
func findInterfaceByAddrWin(addr netip.Addr) string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if ipNet.IP.Equal(addr.AsSlice()) {
					return iface.Name
				}
			}
		}
	}
	return ""
}

// fallbackInterfaceWin 在 route print 失败时回退到遍历接口列表。
func fallbackInterfaceWin() string {
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

// DetectGateway 通过 route print 解析默认网关 IP。
// 排除 corelink TUN 接口（与 DetectPhysicalInterface 一致）。
func DetectGateway() string {
	out, err := exec.Command("route", "print", "0.0.0.0").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			// fields[3] 是接口地址，用于查找接口名以排除 corelink TUN
			if ifAddr, err := netip.ParseAddr(fields[3]); err == nil {
				ifName := findInterfaceByAddrWin(ifAddr)
				if strings.HasPrefix(ifName, "corelink") {
					continue
				}
			}
			if _, err := netip.ParseAddr(fields[2]); err == nil {
				return fields[2]
			}
		}
	}
	return ""
}

// DetectLocalSubnet 检测物理网卡的 IPv4 子网（如 "192.168.1.0/24"）。
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
