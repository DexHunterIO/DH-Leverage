package sources

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockSource implements Source for testing.
type mockSource struct {
	name       string
	markets    []Market
	orders     []Order
	fetchCount atomic.Int32
	err        error
}

func (m *mockSource) Name() string { return m.name }

func (m *mockSource) FetchMarkets(ctx context.Context) ([]Market, error) {
	m.fetchCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return m.markets, nil
}

func (m *mockSource) FetchOrders(ctx context.Context, q OrderQuery) ([]Order, error) {
	m.fetchCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return m.orders, nil
}

func TestCachedSource_Name(t *testing.T) {
	src := &mockSource{name: "test-src"}
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute)
	if cs.Name() != "test-src" {
		t.Fatalf("expected test-src, got %s", cs.Name())
	}
}

func TestCachedSource_FetchMarkets_CacheHit(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test", PoolID: "p1"}},
	}
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute)
	ctx := context.Background()

	// First call: cache miss, hits upstream.
	m1, err := cs.FetchMarkets(ctx)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(m1) != 1 {
		t.Fatalf("expected 1 market, got %d", len(m1))
	}

	// Second call: cache hit, should not call upstream again.
	m2, err := cs.FetchMarkets(ctx)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(m2) != 1 {
		t.Fatalf("expected 1 market, got %d", len(m2))
	}

	if src.fetchCount.Load() != 1 {
		t.Fatalf("expected 1 upstream fetch, got %d", src.fetchCount.Load())
	}
}

func TestCachedSource_FetchMarkets_NilCache(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test"}},
	}
	cs := NewCachedSource(src, nil, time.Minute)
	ctx := context.Background()

	_, err := cs.FetchMarkets(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	_, err = cs.FetchMarkets(ctx)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if src.fetchCount.Load() != 2 {
		t.Fatalf("expected 2 upstream fetches (no cache), got %d", src.fetchCount.Load())
	}
}

func TestCachedSource_FetchMarkets_ZeroTTL(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test"}},
	}
	cs := NewCachedSource(src, NewMemoryCache(), 0)
	ctx := context.Background()

	cs.FetchMarkets(ctx)
	cs.FetchMarkets(ctx)

	if src.fetchCount.Load() != 2 {
		t.Fatalf("expected 2 upstream fetches (zero ttl), got %d", src.fetchCount.Load())
	}
}

func TestCachedSource_FetchMarkets_ErrorNotCached(t *testing.T) {
	src := &mockSource{
		name: "test",
		err:  errors.New("upstream error"),
	}
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute)
	ctx := context.Background()

	_, err := cs.FetchMarkets(ctx)
	if err == nil {
		t.Fatal("expected error")
	}

	// Error should not be cached — next call should hit upstream again.
	src.err = nil
	src.markets = []Market{{Source: "test"}}

	m, err := cs.FetchMarkets(ctx)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 market, got %d", len(m))
	}
}

func TestCachedSource_FetchOrders_CacheHit(t *testing.T) {
	src := &mockSource{
		name:   "test",
		orders: []Order{{Source: "test", ID: "o1"}},
	}
	oc := NewMemoryOrderCache()
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute).WithOrderCache(oc, time.Minute)
	ctx := context.Background()
	q := OrderQuery{Address: "addr1"}

	o1, err := cs.FetchOrders(ctx, q)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(o1) != 1 {
		t.Fatalf("expected 1 order, got %d", len(o1))
	}

	o2, err := cs.FetchOrders(ctx, q)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(o2) != 1 {
		t.Fatalf("expected 1 order, got %d", len(o2))
	}

	if src.fetchCount.Load() != 1 {
		t.Fatalf("expected 1 upstream fetch, got %d", src.fetchCount.Load())
	}
}

func TestCachedSource_FetchOrders_RefreshBypassesCache(t *testing.T) {
	src := &mockSource{
		name:   "test",
		orders: []Order{{Source: "test", ID: "o1"}},
	}
	oc := NewMemoryOrderCache()
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute).WithOrderCache(oc, time.Minute)
	ctx := context.Background()

	// Populate cache.
	cs.FetchOrders(ctx, OrderQuery{Address: "addr1"})

	// Refresh should bypass cache.
	cs.FetchOrders(ctx, OrderQuery{Address: "addr1", Refresh: true})

	if src.fetchCount.Load() != 2 {
		t.Fatalf("expected 2 upstream fetches (refresh), got %d", src.fetchCount.Load())
	}
}

func TestCachedSource_BuildTx_NoTxBuilder(t *testing.T) {
	src := &mockSource{name: "test"}
	cs := NewCachedSource(src, NewMemoryCache(), time.Minute)
	ctx := context.Background()

	_, err := cs.BuildSupply(ctx, TxParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
	_, err = cs.BuildWithdraw(ctx, TxParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
	_, err = cs.BuildBorrow(ctx, TxParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
	_, err = cs.BuildClose(ctx, TxCloseParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
	_, err = cs.SubmitTx(ctx, SubmitParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{-1, "-1"},
		{1000, "1000"},
		{-999, "-999"},
	}
	for _, tc := range cases {
		got := itoa(tc.in)
		if got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOrderKey(t *testing.T) {
	key := orderKey("liqwid", OrderQuery{Address: "abc", Limit: 10})
	want := "orders:liqwid:abc:10"
	if key != want {
		t.Errorf("orderKey = %q, want %q", key, want)
	}
}
