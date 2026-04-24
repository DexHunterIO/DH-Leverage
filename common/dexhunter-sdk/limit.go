package dexhunter

import (
	"context"
	"net/http"
)

// EstimateLimitOrder quotes a limit order without building a tx.
func (c *Client) EstimateLimitOrder(ctx context.Context, req LimitOrderRequest) (*LimitOrderResponse, error) {
	var out LimitOrderResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/limitEstimate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BuildLimitOrder returns an unsigned limit-order CBOR ready for signing.
func (c *Client) BuildLimitOrder(ctx context.Context, req LimitOrderRequest) (*LimitOrderResponse, error) {
	var out LimitOrderResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/swap/limit", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
