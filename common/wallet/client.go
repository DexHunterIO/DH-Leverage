// Package wallet fetches a Cardano address' balance (ADA lovelace + native
// tokens) from Koios and caches it in Redis so repeat calls from the
// frontend don't hammer the public Koios endpoints.
//
// Koios is used because it's free, requires no API key, and is the same
// data source the frontend was already calling directly. By routing the
// call through the backend we get:
//   - a shared cache (multiple tabs / clients benefit)
//   - a stable interface we control
//   - a place to plug in Blockfrost / on-chain parsing later
package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	koiosBase           = "https://api.koios.rest/api/v1"
	koiosAddressInfo    = "/address_info"
	koiosAddressAssets  = "/address_assets"
	defaultCacheTTL     = 30 * time.Second
	defaultKoiosTimeout = 15 * time.Second
)

// Balance is the normalized wallet state we serve from /api/wallet/balance.
type Balance struct {
	Address   string    `json:"address"`
	Lovelace  int64     `json:"lovelace"`
	Tokens    []Token   `json:"tokens"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Token is a single native asset the address holds. Quantity is a string so
// very large amounts don't lose precision through a float round-trip.
type Token struct {
	PolicyID    string `json:"policyId"`
	AssetName   string `json:"assetName"`
	Symbol      string `json:"symbol"`
	Decimals    int    `json:"decimals"`
	Quantity    string `json:"quantity"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// BalanceCache is the small contract a cache backend must satisfy. The
// Redis implementation lives in common/database/redis; an in-memory
// fallback lives below.
type BalanceCache interface {
	Get(ctx context.Context, address string) (*Balance, bool, error)
	Set(ctx context.Context, address string, value *Balance, ttl time.Duration) error
}

// Client is the public entry point. It's safe for concurrent use.
type Client struct {
	http  *http.Client
	cache BalanceCache
	ttl   time.Duration
}

// New returns a Client with a 15s HTTP timeout and the supplied cache
// (may be nil — then every call hits Koios).
func New(cache BalanceCache, ttl time.Duration) *Client {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &Client{
		http:  &http.Client{Timeout: defaultKoiosTimeout},
		cache: cache,
		ttl:   ttl,
	}
}

// FetchBalance returns the balance for `address`, served from cache when
// possible. Cache misses hit Koios; the result is written back on success.
func (c *Client) FetchBalance(ctx context.Context, address string) (*Balance, error) {
	if address == "" {
		return nil, errors.New("wallet: empty address")
	}
	if c.cache != nil {
		if b, ok, err := c.cache.Get(ctx, address); err == nil && ok {
			return b, nil
		}
	}
	b, err := c.fetchFromKoios(ctx, address)
	if err != nil {
		return nil, err
	}
	if c.cache != nil {
		_ = c.cache.Set(ctx, address, b, c.ttl)
	}
	return b, nil
}

// --- Koios wire format ------------------------------------------------------

type koiosAddressInfoRow struct {
	Address string `json:"address"`
	Balance string `json:"balance"` // lovelace, as string
}

type koiosAssetRow struct {
	Address     string `json:"address"`
	PolicyID    string `json:"policy_id"`
	AssetName   string `json:"asset_name"`
	Fingerprint string `json:"fingerprint"`
	Decimals    int    `json:"decimals"`
	Quantity    string `json:"quantity"`
}

func (c *Client) fetchFromKoios(ctx context.Context, address string) (*Balance, error) {
	// Fire both calls concurrently so the endpoint stays snappy.
	var (
		lovelace int64
		tokens   []Token
		errInfo  error
		errAss   error
		wg       sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		var rows []koiosAddressInfoRow
		if err := c.koiosPost(ctx, koiosAddressInfo, address, &rows); err != nil {
			errInfo = err
			return
		}
		if len(rows) > 0 {
			lovelace = parseInt64(rows[0].Balance)
		}
	}()

	go func() {
		defer wg.Done()
		var rows []koiosAssetRow
		if err := c.koiosPost(ctx, koiosAddressAssets, address, &rows); err != nil {
			errAss = err
			return
		}
		tokens = make([]Token, 0, len(rows))
		for _, r := range rows {
			tokens = append(tokens, Token{
				PolicyID:    r.PolicyID,
				AssetName:   r.AssetName,
				Symbol:      hexToAscii(r.AssetName),
				Decimals:    r.Decimals,
				Quantity:    r.Quantity,
				Fingerprint: r.Fingerprint,
			})
		}
	}()

	wg.Wait()

	if errInfo != nil {
		return nil, fmt.Errorf("koios address_info: %w", errInfo)
	}
	if errAss != nil {
		return nil, fmt.Errorf("koios address_assets: %w", errAss)
	}
	return &Balance{
		Address:   address,
		Lovelace:  lovelace,
		Tokens:    tokens,
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (c *Client) koiosPost(ctx context.Context, path, address string, out any) error {
	body, _ := json.Marshal(map[string]any{"_addresses": []string{address}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, koiosBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- helpers ----------------------------------------------------------------

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// hexToAscii turns a hex-encoded asset name into a printable ticker
// (returns "" if the bytes aren't all printable).
func hexToAscii(hexStr string) string {
	if hexStr == "" {
		return ""
	}
	out := make([]byte, 0, len(hexStr)/2)
	for i := 0; i+1 < len(hexStr); i += 2 {
		hi, ok1 := hexNibble(hexStr[i])
		lo, ok2 := hexNibble(hexStr[i+1])
		if !ok1 || !ok2 {
			return ""
		}
		b := hi<<4 | lo
		if b < 32 || b > 126 {
			return ""
		}
		out = append(out, b)
	}
	return string(out)
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// --- in-memory fallback -----------------------------------------------------

// MemoryCache is the fallback BalanceCache used when Redis isn't reachable.
type MemoryCache struct {
	mu   sync.Mutex
	data map[string]memEntry
}

type memEntry struct {
	value   *Balance
	expires time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{data: make(map[string]memEntry)}
}

func (m *MemoryCache) Get(_ context.Context, address string) (*Balance, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[address]
	if !ok || time.Now().After(e.expires) {
		return nil, false, nil
	}
	return e.value, true, nil
}

func (m *MemoryCache) Set(_ context.Context, address string, value *Balance, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[address] = memEntry{value: value, expires: time.Now().Add(ttl)}
	return nil
}
