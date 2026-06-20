// Package admin 实现 CoreLink controller 的管理面 API（spec §9）：
// 管理员账号登录（密码 bcrypt + HMAC 会话 token）与 nodes/acl/keys/relays/certs
// 等资源的 REST handler。管理认证是独立于节点 mTLS 的安全边界。
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// 认证相关的导出错误。
var (
	ErrInvalidCredentials = errors.New("admin: 用户名或密码错误")
	ErrInvalidToken       = errors.New("admin: 无效的会话 token")
	ErrTokenExpired       = errors.New("admin: 会话 token 已过期")
)

// Authenticator 管理单个管理员账号的密码校验与 HMAC 会话 token 签发/校验。
//
// token 格式（轻量 JWT 风格，避免引入重 JWT 库）：
//
//	base64url(payloadJSON) "." base64url(HMAC-SHA256(payloadJSON, key))
//
// payload 为 {"u":<user>,"exp":<unix秒>}。校验时先比对 HMAC（恒定时间），
// 再检查未过期。
type Authenticator struct {
	user     string
	passHash []byte // bcrypt 哈希
	key      []byte // HMAC-SHA256 签名密钥
	ttl      time.Duration
}

// tokenPayload 是 token 内嵌的声明。
type tokenPayload struct {
	User string `json:"u"`
	Exp  int64  `json:"exp"` // Unix 秒
}

// NewAuthenticator 用已有 bcrypt 密码哈希构造（对应配置 AdminPassHash）。
func NewAuthenticator(user string, passHash, key []byte, ttl time.Duration) (*Authenticator, error) {
	if user == "" {
		return nil, errors.New("admin: 管理员用户名不能为空")
	}
	if len(passHash) == 0 {
		return nil, errors.New("admin: 密码哈希不能为空")
	}
	// 校验为合法 bcrypt 哈希：非法值（如配置里误填的短串）会被拒绝，
	// 避免后续按固定长度取前缀时越界 panic（合法 bcrypt 哈希固定 60 字节）。
	if _, err := bcrypt.Cost(passHash); err != nil {
		return nil, fmt.Errorf("admin: 非法的 bcrypt 密码哈希: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("admin: HMAC token 密钥不能为空")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Authenticator{user: user, passHash: passHash, key: key, ttl: ttl}, nil
}

// NewAuthenticatorFromPassword 用明文密码构造（首次启动场景，内部做 bcrypt 哈希）。
func NewAuthenticatorFromPassword(user, password string, key []byte, ttl time.Duration) (*Authenticator, error) {
	if password == "" {
		return nil, errors.New("admin: 管理员密码不能为空")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("admin: bcrypt 哈希失败: %w", err)
	}
	return NewAuthenticator(user, hash, key, ttl)
}

// PasswordHash 返回 bcrypt 哈希（供持久化/展示，敏感，谨慎使用）。
func (a *Authenticator) PasswordHash() []byte { return a.passHash }

// PassHashPrefix 返回 bcrypt 哈希前 n 字节的十六进制串，供日志展示。
// 对哈希长度做下界保护：当 n 超过实际长度时取全部，避免切片越界 panic。
func (a *Authenticator) PassHashPrefix(n int) string {
	if n > len(a.passHash) {
		n = len(a.passHash)
	}
	return fmt.Sprintf("%x", a.passHash[:n])
}

// Login 校验用户名/密码，成功则签发会话 token。
func (a *Authenticator) Login(user, password string) (string, error) {
	// 用户名恒定时间比较，避免泄露存在性时序。
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.user)) == 1
	err := bcrypt.CompareHashAndPassword(a.passHash, []byte(password))
	if !userOK || err != nil {
		return "", ErrInvalidCredentials
	}
	return a.signToken(a.user, time.Now().Add(a.ttl)), nil
}

// signToken 用给定过期时刻签发 token（导出给测试用以构造过期/伪造 token）。
func (a *Authenticator) signToken(user string, exp time.Time) string {
	payload, _ := json.Marshal(tokenPayload{User: user, Exp: exp.Unix()})
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sig := a.hmac(payloadB64)
	return payloadB64 + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// hmac 计算 payloadB64 的 HMAC-SHA256。
func (a *Authenticator) hmac(payloadB64 string) []byte {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payloadB64))
	return mac.Sum(nil)
}

// VerifyToken 校验 token 的签名与有效期，返回 token 中的用户名。
func (a *Authenticator) VerifyToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ErrInvalidToken
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	wantSig := a.hmac(parts[0])
	if !hmac.Equal(gotSig, wantSig) {
		return "", ErrInvalidToken
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	var p tokenPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		return "", ErrInvalidToken
	}
	if time.Now().Unix() >= p.Exp {
		return "", ErrTokenExpired
	}
	return p.User, nil
}

// ─── HTTP 中间件 ──────────────────────────────────────────────────────────────

type ctxKey int

const userCtxKey ctxKey = iota

// UserFromContext 从请求上下文取出已认证用户名（RequireAuth 注入）。
func UserFromContext(ctx context.Context) string {
	u, _ := ctx.Value(userCtxKey).(string)
	return u
}

// RequireAuth 校验 Authorization: Bearer <token>，失败返回 401。
// 成功则把用户名注入 context 并放行。
func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "缺少认证 token")
			return
		}
		user, err := a.VerifyToken(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "认证失败")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken 从 Authorization 头提取 Bearer token。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
