// Package sync 实现配置同步客户端（S3-P2）。
//
// Client 使用 keystore 节点证书构造 mTLS 连接 controller，通过以下三通道保持配置最新：
//
//  1. 通知（主）：gRPC WatchConfig 服务端流，收 ChangeSignal。
//  2. 通知（备）：WebSocket JSON 信号（备用，主断自动切换）。
//  3. HTTP 拉取：收到版本 > 本地的信号 → GET /v1/config（mTLS）→ 解析 NodeConfig → OnConfig 回调。
//
// (epoch, generation) 字典序幂等保证：收到 ≤ 本地版本的信号忽略；拉取成功后更新本地 (epoch, generation)。
package sync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	stdsync "sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"

	version "github.com/x6nux/corelink/internal/version"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// Config 是 sync.Client 的初始化参数。
type Config struct {
	// GRPCAddr 是 controller mTLS gRPC 地址（如 "127.0.0.1:7443"）。
	GRPCAddr string
	// WSAddr 是 controller WebSocket 地址（如 "127.0.0.1:7443"）。
	WSAddr string
	// WSPath 是 WebSocket 端点路径（如 "/v1/watch"）。
	WSPath string
	// HTTPAddr 是 controller mTLS HTTP 基础 URL（如 "https://127.0.0.1:7443"），含协议前缀。
	HTTPAddr string
	// TLSConfig 是 mTLS 客户端配置（已含节点证书 + CA，RootCAs）。
	TLSConfig *tls.Config
	// FailoverCfg 主备切换参数；零值使用默认值。
	FailoverCfg failoverConfig

	// 以下字段仅供同包测试注入假通道工厂（零值时使用真实 gRPC/WS 实现）。
	primaryFn   channelFactory
	secondaryFn channelFactory
}

// Client 是配置同步客户端。
type Client struct {
	cfg         Config
	localGen    atomic.Uint64           // 本地已知 generation（原子）
	localEpoch  atomic.Uint64           // 本地已知 epoch（原子，阶段1恒0）
	OnConfig    func(*genv1.NodeConfig) // 收到新配置时回调（Run 中串行调用）
	OnReconnect func()                  // 重连成功时回调（用于 flush 缓存上报数据）
	httpCli     *http.Client

	fmMu stdsync.Mutex
	fm   *failoverManager
}

// NewClient 构造 Client。
// cfg.TLSConfig 必须包含客户端证书（mTLS）和 CA（RootCAs）。
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:     cfg,
		httpCli: &http.Client{Transport: tunnel.BypassTransport(cfg.TLSConfig), Timeout: 30 * time.Second},
	}
}

// SetLocalGeneration 设置初始已知 generation（epoch 置 0）。
//
// Deprecated: 阶段2 起 controller 可能下发非零 epoch，仅设 generation 会在重启恢复时
// 误判版本。请改用 SetLocalVersion 传入完整 (epoch, generation)。
func (c *Client) SetLocalGeneration(gen uint64) {
	c.SetLocalVersion(version.ConfigVersion{Generation: gen})
}

// SetLocalVersion 设置初始已知 (epoch, generation) 版本（可选，供恢复后跳过不必要拉取）。
func (c *Client) SetLocalVersion(v version.ConfigVersion) {
	c.setLocalVersion(v)
}

// LocalGeneration 返回当前本地 generation。
func (c *Client) LocalGeneration() uint64 {
	return c.localGen.Load()
}

// localVersion 返回当前本地 (epoch, generation) 版本号。
// 注意：epoch 与 generation 是两次独立 atomic.Load，非整体原子读；
// 阶段1 epoch 恒 0 无害，阶段2 如需强一致可改为单个 atomic 打包（epoch<<32 | generation）。
func (c *Client) localVersion() version.ConfigVersion {
	return version.ConfigVersion{Epoch: c.localEpoch.Load(), Generation: c.localGen.Load()}
}

// setLocalVersion 原子更新本地 (epoch, generation) 版本号。
func (c *Client) setLocalVersion(v version.ConfigVersion) {
	c.localEpoch.Store(v.Epoch)
	c.localGen.Store(v.Generation)
}

// shouldFetch 判断来自通知信号的版本是否比本地版本更新，需要触发拉取。
func (c *Client) shouldFetch(sig version.ConfigVersion) bool {
	return c.localVersion().Less(sig)
}

// Run 启动配置同步主循环（阻塞直到 ctx 取消）。
// OnConfig 回调在同一 goroutine 中被调用（串行，调用方不需要加锁）。
func (c *Client) Run(ctx context.Context) {
	// 构造 failoverConfig（使用调用方配置或默认值）
	fCfg := c.cfg.FailoverCfg
	if fCfg.retryInterval == 0 {
		fCfg = defaultFailoverConfig()
	}

	// 工厂函数：gRPC 主通道（默认使用真实 gRPC；测试可通过 cfg.primaryFn 注入）
	primaryFn := c.cfg.primaryFn
	if primaryFn == nil {
		primaryFn = func(ctx context.Context) (notifChannel, error) {
			return dialGRPCChannel(ctx, c.cfg.GRPCAddr, c.cfg.TLSConfig, c.localVersion())
		}
	}

	// 工厂函数：WS 备通道（默认使用真实 WS；测试可通过 cfg.secondaryFn 注入）
	secondaryFn := c.cfg.secondaryFn
	if secondaryFn == nil {
		secondaryFn = func(ctx context.Context) (notifChannel, error) {
			return dialWSChannel(ctx, c.cfg.WSAddr, c.cfg.WSPath, c.cfg.TLSConfig)
		}
	}

	fm := newFailoverManager(primaryFn, secondaryFn, fCfg)

	// 存引用供 AddEndpoint 运行期注入
	c.fmMu.Lock()
	c.fm = fm
	c.fmMu.Unlock()

	// 后台运行 failover 管理器
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go fm.Run(runCtx)

	// 消费统一信号流
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-fm.Signals():
			if !ok {
				return
			}
			c.handleSignal(ctx, sig)
		}
	}
}

// handleSignal 处理一个通知信号：(epoch, generation) 幂等检查 + HTTP 拉取 + 回调。
func (c *Client) handleSignal(ctx context.Context, sig Signal) {
	if ctx.Err() != nil {
		return
	}
	if !sig.Changed {
		return
	}
	sigVer := version.ConfigVersion{Epoch: sig.Epoch, Generation: sig.Generation}
	if !sigVer.IsZero() && !c.shouldFetch(sigVer) {
		// 已知版本或更旧，忽略
		slog.Debug("sync: 忽略过时信号", "sig_epoch", sig.Epoch, "sig_gen", sig.Generation)
		return
	}
	// 拉取完整配置
	lv := c.localVersion()
	fetched, err := c.fetchConfig(ctx, lv)
	if err != nil {
		slog.Warn("sync: 拉取配置失败", "err", err)
		return
	}
	if fetched == nil {
		// 304 Not Modified
		return
	}
	// (epoch, generation) 幂等（HTTP 返回的版本也要检查）
	fetchedVer := version.ConfigVersion{Epoch: fetched.Epoch, Generation: fetched.Generation}
	if !c.localVersion().Less(fetchedVer) {
		slog.Debug("sync: 拉取结果已过时", "cfg_epoch", fetched.Epoch, "cfg_gen", fetched.Generation)
		return
	}
	c.setLocalVersion(fetchedVer)
	// 配置拉取成功：触发 flush 回调（有缓存上报数据就上报，没有就跳过）
	if c.OnReconnect != nil {
		c.OnReconnect()
	}
	if c.OnConfig != nil {
		c.OnConfig(fetched)
	}
}

// fetchConfig 向 controller HTTP 接口拉取完整配置。
// lv 非零时附带 generation 和 epoch 参数；304 返回 (nil, nil)；200 返回 (*NodeConfig, nil)。
func (c *Client) fetchConfig(ctx context.Context, lv version.ConfigVersion) (*genv1.NodeConfig, error) {
	url := c.cfg.HTTPAddr + "/v1/config"
	if !lv.IsZero() {
		url += fmt.Sprintf("?epoch=%d&generation=%d", lv.Epoch, lv.Generation)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("sync: 构造 HTTP 请求失败: %w", err)
	}

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sync: HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, nil
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("sync: 读响应体失败: %w", err)
		}
		var nc genv1.NodeConfig
		if err := protojson.Unmarshal(body, &nc); err != nil {
			return nil, fmt.Errorf("sync: 解析 NodeConfig 失败: %w", err)
		}
		return &nc, nil
	default:
		return nil, fmt.Errorf("sync: HTTP 状态码 %d", resp.StatusCode)
	}
}

// ─────────────────────── gRPC 通道实现 ───────────────────────

type grpcChannel struct {
	stream genv1.ConfigService_WatchConfigClient
	conn   *grpc.ClientConn
}

func dialGRPCChannel(ctx context.Context, addr string, tlsCfg *tls.Config, lv version.ConfigVersion) (notifChannel, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithContextDialer(tunnel.BypassGRPCDialer()),
	)
	if err != nil {
		return nil, fmt.Errorf("sync: gRPC dial %q 失败: %w", addr, err)
	}

	client := genv1.NewConfigServiceClient(conn)
	stream, err := client.WatchConfig(ctx, &genv1.WatchRequest{KnownGeneration: lv.Generation, KnownEpoch: lv.Epoch})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("sync: WatchConfig 失败: %w", err)
	}
	return &grpcChannel{stream: stream, conn: conn}, nil
}

func (g *grpcChannel) Recv(_ context.Context) (Signal, error) {
	sig, err := g.stream.Recv()
	if err != nil {
		return Signal{}, err
	}
	return Signal{Changed: sig.Changed, Generation: sig.Generation, Epoch: sig.Epoch}, nil
}

func (g *grpcChannel) Close() {
	g.conn.Close()
}

// ─────────────────────── WS 通道实现 ───────────────────────

// wsSignalJSON 是 WebSocket JSON 信号体（与 controller ws.go 的 wsSignal 对齐）。
type wsSignalJSON struct {
	Changed    bool   `json:"changed"`
	Generation uint64 `json:"generation"`
	Epoch      uint64 `json:"epoch"`
}

type wsChannel struct {
	conn *websocket.Conn
}

func dialWSChannel(ctx context.Context, addr, path string, tlsCfg *tls.Config) (notifChannel, error) {
	if path == "" {
		path = "/v1/watch"
	}
	scheme := "wss"
	if tlsCfg == nil {
		scheme = "ws"
	}
	url := scheme + "://" + addr + path

	opts := &websocket.DialOptions{}
	if tlsCfg != nil {
		opts.HTTPClient = &http.Client{
			Transport: tunnel.BypassTransport(tlsCfg),
		}
	}

	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, fmt.Errorf("sync: WS dial %q 失败: %w", url, err)
	}
	return &wsChannel{conn: conn}, nil
}

func (w *wsChannel) Recv(ctx context.Context) (Signal, error) {
	var sig wsSignalJSON
	if err := wsjson.Read(ctx, w.conn, &sig); err != nil {
		return Signal{}, err
	}
	return Signal{Changed: sig.Changed, Generation: sig.Generation, Epoch: sig.Epoch}, nil
}

func (w *wsChannel) Close() {
	w.conn.CloseNow()
}

// ─────────────────────── mTLS 构造辅助 ───────────────────────

// BuildMTLSConfig 从 PEM 字节构造 mTLS tls.Config（客户端用）。
// certPEM/keyPEM 是节点证书+私钥；caPEM 是 CA 证书（用于验证 server）。
// serverName 是 controller 主机名/IP（用于 SNI 验证）。
func BuildMTLSConfig(certPEM, keyPEM, caPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("sync: 加载节点证书/私钥失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("sync: 解析 CA 证书失败（PEM 无效）")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// AddEndpoint 运行期向 failover 管理器注入新端点。
// Run 未启动时忽略（端点会在 Run 启动后由 AddEndpoint 动态注入）。
func (c *Client) AddEndpoint(ep EndpointConfig) {
	c.fmMu.Lock()
	fm := c.fm
	c.fmMu.Unlock()
	if fm != nil {
		fm.AddEndpoint(ep)
	}
}
