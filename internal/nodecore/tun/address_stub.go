//go:build !linux && !darwin && !windows

package tun

import "fmt"

// ConfigureAddress 在非 Linux 平台返回不支持错误。
func ConfigureAddress(tunName string, addrCIDR string) error {
	return fmt.Errorf("tun: VIP 地址配置仅支持 Linux/macOS/Windows（当前平台不支持）")
}
