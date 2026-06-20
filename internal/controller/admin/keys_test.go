package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// 测试 createKeyRequest JSON 反序列化与 keyDTO JSON 标签。

func TestCreateKeyRequestDefaults(t *testing.T) {
	// 验证空 JSON 对象反序列化后各字段的零值。
	var req createKeyRequest
	if err := json.Unmarshal([]byte(`{}`), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.Reusable {
		t.Error("Reusable 默认应为 false")
	}
	if req.Tag != "" {
		t.Errorf("Tag 默认应为空, 实际 %q", req.Tag)
	}
	if req.TTLSeconds != 0 {
		t.Errorf("TTLSeconds 默认应为 0, 实际 %d", req.TTLSeconds)
	}
}

func TestCreateKeyRequestFull(t *testing.T) {
	// 验证完整 JSON 反序列化。
	raw := `{"reusable":true,"tag":"team-a","ttl_seconds":7200}`
	var req createKeyRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !req.Reusable {
		t.Error("Reusable 应为 true")
	}
	if req.Tag != "team-a" {
		t.Errorf("Tag = %q", req.Tag)
	}
	if req.TTLSeconds != 7200 {
		t.Errorf("TTLSeconds = %d", req.TTLSeconds)
	}
}

func TestKeyDTOExpiresAtOmitempty(t *testing.T) {
	// ExpiresAt 为 nil 时 JSON 不含 expires_at 键。
	dto := keyDTO{Key: "k1", CreatedAt: time.Now()}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["expires_at"]; ok {
		t.Error("ExpiresAt 为 nil 时不应出现在 JSON 中")
	}
}

func TestToKeyDTOPreservesAllFields(t *testing.T) {
	// 验证 toKeyDTO 不丢失任何字段。
	now := time.Now()
	exp := now.Add(time.Hour)
	ek := &store.EnrollKey{
		Key:       "test-key-full",
		Reusable:  true,
		Tag:       "ops",
		Revoked:   true,
		Consumed:  true,
		ExpiresAt: &exp,
		CreatedAt: now,
	}
	dto := toKeyDTO(ek)
	if dto.Key != ek.Key || dto.Reusable != ek.Reusable || dto.Tag != ek.Tag {
		t.Errorf("基本字段不匹配: %+v", dto)
	}
	if dto.Revoked != ek.Revoked || dto.Consumed != ek.Consumed {
		t.Errorf("状态字段不匹配: revoked=%v consumed=%v", dto.Revoked, dto.Consumed)
	}
	if dto.ExpiresAt == nil || !dto.ExpiresAt.Equal(exp) {
		t.Error("ExpiresAt 不匹配")
	}
	if !dto.CreatedAt.Equal(now) {
		t.Error("CreatedAt 不匹配")
	}
}
