//go:build linux

package splittunnel

import (
	"net/netip"
	"os"
	"strings"
)

// detectGatewayIP 在 Linux 上通过 DetectGateway()（解析 /proc/net/route）获取默认网关 IP。
// 复用已有的 DetectGateway() 函数，避免重复的 hex 解析逻辑。
func detectGatewayIP() netip.Addr {
	gw := DetectGateway()
	if gw == "" {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(gw)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// readDNSServers 从 /etc/resolv.conf 读取 nameserver 列表。
func readDNSServers() []string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				out = append(out, fields[1])
			}
		}
	}
	return out
}
