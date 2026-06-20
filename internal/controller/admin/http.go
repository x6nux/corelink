package admin

import (
	"encoding/json"
	"net/http"
)

// writeJSON 以 application/json 写出 v。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorResponse 是统一的错误响应体。
type errorResponse struct {
	Error string `json:"error"`
}

// writeError 写出 {"error": msg} 与给定状态码。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
