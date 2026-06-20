//go:build windows

package splittunnel

import (
	"net/netip"
	"os/exec"
	"strings"
)

// detectGatewayIP 在 Windows 上通过 route print 解析默认网关 IP。
func detectGatewayIP() netip.Addr {
	out, err := exec.Command("route", "print", "0.0.0.0").Output()
	if err != nil {
		return netip.Addr{}
	}
	// 解析 route print 输出，查找默认路由行。
	// 典型格式: "0.0.0.0          0.0.0.0     192.168.1.1   192.168.1.100      25"
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			if addr, err := netip.ParseAddr(fields[2]); err == nil {
				return addr
			}
		}
	}
	return netip.Addr{}
}

// readDNSServers 在 Windows 上通过 netsh 查询系统 DNS 服务器。
func readDNSServers() []string {
	out, err := exec.Command("netsh", "interface", "ip", "show", "dns").Output()
	if err != nil {
		return nil
	}
	var servers []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// 尝试直接解析为 IP 地址
		if _, err := netip.ParseAddr(line); err == nil {
			servers = append(servers, line)
			continue
		}
		// 匹配 "DNS Servers:" 或 "DNS 服务器:" 格式
		for _, sep := range []string{"Servers:", "服务器:"} {
			if idx := strings.Index(line, sep); idx >= 0 {
				rest := strings.TrimSpace(line[idx+len(sep):])
				if _, err := netip.ParseAddr(rest); err == nil {
					servers = append(servers, rest)
				}
			}
		}
	}
	return servers
}
