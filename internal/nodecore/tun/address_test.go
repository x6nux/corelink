//go:build !linux && !darwin && !windows

package tun

import (
	"testing"
)

// TestConfigureAddress_非Linux返回错误 验证在非 Linux 平台上 ConfigureAddress 返回不支持错误。
func TestConfigureAddress_非Linux返回错误(t *testing.T) {
	err := ConfigureAddress("utun99", "100.64.0.1/32")
	if err == nil {
		t.Fatal("非 Linux 平台上 ConfigureAddress 应返回错误")
	}
	// 错误信息应包含平台不支持的提示
	if got := err.Error(); got == "" {
		t.Fatal("错误信息不应为空")
	}
}

// TestConfigureAddress_不同参数均返回错误 表驱动验证各种输入在非 Linux 均返回错误。
func TestConfigureAddress_不同参数均返回错误(t *testing.T) {
	tests := []struct {
		name     string
		tunName  string
		addrCIDR string
	}{
		{"正常参数", "utun0", "100.64.0.1/32"},
		{"IPv6地址", "utun1", "fd00::1/128"},
		{"空TUN名", "", "10.0.0.1/24"},
		{"空地址", "utun2", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ConfigureAddress(tt.tunName, tt.addrCIDR)
			if err == nil {
				t.Errorf("ConfigureAddress(%q, %q) 应返回错误", tt.tunName, tt.addrCIDR)
			}
		})
	}
}
