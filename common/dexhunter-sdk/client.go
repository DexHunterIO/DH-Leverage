// Package dexhunter is a thin Go SDK for the DexHunter Partners API.
//
// DexHunter is a Cardano DEX aggregator. We use it to execute the swap
// leg of every leveraged trade: once a lending source has fronted the
// borrow, the engine quotes a route via /swap/estimate and executes it
// via /swap/swap → /swap/sign.
//
// The SDK is intentionally minimal — it wraps the public REST surface
// described at https://dexhunter.gitbook.io/dexhunter-partners and does
// not attempt to model every optional field. Add more as the engine
// needs them.
//
// Authentication: every call carries an `X-Partner-Id` header with the
// partner API key obtained from app.dexhunter.io/partners. Pass it to
// New() at construction time.
package dexhunter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultBase is the main DexHunter v3 API host.
	DefaultBase = "https://api-us.dexhunterv3.app"
	// DefaultChartsBase is the WebSocket-enabled charts API host.
	DefaultChartsBase = "https://charts.dhapi.io"

	partnerHeader = "X-Partner-Id"
)

// Client talks to the DexHunter Partners API. It is safe for concurrent
// use; the underlying *http.Client is the only mutable state.
type Client struct {
	base       string
	chartsBase string
	partnerID  string
	http       *http.Client
}

// New constructs a Client with the given partner API key. Pass an empty
// string only when calling endpoints that do not require auth (none of
// the partner endpoints currently allow this).
func New(partnerID string) *Client {
	return &Client{
		base:       DefaultBase,
		chartsBase: DefaultChartsBase,
		partnerID:  partnerID,
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

// WithBase overrides the main API host (e.g. to point at a staging
// deployment). Returns the receiver for chaining.
func (c *Client) WithBase(url string) *Client {
	c.base = url
	return c
}

// WithChartsBase overrides the charts API host.
func (c *Client) WithChartsBase(url string) *Client {
	c.chartsBase = url
	return c
}

// WithHTTPClient swaps in a custom *http.Client (e.g. one with a longer
// timeout, custom transport, or test stub).
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	c.http = hc
	return c
}

// APIError is returned when the DexHunter API responds with a non-2xx
// status. The body is captured verbatim so callers can surface it.
type APIError struct {
	Status int
	Body   string
	URL    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("dexhunter: %s -> %d: %s", e.URL, e.Status, e.Body)
}

// do executes an HTTP request against the chosen base URL, marshalling
// `body` (if non-nil) as JSON and decoding the response into `out` (if
// non-nil). It always sets the partner header.
func (c *Client) do(ctx context.Context, method, base, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	url := base + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.partnerID != "" {
		req.Header.Set(partnerHeader, c.partnerID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(respBody), URL: url}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
