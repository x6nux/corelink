//go:build linux

package firewall

// New 创建适合当前平台的防火墙管理器。
func New() FirewallManager {
	return NewManager(&ExecRunner{})
}
