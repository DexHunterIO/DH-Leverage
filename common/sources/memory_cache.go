package sources

import (
	"context"
	"sync"
	"time"
)

// MemoryCache is the fallback Cache implementation when Redis isn't
// reachable. It is process-local, intentionally tiny, and safe for
// concurrent use.
type MemoryCache struct {
	mu   sync.Mutex
	data map[string]memEntry
}

type memEntry struct {
	value   []Market
	expires time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{data: make(map[string]memEntry)}
}

func (m *MemoryCache) Get(_ context.Context, key string) ([]Market, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false, nil
	}
	return e.value, true, nil
}

func (m *MemoryCache) Set(_ context.Context, key string, value []Market, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = memEntry{value: value, expires: time.Now().Add(ttl)}
	return nil
}

// MemoryOrderCache is the fallback OrderCache when Redis isn't reachable.
type MemoryOrderCache struct {
	mu   sync.Mutex
	data map[string]memOrderEntry
}

type memOrderEntry struct {
	value   []Order
	expires time.Time
}

func NewMemoryOrderCache() *MemoryOrderCache {
	return &MemoryOrderCache{data: make(map[string]memOrderEntry)}
}

func (m *MemoryOrderCache) Get(_ context.Context, key string) ([]Order, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false, nil
	}
	return e.value, true, nil
}

func (m *MemoryOrderCache) Set(_ context.Context, key string, value []Order, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = memOrderEntry{value: value, expires: time.Now().Add(ttl)}
	return nil
}
