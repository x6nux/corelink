package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// singBoxDialer 管理一个 sing-box 子进程，并经其本地 socks 入站拨号。
type singBoxDialer struct {
	cmd       *exec.Cmd
	cfgPath   string
	inner     Dialer     // 指向本地 socks 的 SOCKS5 拨号器
	closeOnce sync.Once  // 保证 Close 仅执行一次，避免重复 Kill/清理
}

// newSingBoxDialer 创建 sing-box 子进程 dialer。
// 返回 DialCloser 类型——调用方必须在不再需要时调 Close() 终止子进程、清理临时文件。
func newSingBoxDialer(p *ProxyOptions) (DialCloser, error) {
	bin := p.SingBoxBinary
	if bin == "" {
		bin = "sing-box"
	}
	// 1. 选本地空闲端口给 sing-box 的 socks 入站
	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	// 2. 渲染配置：mixed 入站(本地) + 调用方 outbound
	// sing-box mixed 入站支持 SOCKS4/4a/5 与 HTTP，配置结构：
	//   {"type":"mixed","listen":"127.0.0.1","listen_port":N}
	// 顶层：{"inbounds":[...],"outbounds":[...]}
	var outbound json.RawMessage = json.RawMessage(p.SingBoxOutbound)
	cfg := map[string]any{
		"inbounds": []any{map[string]any{
			"type": "mixed", "listen": "127.0.0.1", "listen_port": port,
		}},
		"outbounds": []json.RawMessage{outbound},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("tunnel: 渲染 sing-box 配置失败: %w", err)
	}
	f, err := os.CreateTemp("", "corelink-singbox-*.json")
	if err != nil {
		return nil, err
	}
	cfgPath := f.Name()
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(cfgPath)
		return nil, err
	}
	f.Close()
	// 3. 拉起 sing-box 进程
	cmd := exec.Command(bin, "run", "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(cfgPath)
		return nil, fmt.Errorf("tunnel: 启动 sing-box 失败: %w", err)
	}
	// 4. 等本地 socks 端口就绪
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitListening(addr, 5*time.Second); err != nil {
		cmd.Process.Kill()
		cmd.Wait() // 回收子进程，防止僵尸进程
		os.Remove(cfgPath)
		return nil, fmt.Errorf("tunnel: sing-box socks 未就绪: %w", err)
	}
	// 5. 经本地 socks 拨号（复用 SOCKS5 装饰器）
	inner, err := wrapWithProxy(&tcpDialer{}, &ProxyOptions{URL: "socks5://" + addr})
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait() // 回收子进程，防止僵尸进程
		os.Remove(cfgPath)
		return nil, err
	}
	return &singBoxDialer{cmd: cmd, cfgPath: cfgPath, inner: inner}, nil
}

func (d *singBoxDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return d.inner.Dial(ctx, addr)
}

// Close 终止 sing-box 子进程并清理临时配置（并发安全，使用 sync.Once 保证仅执行一次）。
func (d *singBoxDialer) Close() error {
	d.closeOnce.Do(func() {
		if d.cmd != nil && d.cmd.Process != nil {
			d.cmd.Process.Kill()
			d.cmd.Wait()
		}
		if d.cfgPath != "" {
			os.Remove(d.cfgPath)
		}
	})
	return nil
}

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitListening(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 监听超时", addr)
}
