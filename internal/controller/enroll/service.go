// Package enroll 实现 gRPC EnrollService（§5.1/§5.2）。
package enroll

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

const (
	// 节点证书有效期（90 天）
	nodeCertTTL = 90 * 24 * time.Hour
)

// Notifier 节点注册后通知已有节点重拉配置（获取新 peer）。
type Notifier interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// Service 实现 genv1.EnrollServiceServer。
type Service struct {
	genv1.UnimplementedEnrollServiceServer
	st     *store.Store
	ca     *ca.Manager
	ipam   *ipam.Allocator
	notify Notifier // 可选，nil 时注册后不通知其他节点
	// CreateNodeFn 建节点记录的实现，默认 nil 时走 s.st.CreateNode；测试可覆盖以注入失败。
	CreateNodeFn func(*store.Node) error
}

// NewService 构造 EnrollService。
func NewService(st *store.Store, caM *ca.Manager, ipamA *ipam.Allocator, opts ...func(*Service)) *Service {
	s := &Service{st: st, ca: caM, ipam: ipamA}
	for _, o := range opts {
		o(s)
	}
	return s
}

// WithNotifier 设置注册后通知。
func WithNotifier(n Notifier) func(*Service) {
	return func(s *Service) { s.notify = n }
}

// SetNotify 延迟注入通知器（适用于循环依赖场景）。
func (s *Service) SetNotify(n Notifier) { s.notify = n }

// Enroll 节点注册：校验 enrollment key → 原子消费一次性 key → 分配 IP → 签发证书 → 建节点记录。
func (s *Service) Enroll(ctx context.Context, req *genv1.EnrollRequest) (*genv1.EnrollResponse, error) {
	// 1. 校验 enrollment key（存在/未吊销/未过期）。
	ek, err := s.st.GetEnrollKey(req.EnrollmentKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "enrollment key 不存在")
		}
		slog.Error("enroll: 查询 enrollment key 失败", "err", err)
		return nil, status.Error(codes.Internal, "内部错误")
	}
	if ek.Revoked {
		return nil, status.Errorf(codes.PermissionDenied, "enrollment key 已吊销")
	}
	// 一次性 key 已被消费 → 拒绝（与下方 ConsumeOneTimeKey 抢占双重保险，错误信息更清晰）。
	if !ek.Reusable && ek.Consumed {
		return nil, status.Errorf(codes.PermissionDenied, "enrollment key 已被使用")
	}
	if ek.ExpiresAt != nil && time.Now().After(*ek.ExpiresAt) {
		return nil, status.Errorf(codes.PermissionDenied, "enrollment key 已过期")
	}

	// 2. 一次性 key：在分配 IP / 签证书之前原子抢占，消除 TOCTOU 并发重放。
	//    抢占失败（已被其他并发请求消费）→ PermissionDenied，此时尚未建任何节点/分配 IP，无孤儿资源。
	//    可复用 key 仅校验过期/吊销，不消费。
	//    抢占成功后，若后续步骤失败需补偿 un-consume，避免一次性 key 被永久烧毁（bug #17）。
	consumedOneTime := false
	if !ek.Reusable {
		consumed, err := s.st.ConsumeOneTimeKey(req.EnrollmentKey)
		if err != nil {
			slog.Error("enroll: 消费一次性 key 失败", "err", err)
			return nil, status.Error(codes.Internal, "内部错误")
		}
		if !consumed {
			return nil, status.Errorf(codes.PermissionDenied, "enrollment key 已被使用")
		}
		consumedOneTime = true
	}

	// compensateKey 在后续步骤失败时撤销一次性 key 的抢占（bug #17）。
	compensateKey := func() {
		if consumedOneTime {
			if err := s.st.UnconsumeOneTimeKey(req.EnrollmentKey); err != nil {
				slog.Error("enroll: 补偿复位一次性 key 失败", "err", err)
			}
		}
	}

	// 3. 生成顺序数字 node_id（100, 101, ...）
	nodeID, err := s.st.NextNodeID()
	if err != nil {
		compensateKey()
		slog.Error("enroll: 生成 node_id 失败", "err", err)
		return nil, status.Error(codes.Internal, "内部错误")
	}

	// 4. 分配虚拟 IP
	virtualIP, err := s.ipam.Allocate(nodeID)
	if err != nil {
		compensateKey()
		if errors.Is(err, ipam.ErrExhausted) {
			return nil, status.Errorf(codes.ResourceExhausted, "虚拟 IP 地址空间已耗尽")
		}
		slog.Error("enroll: 分配 IP 失败", "err", err)
		return nil, status.Error(codes.Internal, "内部错误")
	}

	// 5. 角色统一为 "node"（不再区分 agent/relay）
	role := "node"

	// 6. 签发节点证书（client 证书：仅 ClientAuth、不复制 CSR SAN）
	certDER, err := s.ca.Issue(req.CsrDer, nodeID, role, nodeCertTTL)
	if err != nil {
		// 尽力释放 IP（忽略回收错误，不会污染主错误路径）并补偿一次性 key。
		_ = s.ipam.Release(virtualIP)
		compensateKey()
		slog.Error("enroll: 签发证书失败", "node_id", nodeID, "err", err)
		return nil, status.Error(codes.Internal, "签发证书失败")
	}

	// 解析刚签发证书的序列号，供后续步骤失败时补偿清理孤儿证书（bug #33）。
	var issuedSerial string
	if c, perr := x509.ParseCertificate(certDER); perr == nil {
		issuedSerial = c.SerialNumber.Text(10)
	}
	// cleanupCert 删除刚记录的证书：该证书从未交付节点，删除而非吊销，避免污染 CRL。
	cleanupCert := func() {
		if issuedSerial != "" {
			if derr := s.st.DeleteCert(issuedSerial); derr != nil {
				slog.Error("enroll: 补偿删除孤儿证书失败", "serial", issuedSerial, "err", derr)
			}
		}
	}

	// 7. 建节点记录
	// Name 默认 "Node-{ID}"
	nodeName := "Node-" + nodeID
	node := &store.Node{
		ID:        nodeID,
		Name:      nodeName,
		Role:      role,
		Hostname:  req.Hostname,
		WGPubKey:  req.WgPublicKey,
		VirtualIP: virtualIP,
		User:      ek.Tag,
	}
	createNode := s.st.CreateNode
	if s.CreateNodeFn != nil {
		createNode = s.CreateNodeFn
	}
	if err := createNode(node); err != nil {
		_ = s.ipam.Release(virtualIP)
		cleanupCert()   // 补偿：删除孤儿证书，避免留下无对应节点的有效证书（#33）
		compensateKey() // 补偿：复位一次性 key，避免永久烧毁（#17）
		slog.Error("enroll: 创建节点记录失败", "node_id", nodeID, "err", err)
		return nil, status.Error(codes.Internal, "内部错误")
	}

	// 通知全网重拉配置（含新节点自身：获取拓扑 + 全量指纹）
	if s.notify != nil {
		existing, _ := s.st.ListNodes()
		allIDs := make([]string, 0, len(existing))
		for _, n := range existing {
			allIDs = append(allIDs, n.ID)
		}
		// existing 在 CreateNode 之后查询，已包含新节点，无需额外 append
		if len(allIDs) > 0 {
			s.notify.RecomputeAndNotify(allIDs...)
		}
	}

	return &genv1.EnrollResponse{
		NodeId:      nodeID,
		VirtualIp:   virtualIP,
		NodeCertDer: certDER,
		CaCertDer:   s.ca.Cert().Raw,
	}, nil
}

// Renew 续签证书：要求 mTLS 已认证，从 peer 证书 CN 取 nodeID。
// 续签前校验旧证书是否已被吊销——已吊销则拒绝（封堵吊销节点自助续命，#5）。
func (s *Service) Renew(ctx context.Context, req *genv1.RenewRequest) (*genv1.EnrollResponse, error) {
	nodeID, oldSerial, role, err := nodeIDFromPeer(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "mTLS 认证失败: %v", err)
	}

	// 旧证书已吊销 → 拒绝续签。
	if oldSerial != "" {
		revoked, err := s.st.IsCertRevoked(oldSerial)
		if err != nil {
			slog.Error("enroll: 查询证书吊销状态失败", "node_id", nodeID, "serial", oldSerial, "err", err)
			return nil, status.Error(codes.Internal, "内部错误")
		}
		if revoked {
			return nil, status.Errorf(codes.PermissionDenied, "证书已吊销，拒绝续签")
		}
	}

	newCertDER, err := s.ca.Renew(oldSerial, req.CsrDer, nodeID, role, nodeCertTTL)
	if err != nil {
		slog.Error("enroll: 续签证书失败", "node_id", nodeID, "err", err)
		return nil, status.Error(codes.Internal, "续签证书失败")
	}

	return &genv1.EnrollResponse{
		NodeId:      nodeID,
		NodeCertDer: newCertDER,
		CaCertDer:   s.ca.Cert().Raw,
	}, nil
}

// ---------- 内部辅助 ----------

// nodeIDFromPeer 从 context 中提取 mTLS peer 证书，返回 (nodeID, oldSerial, role, error)。
func nodeIDFromPeer(ctx context.Context) (nodeID, oldSerial, role string, err error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", "", "", errors.New("context 中无 peer 信息")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		// 也支持直接注入 *tls.ConnectionState（测试用）
		return "", "", "", errors.New("peer AuthInfo 不是 TLS 信息")
	}
	state := tlsInfo.State
	return extractFromTLSState(state)
}

// extractFromTLSState 从 tls.ConnectionState 提取 nodeID/serial/role。
func extractFromTLSState(state tls.ConnectionState) (nodeID, oldSerial, role string, err error) {
	if len(state.PeerCertificates) == 0 {
		return "", "", "", errors.New("TLS 握手中无客户端证书")
	}
	cert := state.PeerCertificates[0]
	nodeID = cert.Subject.CommonName
	if nodeID == "" {
		return "", "", "", errors.New("peer 证书 CN 为空")
	}
	// 提取旧序列号和角色
	oldSerial = cert.SerialNumber.Text(10)
	role = certRole(cert)
	return nodeID, oldSerial, role, nil
}

// certRole 从证书 OU 提取角色。
func certRole(cert *x509.Certificate) string {
	for _, ou := range cert.Subject.OrganizationalUnit {
		if ou == "relay" {
			return "relay"
		}
	}
	return "agent"
}
