package server

import (
	"sync"
	"time"
)

// crlSource 拉取当前 CRL DER（通常是 *ca.Manager.CurrentCRL）。
type crlSource func(validFor time.Duration) ([]byte, error)

// CRLCache 缓存 CRL DER 并按 TTL 刷新，避免每请求重算/重解析（mTLS 热路径）。
type CRLCache struct {
	src   crlSource
	ttl   time.Duration
	clock func() time.Time

	mu       sync.Mutex
	cached   []byte
	cachedAt time.Time
	hasValue bool
}

// NewCRLCache 构造 CRL 缓存。ttl 为缓存有效期；validFor 透传给底层取 CRL 的有效期。
func NewCRLCache(src crlSource, ttl time.Duration) *CRLCache {
	return &CRLCache{src: src, ttl: ttl, clock: time.Now}
}

// SetClock 注入时钟（测试用）。
func (c *CRLCache) SetClock(clock func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clock = clock
}

// Get 返回当前 CRL DER：TTL 内复用缓存，过期则重新拉取并刷新。
func (c *CRLCache) Get() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if c.hasValue && now.Sub(c.cachedAt) < c.ttl {
		return c.cached, nil
	}
	// 取 CRL 有效期取 2 倍 TTL，保证 NextUpdate 覆盖缓存窗口。
	der, err := c.src(2 * c.ttl)
	if err != nil {
		return nil, err
	}
	c.cached = der
	c.cachedAt = now
	c.hasValue = true
	return der, nil
}
