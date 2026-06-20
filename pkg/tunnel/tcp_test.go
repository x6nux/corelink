package tunnel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestTCPDialer_连通性 验证 tcpDialer 可以拨号到本地 TCP 监听。
func TestTCPDialer_连通性(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	// 服务端回显
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	d := &tcpDialer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, err := d.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	msg := []byte("hello-tcp")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "hello-tcp" {
		t.Fatalf("回显 = %q, 期望 hello-tcp", buf)
	}
}

// TestTCPDialer_拨号失败 验证拨向不存在的地址返回错误。
func TestTCPDialer_拨号失败(t *testing.T) {
	d := &tcpDialer{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 拨号到一个几乎不可能在监听的端口
	_, err := d.Dial(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("拨号到不存在的地址应返回错误")
	}
}

// TestTCPDialer_上下文取消 验证上下文取消后 Dial 返回错误。
func TestTCPDialer_上下文取消(t *testing.T) {
	d := &tcpDialer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	_, err := d.Dial(ctx, "127.0.0.1:80")
	if err == nil {
		t.Fatal("上下文已取消时 Dial 应返回错误")
	}
}

// TestTCPListener_AcceptAndAddr 验证 tcpListener 的 Accept 和 Addr 行为。
func TestTCPListener_AcceptAndAddr(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TCP}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	// Addr 应返回非 nil
	addr := ln.Addr()
	if addr == nil {
		t.Fatal("Addr() 不应返回 nil")
	}
	if addr.String() == "" {
		t.Fatal("Addr().String() 不应为空")
	}

	// 从客户端连接
	go func() {
		c, err := net.DialTimeout("tcp", addr.String(), time.Second)
		if err != nil {
			return
		}
		c.Write([]byte("hi"))
		c.Close()
	}()

	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer c.Close()

	buf := make([]byte, 2)
	n, err := c.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hi" {
		t.Fatalf("Accept 后 Read = %q, 期望 hi", buf[:n])
	}
}

// TestTCPListener_Close后Accept返回错误 验证关闭后 Accept 返回错误。
func TestTCPListener_Close后Accept返回错误(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TCP}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ln.Close()
	_, err = ln.Accept()
	if err == nil {
		t.Fatal("关闭后 Accept 应返回错误")
	}
}

// TestNewDialer_TCP构造 验证 NewDialer 可正确构造 TCP Dialer。
func TestNewDialer_TCP构造(t *testing.T) {
	d, err := NewDialer(&Config{Protocol: TCP})
	if err != nil {
		t.Fatalf("NewDialer(TCP): %v", err)
	}
	if d == nil {
		t.Fatal("NewDialer 不应返回 nil")
	}
}

// TestNewDialer_不支持的协议 验证不支持的协议返回错误。
func TestNewDialer_不支持的协议(t *testing.T) {
	_, err := NewDialer(&Config{Protocol: Protocol("unknown-proto")})
	if err == nil {
		t.Fatal("不支持的协议应返回错误")
	}
}

// TestListen_不支持的协议 验证不支持的协议返回错误。
func TestListen_不支持的协议(t *testing.T) {
	_, err := Listen(&Config{Protocol: Protocol("unknown-proto")}, "127.0.0.1:0")
	if err == nil {
		t.Fatal("不支持的协议应返回错误")
	}
}

// TestTLSListener_默认PinnedMode 验证 Listen(TLS, nil TLS opts) 使用 pinned 模式
// 并自动生成自签证书。
func TestTLSListener_默认PinnedMode(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TLS}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(TLS, nil): %v", err)
	}
	defer ln.Close()

	tl, ok := ln.(*tlsListener)
	if !ok {
		t.Fatalf("listener 类型 = %T, 期望 *tlsListener", ln)
	}
	// 默认模式不应使用 tlsConf（tlsConf 仅 acme 用）
	if tl.tlsConf != nil {
		t.Error("默认 pinned 模式 tlsConf 应为 nil（使用 srvCert）")
	}
	// srvCert 应已填充
	if len(tl.srvCert.Certificate) == 0 {
		t.Fatal("默认 pinned 模式应自动生成 srvCert")
	}
}

// TestTLSListener_SetServerCert 验证 setServerCert 可覆盖注入证书。
func TestTLSListener_SetServerCert(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TLS}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	tl := ln.(*tlsListener)
	cert, _, err := GenerateSelfSigned("test.example.com")
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	tl.setServerCert(cert)
	if len(tl.srvCert.Certificate) == 0 {
		t.Fatal("setServerCert 后 srvCert 应非空")
	}
}

// TestTCPDialerAndListener_全双工 验证 TCP 协议可以进行双向通信。
func TestTCPDialerAndListener_全双工(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TCP}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// 服务端先读后写
		buf := make([]byte, 5)
		io.ReadFull(c, buf)
		c.Write([]byte("world"))
	}()

	d, _ := NewDialer(&Config{Protocol: TCP})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, err := d.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.Write([]byte("hello"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "world" {
		t.Fatalf("got %q, want world", buf)
	}
	<-done
}
