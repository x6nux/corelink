package tunnel

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TestGRPCDialer_mTLS 验证 grpcDialer 在 TLS 非 nil 时支持 mTLS（CA 哈希钉扎）。
// 依赖 A6：server 出示 CA 签链，由后续 section 实现。
func TestGRPCDialer_mTLS(t *testing.T) {
	t.Skip("依赖 A6：server 出示 CA 签链，见后续 section")
	ca := newTunnelTestCA(t)
	srvTLS, _ := ca.issueServer(t, "127.0.0.1")
	clientTLS := ca.issueClient(t, "grpc-test-node")

	// ---- 服务端 ----
	srvTLSConf := &tls.Config{
		Certificates: []tls.Certificate{srvTLS},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.pool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	tlsLn := tls.NewListener(rawLn, srvTLSConf)

	grpcSrv := grpc.NewServer()
	genv1.RegisterTunnelServiceServer(grpcSrv, &grpcEchoServer{})
	go grpcSrv.Serve(tlsLn) //nolint:errcheck
	defer grpcSrv.Stop()

	// ---- 客户端（CA 哈希钉扎）----
	caHash := CASPKIHash(ca.cert)

	d, err := newGRPCDialer(&Config{
		Protocol: GRPC,
		TLS: &TLSOptions{
			Mode:         TLSModePinned,
			ServerName:   "127.0.0.1",
			PinnedCAHash: caHash,
		},
	})
	if err != nil {
		t.Fatalf("newGRPCDialer: %v", err)
	}

	// 注入客户端证书。
	gd := d.(*grpcDialer)
	tc, _ := ClientTLSConfig(&TLSOptions{
		Mode:         TLSModePinned,
		ServerName:   "127.0.0.1",
		PinnedCAHash: caHash,
	})
	tc.Certificates = []tls.Certificate{clientTLS}
	gd.creds = credentials.NewTLS(tc)

	// Dial 拿 net.Conn → 回声验证。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx, rawLn.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-mtls-grpc")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// TestGRPCDialer_Insecure 回归验证 Config.TLS == nil 时保持 insecure（向后兼容）。
func TestGRPCDialer_Insecure(t *testing.T) {
	ln, err := newGRPCListener("127.0.0.1:0")
	if err != nil {
		t.Fatalf("newGRPCListener: %v", err)
	}
	defer ln.Close()
	echoServe(t, ln)

	d, err := newGRPCDialer(&Config{Protocol: GRPC})
	if err != nil {
		t.Fatalf("newGRPCDialer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("insecure-grpc")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// ---- 辅助 ----

// grpcEchoServer 是 mTLS 测试用的 echo gRPC handler：收到 Chunk 后原样回发。
type grpcEchoServer struct {
	genv1.UnimplementedTunnelServiceServer
}

func (s *grpcEchoServer) Stream(stream grpc.BidiStreamingServer[genv1.Chunk, genv1.Chunk]) error {
	for {
		chunk, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
}

// TestStreamConn_CloseAndDeadlineConcurrent 复现并回归 #28：
// server 侧 streamConn 的 cancel 回调为 close(done)，Close() 与读超时 fire()
// 各受不同 Once 保护，并发触发会对同一 channel 二次 close 而 panic。
// 修复后 cancel 回调须幂等：done 只被关闭一次，且 done 已关闭。
func TestStreamConn_CloseAndDeadlineConcurrent(t *testing.T) {
	for i := 0; i < 200; i++ {
		done := make(chan struct{})
		// 与 grpcListener.Stream 构造 server 侧 streamConn 的形态一致：
		// cancel = close(done)、无 CloseSend。
		conn := newStreamConn(
			nil, // 本用例不触碰 stream，仅测 cancel 幂等
			func() { close(done) },
			nil,
			grpcAddr{"grpc-server"},
			grpcAddr{"grpc-client"},
		)

		var wg sync.WaitGroup
		wg.Add(2)
		// 路径 A：读超时触发 → fire() → cancel()
		go func() {
			defer wg.Done()
			conn.fire()
		}()
		// 路径 B：Close() → cancel()
		go func() {
			defer wg.Done()
			_ = conn.Close()
		}()
		wg.Wait()

		// done 必须已被关闭（cancel 至少触发一次），且不得 panic（不得二次 close）。
		select {
		case <-done:
		default:
			t.Fatalf("第 %d 轮：done 未被关闭，cancel 回调未触发", i)
		}

		// 再次 Close + 再次 SetReadDeadline(已过期) 仍须幂等、不 panic。
		_ = conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(-time.Second))
	}
}
