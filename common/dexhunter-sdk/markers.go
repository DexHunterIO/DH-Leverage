package dexhunter

import (
	"context"
	"net/http"
)

// SubmitMarker tags a transaction with its order type so DexHunter can
// surface it correctly in their UI / order history. Call this after
// Sign() succeeds for limit, stop-loss, DCA, or swap transactions.
//
// Returns the raw string the API replies with (typically an ack id).
func (c *Client) SubmitMarker(ctx context.Context, req MarkerRequest) (string, error) {
	var out string
	if err := c.do(ctx, http.MethodPost, c.base, "/marking/submit", req, &out); err != nil {
		return "", err
	}
	return out, nil
}
