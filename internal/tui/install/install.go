// Package install 提供 controller / node 共用的 systemd 安装逻辑。
//
// Run 函数按配置执行：root 检查 → 二进制复制 → 数据目录 → 配置文件（存在跳过 /
// 不存在启动向导）→ systemd unit 写入 → daemon-reload + enable + start → 等待启动。
//
// 所有副作用（文件 I/O、systemctl 调用、UID 查询）通过 InstallConfig 中的函数字段
// 注入，方便单测替换。
package install

import (
	"fmt"
	"io"
	"os"
	"time"
)

// InstallConfig 描述一次 install 动作的全部参数与可替换副作用。
type InstallConfig struct {
	// BinaryName 是可执行文件名（如 "corelink-controller"）。
	BinaryName string
	// BinaryDest 是目标安装路径（如 "/usr/local/bin/corelink-controller"）。
	BinaryDest string
	// DataDir 是数据目录（如 "/var/lib/corelink-controller"）。
	DataDir string
	// ConfigPath 是配置文件路径（如 "/etc/corelink-controller.json"）。
	ConfigPath string
	// ServiceName 是 systemd 服务名（如 "corelink-controller"）。
	ServiceName string
	// UnitContent 是 systemd unit 文件完整内容。
	UnitContent string

	// ConfigFn 是配置向导函数，配置文件不存在时调用。
	// 调用形式：ConfigFn([]string{"--output", configPath})。
	ConfigFn func(args []string) error

	// PostConfigFn 在配置文件就绪后调用（无论已存在还是刚生成），
	// 用于补全缺失的密钥/密码等安全字段。controller 注入，node 不设。
	PostConfigFn func(configPath string) error

	// ── 可替换副作用（生产默认值由 DefaultXxx 提供，测试可覆盖）──

	// ExecCmd 执行外部命令（systemctl 等）。
	ExecCmd func(name string, args ...string) error
	// GetUID 返回当前用户 UID（用于 root 检查）。
	GetUID func() int
	// Executable 返回当前可执行文件路径。
	Executable func() (string, error)
	// MkdirAll 创建目录。
	MkdirAll func(path string, perm os.FileMode) error
	// WriteFile 写文件。
	WriteFile func(name string, data []byte, perm os.FileMode) error
	// CopyFile 复制文件并设权限。
	CopyFile func(src, dst string, perm os.FileMode) error
	// Stat 检查文件是否存在。
	Stat func(name string) (os.FileInfo, error)
	// RemoveFile 删除文件（uninstall 用）。
	RemoveFile func(name string) error
	// Sleep 休眠（轮询等待用）。
	Sleep func(d time.Duration)
	// Printf 格式化输出。
	Printf func(format string, a ...any)
}

// fillDefaults 为 nil 的函数字段填充生产默认值。
func (c *InstallConfig) fillDefaults() {
	if c.ExecCmd == nil {
		c.ExecCmd = defaultExecCmd
	}
	if c.GetUID == nil {
		c.GetUID = os.Getuid
	}
	if c.Executable == nil {
		c.Executable = os.Executable
	}
	if c.MkdirAll == nil {
		c.MkdirAll = os.MkdirAll
	}
	if c.WriteFile == nil {
		c.WriteFile = os.WriteFile
	}
	if c.CopyFile == nil {
		c.CopyFile = defaultCopyFile
	}
	if c.Stat == nil {
		c.Stat = os.Stat
	}
	if c.RemoveFile == nil {
		c.RemoveFile = os.Remove
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
	if c.Printf == nil {
		c.Printf = func(format string, a ...any) { fmt.Printf(format, a...) }
	}
}

// Run 执行完整安装流程。
func Run(cfg InstallConfig) error {
	cfg.fillDefaults()

	// 1. 检查 root
	if cfg.GetUID() != 0 {
		return fmt.Errorf("请使用 sudo 运行 %s install", cfg.BinaryName)
	}

	// 2. 复制二进制到目标路径
	exePath, err := cfg.Executable()
	if err != nil {
		return fmt.Errorf("获取当前可执行文件路径失败: %w", err)
	}
	if exePath == cfg.BinaryDest {
		cfg.Printf("[跳过] 二进制已在目标路径 %s\n", cfg.BinaryDest)
	} else {
		cfg.Printf("复制 %s → %s\n", exePath, cfg.BinaryDest)
		if err := cfg.CopyFile(exePath, cfg.BinaryDest, 0755); err != nil {
			return fmt.Errorf("复制二进制失败: %w", err)
		}
	}

	// 3. 创建数据目录
	if err := cfg.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("创建数据目录 %s 失败: %w", cfg.DataDir, err)
	}
	cfg.Printf("数据目录就绪: %s\n", cfg.DataDir)

	// 4. 配置文件
	if _, err := cfg.Stat(cfg.ConfigPath); err == nil {
		cfg.Printf("使用现有配置文件: %s\n", cfg.ConfigPath)
	} else {
		cfg.Printf("未找到配置文件，启动配置向导...\n")
		if cfg.ConfigFn == nil {
			return fmt.Errorf("配置文件 %s 不存在且未提供配置向导", cfg.ConfigPath)
		}
		if err := cfg.ConfigFn([]string{"--output", cfg.ConfigPath}); err != nil {
			return fmt.Errorf("配置向导失败: %w", err)
		}
	}

	// 4b. 配置文件后处理（补全密钥/密码等安全字段）
	if cfg.PostConfigFn != nil {
		if err := cfg.PostConfigFn(cfg.ConfigPath); err != nil {
			return fmt.Errorf("配置后处理失败: %w", err)
		}
	}

	// 5. 生成 systemd unit
	unitPath := fmt.Sprintf("/etc/systemd/system/%s.service", cfg.ServiceName)
	if err := cfg.WriteFile(unitPath, []byte(cfg.UnitContent), 0644); err != nil {
		return fmt.Errorf("写入 systemd unit %s 失败: %w", unitPath, err)
	}
	cfg.Printf("systemd unit 已写入: %s\n", unitPath)

	// 6. 启用并启动
	if err := cfg.ExecCmd("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
	}
	if err := cfg.ExecCmd("systemctl", "enable", cfg.ServiceName); err != nil {
		return fmt.Errorf("systemctl enable %s 失败: %w", cfg.ServiceName, err)
	}
	if err := cfg.ExecCmd("systemctl", "start", cfg.ServiceName); err != nil {
		return fmt.Errorf("systemctl start %s 失败: %w", cfg.ServiceName, err)
	}

	// 7. 等待启动（轮询 is-active，最多 10 次，每次 1 秒）
	cfg.Printf("等待 %s 启动...\n", cfg.ServiceName)
	var started bool
	for i := 0; i < 10; i++ {
		if err := cfg.ExecCmd("systemctl", "is-active", cfg.ServiceName); err == nil {
			started = true
			break
		}
		cfg.Sleep(1 * time.Second)
	}

	// 8. 打印状态
	if started {
		cfg.Printf("%s 已成功启动并设为开机自启。\n", cfg.ServiceName)
		cfg.Printf("配置文件: %s\n", cfg.ConfigPath)
		cfg.Printf("数据目录: %s\n", cfg.DataDir)
		cfg.Printf("管理服务: systemctl status %s\n", cfg.ServiceName)
	} else {
		return fmt.Errorf("%s 启动超时，请检查日志: journalctl -u %s", cfg.ServiceName, cfg.ServiceName)
	}

	return nil
}

// defaultCopyFile 是生产环境的文件复制实现。
func defaultCopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(perm)
}

// Uninstall 停止服务 + 删除 systemd unit + 删除二进制（保留数据目录和配置文件）。
func Uninstall(cfg InstallConfig) error {
	cfg.fillDefaults()

	if cfg.GetUID() != 0 {
		return fmt.Errorf("请使用 sudo 运行 %s uninstall", cfg.BinaryName)
	}

	// 停止 + 禁用服务（忽略错误——可能没在运行）
	_ = cfg.ExecCmd("systemctl", "stop", cfg.ServiceName)
	_ = cfg.ExecCmd("systemctl", "disable", cfg.ServiceName)

	// 删除 systemd unit
	unitPath := fmt.Sprintf("/etc/systemd/system/%s.service", cfg.ServiceName)
	if err := cfg.RemoveFile(unitPath); err != nil && !os.IsNotExist(err) {
		cfg.Printf("删除 unit 文件失败（继续）: %v\n", err)
	} else {
		cfg.Printf("已删除: %s\n", unitPath)
	}
	_ = cfg.ExecCmd("systemctl", "daemon-reload")

	// 删除二进制
	if err := cfg.RemoveFile(cfg.BinaryDest); err != nil && !os.IsNotExist(err) {
		cfg.Printf("删除二进制失败（继续）: %v\n", err)
	} else {
		cfg.Printf("已删除: %s\n", cfg.BinaryDest)
	}

	// 删除 socket 文件（best-effort）
	sockPath := fmt.Sprintf("/var/run/%s.sock", cfg.ServiceName)
	_ = cfg.RemoveFile(sockPath)

	cfg.Printf("\n%s 已卸载。\n", cfg.BinaryName)
	cfg.Printf("数据目录保留: %s（手动删除: rm -rf %s）\n", cfg.DataDir, cfg.DataDir)
	cfg.Printf("配置文件保留: %s（手动删除: rm %s）\n", cfg.ConfigPath, cfg.ConfigPath)
	return nil
}

// Update 从远程下载最新版本替换二进制并重启服务（GitHub 开源后实现）。
func Update(cfg InstallConfig) error {
	cfg.fillDefaults()

	if cfg.GetUID() != 0 {
		return fmt.Errorf("请使用 sudo 运行 %s update", cfg.BinaryName)
	}

	// TODO: 从 GitHub Releases 下载最新版本，校验 checksum，替换二进制，重启服务。
	cfg.Printf("自动更新功能即将上线（GitHub 开源后启用）。\n")
	cfg.Printf("当前可手动更新：\n")
	cfg.Printf("  1. 下载新版二进制\n")
	cfg.Printf("  2. sudo cp <新二进制> %s\n", cfg.BinaryDest)
	cfg.Printf("  3. sudo systemctl restart %s\n", cfg.ServiceName)
	return nil
}

// Reinstall 完全卸载后重新安装（保留数据目录和配置文件）。
func Reinstall(cfg InstallConfig) error {
	cfg.fillDefaults()

	cfg.Printf("=== 卸载旧版本 ===\n")
	// 卸载时不删数据和配置
	if err := Uninstall(cfg); err != nil {
		return fmt.Errorf("卸载失败: %w", err)
	}

	cfg.Printf("\n=== 重新安装 ===\n")
	return Run(cfg)
}

// ServiceCmd 执行 systemctl 子命令（start/stop/restart）。
func ServiceCmd(service, action string) error {
	cmd := defaultExecCmd
	return cmd("systemctl", action, service)
}

// ServiceLog 执行 journalctl 查看日志（-u service -f --no-pager）。
func ServiceLog(service string) error {
	cmd := defaultExecCmd
	return cmd("journalctl", "-u", service, "-f", "--no-pager")
}

// ServiceEnable 设为开机自启。
func ServiceEnable(service string) error { return defaultExecCmd("systemctl", "enable", service) }

// ServiceDisable 取消开机自启。
func ServiceDisable(service string) error { return defaultExecCmd("systemctl", "disable", service) }

// UnitContent 根据参数生成 systemd unit 文件内容。
func UnitContent(binaryDest, configPath, dataDir, serviceName string, extraLines ...string) string {
	unit := fmt.Sprintf(`[Unit]
Description=CoreLink %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve -config %s
Restart=always
RestartSec=5
WorkingDirectory=%s
LimitNOFILE=65535
`, serviceName, binaryDest, configPath, dataDir)

	for _, line := range extraLines {
		unit += line + "\n"
	}

	unit += `
[Install]
WantedBy=multi-user.target
`
	return unit
}
