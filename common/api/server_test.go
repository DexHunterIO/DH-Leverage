package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"dh-leverage/common/sources"
	"dh-leverage/common/wallet"
)

// mockSource implements sources.Source for testing.
type mockSource struct {
	name    string
	markets []sources.Market
	orders  []sources.Order
	err     error
}

func (m *mockSource) Name() string { return m.name }
func (m *mockSource) FetchMarkets(_ context.Context) ([]sources.Market, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.markets, nil
}
func (m *mockSource) FetchOrders(_ context.Context, _ sources.OrderQuery) ([]sources.Order, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.orders, nil
}

// mockTxSource implements both Source and TxBuilder for testing.
type mockTxSource struct {
	mockSource
	builtTx   *sources.BuiltTx
	txHash    string
	buildErr  error
	submitErr error
}

func (m *mockTxSource) BuildSupply(_ context.Context, _ sources.TxParams) (*sources.BuiltTx, error) {
	return m.builtTx, m.buildErr
}
func (m *mockTxSource) BuildWithdraw(_ context.Context, _ sources.TxParams) (*sources.BuiltTx, error) {
	return m.builtTx, m.buildErr
}
func (m *mockTxSource) BuildBorrow(_ context.Context, _ sources.TxParams) (*sources.BuiltTx, error) {
	return m.builtTx, m.buildErr
}
func (m *mockTxSource) BuildClose(_ context.Context, _ sources.TxCloseParams) (*sources.BuiltTx, error) {
	return m.builtTx, m.buildErr
}
func (m *mockTxSource) SubmitTx(_ context.Context, _ sources.SubmitParams) (string, error) {
	return m.txHash, m.submitErr
}

func newTestServer(srcs ...sources.Source) *Server {
	w := wallet.New(nil, 0)
	return New(":0", w, srcs...)
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/api/health", nil)
	resp, err := srv.App().Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]string
	json.Unmarshal(body, &result)
	if result["status"] != "ok" {
		t.Fatalf("expected ok, got %s", result["status"])
	}
}

func TestMarketsEndpoint(t *testing.T) {
	src := &mockSource{
		name: "test",
		markets: []sources.Market{
			{Source: "test", PoolID: "p1", Borrow: sources.Asset{Symbol: "ADA"}},
			{Source: "test", PoolID: "p2", Borrow: sources.Asset{Symbol: "SNEK"}},
		},
	}
	srv := newTestServer(src)

	req := httptest.NewRequest("GET", "/api/markets", nil)
	resp, err := srv.App().Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result marketsResponse
	json.Unmarshal(body, &result)
	if len(result.Markets) != 2 {
		t.Fatalf("expected 2 markets, got %d", len(result.Markets))
	}
	if result.Sources["test"] != "" {
		t.Fatalf("expected empty error, got %q", result.Sources["test"])
	}
}

func TestMarketsEndpoint_SourceFilter(t *testing.T) {
	src1 := &mockSource{
		name:    "liqwid",
		markets: []sources.Market{{Source: "liqwid", PoolID: "p1"}},
	}
	src2 := &mockSource{
		name:    "surf",
		markets: []sources.Market{{Source: "surf", PoolID: "p2"}},
	}
	srv := newTestServer(src1, src2)

	req := httptest.NewRequest("GET", "/api/markets?source=liqwid", nil)
	resp, _ := srv.App().Test(req)
	body, _ := io.ReadAll(resp.Body)
	var result marketsResponse
	json.Unmarshal(body, &result)

	if len(result.Markets) != 1 || result.Markets[0].Source != "liqwid" {
		t.Fatalf("expected 1 liqwid market, got %+v", result.Markets)
	}
}

func TestMarketsEndpoint_TokenFilter(t *testing.T) {
	src := &mockSource{
		name: "test",
		markets: []sources.Market{
			{Source: "test", Borrow: sources.Asset{Symbol: "ADA"}},
			{Source: "test", Borrow: sources.Asset{Symbol: "SNEK"}},
		},
	}
	srv := newTestServer(src)

	req := httptest.NewRequest("GET", "/api/markets?token=SNEK", nil)
	resp, _ := srv.App().Test(req)
	body, _ := io.ReadAll(resp.Body)
	var result marketsResponse
	json.Unmarshal(body, &result)

	if len(result.Markets) != 1 || result.Markets[0].Borrow.Symbol != "SNEK" {
		t.Fatalf("expected 1 SNEK market, got %+v", result.Markets)
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ":8080"},
		{"8080", ":8080"},
		{":8080", ":8080"},
		{"127.0.0.1:3000", "127.0.0.1:3000"},
	}
	for _, tc := range cases {
		got := normalizeAddr(tc.in)
		if got != tc.want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFilterByToken(t *testing.T) {
	ms := []sources.Market{
		{Borrow: sources.Asset{Symbol: "ADA"}},
		{Borrow: sources.Asset{Symbol: "SNEK"}},
		{Collateral: sources.Asset{Symbol: "ADA"}},
	}

	// Filter by SNEK should return only the SNEK row.
	got := filterByToken(ms, "snek")
	if len(got) != 1 {
		t.Fatalf("expected 1 SNEK market, got %d", len(got))
	}
	if got[0].Borrow.Symbol != "SNEK" {
		t.Fatalf("expected SNEK, got %s", got[0].Borrow.Symbol)
	}
}

func TestMatchesToken(t *testing.T) {
	ada := sources.Asset{Symbol: "ADA"}
	snek := sources.Asset{Symbol: "SNEK", PolicyID: "abc123", AssetName: "534e454b"}

	if !matchesToken(ada, "ada") {
		t.Error("ADA should match 'ada'")
	}
	if !matchesToken(snek, "snek") {
		t.Error("SNEK should match 'snek' (case insensitive)")
	}
	if !matchesToken(snek, "abc123") {
		t.Error("SNEK should match policy ID")
	}
	if !matchesToken(snek, "abc123534e454b") {
		t.Error("SNEK should match full unit")
	}
	if matchesToken(snek, "DJED") {
		t.Error("SNEK should not match DJED")
	}
}

func TestIndexEndpoint(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/", nil)
	resp, err := srv.App().Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "" {
		t.Fatal("missing content-type")
	}
	if cc := resp.Header.Get("Cache-Control"); cc == "" {
		t.Fatal("missing Cache-Control header")
	}
}

// --- Orders endpoint ---

func TestOrdersEndpoint(t *testing.T) {
	src := &mockSource{
		name: "test",
		orders: []sources.Order{
			{Source: "test", ID: "o1", Type: "borrow", Status: "active", Asset: sources.Asset{Symbol: "ADA"}},
		},
	}
	srv := newTestServer(src)
	req := httptest.NewRequest("GET", "/api/orders?source=test&address=abc", nil)
	resp, err := srv.App().Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var result ordersResponse
	json.Unmarshal(body, &result)
	if len(result.Orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(result.Orders))
	}
}

func TestOrdersEndpoint_SourceFilter(t *testing.T) {
	src1 := &mockSource{name: "liqwid", orders: []sources.Order{{Source: "liqwid", ID: "1"}}}
	src2 := &mockSource{name: "surf", orders: []sources.Order{{Source: "surf", ID: "2"}}}
	srv := newTestServer(src1, src2)

	req := httptest.NewRequest("GET", "/api/orders?source=surf&address=x", nil)
	resp, _ := srv.App().Test(req)
	body, _ := io.ReadAll(resp.Body)
	var result ordersResponse
	json.Unmarshal(body, &result)
	if len(result.Orders) != 1 || result.Orders[0].Source != "surf" {
		t.Fatalf("expected 1 surf order, got %+v", result.Orders)
	}
}

func TestOrdersEndpoint_SourceError(t *testing.T) {
	src := &mockSource{name: "test", err: errors.New("upstream down")}
	srv := newTestServer(src)
	req := httptest.NewRequest("GET", "/api/orders?source=test&address=x", nil)
	resp, _ := srv.App().Test(req)
	body, _ := io.ReadAll(resp.Body)
	var result ordersResponse
	json.Unmarshal(body, &result)
	if result.Sources["test"] == "" {
		t.Fatal("expected error in sources map")
	}
}

// --- Tx Build endpoint ---

func TestTxBuild_MissingSource(t *testing.T) {
	srv := newTestServer()
	body := `{"amount":100,"address":"addr1xyz"}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTxBuild_MissingAmount(t *testing.T) {
	src := &mockTxSource{mockSource: mockSource{name: "test"}}
	srv := newTestServer(src)
	body := `{"source":"test","address":"addr1xyz","amount":0}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTxBuild_UnknownSource(t *testing.T) {
	srv := newTestServer()
	body := `{"source":"unknown","address":"addr1xyz","amount":100}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestTxBuild_Success(t *testing.T) {
	src := &mockTxSource{
		mockSource: mockSource{name: "test"},
		builtTx:    &sources.BuiltTx{Source: "test", Action: "supply", CBOR: "abcdef"},
	}
	srv := newTestServer(src)
	body := `{"source":"test","address":"addr1xyz","amount":100}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}
	b, _ := io.ReadAll(resp.Body)
	var result sources.BuiltTx
	json.Unmarshal(b, &result)
	if result.CBOR != "abcdef" {
		t.Fatalf("expected cbor=abcdef, got %q", result.CBOR)
	}
}

func TestTxBuild_BuildError(t *testing.T) {
	src := &mockTxSource{
		mockSource: mockSource{name: "test"},
		buildErr:   errors.New("insufficient funds"),
	}
	srv := newTestServer(src)
	body := `{"source":"test","address":"addr1xyz","amount":100}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 502 {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestTxBuild_NoTxBuilder(t *testing.T) {
	// mockSource (without TxBuilder) should return 501
	src := &mockSource{name: "test"}
	srv := newTestServer(src)
	body := `{"source":"test","address":"addr1xyz","amount":100}`
	req := httptest.NewRequest("POST", "/api/tx/supply", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 501 {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
}

// --- Tx Close endpoint ---

func TestTxClose_MissingFields(t *testing.T) {
	srv := newTestServer()
	body := `{"source":"test"}`
	req := httptest.NewRequest("POST", "/api/tx/close", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTxClose_Success(t *testing.T) {
	src := &mockTxSource{
		mockSource: mockSource{name: "test"},
		builtTx:    &sources.BuiltTx{Source: "test", Action: "repay", CBOR: "deadbeef"},
	}
	srv := newTestServer(src)
	body := `{"source":"test","marketId":"pool1","address":"addr1xyz","txHash":"abc123","kind":"repay"}`
	req := httptest.NewRequest("POST", "/api/tx/close", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}
}

// --- Tx Submit endpoint ---

func TestTxSubmit_MissingFields(t *testing.T) {
	srv := newTestServer()
	body := `{"source":"test"}`
	req := httptest.NewRequest("POST", "/api/tx/submit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTxSubmit_Success(t *testing.T) {
	src := &mockTxSource{
		mockSource: mockSource{name: "test"},
		txHash:     "submitted123",
	}
	srv := newTestServer(src)
	body := `{"source":"test","cbor":"aabb","witnessSet":"ccdd"}`
	req := httptest.NewRequest("POST", "/api/tx/submit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}
	b, _ := io.ReadAll(resp.Body)
	var result map[string]string
	json.Unmarshal(b, &result)
	if result["txHash"] != "submitted123" {
		t.Fatalf("expected txHash=submitted123, got %q", result["txHash"])
	}
}

func TestTxSubmit_Error(t *testing.T) {
	src := &mockTxSource{
		mockSource: mockSource{name: "test"},
		submitErr:  errors.New("node rejected"),
	}
	srv := newTestServer(src)
	body := `{"source":"test","cbor":"aabb","witnessSet":"ccdd"}`
	req := httptest.NewRequest("POST", "/api/tx/submit", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := srv.App().Test(req)
	if resp.StatusCode != 502 {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

// --- Markets by token path ---

func TestMarketsByTokenPath(t *testing.T) {
	src := &mockSource{
		name: "test",
		markets: []sources.Market{
			{Source: "test", Borrow: sources.Asset{Symbol: "ADA"}},
			{Source: "test", Borrow: sources.Asset{Symbol: "SNEK"}},
		},
	}
	srv := newTestServer(src)
	req := httptest.NewRequest("GET", "/api/markets/by-token/SNEK", nil)
	resp, _ := srv.App().Test(req)
	body, _ := io.ReadAll(resp.Body)
	var result marketsResponse
	json.Unmarshal(body, &result)
	if len(result.Markets) != 1 || result.Markets[0].Borrow.Symbol != "SNEK" {
		t.Fatalf("expected 1 SNEK market via path, got %+v", result.Markets)
	}
}
