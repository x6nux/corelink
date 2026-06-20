package firewall

import (
	"context"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// FirewallManager 定义跨平台防火墙管理接口。
type FirewallManager interface {
	EnsureChains(ctx context.Context) error
	ApplyDNS(ctx context.Context, cfg *genv1.DNSConfig) error
	ApplyEgress(ctx context.Context, rules []*genv1.EgressRule) error
	Cleanup(ctx context.Context) error
}
