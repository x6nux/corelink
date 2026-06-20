// Package jointoken 实现 CoreLink 入网凭据 token 的编解码与校验。
// token = base64url(JSON{v,h,k,c})：k 承载授权（enrollment_key），
// c 承载认证（CA SPKI 哈希），二者自洽，无需签名。
package jointoken

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// JoinToken 入网凭据的结构化形式。
type JoinToken struct {
	V int    `json:"v"` // 版本，当前固定为 1
	H string `json:"h"` // controller 主机（host，不含端口）
	K string `json:"k"` // enrollment_key（hex 秘密）
	C string `json:"c"` // CA SPKI 哈希，形如 "sha256:<64hex>"
}

// Encode 将 JoinToken 序列化为 base64url（无填充）的 JSON 字符串。
func Encode(tk JoinToken) (string, error) {
	b, err := json.Marshal(tk)
	if err != nil {
		return "", fmt.Errorf("jointoken: 序列化失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode 解析 token：base64url → JSON → JoinToken，并做版本/字段非空/CA 哈希格式校验。
func Decode(s string) (JoinToken, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return JoinToken{}, fmt.Errorf("jointoken: 非法 base64url: %w", err)
	}
	var tk JoinToken
	if err := json.Unmarshal(raw, &tk); err != nil {
		return JoinToken{}, fmt.Errorf("jointoken: 非法 JSON: %w", err)
	}
	if tk.V != 1 {
		return JoinToken{}, fmt.Errorf("jointoken: 不支持的版本 %d（仅支持 1）", tk.V)
	}
	if tk.H == "" {
		return JoinToken{}, fmt.Errorf("jointoken: host 不能为空")
	}
	if tk.K == "" {
		return JoinToken{}, fmt.Errorf("jointoken: enrollment_key 不能为空")
	}
	if err := validateCAHash(tk.C); err != nil {
		return JoinToken{}, err
	}
	return tk, nil
}

// validateCAHash 校验 CA SPKI 哈希形如 "sha256:<64hex>"。
func validateCAHash(c string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(c, prefix) {
		return fmt.Errorf("jointoken: ca_hash 必须以 %q 开头", prefix)
	}
	hexPart := strings.TrimPrefix(c, prefix)
	b, err := hex.DecodeString(hexPart)
	if err != nil {
		return fmt.Errorf("jointoken: ca_hash 含非 hex 字符: %w", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("jointoken: ca_hash 解码后须为 32 字节（SHA-256），实际 %d", len(b))
	}
	return nil
}
