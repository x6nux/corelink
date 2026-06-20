//go:build !linux

package main

import "log/slog"

// cleanupBeforeStart 在非 Linux 平台上是空操作。
func cleanupBeforeStart(_ string) {}

// initSysctl 在非 Linux 平台上是空操作。
func initSysctl(tunName string) {
	slog.Debug("当前平台无需 sysctl 配置")
}

// configureLinkUpAndRoute 在非 Linux 平台上是空操作。
func configureLinkUpAndRoute(tunName, vipCIDR string) {
	slog.Debug("当前平台 TUN 链路/路由由设备创建时自动配置")
}
