package admin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// 测试 errorResponse 序列化与 writeJSON 边界行为。

func TestErrorResponseJSONRoundTrip(t *testing.T) {
	// 验证 errorResponse 序列化 → 反序列化一致性。
	orig := errorResponse{Error: "测试错误消息"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded errorResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Error != orig.Error {
		t.Errorf("Error = %q, 期望 %q", decoded.Error, orig.Error)
	}
}

func TestWriteJSONNilBody(t *testing.T) {
	// writeJSON 传 nil body 时应写出 null。
	w := httptest.NewRecorder()
	writeJSON(w, 200, nil)
	if w.Code != 200 {
		t.Errorf("状态码 = %d, 期望 200", w.Code)
	}
	got := w.Body.String()
	if got != "null\n" {
		t.Errorf("body = %q, 期望 %q", got, "null\n")
	}
}

func TestWriteErrorFormatConsistency(t *testing.T) {
	// 验证 writeError 的输出格式始终为 {"error":"..."} 结构。
	tests := []struct {
		name string
		msg  string
	}{
		{"空消息", ""},
		{"ASCII 消息", "bad request"},
		{"中文消息", "参数格式错误"},
		{"含引号", `key "x" invalid`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, 400, tt.msg)
			var resp errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("反序列化失败: %v, body=%s", err, w.Body.String())
			}
			if resp.Error != tt.msg {
				t.Errorf("Error = %q, 期望 %q", resp.Error, tt.msg)
			}
		})
	}
}
