package configsvc

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// NodeInfo 是 gRPC/WS handler 从 store 获取的最小节点信息。
type NodeInfo struct {
	Generation uint64
}

// NodeInfoGetter 是从 store 取节点信息的最小接口（被 ConfigGRPC 和 ConfigWS 使用）。
type NodeInfoGetter interface {
	GetNodeInfo(nodeID string) (*NodeInfo, error)
}

// ConfigGRPC 实现 genv1.ConfigServiceServer（主通知通道）。
//
// 节点通过 mTLS 连接，nodeID 从 peer 证书 CN 提取。
// 连接建立后立即推送当前 generation，之后持续监听 Notify 推送的变更信号直到流关闭。
type ConfigGRPC struct {
	genv1.UnimplementedConfigServiceServer
	notify *Notify
	getter NodeInfoGetter

	// epoch 指向 Services.epoch（共享原子值），读取当前 控制平面纪元。
	// nil 时退化为恒 0，保持向后兼容。
	epoch *atomic.Uint64
}

// NewConfigGRPC 构造 ConfigGRPC。
// getter 用于查询节点当前 generation。
// epoch 字段为 nil（退化恒 0，向后兼容），如需动态 epoch 请用 newConfigGRPCWithEpoch。
func NewConfigGRPC(n *Notify, getter NodeInfoGetter) *ConfigGRPC {
	return &ConfigGRPC{notify: n, getter: getter}
}

// newConfigGRPCWithEpoch 是 New() 使用的内部构造函数，注入共享 epoch 指针。
func newConfigGRPCWithEpoch(n *Notify, getter NodeInfoGetter, epoch *atomic.Uint64) *ConfigGRPC {
	return &ConfigGRPC{notify: n, getter: getter, epoch: epoch}
}

// loadEpoch 安全读取 epoch 值；epoch 指针为 nil 时返回 0（向后兼容）。
func (g *ConfigGRPC) loadEpoch() uint64 {
	if g.epoch == nil {
		return 0
	}
	return g.epoch.Load()
}

// WatchConfig 实现 ConfigService.WatchConfig 服务端流方法。
//
// 流程：
//  1. 从 gRPC peer 证书 CN 提取 nodeID。
//  2. 查询当前 generation，立即推送一次初始 ChangeSignal。
//  3. 订阅 Notify，将后续变更信号转发给客户端。
//  4. 流 context 取消（客户端断开）→ Unsubscribe，视为离线。
func (g *ConfigGRPC) WatchConfig(req *genv1.WatchRequest, stream genv1.ConfigService_WatchConfigServer) error {
	nodeID, err := NodeIDFromGRPCPeer(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "提取 nodeID 失败: %v", err)
	}

	// 查询当前 generation
	info, err := g.getter.GetNodeInfo(nodeID)
	if err != nil {
		slog.Warn("watch config: 查询节点信息失败", "node_id", nodeID, "err", err)
		return status.Error(codes.NotFound, "节点不存在")
	}

	// 推送初始信号（即使 client 已知当前 generation，也推一次表示"在线确认"）
	initSig := &genv1.ChangeSignal{
		Changed:    true,
		Generation: info.Generation,
		Epoch:      g.loadEpoch(), // S3-A3：动态读取，未注入时恒 0 向后兼容
	}
	if err := stream.Send(initSig); err != nil {
		return err
	}

	// 订阅变更通知
	sid, ch := g.notify.Subscribe(nodeID)
	defer g.notify.Unsubscribe(nodeID, sid)

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				// channel 被关闭（Notify.Close 触发）
				return nil
			}
			sig := &genv1.ChangeSignal{
				Changed:    msg.Changed,
				Generation: msg.Generation,
				Epoch:      g.loadEpoch(), // S3-A3：动态读取，未注入时恒 0 向后兼容
			}
			if err := stream.Send(sig); err != nil {
				return err
			}
		}
	}
}

// ─── nodeID 提取 helper ───────────────────────────────────────────────────────

// NodeIDFromGRPCPeer 从 gRPC stream context 的 peer 证书 CN 提取 nodeID。
func NodeIDFromGRPCPeer(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("context 中无 peer 信息")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", errors.New("peer AuthInfo 不是 TLS 信息")
	}
	return nodeIDFromTLSState(tlsInfo.State)
}

// NodeIDFromTLSCerts 从 TLS ConnectionState 提取 nodeID（HTTP mTLS 路径）。
func NodeIDFromTLSCerts(state *tls.ConnectionState) (string, error) {
	if state == nil {
		return "", errors.New("TLS 连接状态为 nil（非 TLS 连接）")
	}
	return nodeIDFromTLSState(*state)
}

// nodeIDFromTLSState 从 tls.ConnectionState 提取 nodeID（内部共享逻辑）。
func nodeIDFromTLSState(state tls.ConnectionState) (string, error) {
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("TLS 握手中无客户端证书")
	}
	cn := state.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", errors.New("peer 证书 CN 为空")
	}
	return cn, nil
}
