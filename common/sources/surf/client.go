// Package surf is the data-gathering pipe for the Surf lending protocol
// (also known as "Flow Lending" — the API docs live at
// surflending.org/api-docs). Surf is an isolated-pool lender on Cardano:
// each market is a (collateral asset → borrow asset) pair with its own LTV
// and liquidation threshold.
//
// Official API reference: https://surflending.org/api-docs
// All endpoints live under surflending.org/api/*. POST endpoints accept
// flat JSON bodies (no wrapper) and return {cbor: "..."}.
package surf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"dh-leverage/common/sources"
)

const (
	Name        = "surf"
	defaultBase = "https://surflending.org"

	poolInfosPath        = "/api/getAllPoolInfos"
	adaPricePath         = "/api/getAdaPrice"
	activitiesPath       = "/api/getActivities"
	allPositionsPath     = "/api/getAllPositions"
	allOrdersPath        = "/api/getAllOrders" // undocumented but works; single call for all pools
	depositLiquidityPath = "/api/depositLiquidity"
	withdrawLiqPath      = "/api/withdrawLiquidity"
	borrowPath           = "/api/borrow"
)

// Client fetches market data from the Surf Next.js backend.
type Client struct {
	base string
	http *http.Client
}

func New() *Client {
	return &Client{
		base: defaultBase,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// WithBase overrides the protocol host (e.g. for a staging deployment).
func (c *Client) WithBase(url string) *Client {
	c.base = url
	return c
}

func (c *Client) Name() string { return Name }

// --- response shapes (matching /api/getAllPoolInfos) -------------------------

type surfAsset struct {
	Ticker    string `json:"ticker"`
	PolicyID  string `json:"policyId"`
	AssetName string `json:"assetName"`
	Decimals  int    `json:"decimals"`
}

type surfCollateral struct {
	Asset                   surfAsset `json:"asset"`
	Price                   float64   `json:"price"`
	MaxBorrowLTV            float64   `json:"maxBorrowLTV"`
	RecommendedBorrowLTV    float64   `json:"recommendedBorrowLTV"`
	LiquidationThresholdLTV float64   `json:"liquidationThresholdLTV"`
}

type surfPool struct {
	Asset                   surfAsset        `json:"asset"`
	Price                   float64          `json:"price"`
	CollateralAssets        []surfCollateral `json:"collateralAssets"`
	MaxBorrowLTV            float64          `json:"maxBorrowLTV"`
	LiquidationThresholdLTV float64          `json:"liquidationThresholdLTV"`
	TotalSupplied           float64          `json:"totalSupplied"`
	TotalBorrowed           float64          `json:"totalBorrowed"`
	TotalCToken             float64          `json:"totalCToken"`
	Reserve                 float64          `json:"reserve"`
	SupplyAPY               float64          `json:"supplyApy"`
	BorrowAPR               float64          `json:"borrowApr"`
	CToken                  struct {
		PolicyID  string `json:"policyId"`
		AssetName string `json:"assetName"`
	} `json:"cToken"`
}

type poolInfosResponse struct {
	PoolInfos map[string]surfPool `json:"poolInfos"`
}

type adaPriceResponse struct {
	Price float64 `json:"price"`
}

// FetchMarkets returns one Market per (pool, collateral) pair so the routing
// engine can score each leg independently. Surf v1 pools have a single
// collateral asset; v2 will introduce multi-collateral pools and the loop
// below already handles that case.
func (c *Client) FetchMarkets(ctx context.Context) ([]sources.Market, error) {
	pools, err := fetchJSON[poolInfosResponse](ctx, c.http, c.base+poolInfosPath)
	if err != nil {
		return nil, fmt.Errorf("surf getAllPoolInfos: %w", err)
	}

	// ADA→USD so depth is comparable across sources. Best-effort: if the
	// price call fails, fall back to ADA-denominated values.
	adaUSD := 0.0
	if p, err := fetchJSON[adaPriceResponse](ctx, c.http, c.base+adaPricePath); err == nil {
		adaUSD = p.Price
	}

	out := make([]sources.Market, 0, len(pools.PoolInfos)*2)
	for utxoRef, pool := range pools.PoolInfos {
		scale := pow10(pool.Asset.Decimals)
		supplied := pool.TotalSupplied / scale
		borrowed := pool.TotalBorrowed / scale
		available := supplied - borrowed
		if available < 0 {
			available = 0
		}
		// Surf's `price` is the asset's price relative to ADA: ADA itself
		// is 1.0, every CNT is fractional. Multiply by ADA→USD to get USD.
		borrowUSD := pool.Price * adaUSD
		borrow := sources.Asset{
			PolicyID:  pool.Asset.PolicyID,
			AssetName: pool.Asset.AssetName,
			Symbol:    pool.Asset.Ticker,
			Decimals:  pool.Asset.Decimals,
			PriceUsd:  borrowUSD,
		}
		// fToken receipt — Surf calls it cToken in the API. Koios reports
		// Surf cTokens with 0 decimals (they're raw share counts), so we
		// match that here — the frontend divides wallet qty by 10^dec and
		// must agree with Koios.
		receipt := sources.Asset{
			PolicyID:  pool.CToken.PolicyID,
			AssetName: pool.CToken.AssetName,
			Symbol:    "f" + pool.Asset.Ticker,
			Decimals:  0,
		}
		// Supply exchange rate: whole borrow asset per 1 whole cToken.
		//   rate = (totalSupplied / 10^borrowDec) / (totalCToken / 10^0)
		var supplyXR float64
		if pool.TotalCToken > 0 {
			supplyXR = (pool.TotalSupplied / scale) / pool.TotalCToken
		}

		// Emit one Market per collateral option. If a pool somehow has no
		// listed collateral asset, still emit the pool itself so depth is
		// not lost.
		if len(pool.CollateralAssets) == 0 {
			out = append(out, sources.Market{
				Source:                Name,
				PoolID:                utxoRef,
				Borrow:                borrow,
				ReceiptAsset:          receipt,
				SupplyExchangeRate:    supplyXR,
				TotalSuppliedUSD:      supplied * borrowUSD,
				TotalBorrowedUSD:      borrowed * borrowUSD,
				AvailableLiquidityUSD: available * borrowUSD,
				SupplyAPY:             pool.SupplyAPY,
				BorrowAPY:             pool.BorrowAPR,
				LTV:                   pool.MaxBorrowLTV,
				LiquidationThreshold:  pool.LiquidationThresholdLTV,
				Active:                true,
				URL:                   c.base + "/app",
			})
			continue
		}
		for _, col := range pool.CollateralAssets {
			out = append(out, sources.Market{
				Source: Name,
				PoolID: utxoRef,
				Collateral: sources.Asset{
					PolicyID:  col.Asset.PolicyID,
					AssetName: col.Asset.AssetName,
					Symbol:    col.Asset.Ticker,
					Decimals:  col.Asset.Decimals,
					PriceUsd:  col.Price * adaUSD,
				},
				Borrow:                borrow,
				ReceiptAsset:          receipt,
				SupplyExchangeRate:    supplyXR,
				TotalSuppliedUSD:      supplied * borrowUSD,
				TotalBorrowedUSD:      borrowed * borrowUSD,
				AvailableLiquidityUSD: available * borrowUSD,
				SupplyAPY:             pool.SupplyAPY,
				BorrowAPY:             pool.BorrowAPR,
				LTV:                   col.MaxBorrowLTV,
				LiquidationThreshold:  col.LiquidationThresholdLTV,
				Active:                true,
				URL:                   c.base + "/app",
			})
		}
	}
	return out, nil
}

// --- orders ------------------------------------------------------------------

type surfActivity struct {
	Type             string  `json:"type"`
	Address          string  `json:"address"`
	Amount           float64 `json:"amount"`
	Asset            string  `json:"asset"` // policyId+hexName, "" for ADA
	CollateralAmount float64 `json:"collateralAmount"`
	CollateralAsset  string  `json:"collateralAsset"` // policyId+hexName, "" for ADA
	PoolID           string  `json:"poolId"`
	TxHash           string  `json:"txHash"`
	Time             int64   `json:"time"` // unix milliseconds
}

// surfPositionGroup is one entry in the /api/getAllPositions response —
// each pool's active borrow positions for the queried address.
type surfPositionGroup struct {
	PoolID    string         `json:"poolId"`
	Positions []surfPosition `json:"positions"`
}

type surfPosition struct {
	PoolID         string  `json:"poolId"`
	Address        string  `json:"address"`
	Principal      float64 `json:"principal"`
	PrincipalAsset struct {
		PolicyID  string `json:"policyId"`
		AssetName string `json:"assetName"`
	} `json:"principalAsset"`
	Collateral      float64 `json:"collateral"`
	CollateralAsset struct {
		PolicyID  string `json:"policyId"`
		AssetName string `json:"assetName"`
	} `json:"collateralAsset"`
	InterestRate float64 `json:"interestRate"`
	StartTime    int64   `json:"startTime"`
	LTV          float64 `json:"ltv"`
	BorrowID     struct {
		TxHash      string `json:"txHash"`
		OutputIndex int    `json:"outputIndex"`
	} `json:"borrowId"`
	// OutRef is the CURRENT on-chain UTxO for this position. It differs
	// from BorrowID when the position has been rebatched (e.g. interest
	// accrual moves the UTxO). The repay endpoint needs OutRef, not
	// BorrowID.
	OutRef struct {
		TxHash      string `json:"txHash"`
		OutputIndex int    `json:"outputIndex"`
	} `json:"outRef"`
}

type allPositionsResponse struct {
	Positions []surfPositionGroup `json:"positions"`
}

// surfOrder matches the wire shape of a pending order from
// GET /api/getOrders?poolId=X&address=Y (per the official API docs at
// surflending.org/api-docs).
type surfOrder struct {
	PoolID  string  `json:"poolId"`
	Type    string  `json:"type"`
	Address string  `json:"address"`
	Amount  float64 `json:"amount"`
	Asset   struct {
		PolicyID  string `json:"policyId"`
		AssetName string `json:"assetName"`
	} `json:"asset"`
	Collateral      float64 `json:"collateral"`
	CollateralAsset struct {
		PolicyID  string `json:"policyId"`
		AssetName string `json:"assetName"`
	} `json:"collateralAsset"`
	LTV    float64 `json:"ltv"`
	OutRef struct {
		TxHash      string `json:"txHash"`
		OutputIndex int    `json:"outputIndex"`
	} `json:"outRef"`
}

// surfOrderGroup is one entry in the /api/getAllOrders response — each
// pool's list of pending orders (orders that have been submitted to the
// batcher queue but not yet settled into an active position).
type surfOrderGroup struct {
	PoolID string      `json:"poolId"`
	Orders []surfOrder `json:"orders"`
}

type allOrdersResponse struct {
	Orders []surfOrderGroup `json:"orders"`
}

// FetchOrders returns Surf borrow positions. When q.Address is set we hit
// the per-wallet /api/getAllPositions endpoint which returns *active*
// positions; without an address we fall back to the global activity log.
//
// Surf supply positions live as fToken balances in the user's wallet, not
// as a queryable list. The frontend detects supplies by matching wallet
// balances against each market's ReceiptAsset.
func (c *Client) FetchOrders(ctx context.Context, q sources.OrderQuery) ([]sources.Order, error) {
	// Asset/decimals lookup is needed by both code paths.
	pools, err := fetchJSON[poolInfosResponse](ctx, c.http, c.base+poolInfosPath)
	if err != nil {
		return nil, fmt.Errorf("surf getAllPoolInfos: %w", err)
	}
	assets := buildAssetIndex(pools)

	if q.Address != "" {
		// For an address-scoped query we want BOTH the active positions
		// (/api/getAllPositions — settled on chain) AND the pending
		// orders in the batcher queue (/api/getAllOrders — submitted
		// but not yet settled). Otherwise the user sees nothing for
		// the 30-to-60 seconds between submit and settle.
		positions, perr := c.fetchPositions(ctx, q.Address, assets)
		pending, oerr := c.fetchPendingOrders(ctx, q.Address, assets)
		out := append(positions, pending...)
		if perr != nil && oerr != nil {
			return nil, fmt.Errorf("surf positions+orders: %v; %v", perr, oerr)
		}
		return out, nil
	}
	return c.fetchActivities(ctx, q.Limit, assets)
}

// fetchPendingOrders hits /api/getAllOrders for the given address and
// normalizes the response into Orders with Status="pending".
func (c *Client) fetchPendingOrders(ctx context.Context, address string, assets assetIndex) ([]sources.Order, error) {
	url := fmt.Sprintf("%s%s?address=%s", c.base, allOrdersPath, address)
	resp, err := fetchJSON[allOrdersResponse](ctx, c.http, url)
	if err != nil {
		return nil, fmt.Errorf("surf getAllOrders: %w", err)
	}
	out := make([]sources.Order, 0, 4)
	for _, group := range resp.Orders {
		for _, o := range group.Orders {
			borrowAsset := assets.lookup(o.Asset.PolicyID + o.Asset.AssetName)
			colAsset := assets.lookup(o.CollateralAsset.PolicyID + o.CollateralAsset.AssetName)
			out = append(out, sources.Order{
				Source:           Name,
				ID:               o.OutRef.TxHash,
				Type:             normalizeSurfType(o.Type),
				Status:           sources.OrderPending,
				Owner:            o.Address,
				MarketID:         o.PoolID,
				Asset:            borrowAsset,
				Amount:           scaleByDecimals(o.Amount, borrowAsset.Decimals),
				CollateralAsset:  colAsset,
				CollateralAmount: scaleByDecimals(o.Collateral, colAsset.Decimals),
				LTV:              o.LTV,
				TxHash:           o.OutRef.TxHash,
				OutputIndex:      o.OutRef.OutputIndex,
			})
		}
	}
	return out, nil
}

func (c *Client) fetchPositions(ctx context.Context, address string, assets assetIndex) ([]sources.Order, error) {
	url := fmt.Sprintf("%s%s?address=%s", c.base, allPositionsPath, address)
	resp, err := fetchJSON[allPositionsResponse](ctx, c.http, url)
	if err != nil {
		return nil, fmt.Errorf("surf getAllPositions: %w", err)
	}
	out := make([]sources.Order, 0, 8)
	for _, group := range resp.Positions {
		for _, p := range group.Positions {
			borrowAsset := assets.lookup(p.PrincipalAsset.PolicyID + p.PrincipalAsset.AssetName)
			colAsset := assets.lookup(p.CollateralAsset.PolicyID + p.CollateralAsset.AssetName)
			// Use OutRef (current on-chain UTxO) for close/repay, not
			// BorrowID (original tx). The UTxO moves when the position
			// gets rebatched for interest accrual.
			outTx := p.OutRef.TxHash
			outIdx := p.OutRef.OutputIndex
			if outTx == "" {
				outTx = p.BorrowID.TxHash
				outIdx = p.BorrowID.OutputIndex
			}
			out = append(out, sources.Order{
				Source:           Name,
				ID:               p.BorrowID.TxHash,
				Type:             sources.TypeBorrow,
				Status:           sources.OrderActive,
				Owner:            p.Address,
				MarketID:         p.PoolID,
				Asset:            borrowAsset,
				Amount:           scaleByDecimals(p.Principal, borrowAsset.Decimals),
				CollateralAsset:  colAsset,
				CollateralAmount: scaleByDecimals(p.Collateral, colAsset.Decimals),
				APY:              p.InterestRate,
				LTV:              p.LTV,
				TxHash:           outTx,
				OutputIndex:      outIdx,
				TimeMs:           p.StartTime,
			})
		}
	}
	return out, nil
}

func (c *Client) fetchActivities(ctx context.Context, limit int, assets assetIndex) ([]sources.Order, error) {
	url := c.base + activitiesPath
	if limit > 0 {
		url = fmt.Sprintf("%s?limit=%d", url, limit)
	}
	acts, err := fetchJSON[[]surfActivity](ctx, c.http, url)
	if err != nil {
		return nil, fmt.Errorf("surf getActivities: %w", err)
	}
	out := make([]sources.Order, 0, len(acts))
	for _, a := range acts {
		borrowAsset := assets.lookup(a.Asset)
		colAsset := assets.lookup(a.CollateralAsset)
		out = append(out, sources.Order{
			Source:           Name,
			ID:               a.TxHash,
			Type:             normalizeSurfType(a.Type),
			Status:           sources.OrderClosed,
			Owner:            a.Address,
			MarketID:         a.PoolID,
			Asset:            borrowAsset,
			Amount:           scaleByDecimals(a.Amount, borrowAsset.Decimals),
			CollateralAsset:  colAsset,
			CollateralAmount: scaleByDecimals(a.CollateralAmount, colAsset.Decimals),
			TxHash:           a.TxHash,
			TimeMs:           a.Time,
		})
	}
	return out, nil
}

// assetIndex maps "policyId+hexName" → Asset metadata. Empty key = ADA.
type assetIndex map[string]sources.Asset

func (i assetIndex) lookup(unit string) sources.Asset {
	if unit == "" {
		return sources.Asset{Symbol: "ADA", Decimals: 6}
	}
	if a, ok := i[unit]; ok {
		return a
	}
	// Unknown unit — split policy/hexName and emit a stub. Default to 0
	// decimals so the raw amount stays interpretable.
	policy, hex := splitUnit(unit)
	return sources.Asset{PolicyID: policy, AssetName: hex}
}

func buildAssetIndex(pools poolInfosResponse) assetIndex {
	idx := assetIndex{}
	add := func(a surfAsset) {
		if a.PolicyID == "" && a.AssetName == "" {
			return
		}
		idx[a.PolicyID+a.AssetName] = sources.Asset{
			PolicyID:  a.PolicyID,
			AssetName: a.AssetName,
			Symbol:    a.Ticker,
			Decimals:  a.Decimals,
		}
	}
	for _, p := range pools.PoolInfos {
		add(p.Asset)
		for _, c := range p.CollateralAssets {
			add(c.Asset)
		}
	}
	return idx
}

func splitUnit(unit string) (policy, hex string) {
	if len(unit) <= 56 {
		return unit, ""
	}
	return unit[:56], unit[56:]
}

func scaleByDecimals(amt float64, decimals int) float64 {
	if decimals <= 0 {
		return amt
	}
	return amt / pow10(decimals)
}

func normalizeSurfType(t string) string {
	switch t {
	case "Borrow":
		return sources.TypeBorrow
	case "Repay":
		return sources.TypeRepay
	case "Repay With Collateral":
		return sources.TypeRepayCollateral
	case "Deposit":
		return sources.TypeDeposit
	case "Withdraw":
		return sources.TypeWithdraw
	case "Cancel Withdraw":
		return sources.TypeCancelWithdraw
	case "Leveraged Borrow":
		return sources.TypeLeveragedBorrow
	case "Liquidation":
		return sources.TypeLiquidation
	default:
		return sources.TypeUnknown
	}
}

// fetchJSON is a tiny generic GET-and-decode helper.
func fetchJSON[T any](ctx context.Context, hc *http.Client, url string) (T, error) {
	var zero T
	logSurfCall("→ surf GET "+url, nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	logSurfCall(fmt.Sprintf("← surf %d GET", resp.StatusCode), respBytes)
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out T
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// logSurfCall mirrors the Liqwid helper — truncates long bodies so the
// log stays readable when getAllPositions returns megabytes of data.
func logSurfCall(tag string, body []byte) {
	const maxLen = 2000
	if body == nil {
		log.Printf("%s", tag)
		return
	}
	s := string(body)
	if len(s) > maxLen {
		s = s[:maxLen] + fmt.Sprintf("…(+%d bytes)", len(body)-maxLen)
	}
	log.Printf("%s body=%s", tag, s)
}

func pow10(n int) float64 {
	v := 1.0
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// =====================================================================
// TxBuilder — Surf builds transactions through its documented REST API
// (surflending.org/api-docs). Each POST endpoint accepts a flat JSON
// body and returns {cbor: "…"}; no auth needed for the build step.
// Field semantics per the official docs:
//   - poolId: utxoRef "<txhash>:<idx>"
//   - address: bech32 first used address
//   - amount: smallest units of the pool asset
//   - collateralAmount: smallest units of the collateral asset
//   - canonical: boolean (default false)
// =====================================================================

// surfTxRequest is the body shape Surf's /api/{depositLiquidity,
// withdrawLiquidity,borrow} endpoints expect.
//
// canonical is a BOOLEAN in the live API — Surf's own frontend always
// sends `"canonical": false` per network-tab inspection. We were sending
// the wallet name as a string ("lace"), which made Surf's build path
// fall through into a broken aux-data variant. Always send false.
type surfTxRequest struct {
	PoolID           string `json:"poolId"`
	Address          string `json:"address"`
	Amount           int64  `json:"amount"`
	CollateralAmount int64  `json:"collateralAmount,omitempty"`
	Canonical        bool   `json:"canonical"`
}

type surfTxResponse struct {
	CBOR    string   `json:"cbor"`
	Tx      string   `json:"tx,omitempty"`      // some endpoints use "tx"
	Witness []string `json:"witness,omitempty"` // cancelOrder may include witness for leveraged orders
	Error   string   `json:"error,omitempty"`
}

// surfDecimalsForPool looks up the borrow asset's decimals so we can scale
// whole-unit amounts back to smallest units before forwarding.
func (c *Client) surfDecimalsForPool(ctx context.Context, poolID string) (int, error) {
	pools, err := fetchJSON[poolInfosResponse](ctx, c.http, c.base+poolInfosPath)
	if err != nil {
		return 0, err
	}
	if p, ok := pools.PoolInfos[poolID]; ok {
		return p.Asset.Decimals, nil
	}
	return 6, nil // ADA fallback
}

// surfCollateralDecimalsForPool returns the decimals of the FIRST
// collateral asset listed by the pool. Surf v1 pools have a single
// collateral asset per pool, so "first" is correct. Used to scale
// user-entered whole-unit collateral amounts back to raw units.
func (c *Client) surfCollateralDecimalsForPool(ctx context.Context, poolID string) (int, error) {
	pools, err := fetchJSON[poolInfosResponse](ctx, c.http, c.base+poolInfosPath)
	if err != nil {
		return 0, err
	}
	if p, ok := pools.PoolInfos[poolID]; ok && len(p.CollateralAssets) > 0 {
		return p.CollateralAssets[0].Asset.Decimals, nil
	}
	return 0, nil
}

// surfBlockfrostRetryable returns true for Surf upstream errors that the
// Surf backend itself recommends retrying — usually transient Blockfrost
// rate-limit / fetch failures during the UTxO lookup phase.
func surfBlockfrostRetryable(msg string) bool {
	return strings.Contains(msg, "Could not fetch UTxOs from Blockfrost") ||
		strings.Contains(msg, "Blockfrost") && strings.Contains(msg, "Try again")
}

func (c *Client) postTxBuild(ctx context.Context, path string, body any) (string, error) {
	// Surf's tx-builder calls Blockfrost server-side; transient failures
	// are common and the upstream literally tells you to "Try again". Run
	// up to 3 attempts with a small backoff.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		cbor, err := c.postTxBuildOnce(ctx, path, body)
		if err == nil {
			return cbor, nil
		}
		lastErr = err
		if !surfBlockfrostRetryable(err.Error()) {
			return "", err
		}
	}
	return "", lastErr
}

func (c *Client) postTxBuildOnce(ctx context.Context, path string, body any) (string, error) {
	// Surf's live API still expects the body wrapped in {"request": ...}
	// even though the official docs at surflending.org/api-docs show flat
	// bodies. Without the wrapper, the server returns 500 "Cannot read
	// properties of undefined (reading 'poolId')".
	wrapped, err := json.Marshal(map[string]any{"request": body})
	if err != nil {
		return "", err
	}
	logSurfCall("→ surf POST "+c.base+path, wrapped)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(wrapped))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("surf %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	logSurfCall(fmt.Sprintf("← surf %d %s", resp.StatusCode, path), respBytes)
	if resp.StatusCode != http.StatusOK {
		// Try to decode the error message so the retry classifier can read it.
		var errBody surfTxResponse
		if json.Unmarshal(respBytes, &errBody) == nil && errBody.Error != "" {
			return "", fmt.Errorf("surf %s: status %d: %s", path, resp.StatusCode, errBody.Error)
		}
		return "", fmt.Errorf("surf %s: status %d: %s", path, resp.StatusCode, truncate(string(respBytes), 240))
	}
	var out surfTxResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return "", fmt.Errorf("surf %s decode: %w", path, err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("surf %s: %s", path, out.Error)
	}
	cbor := out.CBOR
	if cbor == "" {
		cbor = out.Tx
	}
	if cbor == "" {
		return "", fmt.Errorf("surf %s: empty cbor in response: %s", path, truncate(string(respBytes), 240))
	}
	return cbor, nil
}

// surfPoolRate returns totalSupplied / totalCToken for a pool — the raw
// underlying units per 1 fToken. Used to convert an underlying amount
// to the fToken count that withdrawLiquidity expects.
func (c *Client) surfPoolRate(ctx context.Context, poolID string) float64 {
	pools, err := fetchJSON[poolInfosResponse](ctx, c.http, c.base+poolInfosPath)
	if err != nil {
		return 0
	}
	if p, ok := pools.PoolInfos[poolID]; ok && p.TotalCToken > 0 {
		return p.TotalSupplied / p.TotalCToken
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *Client) BuildSupply(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	dec, err := c.surfDecimalsForPool(ctx, p.MarketID)
	if err != nil {
		return nil, err
	}
	body := surfTxRequest{
		PoolID:    p.MarketID,
		Address:   p.Address,
		Amount:    int64(math.Round(p.Amount * pow10(dec))),
		Canonical: false, // matches what Surf's own frontend sends
	}
	cbor, err := c.postTxBuild(ctx, depositLiquidityPath, body)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{
		Source: Name,
		Action: "supply",
		CBOR:   cbor,
		Hint:   fmt.Sprintf("Supply %.6f to Surf pool %s", p.Amount, truncate(p.MarketID, 16)),
	}, nil
}

func (c *Client) BuildWithdraw(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	// Surf's withdrawLiquidity expects the fToken (cToken) amount to
	// burn, NOT the underlying asset amount. Surf's own frontend sends
	// the raw fToken count (e.g. 185924576 fTokens, not 195311696
	// lovelace). Convert: fTokens = floor(underlyingRaw / rate), where
	// rate = totalSupplied / totalCToken (raw underlying per 1 fToken).
	dec, err := c.surfDecimalsForPool(ctx, p.MarketID)
	if err != nil {
		return nil, err
	}
	underlyingRaw := math.Round(p.Amount * pow10(dec))
	rate := c.surfPoolRate(ctx, p.MarketID)
	var fTokenAmount int64
	if rate > 0 {
		fTokenAmount = int64(math.Floor(underlyingRaw / rate))
	} else {
		fTokenAmount = int64(underlyingRaw)
	}
	if p.Full && fTokenAmount > 0 {
		// Safety: subtract 1 fToken to avoid overshoot from float
		// imprecision in the rate calculation.
		fTokenAmount--
	}
	log.Printf("surf withdraw: underlying=%.0f rate=%.6f full=%v → fTokenAmount=%d",
		underlyingRaw, rate, p.Full, fTokenAmount)
	body := surfTxRequest{
		PoolID:    p.MarketID,
		Address:   p.Address,
		Amount:    fTokenAmount,
		Canonical: false,
	}
	cbor, err := c.postTxBuild(ctx, withdrawLiqPath, body)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{
		Source: Name,
		Action: "withdraw",
		CBOR:   cbor,
		Hint:   fmt.Sprintf("Withdraw %.6f from Surf pool %s", p.Amount, truncate(p.MarketID, 16)),
	}, nil
}

// SubmitTx builds a fully signed tx CBOR by surgically merging the
// wallet's witness set into the original unsigned tx (preserving body
// and auxiliary_data bytes verbatim) then POSTs it to Surf's remote
// /api/wallet/submit endpoint.
//
// We deliberately skip Surf's /api/wallet/assemble endpoint — it
// mutates the auxiliary_data bytes while leaving the body's
// aux_data_hash unchanged, producing ConflictingMetadataHash on
// submit. Our own raw-CBOR merge keeps body AND aux data as opaque
// bytes, so nothing the node hashes is ever mutated.
func (c *Client) SubmitTx(ctx context.Context, p sources.SubmitParams) (string, error) {
	if p.CBOR == "" || p.WitnessSet == "" || p.Address == "" {
		return "", fmt.Errorf("surf submit: cbor, witnessSet, and address required")
	}

	// Surgical local merge — preserves body, aux data, scripts,
	// datums, redeemers byte-for-byte; only slot 0 (vkey witnesses)
	// grows with the wallet's fresh signatures.
	signed, err := sources.MergeTxWitnesses(p.CBOR, p.WitnessSet)
	if err != nil {
		return "", fmt.Errorf("surf merge witnesses: %w", err)
	}
	log.Printf("surf submit: merged witnesses, signed tx = %d bytes", len(signed)/2)

	// POST the fully signed tx directly to /api/wallet/submit.
	submitted, err := c.postWallet(ctx, "submit", map[string]any{
		"address": p.Address,
		"tx":      signed,
	})
	if err != nil {
		// "All inputs are spent" / "already been included" means the
		// tx already landed on chain on a previous attempt — this is
		// a duplicate submission, not a failure. Return a synthetic
		// "already-on-chain" marker so the UI can show a success state
		// and the user can refresh My Orders to see the new position.
		if isAlreadySubmittedError(err.Error()) {
			log.Printf("surf submit: tx already on chain (duplicate submission), treating as success")
			return "already-on-chain", nil
		}
		return "", fmt.Errorf("surf submit: %w", err)
	}
	var subBody struct {
		TxHash string `json:"txHash"`
		Hash   string `json:"hash"`
	}
	if err := json.Unmarshal(submitted, &subBody); err != nil {
		return "", fmt.Errorf("surf submit decode: %w", err)
	}
	hash := subBody.TxHash
	if hash == "" {
		hash = subBody.Hash
	}
	if hash == "" {
		return "", fmt.Errorf("surf submit: no txHash in response: %s", truncate(string(submitted), 240))
	}
	return hash, nil
}

// isAlreadySubmittedError detects the Conway-era "All inputs are spent"
// / "already been included" error message returned by cardano-node when
// you submit a transaction whose inputs were already consumed by a
// previous submission. This is a duplicate submission, not a failure.
func isAlreadySubmittedError(msg string) bool {
	return strings.Contains(msg, "All inputs are spent") ||
		strings.Contains(msg, "already been included") ||
		strings.Contains(msg, "BadInputsUTxO")
}

// postWallet posts JSON to /api/wallet/{action} and returns the raw
// response body for the caller to decode. Used by SubmitTx for the
// assemble + submit steps.
func (c *Client) postWallet(ctx context.Context, action string, body any) ([]byte, error) {
	wrapped, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := c.base + "/api/wallet/" + action
	logSurfCall("→ surf POST "+url, wrapped)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(wrapped))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("surf wallet/%s: %w", action, err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	logSurfCall(fmt.Sprintf("← surf %d wallet/%s", resp.StatusCode, action), respBytes)
	if resp.StatusCode != http.StatusOK {
		var errBody surfTxResponse
		if json.Unmarshal(respBytes, &errBody) == nil && errBody.Error != "" {
			return nil, fmt.Errorf("status %d: %s", resp.StatusCode, errBody.Error)
		}
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(respBytes), 240))
	}
	return respBytes, nil
}

// BuildClose builds a Surf tx that either repays an active position
// (via /api/repay) or cancels a pending batcher order (via
// /api/cancelOrder). Both endpoints take the SAME body shape and use
// the outRef to identify the target — they return an unsigned CBOR
// the user signs to authorize the close.
//
// No amount field: the protocol computes the exact repayment amount
// from the on-chain position itself, so a full repay is the only
// operation. Partial repay isn't supported on Surf v1.
func (c *Client) BuildClose(ctx context.Context, p sources.TxCloseParams) (*sources.BuiltTx, error) {
	if p.MarketID == "" || p.Address == "" || p.TxHash == "" {
		return nil, fmt.Errorf("surf close: marketId, address and txHash required")
	}
	kind := p.Kind
	if kind == "" {
		kind = "repay"
	}
	var path string
	switch kind {
	case "repay":
		path = "/api/repay"
	case "cancel":
		path = "/api/cancelOrder"
	default:
		return nil, fmt.Errorf("surf close: unknown kind %q", kind)
	}

	txHash := p.TxHash
	outIdx := p.OutputIndex

	// For repay: the cached outRef may be stale because the on-chain
	// UTxO moves when interest accrues or the position gets rebatched.
	// Re-fetch the current positions and find the live outRef for this
	// pool+address before calling the repay endpoint.
	if kind == "repay" {
		if fresh, err := c.freshOutRef(ctx, p.Address, p.MarketID); err == nil && fresh != nil {
			log.Printf("surf close: refreshed outRef %s#%d → %s#%d",
				txHash, outIdx, fresh.TxHash, fresh.OutputIndex)
			txHash = fresh.TxHash
			outIdx = fresh.OutputIndex
		}
	}

	body := map[string]any{
		"poolId":  p.MarketID,
		"address": p.Address,
		"outRef": map[string]any{
			"txHash":      txHash,
			"outputIndex": outIdx,
		},
		"canonical": false,
	}

	cbor, err := c.postTxBuild(ctx, path, body)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{
		Source: Name,
		Action: kind,
		CBOR:   cbor,
		Hint:   fmt.Sprintf("%s Surf position %s#%d", kind, truncate(txHash, 12), outIdx),
	}, nil
}

// freshOutRef queries /api/getAllPositions for the given address and
// returns the current outRef for the first position matching poolID.
// Returns nil if not found (caller falls back to the cached outRef).
func (c *Client) freshOutRef(ctx context.Context, address, poolID string) (*struct {
	TxHash      string
	OutputIndex int
}, error) {
	url := fmt.Sprintf("%s%s?address=%s", c.base, allPositionsPath, address)
	resp, err := fetchJSON[allPositionsResponse](ctx, c.http, url)
	if err != nil {
		return nil, err
	}
	for _, group := range resp.Positions {
		for _, p := range group.Positions {
			if p.PoolID == poolID {
				tx := p.OutRef.TxHash
				idx := p.OutRef.OutputIndex
				if tx == "" {
					tx = p.BorrowID.TxHash
					idx = p.BorrowID.OutputIndex
				}
				return &struct {
					TxHash      string
					OutputIndex int
				}{
					TxHash: tx, OutputIndex: idx,
				}, nil
			}
		}
	}
	return nil, nil
}

func (c *Client) BuildBorrow(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	dec, err := c.surfDecimalsForPool(ctx, p.MarketID)
	if err != nil {
		return nil, err
	}
	// Each collateral asset has its OWN decimals (SNEK = 0, NIGHT = 6,
	// etc.). Don't assume it matches the borrow asset's decimals.
	colDec, err := c.surfCollateralDecimalsForPool(ctx, p.MarketID)
	if err != nil {
		return nil, err
	}
	body := surfTxRequest{
		PoolID:           p.MarketID,
		Address:          p.Address,
		Amount:           int64(math.Round(p.Amount * pow10(dec))),
		CollateralAmount: int64(math.Round(p.CollateralAmount * pow10(colDec))),
		Canonical:        false, // matches what Surf's own frontend sends
	}
	cbor, err := c.postTxBuild(ctx, borrowPath, body)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{
		Source: Name,
		Action: "borrow",
		CBOR:   cbor,
		Hint:   fmt.Sprintf("Borrow %.6f from Surf pool %s", p.Amount, truncate(p.MarketID, 16)),
	}, nil
}
