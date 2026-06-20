package configsvc

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/x6nux/corelink/internal/controller/store"
)

// okPingDB 打开内存 SQLite 并返回底层 *sql.DB（可 PingContext）。
func okPingDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open 内存库失败: %v", err)
	}
	sqlDB, err := st.DB().DB()
	if err != nil {
		t.Fatalf("取底层 *sql.DB 失败: %v", err)
	}
	// 测试结束时关闭连接
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// TestHealthHandler 验证 /v1/health readiness 端点：
// - DB 可达 + fresh() 返回 true → 200，body 含 {"epoch":0}
// - fresh() 返回 false → 503
func TestHealthHandler(t *testing.T) {
	// db ok + tick 新鲜 → 200
	h := HealthHandler(okPingDB(t), func() bool { return true })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	body := rec.Body.String()
	if body != `{"epoch":0}` {
		t.Errorf("200 body 期望 {\"epoch\":0}，实际 %q", body)
	}

	// tick 陈旧 → 503
	h2 := HealthHandler(okPingDB(t), func() bool { return false })
	rec2 := httptest.NewRecorder()
	h2(rec2, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d want 503", rec2.Code)
	}
}
