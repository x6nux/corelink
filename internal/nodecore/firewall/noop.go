package firewall

import (
	"context"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// NoopManager 是空操作防火墙管理器（测试和非 Linux 平台使用）。
type NoopManager struct{}

var _ FirewallManager = (*NoopManager)(nil)

func (m *NoopManager) EnsureChains(_ context.Context) error                       { return nil }
func (m *NoopManager) ApplyDNS(_ context.Context, _ *genv1.DNSConfig) error       { return nil }
func (m *NoopManager) ApplyEgress(_ context.Context, _ []*genv1.EgressRule) error { return nil }
func (m *NoopManager) Cleanup(_ context.Context) error                            { return nil }
