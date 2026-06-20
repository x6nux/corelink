package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// startRPCServer 在临时目录启动 RPC 服务器，返回 socket 路径。
func startRPCServer(t *testing.T, srv *rpc.Server) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	go func() { _ = srv.Serve(sockPath) }()

	// 等待 socket 文件出现
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sockPath
}
