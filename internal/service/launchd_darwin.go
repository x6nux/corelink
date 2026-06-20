//go:build darwin

package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LaunchdManager 使用 launchd 管理服务。
type LaunchdManager struct {
	name  string
	label string
}

// New 返回当前平台对应的服务管理器（macOS: launchd）。
// name 必须为合法服务名（仅允许 [a-zA-Z0-9_-]），否则 panic（编程错误）。
func New(name string) Manager {
	if err := ValidateServiceName(name); err != nil {
		panic(err)
	}
	return &LaunchdManager{name: name, label: "com." + name}
}

// xmlEscape 对字符串进行 XML 转义，防止 plist 注入
// （处理 <, >, &, ", ' 等特殊字符）。
func xmlEscape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		// xml.EscapeText 写入 bytes.Buffer 不会返回错误
		return s
	}
	return buf.String()
}

func (m *LaunchdManager) Install(cfg ServiceConfig) error {
	// 校验服务名
	if err := ValidateServiceName(m.name); err != nil {
		return err
	}
	// 校验路径字段，防止换行/空字节
	if err := validateServiceConfig(cfg); err != nil {
		return err
	}

	// 构建 ProgramArguments 列表：binary + serve + -config + configPath + 额外 Args。
	// 所有用户输入字段经过 XML 转义后再嵌入 plist，防止 <, >, & 等字符破坏 XML 结构。
	var argItems strings.Builder
	for _, arg := range []string{cfg.BinaryPath, "serve", "-config", cfg.ConfigPath} {
		fmt.Fprintf(&argItems, "        <string>%s</string>\n", xmlEscape(arg))
	}
	for _, arg := range cfg.Args {
		fmt.Fprintf(&argItems, "        <string>%s</string>\n", xmlEscape(arg))
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>WorkingDirectory</key><string>%s</string>
    <key>StandardOutPath</key><string>/var/log/%s.log</string>
    <key>StandardErrorPath</key><string>/var/log/%s.err</string>
</dict>
</plist>`,
		xmlEscape(m.label),
		argItems.String(),
		xmlEscape(cfg.DataDir),
		xmlEscape(m.name),
		xmlEscape(m.name))

	// 原子写入：先写临时文件，再 os.Rename 到目标路径，
	// 避免写入过程中崩溃导致 plist 文件损坏。
	plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.label)
	tmpPath := plistPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("service: 创建临时 plist 文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, plistPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("service: 重命名 plist 文件失败: %w", err)
	}
	return nil
}

func (m *LaunchdManager) Uninstall() error {
	_ = m.Stop()
	plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.label)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("service: 删除 plist 文件失败: %w", err)
	}
	return nil
}

func (m *LaunchdManager) Start() error {
	if err := exec.Command("launchctl", "load", fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.label)).Run(); err != nil {
		return fmt.Errorf("service: launchctl load 失败: %w", err)
	}
	return nil
}

func (m *LaunchdManager) Stop() error {
	if err := exec.Command("launchctl", "unload", fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.label)).Run(); err != nil {
		return fmt.Errorf("service: launchctl unload 失败: %w", err)
	}
	return nil
}

func (m *LaunchdManager) Restart() error {
	_ = m.Stop()
	return m.Start()
}

// Enable 对 launchd 为空操作，通过 plist 中 RunAtLoad=true 实现自启。
func (m *LaunchdManager) Enable() error { return nil }

// Disable 对 launchd 为空操作，通过卸载 plist 禁用。
func (m *LaunchdManager) Disable() error { return nil }

func (m *LaunchdManager) Status() (ServiceStatus, error) {
	// 使用 launchctl list <label> 精确查询单个服务，避免子串误匹配
	out, err := exec.Command("launchctl", "list", m.label).Output()
	if err == nil && len(out) > 0 {
		// 命令成功 = 服务已加载（运行中或正在退出）
		return StatusRunning, nil
	}
	// 命令失败 = 服务未加载，检查 plist 是否存在
	plistPath := fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.label)
	if _, err := os.Stat(plistPath); err == nil {
		return StatusStopped, nil
	}
	return StatusNotInstalled, nil
}

func (m *LaunchdManager) Logs(lines int) (string, error) {
	logPath := fmt.Sprintf("/var/log/%s.log", m.name)
	out, err := exec.Command("tail", "-n", fmt.Sprintf("%d", lines), logPath).Output()
	if err != nil {
		return "", fmt.Errorf("service: 读取日志失败: %w", err)
	}
	return string(out), nil
}
