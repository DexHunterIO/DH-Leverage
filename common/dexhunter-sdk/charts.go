package dexhunter

import (
	"context"
	"net/http"
)

// Chart fetches OHLCV candlesticks for a token pair. The Charts API
// lives on a separate host (DefaultChartsBase) but uses the same
// partner-id auth.
func (c *Client) Chart(ctx context.Context, req ChartRequest) (*ChartResponse, error) {
	var out ChartResponse
	if err := c.do(ctx, http.MethodPost, c.chartsBase, "/charts", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
