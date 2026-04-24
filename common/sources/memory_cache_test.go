package sources

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCache_GetMiss(t *testing.T) {
	c := NewMemoryCache()
	_, ok, err := c.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestMemoryCache_SetAndGet(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	markets := []Market{{Source: "test", PoolID: "pool1"}}

	if err := c.Set(ctx, "k", markets, 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 1 || got[0].PoolID != "pool1" {
		t.Fatalf("unexpected value: %+v", got)
	}
}

func TestMemoryCache_Expiry(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	markets := []Market{{Source: "test"}}

	if err := c.Set(ctx, "k", markets, 1*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	_, ok, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestMemoryOrderCache_GetMiss(t *testing.T) {
	c := NewMemoryOrderCache()
	_, ok, err := c.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestMemoryOrderCache_SetAndGet(t *testing.T) {
	c := NewMemoryOrderCache()
	ctx := context.Background()
	orders := []Order{{Source: "test", ID: "o1", Status: OrderActive}}

	if err := c.Set(ctx, "k", orders, 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 1 || got[0].ID != "o1" {
		t.Fatalf("unexpected value: %+v", got)
	}
}

func TestMemoryOrderCache_Expiry(t *testing.T) {
	c := NewMemoryOrderCache()
	ctx := context.Background()
	orders := []Order{{Source: "test"}}

	if err := c.Set(ctx, "k", orders, 1*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	_, ok, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}
