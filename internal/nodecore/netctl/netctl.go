package netctl

import "net/netip"

// RouteManager 管理系统路由表。
type RouteManager interface {
	AddRoute(dst netip.Prefix, gateway netip.Addr, ifName string) error
	RemoveRoute(dst netip.Prefix, gateway netip.Addr) error
	SetAutoRoute(tunName string, tunAddrs []netip.Prefix, gw4, gw6 netip.Addr) error
	UnsetAutoRoute() error
}

// InterfaceDetector 检测系统网络接口和网关。
type InterfaceDetector interface {
	DefaultInterface() (name string, gateway netip.Addr, err error)
	LocalSubnet() (netip.Prefix, error)
	SystemDNSServers() ([]netip.Addr, error)
}

// DNSManager 管理系统 DNS 配置。
type DNSManager interface {
	FlushCache() error
	SetTUNDNS(ifName string, servers []netip.Addr) error
	RestoreDNS() error
}
