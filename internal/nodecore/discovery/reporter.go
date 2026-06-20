package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// Report 是上报给 controller 的发现结果。
type Report struct {
	RouteID    uint   `json:"route_id"`
	NodeID     string `json:"node_id"`
	TargetIP   string `json:"target_ip"`
	VIPIP      string `json:"vip_ip"`
	ObservedAt string `json:"observed_at"`
}

// NeighborSource 提供邻居表数据的接口（便于测试注入 fake）。
type NeighborSource interface {
	IPNeigh(ctx context.Context) (string, error)
}

// HTTPReporter 通过 mTLS HTTP 上报发现结果到 controller。
type HTTPReporter struct {
	client  *http.Client
	baseURL string
	nodeID  string
}

// NewHTTPReporter 创建 HTTP reporter。
func NewHTTPReporter(client *http.Client, baseURL, nodeID string) *HTTPReporter {
	return &HTTPReporter{client: client, baseURL: baseURL, nodeID: nodeID}
}

// Submit 上报一条发现结果。
func (r *HTTPReporter) Submit(ctx context.Context, report Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/route-discovery/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery report: status %d", resp.StatusCode)
	}
	return nil
}

// RunDiscoveryLoop 运行发现循环：定期扫描邻居表并上报。
func RunDiscoveryLoop(ctx context.Context, configs []*genv1.DiscoveryConfig, source NeighborSource, reporter *HTTPReporter) {
	if len(configs) == 0 {
		return
	}

	for _, cfg := range configs {
		interval := time.Duration(cfg.IntervalSeconds) * time.Second
		if interval == 0 {
			interval = 30 * time.Second
		}
		go runSingleDiscovery(ctx, cfg, source, reporter, interval)
	}
}

func runSingleDiscovery(ctx context.Context, cfg *genv1.DiscoveryConfig, source NeighborSource, reporter *HTTPReporter, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doDiscoveryScan(ctx, cfg, source, reporter)
		}
	}
}

func doDiscoveryScan(ctx context.Context, cfg *genv1.DiscoveryConfig, source NeighborSource, reporter *HTTPReporter) {
	output, err := source.IPNeigh(ctx)
	if err != nil {
		slog.Debug("discovery scan: ip neigh failed", "err", err)
		return
	}

	entries := ParseIPNeigh(output)
	reachable := FilterReachable(entries)
	inRange := FilterByCIDR(reachable, cfg.TargetCidr)

	for _, entry := range inRange {
		vipIP, err := MapToVIP(entry.IP.String(), cfg.TargetCidr, cfg.VipPoolCidr)
		if err != nil {
			slog.Debug("discovery scan: map to vip failed", "target", entry.IP, "err", err)
			continue
		}

		report := Report{
			RouteID:    uint(cfg.RouteId),
			NodeID:     reporter.nodeID,
			TargetIP:   entry.IP.String(),
			VIPIP:      vipIP,
			ObservedAt: time.Now().Format(time.RFC3339),
		}
		if err := reporter.Submit(ctx, report); err != nil {
			slog.Debug("discovery scan: submit failed", "target", entry.IP, "err", err)
		}
	}
}
