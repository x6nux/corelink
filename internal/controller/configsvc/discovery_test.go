package configsvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

type fakeDiscoveryStore struct {
	mappings []store.DiscoveredMapping
	route    *store.PublishedRoute
}

func (f *fakeDiscoveryStore) UpsertDiscoveredMapping(m *store.DiscoveredMapping) error {
	for i, existing := range f.mappings {
		if existing.RouteID == m.RouteID && existing.NodeID == m.NodeID && existing.TargetIP == m.TargetIP {
			m.ID = existing.ID
			f.mappings[i] = *m
			return nil
		}
	}
	m.ID = uint(len(f.mappings) + 1)
	f.mappings = append(f.mappings, *m)
	return nil
}

func (f *fakeDiscoveryStore) GetPublishedRoute(id uint) (*store.PublishedRoute, error) {
	if f.route != nil && f.route.ID == id {
		return f.route, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeDiscoveryStore) ListDiscoveredMappingsByRoute(routeID uint) ([]store.DiscoveredMapping, error) {
	var out []store.DiscoveredMapping
	for _, m := range f.mappings {
		if m.RouteID == routeID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeDiscoveryStore) SetDiscoveredMappingWinner(id uint, winner bool) error {
	for i := range f.mappings {
		if f.mappings[i].ID == id {
			f.mappings[i].Winner = winner
		}
	}
	return nil
}

type fakeDiscoveryNotify struct {
	called int
}

func (f *fakeDiscoveryNotify) RecomputeAndNotify(_ ...string) {
	f.called++
}

func TestDiscoveryReport(t *testing.T) {
	st := &fakeDiscoveryStore{
		route: &store.PublishedRoute{
			ID: 1, NodeID: "node-a", Kind: "discovered_mapping",
			VIPCIDR: "100.64.0.0/16", TargetCIDR: "10.1.0.0/16",
			Priority: 100, Enabled: true,
		},
	}
	notify := &fakeDiscoveryNotify{}
	h := NewDiscoveryHTTP(st, notify)

	report := discoveryReport{
		RouteID:    1,
		NodeID:     "node-a",
		TargetIP:   "10.1.0.8",
		VIPIP:      "100.64.0.8",
		ObservedAt: time.Now().Format(time.RFC3339),
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/v1/route-discovery/report", bytes.NewReader(body))
	req.TLS = fakeTLSState("node-a")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if notify.called != 1 {
		t.Fatalf("notify called = %d, want 1", notify.called)
	}
	if len(st.mappings) != 1 {
		t.Fatalf("mappings = %d, want 1", len(st.mappings))
	}
	if !st.mappings[0].Winner {
		t.Fatal("首条映射应为 winner")
	}
}

func TestDiscoveryReportWinnerSelection(t *testing.T) {
	st := &fakeDiscoveryStore{
		route: &store.PublishedRoute{
			ID: 1, NodeID: "node-a", Kind: "discovered_mapping",
			VIPCIDR: "100.64.0.0/16", TargetCIDR: "10.1.0.0/16",
			Priority: 100, Enabled: true,
		},
		mappings: []store.DiscoveredMapping{
			{ID: 1, RouteID: 1, NodeID: "node-a", TargetIP: "10.1.0.8", VIPIP: "100.64.0.8",
				Priority: 100, ObservedAt: time.Now().Add(-time.Minute), Winner: true},
		},
	}
	notify := &fakeDiscoveryNotify{}
	h := NewDiscoveryHTTP(st, notify)

	report := discoveryReport{
		RouteID:    1,
		NodeID:     "node-b",
		TargetIP:   "10.1.0.8",
		VIPIP:      "100.64.0.8",
		ObservedAt: time.Now().Format(time.RFC3339),
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/v1/route-discovery/report", bytes.NewReader(body))
	req.TLS = fakeTLSState("node-b")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	// 新上报（更新时间更新）应成为 winner
	var hasWinner bool
	for _, m := range st.mappings {
		if m.Winner && m.NodeID == "node-b" {
			hasWinner = true
		}
	}
	if !hasWinner {
		t.Fatal("node-b 应该成为 winner（更新的 observed_at）")
	}
}
