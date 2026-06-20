//go:build !darwin && !linux && !windows

package splittunnel

import (
	"net"
	"strings"
)

// DetectPhysicalInterface 在非 Linux 平台上 fallback 检测。
func DetectPhysicalInterface() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 && !strings.HasPrefix(iface.Name, "lo") && !strings.HasPrefix(iface.Name, "corelink") {
			return iface.Name
		}
	}
	return ""
}

// DetectGateway 非 Linux 占位。
func DetectGateway() string { return "" }

// DetectLocalSubnet 非 Linux 占位。
func DetectLocalSubnet(_ string) string { return "" }
