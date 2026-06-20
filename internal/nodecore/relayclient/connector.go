package relayclient

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// dialConnector 是 Connector 的真实实现：通过 pkg/tunnel 拨到 relay 的隧道端点，
// 以验证 relay 可达并维持一条会话连接；连接断开（读到 EOF/错误）即触发重连。
//
// S3 范围：数据面的双通道收发由 bind 包在 Bind 内独立维护；本连接器作为
// "接入会话/可达性"的监督通道（也是 S4 relay 接入握手协议的承载基础）。
// relay 协议为 S4，S3 仅打通"连接 + 断线感知 + 被动重连"。
type dialConnector struct {
	dialer tunnel.Dialer

	mu   sync.Mutex
	conn net.Conn
}

// NewDialConnector 构造基于 pkg/tunnel 的连接器。
func NewDialConnector() (*dialConnector, error) {
	d, err := tunnel.NewDialer(&tunnel.Config{Protocol: tunnel.TCP})
	if err != nil {
		return nil, fmt.Errorf("relayclient: 创建隧道 dialer: %w", err)
	}
	return &dialConnector{dialer: d}, nil
}

// Connect 拨到 relay 隧道端点。成功则保存连接，供 Wait 监视。
func (d *dialConnector) Connect(ctx context.Context, ep *genv1.RelayEndpoint) error {
	addr := endpointAddr(ep.GetTunnel())
	if addr == "" {
		return fmt.Errorf("relayclient: relay %q 无隧道端点", ep.GetRelayId())
	}
	c, err := d.dialer.Dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("relayclient: 拨 relay 隧道 %s: %w", addr, err)
	}
	d.mu.Lock()
	d.conn = c
	d.mu.Unlock()
	return nil
}

// Wait 阻塞直到底层连接断开（读到 EOF/错误）或 ctx 取消。
func (d *dialConnector) Wait(ctx context.Context) error {
	d.mu.Lock()
	c := d.conn
	d.mu.Unlock()
	if c == nil {
		return fmt.Errorf("relayclient: 未连接")
	}

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		// 阻塞读：relay 端关闭或网络错误时返回，视为断开。
		_, err := c.Read(buf)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		c.Close()
		return ctx.Err()
	case err := <-errCh:
		c.Close()
		if err == nil {
			err = fmt.Errorf("relayclient: relay 连接已关闭")
		}
		return err
	}
}

// endpointAddr 把 genv1.Endpoint 拼成 host:port；无效时返回空串。
func endpointAddr(ep *genv1.Endpoint) string {
	if ep == nil || ep.GetHost() == "" || ep.GetPort() == 0 {
		return ""
	}
	return net.JoinHostPort(ep.GetHost(), strconv.Itoa(int(ep.GetPort())))
}

// protoToTunnel 把 proto 的 TunnelProtocol 映射为 pkg/tunnel.Protocol。
func protoToTunnel(p genv1.TunnelProtocol) tunnel.Protocol {
	switch p {
	case genv1.TunnelProtocol_TUNNEL_PROTOCOL_WS:
		return tunnel.WS
	case genv1.TunnelProtocol_TUNNEL_PROTOCOL_TLS:
		return tunnel.TLS
	case genv1.TunnelProtocol_TUNNEL_PROTOCOL_WSS:
		return tunnel.WSS
	case genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC:
		return tunnel.GRPC
	default:
		return tunnel.TCP
	}
}

var _ = protoToTunnel // S4 relay 选择隧道协议时使用
