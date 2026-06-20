// Package enroll 实现 agent/relay 节点的注册客户端（S3-P1）。
//
// Enroll 流程（spec §5.1）：
//  1. 若 keystore 已有身份 → 跳过（幂等）。
//  2. EnsureWGKey（幂等）。
//  3. pki.GenerateCSR → CSR DER + ECDSA 私钥。
//  4. 构造外层 TLS：用 controller_ca_hash 做 CA 哈希钉扎（pinned 模式）验证 controller。
//  5. gRPC dial cfg.ControllerEnrollAddr（NewClient + credentials.NewTLS）。
//  6. 调 EnrollServiceClient.Enroll。
//  7. SaveIdentity（证书/私钥/CA/IP/nodeID）→ keystore。
package enroll

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/keystore"
	"github.com/x6nux/corelink/internal/pki"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// Enroll 注册节点：若 keystore 无身份则向 controller 注册，否则直接返回 nil（幂等）。
// cfg 必须已通过 Validate()；ks 的 DataDir 目录必须已存在。
func Enroll(ctx context.Context, cfg *config.Config, ks *keystore.KeyStore) error {
	// ① 幂等：已有身份直接跳过
	if ks.HasIdentity() {
		return nil
	}

	// ② 生成（或加载）WG 密钥（可选：新数据面不再需要 WG 密钥）
	var wgPub string
	if err := ks.EnsureWGKey(); err != nil {
		// WG 密钥生成失败不阻塞注册；新数据面不依赖 WG 密钥
		slog.Warn("enroll: EnsureWGKey 失败（非致命，新数据面不依赖 WG 密钥）", "err", err)
	} else {
		wgPub, _ = ks.WGPublicKey()
	}

	// ③ 生成 CSR + 节点私钥（CN 先用 hostname，注册后由 controller 分配真正 nodeID 并写入证书）
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	csrDER, nodeKey, err := pki.GenerateCSR(hostname)
	if err != nil {
		return fmt.Errorf("enroll: GenerateCSR 失败: %w", err)
	}

	// ④ 构造外层 TLS
	tlsOpts := buildTLSOptions(cfg)
	tlsCfg, err := tunnel.ClientTLSConfig(tlsOpts)
	if err != nil {
		return fmt.Errorf("enroll: 构造 TLS 配置失败: %w", err)
	}

	// ⑤ gRPC dial
	conn, err := grpc.NewClient(
		cfg.ControllerEnrollAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithContextDialer(tunnel.BypassGRPCDialer()),
	)
	if err != nil {
		return fmt.Errorf("enroll: gRPC dial %q 失败: %w", cfg.ControllerEnrollAddr, err)
	}
	defer conn.Close()

	// ⑥ 调 Enroll RPC
	client := genv1.NewEnrollServiceClient(conn)
	resp, err := client.Enroll(ctx, &genv1.EnrollRequest{
		EnrollmentKey: cfg.EnrollmentKey,
		WgPublicKey:   wgPub,
		CsrDer:        csrDER,
		Hostname:      hostname,
	})
	if err != nil {
		return fmt.Errorf("enroll: Enroll RPC 失败: %w", err)
	}

	// ⑦ 校验响应字段（#36）：证书非空且可解析、IP/nodeID 非空，确保不落地损坏身份。
	if len(resp.NodeCertDer) == 0 || len(resp.CaCertDer) == 0 {
		return fmt.Errorf("enroll: 响应证书为空（node=%d ca=%d）", len(resp.NodeCertDer), len(resp.CaCertDer))
	}
	if _, err := x509.ParseCertificate(resp.NodeCertDer); err != nil {
		return fmt.Errorf("enroll: 响应节点证书不可解析: %w", err)
	}
	if _, err := x509.ParseCertificate(resp.CaCertDer); err != nil {
		return fmt.Errorf("enroll: 响应 CA 证书不可解析: %w", err)
	}
	if resp.VirtualIp == "" || resp.NodeId == "" {
		return fmt.Errorf("enroll: 响应缺少 virtual_ip 或 node_id（ip=%q id=%q）", resp.VirtualIp, resp.NodeId)
	}

	// ⑧ 持久化身份
	if err := ks.SaveIdentity(resp.NodeCertDer, nodeKey, resp.CaCertDer, resp.VirtualIp, resp.NodeId); err != nil {
		return fmt.Errorf("enroll: 保存身份失败: %w", err)
	}
	return nil
}

// ─────────────────────── 内部辅助 ───────────────────────

// buildTLSOptions 根据 cfg 构造 TLSOptions：用 controller CA 哈希钉扎（pinned 模式）。
func buildTLSOptions(cfg *config.Config) *tunnel.TLSOptions {
	return &tunnel.TLSOptions{
		Mode:         tunnel.TLSModePinned,
		ServerName:   serverNameFromAddr(cfg.ControllerEnrollAddr),
		PinnedCAHash: cfg.ControllerCAHash,
	}
}

// serverNameFromAddr 从 "host:port" 格式的地址中提取 host 部分。
// 若解析失败则原样返回（gRPC 层会继续处理）。
func serverNameFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
