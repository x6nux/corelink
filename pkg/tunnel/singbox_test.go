package tunnel

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"
)

func TestSingBoxSubprocessDirectOutbound(t *testing.T) {
	if _, err := exec.LookPath("sing-box"); err != nil {
		t.Skip("未找到 sing-box 可执行文件，跳过子进程出站测试")
	}
	ln, _ := Listen(&Config{Protocol: TCP}, "127.0.0.1:0")
	defer ln.Close()
	echoServe(t, ln)

	// direct 出站：sing-box 直连目标
	d, err := newSingBoxDialer(&ProxyOptions{SingBoxOutbound: `{"type":"direct"}`})
	if err != nil {
		t.Fatalf("newSingBoxDialer: %v", err)
	}
	if closer, ok := d.(interface{ Close() error }); ok {
		defer closer.Close() // 关闭时终止 sing-box 子进程
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("经 sing-box 子进程拨号失败: %v", err)
	}
	defer c.Close()
	c.Write([]byte("ok"))
	buf := make([]byte, 2)
	io.ReadFull(c, buf)
	if string(buf) != "ok" {
		t.Fatalf("echo=%q", buf)
	}
}
