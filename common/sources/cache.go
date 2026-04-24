package sources

import (
	"context"
	"sync"
	"time"
)

// Cache is the storage backend used by CachedSource for market lists.
// Implementations live in common/database/{redis,mongo,…}; an in-memory
// fallback ships in this package.
type Cache interface {
	Get(ctx context.Context, key string) ([]Market, bool, error)
	Set(ctx context.Context, key string, value []Market, ttl time.Duration) error
}

// OrderCache is the parallel storage backend used for per-user order
// listings (active loans, positions, historical activity). Keyed by
// (source, address, limit) upstream.
type OrderCache interface {
	Get(ctx context.Context, key string) ([]Order, bool, error)
	Set(ctx context.Context, key string, value []Order, ttl time.Duration) error
}

// CachedSource wraps a Source so FetchMarkets returns from the cache when
// fresh. A singleflight-style lock ensures concurrent callers during a
// refresh share one upstream call instead of stampeding it.
//
// FetchOrders is also cached when an orderCache is configured — keyed by
// (source, address, limit). Different address/limit combinations live in
// separate cache entries.
//
// Errors are not cached: a failed refresh returns the error to all waiters
// and the next call retries fresh.
type CachedSource struct {
	inner Source
	cache Cache
	ttl   time.Duration
	key   string

	orderCache OrderCache
	orderTTL   time.Duration

	mu      sync.Mutex
	pending *pendingFetch
}

type pendingFetch struct {
	done    chan struct{}
	markets []Market
	err     error
}

// NewCachedSource composes a Source with a Cache and a per-entry TTL. A
// non-positive ttl disables caching (every call hits the upstream).
func NewCachedSource(inner Source, cache Cache, ttl time.Duration) *CachedSource {
	return &CachedSource{
		inner: inner,
		cache: cache,
		ttl:   ttl,
		key:   "markets:" + inner.Name(),
	}
}

// WithOrderCache wires an OrderCache + TTL for per-user position caching.
// A non-positive ttl disables the order cache. Returns the same pointer so
// callers can chain:
//
//	sources.NewCachedSource(s, c, ttl).WithOrderCache(oc, 30*time.Second)
func (c *CachedSource) WithOrderCache(oc OrderCache, ttl time.Duration) *CachedSource {
	c.orderCache = oc
	c.orderTTL = ttl
	return c
}

func (c *CachedSource) Name() string { return c.inner.Name() }

func (c *CachedSource) FetchMarkets(ctx context.Context) ([]Market, error) {
	if c.ttl <= 0 || c.cache == nil {
		return c.inner.FetchMarkets(ctx)
	}

	// 1. Cache hit?
	if v, ok, err := c.cache.Get(ctx, c.key); err == nil && ok {
		return v, nil
	}

	// 2. Singleflight: only one fetch at a time per source.
	c.mu.Lock()
	if c.pending != nil {
		p := c.pending
		c.mu.Unlock()
		select {
		case <-p.done:
			return p.markets, p.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := &pendingFetch{done: make(chan struct{})}
	c.pending = p
	c.mu.Unlock()

	v, err := c.inner.FetchMarkets(ctx)

	c.mu.Lock()
	p.markets = v
	p.err = err
	c.pending = nil
	c.mu.Unlock()
	close(p.done)

	if err == nil {
		// Best-effort cache write — failure here is non-fatal.
		_ = c.cache.Set(ctx, c.key, v, c.ttl)
	}
	return v, err
}

// FetchOrders caches per (source, address, limit) when an OrderCache is
// configured. Zero TTL or missing cache passes through to the upstream.
// When q.Refresh is true we bypass the cache read AND overwrite the
// cache entry on success — useful right after a tx submit when the
// frontend wants to see the new position without waiting for the TTL.
func (c *CachedSource) FetchOrders(ctx context.Context, q OrderQuery) ([]Order, error) {
	if c.orderCache == nil || c.orderTTL <= 0 {
		return c.inner.FetchOrders(ctx, q)
	}
	key := orderKey(c.inner.Name(), q)
	if !q.Refresh {
		if v, ok, err := c.orderCache.Get(ctx, key); err == nil && ok {
			return v, nil
		}
	}
	v, err := c.inner.FetchOrders(ctx, q)
	if err != nil {
		return nil, err
	}
	_ = c.orderCache.Set(ctx, key, v, c.orderTTL)
	return v, nil
}

func orderKey(source string, q OrderQuery) string {
	return "orders:" + source + ":" + q.Address + ":" + itoa(q.Limit)
}

// --- TxBuilder delegation ---------------------------------------------------
// CachedSource passes Build* calls straight through to the inner source so
// the API handler can type-assert through the cache wrapper.

func (c *CachedSource) BuildSupply(ctx context.Context, p TxParams) (*BuiltTx, error) {
	if b, ok := c.inner.(TxBuilder); ok {
		return b.BuildSupply(ctx, p)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (c *CachedSource) BuildWithdraw(ctx context.Context, p TxParams) (*BuiltTx, error) {
	if b, ok := c.inner.(TxBuilder); ok {
		return b.BuildWithdraw(ctx, p)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (c *CachedSource) BuildBorrow(ctx context.Context, p TxParams) (*BuiltTx, error) {
	if b, ok := c.inner.(TxBuilder); ok {
		return b.BuildBorrow(ctx, p)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (c *CachedSource) SubmitTx(ctx context.Context, p SubmitParams) (string, error) {
	if b, ok := c.inner.(TxBuilder); ok {
		return b.SubmitTx(ctx, p)
	}
	return "", ErrUnsupportedTxBuilder
}

func (c *CachedSource) BuildClose(ctx context.Context, p TxCloseParams) (*BuiltTx, error) {
	if b, ok := c.inner.(TxBuilder); ok {
		return b.BuildClose(ctx, p)
	}
	return nil, ErrUnsupportedTxBuilder
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
