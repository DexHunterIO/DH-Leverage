package sources

import (
	"context"
	"errors"
	"testing"
)

type mockPersister struct {
	called  bool
	markets []Market
	err     error
}

func (m *mockPersister) PersistMarkets(_ context.Context, _ string, ms []Market) error {
	m.called = true
	m.markets = ms
	return m.err
}

func TestPersistedSource_Name(t *testing.T) {
	src := &mockSource{name: "liqwid"}
	ps := NewPersistedSource(src, NoopPersister{})
	if ps.Name() != "liqwid" {
		t.Fatalf("expected liqwid, got %s", ps.Name())
	}
}

func TestPersistedSource_PersistsOnSuccess(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test", PoolID: "p1"}},
	}
	p := &mockPersister{}
	ps := NewPersistedSource(src, p)

	ms, err := ps.FetchMarkets(context.Background())
	if err != nil {
		t.Fatalf("FetchMarkets: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("expected 1 market, got %d", len(ms))
	}
	if !p.called {
		t.Fatal("persister not called")
	}
	if len(p.markets) != 1 {
		t.Fatalf("persister got %d markets", len(p.markets))
	}
}

func TestPersistedSource_UpstreamError(t *testing.T) {
	src := &mockSource{name: "test", err: errors.New("fail")}
	p := &mockPersister{}
	ps := NewPersistedSource(src, p)

	_, err := ps.FetchMarkets(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if p.called {
		t.Fatal("persister should not be called on upstream error")
	}
}

func TestPersistedSource_PersisterErrorNonFatal(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test"}},
	}
	p := &mockPersister{err: errors.New("db down")}
	ps := NewPersistedSource(src, p)

	ms, err := ps.FetchMarkets(context.Background())
	if err != nil {
		t.Fatalf("FetchMarkets should succeed despite persister error: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("expected 1 market, got %d", len(ms))
	}
}

func TestPersistedSource_NilPersister(t *testing.T) {
	src := &mockSource{
		name:    "test",
		markets: []Market{{Source: "test"}},
	}
	ps := NewPersistedSource(src, nil)

	ms, err := ps.FetchMarkets(context.Background())
	if err != nil {
		t.Fatalf("FetchMarkets: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("expected 1 market, got %d", len(ms))
	}
}

func TestPersistedSource_FetchOrders(t *testing.T) {
	src := &mockSource{
		name:   "test",
		orders: []Order{{Source: "test", ID: "o1"}},
	}
	ps := NewPersistedSource(src, NoopPersister{})

	os, err := ps.FetchOrders(context.Background(), OrderQuery{})
	if err != nil {
		t.Fatalf("FetchOrders: %v", err)
	}
	if len(os) != 1 || os[0].ID != "o1" {
		t.Fatalf("unexpected orders: %+v", os)
	}
}

func TestPersistedSource_BuildTx_NoTxBuilder(t *testing.T) {
	src := &mockSource{name: "test"}
	ps := NewPersistedSource(src, NoopPersister{})
	ctx := context.Background()

	_, err := ps.BuildSupply(ctx, TxParams{})
	if !errors.Is(err, ErrUnsupportedTxBuilder) {
		t.Fatalf("expected ErrUnsupportedTxBuilder, got %v", err)
	}
}

func TestNoopPersister(t *testing.T) {
	err := NoopPersister{}.PersistMarkets(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("NoopPersister should return nil: %v", err)
	}
}
