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

// OrderCache implements sources.OrderCache against the same Redis server
// used by the markets/balances caches. Keys are whatever the caller passes
// in; CachedSource uses `orders:<source>:<address>:<limit>`.
type OrderCache struct {
	client *goredis.Client
}

// NewOrderCache parses the URL, dials Redis, and pings to confirm liveness.
func NewOrderCache(ctx context.Context, url string) (*OrderCache, error) {
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
	return &OrderCache{client: client}, nil
}

func (c *OrderCache) Close() error { return c.client.Close() }

func (c *OrderCache) Get(ctx context.Context, key string) ([]sources.Order, bool, error) {
	b, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var orders []sources.Order
	if err := json.Unmarshal(b, &orders); err != nil {
		return nil, false, fmt.Errorf("redis: unmarshal orders: %w", err)
	}
	return orders, true, nil
}

func (c *OrderCache) Set(ctx context.Context, key string, value []sources.Order, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis: marshal orders: %w", err)
	}
	return c.client.Set(ctx, key, b, ttl).Err()
}
