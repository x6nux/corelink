//go:build linux

package splittunnel

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

// DetectPhysicalInterface 检测默认路由的出口网卡名。
// 排除 corelink TUN 接口和 lo 回环（/1 路由也匹配 00000000 目标）。
func DetectPhysicalInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "00000000" {
				name := fields[0]
				if strings.HasPrefix(name, "corelink") || strings.HasPrefix(name, "lo") {
					continue
				}
				return name
			}
		}
	}
	return fallbackInterface()
}

func fallbackInterface() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 && !strings.HasPrefix(iface.Name, "lo") && !strings.HasPrefix(iface.Name, "corelink") {
			return iface.Name
		}
	}
	return ""
}

// DetectGateway 检测默认网关 IP。
func DetectGateway() string {
	data, _ := os.ReadFile("/proc/net/route")
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "00000000" {
			gw := fields[2]
			if len(gw) == 8 {
				a := hexNib(gw[6])<<4 | hexNib(gw[7])
				b := hexNib(gw[4])<<4 | hexNib(gw[5])
				c := hexNib(gw[2])<<4 | hexNib(gw[3])
				d := hexNib(gw[0])<<4 | hexNib(gw[1])
				return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d)
			}
		}
	}
	return ""
}

// DetectLocalSubnet 检测物理网卡的 IPv4 子网（如 "10.0.0.0/16"），
// 用于策略路由排除本地流量。
func DetectLocalSubnet(physIfce string) string {
	if physIfce == "" {
		return ""
	}
	out, err := exec.Command("ip", "-4", "addr", "show", "dev", physIfce).CombinedOutput()
	if err != nil {
		return ""
	}
	// 解析 "inet 10.0.2.2/16" 行
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "inet ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		cidr := fields[1] // e.g. "10.0.2.2/16"
		if p, err := netip.ParsePrefix(cidr); err == nil {
			// 取网络前缀（如 10.0.2.2/16 → 10.0.0.0/16）
			return p.Masked().String()
		}
	}
	return ""
}

func hexNib(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 10
	}
	if c >= 'A' && c <= 'F' {
		return c - 'A' + 10
	}
	return 0
}
