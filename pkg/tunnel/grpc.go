package tunnel

// grpc.go 参考 go-gost 的 grpc 隧道设计，用 google.golang.org/grpc 自建
// 轻量级双向流传输：一条 TunnelService.Stream 双向流，两端把字节流切片封装在
// Chunk 中互传，streamConn 把单条双向流适配为面向流的 net.Conn。
//
// 不依赖任何 go-gost 库。insecure 传输（无 TLS），外层 TLS 由上层另加，与
// 其它 Protocol 对称。

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// chunkStream 是 client/server 双向流的公共方法集。
// grpc.BidiStreamingClient[Chunk,Chunk] 与 grpc.BidiStreamingServer[Chunk,Chunk]
// 都满足该接口（Send/Recv 签名一致）。
type chunkStream interface {
	Send(*genv1.Chunk) error
	Recv() (*genv1.Chunk, error)
}

// grpcAddr 是 streamConn 用的占位 net.Addr（gRPC 流本身不暴露底层四元组）。
type grpcAddr struct{ s string }

func (a grpcAddr) Network() string { return "grpc" }
func (a grpcAddr) String() string  { return a.s }

// streamConn 把一条 gRPC 双向流包装成 net.Conn。
//
// Read 维护内部残余缓冲 buf：buf 空时 Recv 一个 Chunk 填充，再按调用方请求的
// 长度切片返回，正确处理拆包/粘包。Write 把整段装进一个 Chunk 调 Send。
type streamConn struct {
	stream chunkStream

	// closeSend 在 Close 时调用：client 侧 = CloseSend；server 侧为 nil。
	closeSend func() error
	// cancel 取消承载流的 context：client 侧取消拨号 context；server 侧令 handler 返回。
	cancel context.CancelFunc
	// cancelOnce 保证 cancel 至多被调用一次：Close() 与读超时 fire() 两条路径
	// 各受不同 Once 保护、都会触发 cancel，若 server 侧 cancel 回调为 close(done)
	// 这类非幂等闭包，二次调用会 panic（#28）。在 streamConn 内统一兜底幂等。
	cancelOnce sync.Once

	local  net.Addr
	remote net.Addr

	readMu  sync.Mutex
	buf     []byte     // 上一次 Recv 未读完的残余
	writeMu sync.Mutex // 串行化 Send：gRPC 同一 stream 不可并发 SendMsg

	// 读超时：基于 SetReadDeadline 设置，到期时由 timer 回调 cancel 流。
	//
	// readErr 与 deadlineMu 解耦：readErr 用 atomic.Pointer + readErrOnce 串行化
	// 写入（避免 fire 回调重入 deadlineMu 造成非递归锁死锁，见 SetReadDeadline）。
	// deadlineMu 只保护 readTimer 字段。
	deadlineMu  sync.Mutex
	readTimer   *time.Timer
	readErrOnce sync.Once
	readErr     atomic.Pointer[error]

	closeOnce sync.Once
}

func newStreamConn(stream chunkStream, cancel context.CancelFunc, closeSend func() error, local, remote net.Addr) *streamConn {
	return &streamConn{
		stream:    stream,
		cancel:    cancel,
		closeSend: closeSend,
		local:     local,
		remote:    remote,
	}
}

// doCancel 经 cancelOnce 至多调用一次 cancel，使 Close() 与 fire() 两条路径
// 并发触发时 cancel 回调（可能为 close(done) 等非幂等闭包）只执行一次（#28）。
func (c *streamConn) doCancel() {
	c.cancelOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
}

func (c *streamConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	// 内部循环：遇空 Chunk 重新 Recv，绝不把 (0,nil) 抛给调用方
	// （否则裸 Read 调用方可能 busy-loop）。
	for len(c.buf) == 0 {
		if err := c.readDeadlineErr(); err != nil {
			return 0, err
		}
		chunk, err := c.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			// 读超时被 cancel 触发时返回超时错误而非裸 context.Canceled。
			if de := c.readDeadlineErr(); de != nil {
				return 0, de
			}
			return 0, err
		}
		c.buf = chunk.GetData()
		// c.buf 为空则继续循环重新 Recv。
	}

	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *streamConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// 拷贝一份：Send 可能异步引用，调用方可能复用 p。
	data := make([]byte, len(p))
	copy(data, p)
	// gRPC 约束：同一 stream 不可并发 SendMsg，writeMu 串行化（与 readMu 对称）。
	c.writeMu.Lock()
	err := c.stream.Send(&genv1.Chunk{Data: data})
	c.writeMu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *streamConn) Close() error {
	c.closeOnce.Do(func() {
		if c.closeSend != nil {
			_ = c.closeSend()
		}
		c.deadlineMu.Lock()
		if c.readTimer != nil {
			c.readTimer.Stop()
		}
		c.deadlineMu.Unlock()
		c.doCancel()
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr  { return c.local }
func (c *streamConn) RemoteAddr() net.Addr { return c.remote }

// readDeadlineErr 返回读超时已触发的错误（若有）。无锁，原子读 readErr。
func (c *streamConn) readDeadlineErr() error {
	if p := c.readErr.Load(); p != nil {
		return *p
	}
	return nil
}

// fire 标记读超时已触发并 cancel 流，令阻塞中的 Recv 返回。
// 用 readErrOnce 串行化、原子写 readErr——刻意不依赖 deadlineMu，
// 因为 SetReadDeadline 持 deadlineMu 时（d<=0 分支）会直接调用 fire，
// 若 fire 再 Lock(deadlineMu) 将造成非递归锁重入死锁。
func (c *streamConn) fire() {
	c.readErrOnce.Do(func() {
		err := error(os.ErrDeadlineExceeded)
		c.readErr.Store(&err)
		c.doCancel()
	})
}

// SetReadDeadline 基于 context 取消实现读超时：到期后 cancel 流并令后续/阻塞的
// Recv 返回超时错误。t 为零值表示清除超时。
//
// 注意：超时一旦触发即不可恢复——底层 gRPC stream 已被 cancel，
// 后续任何 Read 均返回 os.ErrDeadlineExceeded。调用方应在超时后
// 关闭连接并重新建立，不要尝试重置截止时间继续复用。
func (c *streamConn) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()

	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		// fire 不获取 deadlineMu，这里持锁直接调用是安全的（无重入死锁）。
		c.fire()
		return nil
	}
	c.readTimer = time.AfterFunc(d, c.fire)
	return nil
}

// SetWriteDeadline 不支持精确写超时（gRPC Send 由流 context 控制），返回 nil 以
// 满足 net.Conn 接口且不 panic。
func (c *streamConn) SetWriteDeadline(t time.Time) error { return nil }

// SetDeadline 等价于同时设置读截止时间；写截止时间不支持（见上）。
func (c *streamConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// ---- Listener 侧 ----

// grpcListener 基于 net.Listen + grpc.Server，Stream handler 把 server stream
// 包成 streamConn 推入 accept chan（参照 ws.go 的 wsListener chan+server 结构）。
type grpcListener struct {
	genv1.UnimplementedTunnelServiceServer

	ln     net.Listener
	srv    *grpc.Server
	accept chan net.Conn
	closed chan struct{}

	closeOnce sync.Once
}

func newGRPCListener(addr string) (Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	l := &grpcListener{
		ln:     ln,
		accept: make(chan net.Conn, 8),
		closed: make(chan struct{}),
	}
	l.srv = grpc.NewServer()
	genv1.RegisterTunnelServiceServer(l.srv, l)
	go l.srv.Serve(ln)
	return l, nil
}

// Stream 是 TunnelService 的 server 端实现：把这条双向流包成 streamConn 推给
// Accept，并阻塞直到连接关闭（否则 handler 返回会提前结束流）。
func (l *grpcListener) Stream(stream grpc.BidiStreamingServer[genv1.Chunk, genv1.Chunk]) error {
	done := make(chan struct{})
	// cancel 回调（close(done)）非幂等，但 streamConn 内部用 cancelOnce 保证
	// cancel 至多触发一次（Close 与读超时 fire 两条路径并发亦安全），见 #28。
	conn := newStreamConn(
		stream,
		func() { close(done) }, // cancel：令 handler 返回
		nil,                    // server 侧无 CloseSend
		grpcAddr{l.ln.Addr().String()},
		grpcAddr{"grpc-client"},
	)
	select {
	case l.accept <- conn:
	case <-l.closed:
		return nil
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	// 阻塞直到 conn.Close()（done）、流 context 取消、或 listener 关闭。
	select {
	case <-done:
	case <-stream.Context().Done():
	case <-l.closed:
	}
	return nil
}

func (l *grpcListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *grpcListener) Addr() net.Addr { return l.ln.Addr() }

func (l *grpcListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.srv.Stop()
	})
	return nil
}

// ---- Dialer 侧 ----

// grpcDialer 用 grpc.NewClient（v1.63+ 推荐，非弃用）建连，
// 开 Stream 后包成 streamConn 返回。
//
// 支持 mTLS：cfg.TLS 非 nil 时调 ClientTLSConfig 构造 TLS config，
// 通过 credentials.NewTLS 注入；Go gRPC 自动设 ALPN "h2"，
// 与 M1 多协议 listener 的 ALPN 分流匹配。
// cfg.TLS 为 nil 时保持 insecure（向后兼容）。
type grpcDialer struct {
	creds credentials.TransportCredentials
}

func newGRPCDialer(cfg *Config) (Dialer, error) {
	d := &grpcDialer{}
	if cfg.TLS != nil {
		tc, err := ClientTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		d.creds = credentials.NewTLS(tc)
	} else {
		d.creds = insecure.NewCredentials()
	}
	return d, nil
}

func (d *grpcDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(d.creds),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Control: BindControl}).DialContext(ctx, "tcp", addr)
		}),
	)
	if err != nil {
		return nil, err
	}
	client := genv1.NewTunnelServiceClient(cc)

	// 用独立 context 承载流的生命周期——不继承拨号 ctx 的超时，
	// 否则带超时的拨号 ctx 会在超时后取消已建立的流。
	// 拨号阶段的超时由 grpc.DialContext(ctx, ...) 已控制。
	streamCtx, cancel := context.WithCancel(context.Background())
	stream, err := client.Stream(streamCtx)
	if err != nil {
		cancel()
		_ = cc.Close()
		return nil, err
	}

	conn := newStreamConn(
		stream,
		func() {
			cancel()
			_ = cc.Close()
		},
		stream.CloseSend, // client 侧 Close 时 CloseSend
		grpcAddr{"grpc-client"},
		grpcAddr{addr},
	)
	return conn, nil
}
