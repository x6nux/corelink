//go:build !linux

package firewall

// New 创建适合当前平台的防火墙管理器（非 Linux 返回 NoopManager）。
func New() FirewallManager {
	return &NoopManager{}
}
