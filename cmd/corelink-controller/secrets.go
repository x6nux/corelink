package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// ensureControllerSecrets 检查配置文件中的密钥字段，缺失的自动生成并回写。
//
// 检查项（仅密钥，不含密码——密码存 DB）：
//   - CAEncKey：32 字节 AES-256 密钥（加密 DB 中 CA 私钥）
//   - AdminTokenKey：32 字节 HMAC-SHA256 密钥（签名管理员会话 token）
//
// 用 map[string]any 读写 JSON，保留未知字段不丢失。
func ensureControllerSecrets(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置: %w", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析配置: %w", err)
	}

	changed := false

	// 清理遗留的 CAEncKey（已迁移到 DB）
	if _, ok := cfg["CAEncKey"]; ok {
		delete(cfg, "CAEncKey")
		changed = true
		fmt.Println("  ✓ 已移除配置中的 CAEncKey（已存储在数据库中）")
	}

	// AdminTokenKey：base64 编码的 32 字节
	if !hasNonEmptyBytes(cfg, "AdminTokenKey") {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("生成 AdminTokenKey: %w", err)
		}
		cfg["AdminTokenKey"] = base64.StdEncoding.EncodeToString(key)
		changed = true
		fmt.Println("  ✓ 已生成 AdminTokenKey（会话签名密钥）")
	}

	// 清理遗留的明文密码字段（密码已迁移到 DB）
	if _, ok := cfg["AdminPassword"]; ok {
		delete(cfg, "AdminPassword")
		changed = true
		fmt.Println("  ✓ 已移除配置中的明文密码（密码已存储在数据库中）")
	}
	if _, ok := cfg["AdminPassHash"]; ok {
		delete(cfg, "AdminPassHash")
		changed = true
		fmt.Println("  ✓ 已移除配置中的密码哈希（密码已存储在数据库中）")
	}

	if !changed {
		return nil
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0600); err != nil {
		return fmt.Errorf("回写配置: %w", err)
	}
	return nil
}

// hasNonEmptyBytes 检查 map 中某个字段是否有非空的字节值。
func hasNonEmptyBytes(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case string:
		return val != ""
	case []any:
		return len(val) > 0
	}
	return false
}
