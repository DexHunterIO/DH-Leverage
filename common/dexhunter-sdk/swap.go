package dexhunter

import (
	"context"
	"net/http"
	"net/url"
)

// EstimateSwap quotes a forward swap (fixed input → estimated output)
// without building a transaction. Use this for routing decisions.
func (c *Client) EstimateSwap(ctx context.Context, req EstimateRequest) (*EstimateResponse, error) {
	var out EstimateResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/estimate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReverseEstimateSwap quotes a reverse swap (fixed output → required
// input). Useful when the engine needs to size a swap to repay an exact
// borrow amount.
func (c *Client) ReverseEstimateSwap(ctx context.Context, req ReverseEstimateRequest) (*ReverseEstimateResponse, error) {
	var out ReverseEstimateResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/reverseEstimate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BuildSwap returns an unsigned swap transaction CBOR. Sign it with the
// user's wallet and pass the witnesses to Sign().
func (c *Client) BuildSwap(ctx context.Context, req SwapRequest) (*SwapResponse, error) {
	var out SwapResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/swap", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Sign submits a witnessed swap transaction. The returned strat_id can
// be used to track the order through /swap/orders.
func (c *Client) Sign(ctx context.Context, req SignRequest) (*SignResponse, error) {
	var out SignResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/sign", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelSwap builds a cancellation transaction for an open order. The
// caller must sign and submit the returned CBOR via Sign().
func (c *Client) CancelSwap(ctx context.Context, req CancelRequest) (*CancelResponse, error) {
	var out CancelResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/cancel", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AveragePrice returns the rolling average price between two tokens
// keyed by interval (e.g. "1h", "24h", "7d"). Map shape is determined
// by DexHunter — pass through as-is.
func (c *Client) AveragePrice(ctx context.Context, tokenIn, tokenOut string) (map[string]float64, error) {
	out := map[string]float64{}
	path := "/swap/averagePrice/" + url.PathEscape(tokenIn) + "/" + url.PathEscape(tokenOut)
	if err := c.do(ctx, http.MethodGet, c.base, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Orders returns the swap/limit/DCA order history for a wallet address.
func (c *Client) Orders(ctx context.Context, userAddress string) ([]Order, error) {
	var out []Order
	path := "/swap/orders/" + url.PathEscape(userAddress)
	if err := c.do(ctx, http.MethodGet, c.base, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Wallet returns the ADA + token balances for one or more wallet
// addresses. DexHunter aggregates the addresses server-side.
func (c *Client) Wallet(ctx context.Context, addresses ...string) (*WalletInfoResponse, error) {
	var out WalletInfoResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/wallet", WalletRequest{Addresses: addresses}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
