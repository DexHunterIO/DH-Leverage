// Package redis provides a Redis-backed implementation of sources.Cache.
//
// It is intentionally tiny: parse the URL, ping it once at construction,
// and serialize market lists as JSON. If the connection fails at startup
// the caller is expected to fall back to an in-memory cache.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"dh-leverage/common/sources"
)

// Cache implements sources.Cache against a Redis server.
type Cache struct {
	client *goredis.Client
}

// NewMarketsCache parses the URL, dials Redis, pings to confirm liveness,
// and returns a ready-to-use Cache. The caller must Close() it on shutdown.
func NewMarketsCache(ctx context.Context, url string) (*Cache, error) {
	if url == "" {
		return nil, errors.New("redis: empty URL")
	}
	opt, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}
	client := goredis.NewClient(opt)
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return &Cache{client: client}, nil
}

func (c *Cache) Close() error { return c.client.Close() }

func (c *Cache) Get(ctx context.Context, key string) ([]sources.Market, bool, error) {
	b, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var ms []sources.Market
	if err := json.Unmarshal(b, &ms); err != nil {
		return nil, false, fmt.Errorf("redis: unmarshal: %w", err)
	}
	return ms, true, nil
}

func (c *Cache) Set(ctx context.Context, key string, value []sources.Market, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis: marshal: %w", err)
	}
	return c.client.Set(ctx, key, b, ttl).Err()
}
