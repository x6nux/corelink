package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// helper: start server in background, return sockPath and cleanup func.
func startServer(t *testing.T, s *Server) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(sockPath) }()

	// Wait until the socket file exists (server is listening).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { _ = s.Close() })
	return sockPath
}

// helper: dial and return conn + scanner.
func dial(t *testing.T, sockPath string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return conn, sc
}

// helper: write a JSON-RPC request line.
func writeReq(t *testing.T, conn net.Conn, req *Request) {
	t.Helper()
	data, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// helper: read one response line.
func readResp(t *testing.T, sc *bufio.Scanner) *Response {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("expected response line, got EOF or error: %v", sc.Err())
	}
	resp, err := DecodeResponse(sc.Bytes())
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestServer_BasicCall(t *testing.T) {
	s := NewServer()
	s.Register("echo", func(params json.RawMessage) (any, error) {
		// Return params as-is (raw JSON).
		var v any
		if err := json.Unmarshal(params, &v); err != nil {
			return nil, err
		}
		return v, nil
	})

	sockPath := startServer(t, s)
	conn, sc := dial(t, sockPath)

	id := 1
	req := &Request{JSONRPC: "2.0", Method: "echo", Params: json.RawMessage(`{"msg":"hello"}`), ID: &id}
	writeReq(t, conn, req)

	resp := readResp(t, sc)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.ID == nil || *resp.ID != 1 {
		t.Fatalf("expected id=1, got %v", resp.ID)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", result["msg"])
	}
}

func TestServer_MethodNotFound(t *testing.T) {
	s := NewServer()
	sockPath := startServer(t, s)
	conn, sc := dial(t, sockPath)

	id := 1
	req := &Request{JSONRPC: "2.0", Method: "nonexistent", ID: &id}
	writeReq(t, conn, req)

	resp := readResp(t, sc)
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected code %d, got %d", CodeMethodNotFound, resp.Error.Code)
	}
}

func TestServer_HandlerError(t *testing.T) {
	s := NewServer()
	s.Register("fail", func(params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("something went wrong")
	})

	sockPath := startServer(t, s)
	conn, sc := dial(t, sockPath)

	id := 1
	req := &Request{JSONRPC: "2.0", Method: "fail", ID: &id}
	writeReq(t, conn, req)

	resp := readResp(t, sc)
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != CodeInternalError {
		t.Fatalf("expected code %d, got %d", CodeInternalError, resp.Error.Code)
	}
}

func TestServer_ParseError(t *testing.T) {
	s := NewServer()
	sockPath := startServer(t, s)
	conn, sc := dial(t, sockPath)

	// Send non-JSON.
	_, _ = conn.Write([]byte("this is not json\n"))

	resp := readResp(t, sc)
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != CodeParseError {
		t.Fatalf("expected code %d, got %d", CodeParseError, resp.Error.Code)
	}
}

func TestServer_Notification(t *testing.T) {
	s := NewServer()
	called := make(chan struct{}, 1)
	s.Register("notify", func(params json.RawMessage) (any, error) {
		called <- struct{}{}
		return nil, nil
	})

	sockPath := startServer(t, s)
	conn, _ := dial(t, sockPath)

	// Notification: ID is nil.
	req := &Request{JSONRPC: "2.0", Method: "notify", Params: json.RawMessage(`{}`)}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}

	// Wait for handler to be called.
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called for notification")
	}

	// Now send a request with an ID to verify the connection is still alive
	// and no spurious response was sent for the notification.
	id := 99
	reqWithID := &Request{JSONRPC: "2.0", Method: "notify", ID: &id}
	writeReq(t, conn, reqWithID)

	// Set read deadline — the only response we should get is for id=99.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	resp := readResp(t, sc)
	if resp.ID == nil || *resp.ID != 99 {
		t.Fatalf("expected id=99, got %v", resp.ID)
	}
}

func TestServer_StreamHandler(t *testing.T) {
	s := NewServer()
	s.RegisterStream("logs", func(ctx context.Context, params json.RawMessage, send func(any) error) error {
		for i := range 3 {
			if err := send(map[string]int{"seq": i}); err != nil {
				return err
			}
		}
		return nil
	})

	sockPath := startServer(t, s)
	conn, sc := dial(t, sockPath)

	id := 1
	req := &Request{JSONRPC: "2.0", Method: "logs", ID: &id}
	writeReq(t, conn, req)

	// Expect 3 data lines + 1 end-of-stream marker = 4 lines total.
	for i := range 3 {
		resp := readResp(t, sc)
		if resp.Error != nil {
			t.Fatalf("stream item %d: unexpected error: %v", i, resp.Error)
		}
		var m map[string]int
		if err := json.Unmarshal(resp.Result, &m); err != nil {
			t.Fatalf("stream item %d: unmarshal: %v", i, err)
		}
		if m["seq"] != i {
			t.Fatalf("stream item %d: expected seq=%d, got %d", i, i, m["seq"])
		}
	}

	// End-of-stream marker.
	eos := readResp(t, sc)
	if eos.Error != nil {
		t.Fatalf("eos: unexpected error: %v", eos.Error)
	}
	if string(eos.Result) != "null" {
		t.Fatalf("eos: expected null result, got %s", eos.Result)
	}
}

func TestServer_ConcurrentClients(t *testing.T) {
	s := NewServer()
	s.Register("add", func(params json.RawMessage) (any, error) {
		var args struct {
			A int `json:"a"`
			B int `json:"b"`
		}
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
		return args.A + args.B, nil
	})

	sockPath := startServer(t, s)

	var wg sync.WaitGroup
	for c := range 3 {
		wg.Go(func() {
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				t.Errorf("client %d: dial: %v", c, err)
				return
			}
			defer conn.Close()
			sc := bufio.NewScanner(conn)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

			id := c + 1
			req := &Request{
				JSONRPC: "2.0",
				Method:  "add",
				Params:  json.RawMessage(fmt.Sprintf(`{"a":%d,"b":%d}`, c, c*10)),
				ID:      &id,
			}
			data, err := EncodeRequest(req)
			if err != nil {
				t.Errorf("client %d: encode: %v", c, err)
				return
			}
			if _, err := conn.Write(data); err != nil {
				t.Errorf("client %d: write: %v", c, err)
				return
			}

			if !sc.Scan() {
				t.Errorf("client %d: no response", c)
				return
			}
			resp, err := DecodeResponse(sc.Bytes())
			if err != nil {
				t.Errorf("client %d: decode response: %v", c, err)
				return
			}
			if resp.Error != nil {
				t.Errorf("client %d: unexpected error: %v", c, resp.Error)
				return
			}
			var sum int
			if err := json.Unmarshal(resp.Result, &sum); err != nil {
				t.Errorf("client %d: unmarshal result: %v", c, err)
				return
			}
			expected := c + c*10
			if sum != expected {
				t.Errorf("client %d: expected %d, got %d", c, expected, sum)
			}
		})
	}
	wg.Wait()
}

func TestServer_CloseGraceful(t *testing.T) {
	s := NewServer()

	// A handler that blocks until the channel is closed.
	gate := make(chan struct{})
	handlerDone := make(chan struct{})
	s.Register("slow", func(params json.RawMessage) (any, error) {
		defer close(handlerDone)
		<-gate
		return "ok", nil
	})

	sockPath := startServer(t, s)

	// Connect and send a request that will block.
	conn, _ := dial(t, sockPath)
	id := 1
	req := &Request{JSONRPC: "2.0", Method: "slow", ID: &id}
	writeReq(t, conn, req)

	// Give the handler time to start.
	time.Sleep(50 * time.Millisecond)

	// Close the server — this should stop Accept but wait for the handler.
	closeDone := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closeDone)
	}()

	// Server should NOT have returned yet (handler is still running).
	select {
	case <-closeDone:
		t.Fatal("Close returned before handler finished")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}

	// Unblock the handler.
	close(gate)

	// Now Close should return.
	select {
	case <-closeDone:
		// OK.
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after handler finished")
	}
}

func TestServer_SocketPermission(t *testing.T) {
	s := NewServer()
	sockPath := startServer(t, s)

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("expected permission 0600, got %04o", perm)
	}
}

func TestServer_RemoveStaleSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stale.sock")

	// Create a fake file at the socket path.
	if err := os.WriteFile(sockPath, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewServer()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(sockPath) }()

	// Wait for socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(sockPath); err == nil {
			// Verify it's now a socket, not the old file.
			if info.Mode()&os.ModeSocket != 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Verify we can actually connect.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial after stale removal: %v", err)
	}
	conn.Close()

	_ = s.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}

// TestServer_StreamCloseGraceful 复现 bug #12：长流处理器（如 system.logs）
// 阻塞在 <-ctx.Done() 时，Server.Close() 必须能让其收到取消并在有限时间内返回，
// 不得因读循环被同步流调用卡住而永久挂起。
func TestServer_StreamCloseGraceful(t *testing.T) {
	s := NewServer()

	// 流处理器进入后阻塞，仅靠 ctx 取消退出——与真实 system.logs 同模式。
	entered := make(chan struct{})
	streamDone := make(chan struct{})
	s.RegisterStream("logs", func(ctx context.Context, _ json.RawMessage, send func(any) error) error {
		close(entered)
		defer close(streamDone)
		<-ctx.Done()
		return ctx.Err()
	})

	sockPath := startServer(t, s)
	conn, _ := dial(t, sockPath)

	// 发起长流请求。
	id := 1
	req := &Request{JSONRPC: "2.0", Method: "logs", ID: &id}
	writeReq(t, conn, req)

	// 等待流处理器真正进入（确认读循环已派发该流）。
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not start")
	}

	// 关闭服务端：应关闭连接并取消流 ctx，让流处理器退出，Close 有限返回。
	closeDone := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return — stream handler leaked / read loop blocked")
	}

	// 流处理器必须已退出（收到 ctx.Done）。
	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler was not cancelled")
	}
}

// TestServer_StreamNoLeakOnClientDisconnect 验证 bug #12 的另一维度：
// 客户端单方关闭连接（不调用 Server.Close）时，对端 EOF 须让读循环退出、
// conn 级 ctx 取消，正在运行的流处理器收到 ctx.Done 退出，不泄漏 goroutine。
func TestServer_StreamNoLeakOnClientDisconnect(t *testing.T) {
	s := NewServer()

	entered := make(chan struct{}, 1)
	streamDone := make(chan struct{}, 1)
	s.RegisterStream("logs", func(ctx context.Context, _ json.RawMessage, send func(any) error) error {
		entered <- struct{}{}
		<-ctx.Done()
		streamDone <- struct{}{}
		return ctx.Err()
	})

	// 用短路径 socket：本测试名较长，t.TempDir() 拼出的路径会超过 macOS
	// unix socket 104 字节上限，故单独建一个短 tmp 目录承载 socket。
	dir, err := os.MkdirTemp("", "rpc")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(sockPath) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		select {
		case serveErr := <-errCh:
			t.Fatalf("serve: %v", serveErr)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 直接 net.Dial（不走 dial helper 的 t.Cleanup，需手动控制关闭时机）。
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	id := 1
	req := &Request{JSONRPC: "2.0", Method: "logs", ID: &id}
	writeReq(t, conn, req)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not start")
	}

	// 客户端单方断开——不调用 s.Close()。
	if err := conn.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}

	// 流处理器须因 conn EOF → 读循环退出 → ctx 取消而退出。
	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler leaked after client disconnect")
	}

	// 兜底：Close 仍应有限返回（此时无活动流）。
	closeDone := make(chan struct{})
	go func() { _ = s.Close(); close(closeDone) }()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
}

// tempErr 是一个实现 net.Error 且 Temporary()==true 的临时错误。
type tempErr struct{}

func (tempErr) Error() string   { return "temporary accept error" }
func (tempErr) Timeout() bool   { return false }
func (tempErr) Temporary() bool { return true }

// fakeListener 按预设脚本返回 Accept 结果，用于驱动 acceptLoop。
// 先返回 temps 次临时错误，耗尽后返回 fatalErr 让循环退出。
type fakeListener struct {
	mu       sync.Mutex
	calls    int   // Accept 被调用次数
	temps    int   // 还需返回多少次临时错误
	fatalErr error // 临时错误耗尽后返回的致命错误
	closed   chan struct{}
}

func newFakeListener(temps int, fatalErr error) *fakeListener {
	return &fakeListener{temps: temps, fatalErr: fatalErr, closed: make(chan struct{})}
}

func (f *fakeListener) Accept() (net.Conn, error) {
	f.mu.Lock()
	f.calls++
	if f.temps > 0 {
		f.temps--
		f.mu.Unlock()
		return nil, tempErr{}
	}
	f.mu.Unlock()
	return nil, f.fatalErr
}

func (f *fakeListener) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

func (f *fakeListener) Addr() net.Addr { return dummyAddr{} }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "fake" }
func (dummyAddr) String() string  { return "fake" }

// 致命错误（非临时）应让 acceptLoop 返回该错误。
func TestServer_AcceptFatalError_Returns(t *testing.T) {
	s := NewServer()
	fatal := errors.New("fatal accept failure")
	fl := newFakeListener(0, fatal)

	err := s.acceptLoop(fl)
	if !errors.Is(err, fatal) {
		t.Fatalf("acceptLoop 应返回致命错误，得到 %v", err)
	}
}

// 临时错误应退避重试而非退出；恢复后（脚本转为致命错误）才退出。
// 同时验证退避不 busy-loop：在限定时间内调用次数有上界。
func TestServer_AcceptTemporaryError_Retries(t *testing.T) {
	s := NewServer()
	fatal := errors.New("eventual fatal")
	// 注入 5 次临时错误，之后返回致命错误。
	fl := newFakeListener(5, fatal)

	start := time.Now()
	err := s.acceptLoop(fl)
	elapsed := time.Since(start)

	if !errors.Is(err, fatal) {
		t.Fatalf("acceptLoop 应在临时错误耗尽后返回致命错误，得到 %v", err)
	}
	// 5 次临时错误 + 1 次致命错误 = 6 次 Accept 调用。
	if fl.calls != 6 {
		t.Fatalf("Accept 调用次数应为 6（5 临时 + 1 致命），得到 %d", fl.calls)
	}
	// 退避存在 → 5 次临时错误至少累积一定 sleep（首次退避 ≥ 5ms）。
	if elapsed < 5*time.Millisecond {
		t.Fatalf("临时错误应触发退避 sleep，elapsed=%v 过短（疑似 busy-loop）", elapsed)
	}
	// 退避有上界 → 5 次重试不应耗时过久（指数退避封顶 1s，但 5 次远不到上限）。
	if elapsed > 2*time.Second {
		t.Fatalf("退避耗时 %v 过长，疑似上界失效", elapsed)
	}
}

// listener 正常关闭（Close 后 Accept 返回错误）时，acceptLoop 应优雅退出返回 nil。
func TestServer_AcceptCloseGraceful_ReturnsNil(t *testing.T) {
	s := NewServer()
	// 关闭 done 模拟 Close 已触发；fatalErr 模拟 Close 后 Accept 返回的错误。
	close(s.done)
	fl := newFakeListener(0, errors.New("use of closed network connection"))

	err := s.acceptLoop(fl)
	if err != nil {
		t.Fatalf("关闭中（s.done 已关）acceptLoop 应返回 nil，得到 %v", err)
	}
}
