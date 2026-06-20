package topostore

import (
	"reflect"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/controller/topology"
)

// newMemStore 开内存 sqlite 库并迁移（纯 Go，无 CGO）。
func newMemStore(t *testing.T) *TopoStore {
	t.Helper()
	s, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return New(s.DB())
}

// ---------- SaveQuality / LoadQuality ----------

func TestQualityRoundTrip(t *testing.T) {
	ts := newMemStore(t)
	now := time.Now().Truncate(time.Second)
	edges := []QualityEdgeRecord{
		{Src: "a", Dst: "b", Ingress: "e1", RTTms: 10, LossPermille: 1, UpdatedAt: now},
		{Src: "a", Dst: "c", Ingress: "e2", RTTms: 20, LossPermille: 2, UpdatedAt: now},
	}
	if err := ts.SaveQuality(edges); err != nil {
		t.Fatalf("SaveQuality: %v", err)
	}
	got, err := ts.LoadQuality()
	if err != nil {
		t.Fatalf("LoadQuality: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2", len(got))
	}
	// 排序保证：a/b/e1 在 a/c/e2 前。
	if got[0].Dst != "b" || got[1].Dst != "c" {
		t.Fatalf("not sorted: %+v", got)
	}
	if got[0].RTTms != 10 || got[0].LossPermille != 1 {
		t.Fatalf("value mismatch: %+v", got[0])
	}
}

func TestQualityUpsertOverwrite(t *testing.T) {
	ts := newMemStore(t)
	now := time.Now().Truncate(time.Second)
	if err := ts.SaveQuality([]QualityEdgeRecord{
		{Src: "a", Dst: "b", Ingress: "e1", RTTms: 10, LossPermille: 1, UpdatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	// 同主键再存，应覆盖。
	later := now.Add(time.Minute)
	if err := ts.SaveQuality([]QualityEdgeRecord{
		{Src: "a", Dst: "b", Ingress: "e1", RTTms: 99, LossPermille: 5, UpdatedAt: later},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadQuality()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1 (upsert)", len(got))
	}
	if got[0].RTTms != 99 || got[0].LossPermille != 5 {
		t.Fatalf("upsert did not overwrite: %+v", got[0])
	}
}

func TestQualityEmpty(t *testing.T) {
	ts := newMemStore(t)
	if err := ts.SaveQuality(nil); err != nil {
		t.Fatalf("SaveQuality(nil): %v", err)
	}
	got, err := ts.LoadQuality()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d edges, want 0", len(got))
	}
}

// ---------- SaveResult / LoadLatestResult ----------

func TestResultLatestVersion(t *testing.T) {
	ts := newMemStore(t)
	if err := ts.SaveResult(1, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveResult(3, []byte("v3")); err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveResult(2, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	ver, blob, ok, err := ts.LoadLatestResult()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if ver != 3 || string(blob) != "v3" {
		t.Fatalf("got ver=%d blob=%q, want 3/v3", ver, blob)
	}
}

func TestResultSameVersionIdempotent(t *testing.T) {
	ts := newMemStore(t)
	if err := ts.SaveResult(5, []byte("old")); err != nil {
		t.Fatal(err)
	}
	// 同版本再存，幂等更新 blob。
	if err := ts.SaveResult(5, []byte("new")); err != nil {
		t.Fatal(err)
	}
	ver, blob, ok, err := ts.LoadLatestResult()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || ver != 5 || string(blob) != "new" {
		t.Fatalf("got ver=%d blob=%q ok=%v, want 5/new/true", ver, blob, ok)
	}
}

func TestResultEmpty(t *testing.T) {
	ts := newMemStore(t)
	_, _, ok, err := ts.LoadLatestResult()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok=true on empty store, want false")
	}
}

// ---------- SaveIngressSets / LoadIngressSets ----------

func TestIngressRoundTrip(t *testing.T) {
	ts := newMemStore(t)
	now := time.Now().Truncate(time.Second)
	rows := []IngressRecord{
		{NodeID: "n2", Blob: []byte("b2"), UpdatedAt: now},
		{NodeID: "n1", Blob: []byte("b1"), UpdatedAt: now},
	}
	if err := ts.SaveIngressSets(rows); err != nil {
		t.Fatal(err)
	}
	got, err := ts.LoadIngressSets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	// 按 NodeID 升序。
	if got[0].NodeID != "n1" || got[1].NodeID != "n2" {
		t.Fatalf("not sorted: %+v", got)
	}
	if string(got[0].Blob) != "b1" {
		t.Fatalf("blob mismatch: %q", got[0].Blob)
	}
	// upsert 覆盖。
	if err := ts.SaveIngressSets([]IngressRecord{{NodeID: "n1", Blob: []byte("b1new"), UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.LoadIngressSets()
	if len(got) != 2 || string(got[0].Blob) != "b1new" {
		t.Fatalf("upsert failed: %+v", got)
	}
}

// ---------- PruneStale ----------

func TestPruneStale(t *testing.T) {
	ts := newMemStore(t)
	now := time.Now()
	edges := []QualityEdgeRecord{
		{Src: "a", Dst: "b", Ingress: "e1", RTTms: 1, UpdatedAt: now.Add(-5 * time.Minute)},  // 新
		{Src: "a", Dst: "c", Ingress: "e2", RTTms: 2, UpdatedAt: now.Add(-40 * time.Minute)}, // 旧（超 30min）
		{Src: "a", Dst: "d", Ingress: "e3", RTTms: 3, UpdatedAt: now.Add(-90 * time.Minute)}, // 旧
	}
	if err := ts.SaveQuality(edges); err != nil {
		t.Fatal(err)
	}
	n, err := ts.PruneStale(now, DefaultHardTTL)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	got, _ := ts.LoadQuality()
	if len(got) != 1 || got[0].Dst != "b" {
		t.Fatalf("after prune: %+v, want only a/b", got)
	}
}

// ---------- ClassifyStale ----------

func TestClassifyStale(t *testing.T) {
	now := time.Now()
	records := []QualityEdgeRecord{
		{Src: "a", Dst: "b", Ingress: "e1", UpdatedAt: now.Add(-5 * time.Minute)},  // fresh (<=10min)
		{Src: "a", Dst: "c", Ingress: "e2", UpdatedAt: now.Add(-10 * time.Minute)}, // 边界 fresh (==10min)
		{Src: "a", Dst: "d", Ingress: "e3", UpdatedAt: now.Add(-15 * time.Minute)}, // stale (>10min)
	}
	fresh, stale := ClassifyStale(records, now, DefaultStaleTTL)
	if len(fresh) != 2 {
		t.Fatalf("fresh=%d, want 2", len(fresh))
	}
	if len(stale) != 1 || stale[0].Dst != "d" {
		t.Fatalf("stale=%+v, want only d", stale)
	}
}

// ---------- MarshalResult / UnmarshalResult round-trip ----------

func TestMarshalResultRoundTrip(t *testing.T) {
	r := topology.Result{
		Version: 42,
		Roles: map[string]topology.Role{
			"a": topology.RoleTransit,
			"b": topology.RoleLeaf,
		},
		Neighbors: map[string][]topology.NeighborSpec{
			"a": {{NodeID: "b", Ingresses: []string{"e1", "e2"}}},
		},
		Baseline: map[topology.RoutePair][][]topology.Hop{
			{Src: "a", Dst: "c"}: {
				{{Node: "a", Ingress: "e1"}, {Node: "c", Ingress: "e3"}},
				{{Node: "a", Ingress: "e2"}, {Node: "c", Ingress: "e4"}},
			},
			{Src: "c", Dst: "a"}: {
				{{Node: "c", Ingress: "e3"}, {Node: "a", Ingress: "e1"}},
			},
		},
		ProbeSets: map[string][]topology.ProbeTarget{
			"a": {{NodeID: "c", IngressIDs: []string{"e3", "e4"}}},
		},
	}
	blob, err := MarshalResult(r)
	if err != nil {
		t.Fatalf("MarshalResult: %v", err)
	}
	got, err := UnmarshalResult(blob)
	if err != nil {
		t.Fatalf("UnmarshalResult: %v", err)
	}
	if !reflect.DeepEqual(r, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n  got %+v", r, got)
	}
}

func TestMarshalResultEmptyRoundTrip(t *testing.T) {
	// 空 Result（无 Baseline）也应 round-trip 一致。
	r := topology.Result{Version: 1}
	blob, err := MarshalResult(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalResult(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, got) {
		t.Fatalf("empty round-trip mismatch:\n want %+v\n  got %+v", r, got)
	}
}

// ---------- 端到端：MarshalResult → SaveResult → LoadLatestResult → UnmarshalResult ----------

func TestResultBlobEndToEnd(t *testing.T) {
	ts := newMemStore(t)
	r := topology.Result{
		Version: 7,
		Roles:   map[string]topology.Role{"a": topology.RoleTransit},
		Baseline: map[topology.RoutePair][][]topology.Hop{
			{Src: "a", Dst: "b"}: {{{Node: "a", Ingress: "e1"}}},
		},
	}
	blob, err := MarshalResult(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SaveResult(r.Version, blob); err != nil {
		t.Fatal(err)
	}
	ver, gotBlob, ok, err := ts.LoadLatestResult()
	if err != nil || !ok || ver != 7 {
		t.Fatalf("LoadLatestResult: ver=%d ok=%v err=%v", ver, ok, err)
	}
	got, err := UnmarshalResult(gotBlob)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, got) {
		t.Fatalf("end-to-end mismatch:\n want %+v\n  got %+v", r, got)
	}
}
