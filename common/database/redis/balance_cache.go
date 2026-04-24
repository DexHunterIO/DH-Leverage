package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"dh-leverage/common/wallet"
)

// BalanceCache implements wallet.BalanceCache against the same Redis client
// as the markets cache. Keys are prefixed `balance:` so they don't collide
// with `markets:*`.
type BalanceCache struct {
	client *goredis.Client
}

// NewBalanceCache parses the URL, dials Redis, and pings to confirm
// liveness. Returns an error on failure so the caller can fall back to
// wallet.NewMemoryCache.
func NewBalanceCache(ctx context.Context, url string) (*BalanceCache, error) {
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
	return &BalanceCache{client: client}, nil
}

func (c *BalanceCache) Close() error { return c.client.Close() }

func (c *BalanceCache) key(address string) string { return "balance:" + address }

func (c *BalanceCache) Get(ctx context.Context, address string) (*wallet.Balance, bool, error) {
	b, err := c.client.Get(ctx, c.key(address)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var bal wallet.Balance
	if err := json.Unmarshal(b, &bal); err != nil {
		return nil, false, fmt.Errorf("redis: unmarshal balance: %w", err)
	}
	return &bal, true, nil
}

func (c *BalanceCache) Set(ctx context.Context, address string, value *wallet.Balance, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis: marshal balance: %w", err)
	}
	return c.client.Set(ctx, c.key(address), b, ttl).Err()
}
