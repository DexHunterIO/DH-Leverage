package sources

import (
	"context"
	"log"
)

// Persister is a write-through hook that gets called every time a Source
// successfully fetches markets from upstream. It is used to mirror live
// market state into a persistent store (Mongo today). Persistence failures
// must NEVER fail the upstream fetch — wrap your implementation accordingly.
type Persister interface {
	PersistMarkets(ctx context.Context, source string, markets []Market) error
}

// NoopPersister is the default when no persistence backend is configured.
type NoopPersister struct{}

func (NoopPersister) PersistMarkets(context.Context, string, []Market) error { return nil }

// PersistedSource wraps a Source with a Persister so every successful
// FetchMarkets call mirrors the result to the persistent store. The
// persister is called BEFORE the markets are returned, but its errors are
// downgraded to log lines so the API never fails because the DB is down.
//
// Compose this INSIDE the cache wrapper so cache hits don't re-persist
// unchanged data:
//
//	src = NewPersistedSource(liqwid.New(), persister)
//	src = NewCachedSource(src, cache, ttl)
type PersistedSource struct {
	inner     Source
	persister Persister
}

func NewPersistedSource(inner Source, p Persister) *PersistedSource {
	if p == nil {
		p = NoopPersister{}
	}
	return &PersistedSource{inner: inner, persister: p}
}

func (p *PersistedSource) Name() string { return p.inner.Name() }

func (p *PersistedSource) FetchMarkets(ctx context.Context) ([]Market, error) {
	ms, err := p.inner.FetchMarkets(ctx)
	if err != nil {
		return nil, err
	}
	if perr := p.persister.PersistMarkets(ctx, p.inner.Name(), ms); perr != nil {
		log.Printf("persister: %s markets persist failed (non-fatal): %v", p.inner.Name(), perr)
	}
	return ms, nil
}

func (p *PersistedSource) FetchOrders(ctx context.Context, q OrderQuery) ([]Order, error) {
	return p.inner.FetchOrders(ctx, q)
}

// --- TxBuilder delegation ---------------------------------------------------

func (p *PersistedSource) BuildSupply(ctx context.Context, params TxParams) (*BuiltTx, error) {
	if b, ok := p.inner.(TxBuilder); ok {
		return b.BuildSupply(ctx, params)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (p *PersistedSource) BuildWithdraw(ctx context.Context, params TxParams) (*BuiltTx, error) {
	if b, ok := p.inner.(TxBuilder); ok {
		return b.BuildWithdraw(ctx, params)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (p *PersistedSource) BuildBorrow(ctx context.Context, params TxParams) (*BuiltTx, error) {
	if b, ok := p.inner.(TxBuilder); ok {
		return b.BuildBorrow(ctx, params)
	}
	return nil, ErrUnsupportedTxBuilder
}

func (p *PersistedSource) SubmitTx(ctx context.Context, params SubmitParams) (string, error) {
	if b, ok := p.inner.(TxBuilder); ok {
		return b.SubmitTx(ctx, params)
	}
	return "", ErrUnsupportedTxBuilder
}

func (p *PersistedSource) BuildClose(ctx context.Context, params TxCloseParams) (*BuiltTx, error) {
	if b, ok := p.inner.(TxBuilder); ok {
		return b.BuildClose(ctx, params)
	}
	return nil, ErrUnsupportedTxBuilder
}
