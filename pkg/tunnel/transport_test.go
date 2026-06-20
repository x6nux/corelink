package tunnel

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func echoServe(t *testing.T, ln Listener) {
	t.Helper()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
}

func roundTrip(t *testing.T, d Dialer, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := d.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	msg := []byte("ping")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
}

func TestTCPRoundTrip(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TCP}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	echoServe(t, ln)
	d, _ := NewDialer(&Config{Protocol: TCP})
	roundTrip(t, d, ln.Addr().String())
}

func TestTLSRoundTripWithPinning(t *testing.T) {
	// 依赖 A6：CA 钉扎要求 server 出示 CA 签的完整链，见后续 section
	t.Skip("依赖 A6：CA 钉扎需 server 出示完整链，见后续 section")
	_ = x509.Certificate{}
}

func TestWSRoundTrip(t *testing.T) {
	ln, err := Listen(&Config{Protocol: WS, WSPath: "/tunnel"}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	echoServe(t, ln)
	d, _ := NewDialer(&Config{Protocol: WS, WSPath: "/tunnel"})
	roundTrip(t, d, ln.Addr().String())
}

func TestWSSRoundTripWithPinning(t *testing.T) {
	// 依赖 A6：CA 钉扎要求 server 出示 CA 签的完整链，见后续 section
	t.Skip("依赖 A6：CA 钉扎需 server 出示完整链，见后续 section")
}

// TestGRPCReadDeadlinePast 回归 S-2：SetReadDeadline(过去时间) 后阻塞的 Read
// 必须立即返回 os.ErrDeadlineExceeded，不得因 fire 重入 deadlineMu 而死锁。
func TestGRPCReadDeadlinePast(t *testing.T) {
	// server 端只 Accept 不回写，制造一个会永久阻塞的 Read。
	ln, err := Listen(&Config{Protocol: GRPC}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // 持有连接，不读不写
		}
	}()

	d, err := NewDialer(&Config{Protocol: GRPC})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := d.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	// 过去时间：SetReadDeadline 直接走 d<=0 分支调用 fire（曾经的死锁点）。
	if err := c.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := c.Read(buf)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Read err = %v, want os.ErrDeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read 未在 deadline 后返回——可能死锁/挂起")
	}
}

func TestGRPCRoundTrip(t *testing.T) {
	ln, err := Listen(&Config{Protocol: GRPC}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	echoServe(t, ln)
	d, err := NewDialer(&Config{Protocol: GRPC})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	roundTrip(t, d, ln.Addr().String())
}

// TestListenTLSACMEUsesGetCertificate 回归 #38：Listen(TLS,acme) 不得再产出
// 用零值证书握手、必然失败的 listener；应携带 ServerTLSConfig 的 GetCertificate 钩子。
func TestListenTLSACMEUsesGetCertificate(t *testing.T) {
	dir := t.TempDir()
	ln, err := Listen(&Config{Protocol: TLS, TLS: &TLSOptions{
		Mode:         TLSModeACME,
		ACMEDomains:  []string{"example.com"},
		ACMECacheDir: dir,
	}}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(TLS,acme): %v", err)
	}
	defer ln.Close()

	tl, ok := ln.(*tlsListener)
	if !ok {
		t.Fatalf("listener 类型 = %T, want *tlsListener", ln)
	}
	// ACME 模式下握手 config 必须来自 ServerTLSConfig（含 GetCertificate），
	// 而不是用零值 srvCert 直接构造——后者会令每次握手必然失败。
	if tl.tlsConf == nil {
		t.Fatal("ACME 模式 tlsListener.tlsConf 为 nil——仍会用零值证书握手（必失败）")
	}
	if tl.tlsConf.GetCertificate == nil {
		t.Fatal("ACME 模式 tlsConf 缺少 GetCertificate 钩子")
	}
}

// TestListenTLSUnknownModeFailsFast 回归 #38：不支持的 TLS 模式应在构造时
// 直接返回 error，而不是延迟到握手期才暴露为「必失败 listener」。
func TestListenTLSUnknownModeFailsFast(t *testing.T) {
	ln, err := Listen(&Config{Protocol: TLS, TLS: &TLSOptions{Mode: TLSMode("bogus")}}, "127.0.0.1:0")
	if err == nil {
		ln.Close()
		t.Fatal("Listen(TLS,未知模式) 应返回 error，实际返回了 listener")
	}
}
