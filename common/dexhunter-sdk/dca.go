package dexhunter

import (
	"context"
	"net/http"
	"net/url"
)

// EstimateDCA projects a DCA schedule without committing to it.
func (c *Client) EstimateDCA(ctx context.Context, req CreateDCARequest) (*CreateDCAResponse, error) {
	var out CreateDCAResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/dca/estimate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDCA builds a DCA schedule transaction. Sign and submit via Sign().
func (c *Client) CreateDCA(ctx context.Context, req CreateDCARequest) (*CreateDCAResponse, error) {
	var out CreateDCAResponse
	if err := c.do(ctx, http.MethodPost, c.base, "/dca/create", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelDCA builds a cancel-DCA transaction. The endpoint returns a
// loose key/value object so the SDK exposes it as a generic map.
func (c *Client) CancelDCA(ctx context.Context, req CancelDCARequest) (map[string]string, error) {
	out := map[string]string{}
	if err := c.do(ctx, http.MethodPost, c.base, "/dca/cancel", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListDCA returns the active DCA schedules for a wallet address.
func (c *Client) ListDCA(ctx context.Context, address string) ([]DCAOrder, error) {
	var out []DCAOrder
	path := "/dca/" + url.PathEscape(address)
	if err := c.do(ctx, http.MethodGet, c.base, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
