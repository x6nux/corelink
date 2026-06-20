//go:build windows

package service

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// WinSvcManager 使用 Windows Service Control Manager 管理服务。
type WinSvcManager struct {
	name string
}

// New 创建指定名称的 Windows 服务管理器。
// name 必须为合法服务名（仅允许 [a-zA-Z0-9_-]），否则 panic（编程错误）。
func New(name string) Manager {
	if err := ValidateServiceName(name); err != nil {
		panic(err)
	}
	return &WinSvcManager{name: name}
}

func (m *WinSvcManager) Install(cfg ServiceConfig) error {
	// 校验服务名，防止 SCM/XPath 注入
	if err := ValidateServiceName(m.name); err != nil {
		return err
	}
	// 校验路径字段
	if err := validateServiceConfig(cfg); err != nil {
		return err
	}

	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	// 检查服务是否已存在
	s, err := scm.OpenService(m.name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service: 服务 %s 已存在", m.name)
	}

	// 构造服务启动参数: serve -config <path> [extra args...]
	args := append([]string{"serve", "-config", cfg.ConfigPath}, cfg.Args...)
	s, err = scm.CreateService(m.name, cfg.BinaryPath, mgr.Config{
		DisplayName: cfg.DisplayName,
		Description: cfg.Description,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("service: 创建服务失败: %w", err)
	}
	defer s.Close()

	// 配置故障恢复策略（崩溃后自动重启，延迟递增）
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400) // 24 小时后重置失败计数器

	return nil
}

func (m *WinSvcManager) Uninstall() error {
	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return fmt.Errorf("service: 打开服务 %s 失败: %w", m.name, err)
	}
	defer s.Close()

	// 先尝试停止服务再删除
	_ = controlService(s, svc.Stop, svc.Stopped)
	return s.Delete()
}

func (m *WinSvcManager) Start() error {
	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return fmt.Errorf("service: 打开服务 %s 失败: %w", m.name, err)
	}
	defer s.Close()
	return s.Start()
}

func (m *WinSvcManager) Stop() error {
	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return fmt.Errorf("service: 打开服务 %s 失败: %w", m.name, err)
	}
	defer s.Close()
	return controlService(s, svc.Stop, svc.Stopped)
}

func (m *WinSvcManager) Restart() error {
	if err := m.Stop(); err != nil {
		return fmt.Errorf("service: 重启时停止服务失败: %w", err)
	}
	time.Sleep(time.Second)
	return m.Start()
}

func (m *WinSvcManager) Enable() error {
	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return fmt.Errorf("service: 打开服务 %s 失败: %w", m.name, err)
	}
	defer s.Close()

	conf, err := s.Config()
	if err != nil {
		return fmt.Errorf("service: 读取服务配置失败: %w", err)
	}
	conf.StartType = mgr.StartAutomatic
	return s.UpdateConfig(conf)
}

func (m *WinSvcManager) Disable() error {
	scm, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service: 连接 SCM 失败: %w", err)
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return fmt.Errorf("service: 打开服务 %s 失败: %w", m.name, err)
	}
	defer s.Close()

	conf, err := s.Config()
	if err != nil {
		return fmt.Errorf("service: 读取服务配置失败: %w", err)
	}
	conf.StartType = mgr.StartDisabled
	return s.UpdateConfig(conf)
}

func (m *WinSvcManager) Status() (ServiceStatus, error) {
	scm, err := mgr.Connect()
	if err != nil {
		// 无法连接 SCM — 当前用户可能缺权限，报告为未安装
		return StatusNotInstalled, nil
	}
	defer scm.Disconnect()

	s, err := scm.OpenService(m.name)
	if err != nil {
		return StatusNotInstalled, nil
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return StatusUnknown, fmt.Errorf("service: 查询服务状态失败: %w", err)
	}
	switch status.State {
	case svc.Running:
		return StatusRunning, nil
	case svc.Stopped:
		return StatusStopped, nil
	default:
		return StatusUnknown, nil
	}
}

func (m *WinSvcManager) Logs(lines int) (string, error) {
	// 校验服务名，防止 XPath 查询注入（wevtutil 的 /q 参数内嵌 XPath）
	if err := ValidateServiceName(m.name); err != nil {
		return "", fmt.Errorf("service: 日志查询拒绝: %w", err)
	}

	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return "", fmt.Errorf("service: 获取系统目录失败: %w", err)
	}
	cmd := exec.Command(filepath.Join(system32, "wevtutil.exe"),
		"qe", "System",
		"/q:*[System[Provider[@Name='"+m.name+"']]]",
		"/c:"+fmt.Sprint(lines),
		"/rd:true",
		"/f:text")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("service: 查询事件日志失败: %w", err)
	}
	return string(out), nil
}

// controlService 向服务发送控制命令并等待目标状态。
func controlService(s *mgr.Service, cmd svc.Cmd, target svc.State) error {
	status, err := s.Control(cmd)
	if err != nil {
		return fmt.Errorf("service: 发送控制命令失败: %w", err)
	}
	// 最多等待 30 秒
	for i := 0; i < 30 && status.State != target; i++ {
		time.Sleep(time.Second)
		status, err = s.Query()
		if err != nil {
			return fmt.Errorf("service: 查询服务状态失败: %w", err)
		}
	}
	if status.State != target {
		return fmt.Errorf("service: 超时等待状态变更到 %d", target)
	}
	return nil
}
