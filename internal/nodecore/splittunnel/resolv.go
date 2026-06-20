//go:build linux

package splittunnel

import (
	"log/slog"
	"os"
)

const (
	resolvPath   = "/etc/resolv.conf"
	resolvBackup = "/etc/resolv.conf.corelink.bak"
)

// OverrideResolv 备份原 resolv.conf 并覆盖为公网 DNS（走 TUN 被拦截）。
// 使用非本地子网的公网 DNS 地址，确保 DNS 流量进入 TUN 被 wrapper 拦截。
func OverrideResolv() {
	orig, err := os.ReadFile(resolvPath)
	if err != nil {
		slog.Warn("splittunnel: 读取 resolv.conf 失败", "err", err)
		return
	}
	// 备份（仅首次，避免覆盖已有备份）
	if _, err := os.Stat(resolvBackup); os.IsNotExist(err) {
		if err := os.WriteFile(resolvBackup, orig, 0644); err != nil {
			slog.Warn("splittunnel: 备份 resolv.conf 失败", "err", err)
			return
		}
	}
	// 覆盖为公网 DNS（这些地址不在本地子网排除规则内，会走 TUN）
	newConf := "# corelink split-tunnel DNS override\nnameserver 8.8.8.8\nnameserver 1.1.1.1\n"
	if err := os.WriteFile(resolvPath, []byte(newConf), 0644); err != nil {
		slog.Warn("splittunnel: 覆盖 resolv.conf 失败", "err", err)
		return
	}
	slog.Info("splittunnel: DNS 已覆盖为公网 DNS（走 TUN 拦截）")
}

// RestoreResolv 从备份恢复原 resolv.conf。
func RestoreResolv() {
	bak, err := os.ReadFile(resolvBackup)
	if err != nil {
		return // 无备份，不操作
	}
	if err := os.WriteFile(resolvPath, bak, 0644); err != nil {
		slog.Warn("splittunnel: 恢复 resolv.conf 失败", "err", err)
		return
	}
	os.Remove(resolvBackup)
	slog.Info("splittunnel: DNS 配置已恢复")
}
