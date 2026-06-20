package configsvc

import (
	"database/sql"
	"net/http"
)

// HealthHandler 返回 readiness 探活 handler：DB 可达 + 后台 ticker 新鲜 → 200，否则 503。
// fresh 由调用方注入（比较 now-lastTickAt 是否在阈值内）。
// 阶段2 controller 在朝时 epoch 恒 0（水位基线），200 的 body 恒为 {"epoch":0}。
func HealthHandler(db *sql.DB, fresh func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := db.PingContext(r.Context()); err != nil || !fresh() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"epoch":0}`)) // controller 在朝恒 0，水位基线
	}
}
