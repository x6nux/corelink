//go:build !linux && !darwin && !windows

package service

import "fmt"

var errUnsupported = fmt.Errorf("service: 当前平台不支持服务管理")

type stubManager struct{}

// New 在不支持的平台上返回占位实现。
// name 必须为合法服务名（仅允许 [a-zA-Z0-9_-]），否则 panic（编程错误）。
func New(name string) Manager {
	if err := ValidateServiceName(name); err != nil {
		panic(err)
	}
	return &stubManager{}
}

func (m *stubManager) Install(ServiceConfig) error    { return errUnsupported }
func (m *stubManager) Uninstall() error               { return errUnsupported }
func (m *stubManager) Start() error                   { return errUnsupported }
func (m *stubManager) Stop() error                    { return errUnsupported }
func (m *stubManager) Restart() error                 { return errUnsupported }
func (m *stubManager) Enable() error                  { return errUnsupported }
func (m *stubManager) Disable() error                 { return errUnsupported }
func (m *stubManager) Status() (ServiceStatus, error) { return StatusUnknown, errUnsupported }
func (m *stubManager) Logs(int) (string, error)       { return "", errUnsupported }
