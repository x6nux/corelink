//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SystemdManager 使用 systemctl 管理 systemd 服务。
type SystemdManager struct {
	name string
}

// New 创建指定名称的 systemd 服务管理器。
// name 必须为合法服务名（仅允许 [a-zA-Z0-9_-]），否则 panic（编程错误）。
func New(name string) Manager {
	if err := ValidateServiceName(name); err != nil {
		panic(err)
	}
	return &SystemdManager{name: name}
}

func (m *SystemdManager) unitPath() string {
	return fmt.Sprintf("/etc/systemd/system/%s.service", m.name)
}

func (m *SystemdManager) Install(cfg ServiceConfig) error {
	// 校验服务名，防止路径穿越
	if err := ValidateServiceName(m.name); err != nil {
		return err
	}
	// 校验路径字段，防止换行符/空字节注入 systemd 指令
	if err := validateServiceConfig(cfg); err != nil {
		return err
	}

	// 使用 strings.Builder 拼接 unit 内容，避免 text/template 注入风险。
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + cfg.Description + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	// systemd ExecStart 使用双引号包裹可能含空格的路径。
	// 路径字段已由 validateServiceConfig 校验过无换行/空字节。
	// 转义 %：systemd 将 % 解释为说明符（如 %h=home），路径中的 % 必须写成 %%。
	escapedBin := strings.ReplaceAll(cfg.BinaryPath, "%", "%%")
	escapedCfg := strings.ReplaceAll(cfg.ConfigPath, "%", "%%")
	execLine := fmt.Sprintf("ExecStart=\"%s\" serve -config \"%s\"", escapedBin, escapedCfg)
	for _, arg := range cfg.Args {
		execLine += fmt.Sprintf(" \"%s\"", strings.ReplaceAll(arg, "%", "%%"))
	}
	b.WriteString(execLine + "\n")
	if cfg.DataDir != "" {
		b.WriteString("WorkingDirectory=" + strings.ReplaceAll(cfg.DataDir, "%", "%%") + "\n")
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("LimitNOFILE=65535\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")

	// 原子写入：先写临时文件，再 os.Rename 到目标路径，
	// 避免写入过程中崩溃导致 unit 文件损坏。
	target := m.unitPath()
	tmpPath := target + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("service: 创建临时 unit 文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("service: 重命名 unit 文件失败: %w", err)
	}
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("service: daemon-reload 失败: %w", err)
	}
	return nil
}

func (m *SystemdManager) Uninstall() error {
	_ = m.Stop()
	_ = m.Disable()
	if err := os.Remove(m.unitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("service: 删除 unit 文件失败: %w", err)
	}
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("service: daemon-reload 失败: %w", err)
	}
	return nil
}

func (m *SystemdManager) Start() error {
	if err := exec.Command("systemctl", "start", m.name).Run(); err != nil {
		return fmt.Errorf("service: systemctl start %s 失败: %w", m.name, err)
	}
	return nil
}

func (m *SystemdManager) Stop() error {
	if err := exec.Command("systemctl", "stop", m.name).Run(); err != nil {
		return fmt.Errorf("service: systemctl stop %s 失败: %w", m.name, err)
	}
	return nil
}

func (m *SystemdManager) Restart() error {
	if err := exec.Command("systemctl", "restart", m.name).Run(); err != nil {
		return fmt.Errorf("service: systemctl restart %s 失败: %w", m.name, err)
	}
	return nil
}

func (m *SystemdManager) Enable() error {
	if err := exec.Command("systemctl", "enable", m.name).Run(); err != nil {
		return fmt.Errorf("service: systemctl enable %s 失败: %w", m.name, err)
	}
	return nil
}

func (m *SystemdManager) Disable() error {
	if err := exec.Command("systemctl", "disable", m.name).Run(); err != nil {
		return fmt.Errorf("service: systemctl disable %s 失败: %w", m.name, err)
	}
	return nil
}

func (m *SystemdManager) Status() (ServiceStatus, error) {
	out, err := exec.Command("systemctl", "is-active", m.name).Output()
	status := strings.TrimSpace(string(out))
	if err != nil {
		if status == "inactive" {
			return StatusStopped, nil
		}
		// 服务可能不存在
		if _, statErr := os.Stat(m.unitPath()); os.IsNotExist(statErr) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, nil
	}
	if status == "active" {
		return StatusRunning, nil
	}
	return StatusStopped, nil
}

func (m *SystemdManager) Logs(lines int) (string, error) {
	out, err := exec.Command("journalctl", "-u", m.name, "-n", fmt.Sprint(lines), "--no-pager").Output()
	if err != nil {
		return "", fmt.Errorf("service: 查询日志失败: %w", err)
	}
	return string(out), nil
}
