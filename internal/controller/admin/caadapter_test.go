package admin

import (
	"strings"
	"testing"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// newTestCAManager 用内存 store 构造一个真实 CA。
func newTestCAManager(t *testing.T) *ca.Manager {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mgr, err := ca.EnsureCA(st, "corelink-test-ca", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	return mgr
}

func TestCAAdapterCAPublicKeyHashMatchesSPKI(t *testing.T) {
	mgr := newTestCAManager(t)
	a := NewCAAdapter(mgr)
	got, err := a.CAPublicKeyHash()
	if err != nil {
		t.Fatalf("CAPublicKeyHash: %v", err)
	}
	want := tunnel.CASPKIHash(mgr.Cert())
	if got != want {
		t.Fatalf("CAPublicKeyHash=%q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("哈希应以 sha256: 前缀，得 %q", got)
	}
}
