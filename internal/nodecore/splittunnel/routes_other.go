//go:build !linux && !darwin && !windows

package splittunnel

import "log/slog"

// 其他平台（FreeBSD/OpenBSD 等）的策略路由空实现。

const (
	PolicyTableID  = 0
	PolicyRulePrio = 0
	FwMarkBypass   = 0
	FwMarkRulePrio = 0
	SubnetRulePrio = 0
)

// InstallPolicyRoutes 在不支持的平台上无操作。
func InstallPolicyRoutes(tunName, physIfce string) error {
	slog.Warn("splittunnel: 当前平台不支持策略路由")
	return nil
}

// RemovePolicyRoutes 在不支持的平台上无操作。
func RemovePolicyRoutes() {}

// InstallMasquerade 在不支持的平台上无操作。
func InstallMasquerade(physIfce string) error {
	slog.Warn("splittunnel: 当前平台不支持 MASQUERADE")
	return nil
}

// RemoveMasquerade 在不支持的平台上无操作。
func RemoveMasquerade(physIfce string) {}
