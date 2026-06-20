//go:build windows

// Package windnsapi 提供 Windows DNS 客户端缓存刷新 API 绑定。
package windnsapi

import "golang.org/x/sys/windows"

var (
	moddnsapi                 = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = moddnsapi.NewProc("DnsFlushResolverCache")
)

// FlushResolverCache 调用 DnsFlushResolverCache 清除 Windows DNS 客户端缓存。
func FlushResolverCache() error {
	r0, _, _ := procDnsFlushResolverCache.Call()
	if r0 == 0 {
		return windows.ERROR_GEN_FAILURE
	}
	return nil
}
