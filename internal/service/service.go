// Package service 提供跨平台的系统服务管理（安装/启停/状态查询）。
//
//   - Windows: 使用 SCM (Service Control Manager) API
//   - Linux: 使用 systemd (systemctl)
//   - macOS: 使用 launchd (launchctl)
package service

import (
	"fmt"
	"regexp"
	"strings"
)

// ServiceStatus 表示服务的运行状态。
type ServiceStatus int

const (
	StatusUnknown      ServiceStatus = iota
	StatusRunning                    // 服务正在运行
	StatusStopped                    // 服务已停止
	StatusNotInstalled               // 服务未安装
)

func (s ServiceStatus) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusStopped:
		return "stopped"
	case StatusNotInstalled:
		return "not-installed"
	default:
		return "unknown"
	}
}

// ServiceConfig 包含服务安装所需的配置信息。
type ServiceConfig struct {
	BinaryPath  string   // 可执行文件路径
	ConfigPath  string   // 配置文件路径
	DataDir     string   // 工作数据目录
	DisplayName string   // 服务显示名称
	Description string   // 服务描述
	Args        []string // 额外启动参数
}

// Manager 定义系统服务管理接口。
type Manager interface {
	// Install 注册并安装系统服务。
	Install(cfg ServiceConfig) error
	// Uninstall 移除已安装的系统服务。
	Uninstall() error
	// Start 启动服务。
	Start() error
	// Stop 停止服务。
	Stop() error
	// Restart 重启服务（先停后启）。
	Restart() error
	// Enable 启用服务（设为开机自启）。
	Enable() error
	// Disable 禁用服务（取消开机自启）。
	Disable() error
	// Status 查询服务当前状态。
	Status() (ServiceStatus, error)
	// Logs 获取最近 N 行服务日志。
	Logs(lines int) (string, error)
}

// validServiceName 匹配合法的服务名（字母/数字/下划线/连字符）。
var validServiceName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ValidateServiceName 校验服务名是否安全（仅允许字母/数字/下划线/连字符）。
// 防止服务名注入操作系统命令或查询语句。
func ValidateServiceName(name string) error {
	if !validServiceName.MatchString(name) {
		return fmt.Errorf("service: 服务名 %q 不合法，仅允许 [a-zA-Z0-9_-]", name)
	}
	return nil
}

// validatePath 校验路径不含换行符、空字节或模板语法，防止注入。
func validatePath(path, fieldName string) error {
	if strings.ContainsAny(path, "\n\r\x00") {
		return fmt.Errorf("service: %s 含非法字符（换行或空字节）", fieldName)
	}
	if strings.Contains(path, "{{") {
		return fmt.Errorf("service: %s 含非法模板语法 '{{'", fieldName)
	}
	return nil
}

// validateServiceConfig 校验 ServiceConfig 中所有路径字段。
func validateServiceConfig(cfg ServiceConfig) error {
	for _, check := range []struct {
		val  string
		name string
	}{
		{cfg.BinaryPath, "BinaryPath"},
		{cfg.ConfigPath, "ConfigPath"},
		{cfg.DataDir, "DataDir"},
		{cfg.DisplayName, "DisplayName"},
		{cfg.Description, "Description"},
	} {
		if err := validatePath(check.val, check.name); err != nil {
			return err
		}
	}
	for i, arg := range cfg.Args {
		if err := validatePath(arg, fmt.Sprintf("Args[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}
