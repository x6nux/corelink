package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

type fakeNeighborSource struct {
	output string
}

func (f *fakeNeighborSource) IPNeigh(_ context.Context) (string, error) {
	return f.output, nil
}

func TestDoDiscoveryScan(t *testing.T) {
	var reports []Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	source := &fakeNeighborSource{
		output: "10.1.0.8 dev eth0 lladdr aa:bb:cc:dd:ee:08 REACHABLE\n10.1.0.9 dev eth0 lladdr aa:bb:cc:dd:ee:09 STALE\n",
	}
	reporter := NewHTTPReporter(srv.Client(), srv.URL, "node-a")

	cfg := &genv1.DiscoveryConfig{
		RouteId:     1,
		TargetCidr:  "10.1.0.0/24",
		VipPoolCidr: "100.64.0.0/24",
		Mode:        "arp",
	}

	doDiscoveryScan(context.Background(), cfg, source, reporter)

	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(reports))
	}

	found8, found9 := false, false
	for _, r := range reports {
		if r.TargetIP == "10.1.0.8" && r.VIPIP == "100.64.0.8" {
			found8 = true
		}
		if r.TargetIP == "10.1.0.9" && r.VIPIP == "100.64.0.9" {
			found9 = true
		}
	}
	if !found8 || !found9 {
		t.Fatalf("应发现 10.1.0.8→100.64.0.8 和 10.1.0.9→100.64.0.9, got %+v", reports)
	}
}

func TestRunDiscoveryLoopCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &fakeNeighborSource{output: ""}
	reporter := NewHTTPReporter(http.DefaultClient, "http://localhost:0", "node-a")

	cfg := &genv1.DiscoveryConfig{
		RouteId:         1,
		TargetCidr:      "10.1.0.0/24",
		VipPoolCidr:     "100.64.0.0/24",
		IntervalSeconds: 1,
	}

	done := make(chan struct{})
	go func() {
		RunDiscoveryLoop(ctx, []*genv1.DiscoveryConfig{cfg}, source, reporter)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	// RunDiscoveryLoop 启动的是 goroutine，主函数已返回
	<-done
}
