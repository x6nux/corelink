//go:build !linux && !darwin && !windows

package splittunnel

import "net/netip"

// detectGatewayIP 其他平台返回零值。
func detectGatewayIP() netip.Addr { return netip.Addr{} }

// readDNSServers 其他平台返回空列表。
func readDNSServers() []string { return nil }
