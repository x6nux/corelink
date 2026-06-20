package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/rpc"
)

// RPCClient 封装 rpc.Client，提供 tea.Cmd 异步调用模式。
type RPCClient struct {
	inner *rpc.Client
}

// NewRPCClient 连接指定 Unix socket 路径，返回 RPCClient。
func NewRPCClient(sockPath string) (*RPCClient, error) {
	c, err := rpc.Dial(sockPath)
	if err != nil {
		return nil, err
	}
	return &RPCClient{inner: c}, nil
}

// RPCResult 异步 RPC 调用的结果 Msg。
type RPCResult struct {
	Method string
	Result any   // 成功时的反序列化结果
	Err    error // 失败时的错误
}

// Call 返回一个 tea.Cmd，异步执行 RPC 调用，结果通过 tea.Msg 传回。
// resultFactory 是一个构造空 result 对象的函数（如 func() any { return new(StatusResult) }）。
// 返回的 Msg 类型是 RPCResult{Method, Result, Err}。
func (c *RPCClient) Call(method string, params any, resultFactory func() any) tea.Cmd {
	return func() tea.Msg {
		result := resultFactory()
		err := c.inner.Call(method, params, result)
		if err != nil {
			return RPCResult{Method: method, Err: err}
		}
		return RPCResult{Method: method, Result: result}
	}
}

// Close 关闭底层连接。
func (c *RPCClient) Close() error {
	return c.inner.Close()
}

// TickMsg 定时刷新 Msg。
type TickMsg struct{}

// TickCmd 返回 N 秒后触发 TickMsg 的 Cmd。
func TickCmd(seconds int) tea.Cmd {
	return tea.Tick(time.Duration(seconds)*time.Second, func(time.Time) tea.Msg {
		return TickMsg{}
	})
}
