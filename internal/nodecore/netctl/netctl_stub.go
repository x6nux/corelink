//go:build !linux && !darwin && !windows

package netctl

import (
	"fmt"
	"net/netip"
)

var errUnsupported = fmt.Errorf("netctl: 当前平台不支持")

// ─────────────────────── 路由管理（占位） ───────────────────────

type stubRouteManager struct{}

func NewRouteManager() RouteManager { return &stubRouteManager{} }

func (m *stubRouteManager) AddRoute(netip.Prefix, netip.Addr, string) error { return errUnsupported }
func (m *stubRouteManager) RemoveRoute(netip.Prefix, netip.Addr) error      { return errUnsupported }
func (m *stubRouteManager) SetAutoRoute(string, []netip.Prefix, netip.Addr, netip.Addr) error {
	return errUnsupported
}
func (m *stubRouteManager) UnsetAutoRoute() error { return errUnsupported }

// ─────────────────────── 接口检测（占位） ───────────────────────

type stubInterfaceDetector struct{}

func NewInterfaceDetector() InterfaceDetector { return &stubInterfaceDetector{} }

func (d *stubInterfaceDetector) DefaultInterface() (string, netip.Addr, error) {
	return "", netip.Addr{}, errUnsupported
}
func (d *stubInterfaceDetector) LocalSubnet() (netip.Prefix, error) {
	return netip.Prefix{}, errUnsupported
}
func (d *stubInterfaceDetector) SystemDNSServers() ([]netip.Addr, error) {
	return nil, errUnsupported
}

// ─────────────────────── DNS 管理（占位） ───────────────────────

type stubDNSManager struct{}

func NewDNSManager() DNSManager { return &stubDNSManager{} }

func (m *stubDNSManager) FlushCache() error                    { return errUnsupported }
func (m *stubDNSManager) SetTUNDNS(string, []netip.Addr) error { return errUnsupported }
func (m *stubDNSManager) RestoreDNS() error                    { return errUnsupported }
