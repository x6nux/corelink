package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// TestToKeyDTO 验证 toKeyDTO 转换所有字段
func TestToKeyDTO(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour)
	ek := &store.EnrollKey{
		Key:       "test-key-abc",
		Reusable:  true,
		Tag:       "dev",
		Revoked:   false,
		Consumed:  true,
		CreatedAt: now,
		ExpiresAt: &exp,
	}
	dto := toKeyDTO(ek)
	if dto.Key != "test-key-abc" {
		t.Errorf("key = %q, want %q", dto.Key, "test-key-abc")
	}
	if !dto.Reusable {
		t.Error("reusable 应为 true")
	}
	if dto.Tag != "dev" {
		t.Errorf("tag = %q, want %q", dto.Tag, "dev")
	}
	if !dto.Consumed {
		t.Error("consumed 应为 true")
	}
	if dto.ExpiresAt == nil {
		t.Fatal("expires_at 不应为 nil")
	}
}

// TestRandomEnrollKey 验证随机密钥生成
func TestRandomEnrollKey(t *testing.T) {
	key1, err := randomEnrollKey()
	if err != nil {
		t.Fatalf("randomEnrollKey: %v", err)
	}
	if len(key1) != 64 {
		t.Errorf("key 长度 = %d，期望 64", len(key1))
	}
	// 验证唯一性
	key2, err := randomEnrollKey()
	if err != nil {
		t.Fatalf("randomEnrollKey: %v", err)
	}
	if key1 == key2 {
		t.Error("两次生成的密钥不应相同")
	}
}

// TestHandleKeysRevoke_InvalidJSON 验证无效 JSON 参数返回错误
func TestHandleKeysRevoke_InvalidJSON(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleKeysRevoke(deps)
	_, err := h(json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}

// TestHandleKeysList_EmptyStore 验证空 store 返回空数组
func TestHandleKeysList_EmptyStore(t *testing.T) {
	ms := &mockStore{keys: []store.EnrollKey{}}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleKeysList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []keyDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空 store 应返回空数组，got %d", len(got))
	}
}
