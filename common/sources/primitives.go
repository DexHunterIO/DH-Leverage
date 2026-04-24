// Package sources defines the shared types every leverage source must
// implement, plus small helpers used by more than one source.
//
// A "source" is a Cardano lending/borrowing protocol (Liqwid, Levvy, Surf, …).
// Each source has its own subpackage with a Client implementing Source.
package sources

import "context"

// Asset identifies a Cardano native token. For ADA, PolicyID and AssetName
// are empty strings.
type Asset struct {
	PolicyID  string  `json:"policyId"`
	AssetName string  `json:"assetName"`
	Symbol    string  `json:"symbol"`
	Decimals  int     `json:"decimals"`
	PriceUsd  float64 `json:"priceUsd,omitempty"`
}

// Market is a normalized lending/borrowing market across every source.
//
// Some protocols (Liqwid) expose per-asset markets where any supplied asset
// can act as collateral for any other; in that shape Collateral is left zero
// and only Borrow is filled. Pair-isolated protocols (Surf) fill both.
//
// Depth fields are denominated in USD so markets are comparable across
// sources without dragging Cardano price data into the routing layer.
type Market struct {
	Source     string `json:"source"`
	PoolID     string `json:"poolId"`
	Collateral Asset  `json:"collateral"`
	Borrow     Asset  `json:"borrow"`
	// ReceiptAsset is the qToken / fToken / cToken minted to suppliers when
	// they deposit into this market. The frontend uses it to detect supply
	// positions by scanning the user's wallet — if the wallet holds this
	// asset, the user has a supply position in this market.
	ReceiptAsset Asset `json:"receiptAsset,omitempty"`
	// CollateralID is the protocol-internal identifier for the collateral
	// leg of this row. Liqwid uses it in BorrowTransactionInputCollateral
	// (shape "<qTokenMarketId>.<policyHash>", e.g. "Ada.a04ce7..."). Surf
	// doesn't need an explicit collateral id — collateral is inferred
	// from the pool. Optional; empty string if not applicable.
	CollateralID string `json:"collateralId,omitempty"`
	// SupplyExchangeRate is "whole borrow asset per 1 whole receipt token",
	// so the frontend can convert a receipt balance back to the underlying
	// amount the user supplied: underlying = receiptQty * supplyExchangeRate.
	SupplyExchangeRate    float64 `json:"supplyExchangeRate,omitempty"`
	TotalSuppliedUSD      float64 `json:"totalSuppliedUsd"`
	TotalBorrowedUSD      float64 `json:"totalBorrowedUsd"`
	AvailableLiquidityUSD float64 `json:"availableLiquidityUsd"`
	SupplyAPY             float64 `json:"supplyApy"`
	BorrowAPY             float64 `json:"borrowApy"`
	LTV                   float64 `json:"ltv"`
	LiquidationThreshold  float64 `json:"liquidationThreshold"`
	// MinSupply is the smallest whole-unit amount the protocol accepts
	// for a supply or borrow on this market. 0 = no minimum.
	MinSupply float64 `json:"minSupply,omitempty"`
	Active    bool    `json:"active"`
	URL       string  `json:"url,omitempty"`
}

// Order is a normalized lending/borrowing order from any source.
//
// Concrete shape varies per protocol — Liqwid exposes long-lived "loans"
// (Status="active"), Surf exposes a settled activity log (Status="closed").
// Pending in-batch orders ("pending") are reserved for protocols that
// expose a queue separately. Use Status to discriminate, and the optional
// fields to access whatever the protocol gave us.
type Order struct {
	Source           string  `json:"source"`
	ID               string  `json:"id"`
	Type             string  `json:"type"`   // borrow|repay|lend|withdraw|deposit|liquidation|leveragedBorrow|cancel|unknown
	Status           string  `json:"status"` // active|closed|pending
	Owner            string  `json:"owner"`  // pubkey hash (Liqwid) or bech32 (Surf)
	MarketID         string  `json:"marketId,omitempty"`
	Asset            Asset   `json:"asset"`
	Amount           float64 `json:"amount"`
	AmountUSD        float64 `json:"amountUsd,omitempty"`
	CollateralAsset  Asset   `json:"collateralAsset,omitempty"`
	CollateralAmount float64 `json:"collateralAmount,omitempty"`
	Interest         float64 `json:"interest,omitempty"`
	APY              float64 `json:"apy,omitempty"`
	LTV              float64 `json:"ltv,omitempty"`
	HealthFactor     float64 `json:"healthFactor,omitempty"`
	TxHash           string  `json:"txHash,omitempty"`
	// OutputIndex is the UTxO index paired with TxHash — together they
	// form the outRef that Surf's /api/repay and /api/cancelOrder use
	// to identify the position/order. Liqwid doesn't need it.
	OutputIndex int   `json:"outputIndex,omitempty"`
	TimeMs      int64 `json:"timeMs"`
}

// OrderQuery is a best-effort filter set; sources ignore options they don't
// support natively. A zero query means "everything available, source default
// page size".
type OrderQuery struct {
	Address string // pubkey hash or bech32 — meaning is source-specific
	Limit   int    // 0 = source default
	// Refresh bypasses the orders cache for this specific call. Used by
	// the My Orders refresh button so a freshly submitted tx shows up
	// without waiting for the cache to expire.
	Refresh bool
}

// Order status / type constants — constants exist mainly so callers can
// switch on them safely. Sources are free to emit other values.
const (
	OrderActive  = "active"
	OrderClosed  = "closed"
	OrderPending = "pending"

	TypeBorrow          = "borrow"
	TypeRepay           = "repay"
	TypeRepayCollateral = "repayWithCollateral"
	TypeLend            = "lend"
	TypeDeposit         = "deposit"
	TypeWithdraw        = "withdraw"
	TypeCancelWithdraw  = "cancelWithdraw"
	TypeLeveragedBorrow = "leveragedBorrow"
	TypeLiquidation     = "liquidation"
	TypeUnknown         = "unknown"
)

// Source is a leverage source. Each implementation lives in its own
// subpackage so the routing engine can depend on this interface alone.
type Source interface {
	Name() string
	FetchMarkets(ctx context.Context) ([]Market, error)
	FetchOrders(ctx context.Context, q OrderQuery) ([]Order, error)
}

// ErrUnsupportedTxBuilder is returned when a wrapped source doesn't
// implement the TxBuilder interface (used by Cached / Persisted wrappers).
var ErrUnsupportedTxBuilder = errSentinel("source does not implement TxBuilder")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
