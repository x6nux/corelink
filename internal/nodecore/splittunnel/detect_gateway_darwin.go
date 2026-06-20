//go:build darwin

package splittunnel

import (
	"net/netip"
	"os"
	"strings"
)

// detectGatewayIP 在 macOS 上通过 DetectGateway()（BSD route socket）获取默认网关 IP。
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

// readDNSServers 在 macOS 上解析 /etc/resolv.conf 获取 nameserver 列表。
// macOS 的 /etc/resolv.conf 通常由 mDNSResponder 维护，格式与 Linux 兼容。
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
