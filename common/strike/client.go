// Package strike is a client for the Strike Finance v2 perpetuals API
// (https://api.strikefinance.org) using the BUILDER CODES model — the right fit
// for a multi-user dApp. Each user connects their wallet once; Strike issues a
// per-user "API wallet" (Ed25519) linked to our builder code, and we trade /
// deposit / withdraw on their behalf, earning fee BPS on fills.
//
// Onboarding (per user, once):
//  1. GenerateKeyPair → Ed25519 keypair for this user.
//  2. RequestSignature{address,chain,code,public_key,fee_share_bps} → {nonce, message_to_sign}.
//  3. User signs message_to_sign in their wallet (CIP-30 → "{coseSign1Hex}:{coseKeyHex}").
//  4. VerifySignature{nonce, wallet_signature} → {account_id, ...}. Store the
//     keypair + account_id (encrypted) keyed by the user's address.
//
// Thereafter, authenticated calls are signed with that user's Ed25519 key via the
// X-API-Wallet-* headers (message "{METHOD}:{PATH}:{TS}:{NONCE}:{hex(sha256(body))}"),
// and orders carry X-Builder-Fee-Bps to collect the fee.
package strike

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	Name = "strike"
	// DefaultBase is the official v2 API host.
	DefaultBase = "https://api.strikefinance.org"
	// ChainCardano is the chain value for Cardano wallets.
	ChainCardano = "cardano"
)

// Client talks to the Strike v2 builder API. It holds the builder-level config
// (code + fee); per-user auth uses a Credential passed per call.
type Client struct {
	base           string
	http           *http.Client
	builderCode    string
	feeBps         int
	withdrawLeader string // batcher leaderAddress for the withdraw-batcher tx
}

// Credential is a connected user's API-wallet key + Strike account, used to sign
// that user's requests. PrivateKey is the Ed25519 key our backend generated and
// the user authorized via the connect flow.
type Credential struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  string // hex
	AccountID  string
}

// New builds a Client. builderCode + feeBps come from registering at
// app.strikefinance.org/builder-codes (feeBps ≤ your registered share, ≤100).
func New(builderCode string, feeBps int) *Client {
	return &Client{
		base:        DefaultBase,
		http:        &http.Client{Timeout: 30 * time.Second},
		builderCode: builderCode,
		feeBps:      feeBps,
	}
}

// WithBase overrides the API host (official host only).
func (c *Client) WithBase(base string) *Client {
	if base != "" {
		c.base = base
	}
	return c
}

// BuilderEnabled reports whether a builder code is configured (trading possible).
func (c *Client) BuilderEnabled() bool { return c.builderCode != "" }

// WithWithdrawLeader sets the batcher leaderAddress used by WithdrawBatcher.
func (c *Client) WithWithdrawLeader(addr string) *Client { c.withdrawLeader = addr; return c }

// WithdrawLeader returns the configured batcher leaderAddress.
func (c *Client) WithdrawLeader() string { return c.withdrawLeader }

// FeeBps is the builder fee in basis points sent on orders.
func (c *Client) FeeBps() int { return c.feeBps }

// GenerateKeyPair returns a fresh Ed25519 API-wallet key pair as hex (private =
// 64-char seed, public = 64-char) for a user being onboarded.
func GenerateKeyPair() (privateSeedHex, publicHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub), nil
}

// CredentialFromHex rebuilds a Credential from a stored 32-byte seed (64 hex).
func CredentialFromHex(seedHex, publicHex, accountID string) (*Credential, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("strike: bad credential seed (want 32-byte hex)")
	}
	return &Credential{
		PrivateKey: ed25519.NewKeyFromSeed(seed),
		PublicKey:  publicHex,
		AccountID:  accountID,
	}, nil
}

// --- onboarding (builder connect) --------------------------------------------

// RequestSignatureResponse is /auth/builder/request-signature's reply.
type RequestSignatureResponse struct {
	Nonce         string `json:"nonce"`
	MessageToSign string `json:"message_to_sign"`
	Message       string `json:"message,omitempty"`
}

// RequestSignature starts the connect flow: it asks Strike for a challenge the
// user must sign, binding our builder code + the generated public key to the
// user's address. No auth (this IS the auth bootstrap).
func (c *Client) RequestSignature(ctx context.Context, address, chain, publicKeyHex string, feeBps int) (*RequestSignatureResponse, error) {
	if c.builderCode == "" {
		return nil, fmt.Errorf("strike: builder code not configured (STRIKE_BUILDER_CODE)")
	}
	body := map[string]any{
		"address":       address,
		"chain":         chain,
		"code":          c.builderCode,
		"public_key":    publicKeyHex,
		"fee_share_bps": feeBps, // the LIVE API uses this (rejects max_fee_bps as unknown; the reference repo is a different version)
	}
	raw, err := c.do(ctx, http.MethodPost, "/auth/builder/request-signature", body, nil, 0)
	if err != nil {
		return nil, err
	}
	var out RequestSignatureResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("strike: decode request-signature: %w", err)
	}
	return &out, nil
}

// VerifySignatureResponse is /auth/builder/verify-signature's reply.
type VerifySignatureResponse struct {
	AccountID          string `json:"account_id"`
	BuilderCode        string `json:"builder_code"`
	FeeShareBps        int    `json:"fee_share_bps"`
	APIWalletID        int64  `json:"api_wallet_id"`
	APIWalletPublicKey string `json:"api_wallet_public_key"`
}

// VerifySignature completes the connect flow: the user's wallet signature over
// message_to_sign proves they authorized our key. walletSignature for Cardano is
// the CIP-30 COSE pair "{coseSign1Hex}:{coseKeyHex}". Returns the user's account.
func (c *Client) VerifySignature(ctx context.Context, nonce, walletSignature string) (*VerifySignatureResponse, error) {
	// The live API takes exactly {nonce, wallet_signature} and rejects unknown
	// fields (the reference repo's address/chain are a different API version).
	body := map[string]any{"nonce": nonce, "wallet_signature": walletSignature}
	raw, err := c.do(ctx, http.MethodPost, "/auth/builder/verify-signature", body, nil, 0)
	if err != nil {
		return nil, err
	}
	var out VerifySignatureResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("strike: decode verify-signature: %w", err)
	}
	return &out, nil
}

// --- public reads (no auth) --------------------------------------------------

// Positions returns open positions for a blockchain address (no auth).
func (c *Client) Positions(ctx context.Context, blockchainAddress string) (json.RawMessage, error) {
	q := url.Values{"blockchain_address": {blockchainAddress}}
	return c.do(ctx, http.MethodGet, "/v2/positions?"+q.Encode(), nil, nil, 0)
}

// Account returns account info for a blockchain address (no auth).
func (c *Client) Account(ctx context.Context, blockchainAddress string) (json.RawMessage, error) {
	q := url.Values{"blockchain_address": {blockchainAddress}}
	return c.do(ctx, http.MethodGet, "/v2/account?"+q.Encode(), nil, nil, 0)
}

// Markets lists all tradeable perp markets (symbol like "ADA-USD", base asset,
// default leverage, …). Public, no auth.
func (c *Client) Markets(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v2/markets", nil, nil, 0)
}

// --- authenticated trading (per-user credential) -----------------------------

// CreateOrderRequest is the body for POST /v2/order. Market buy = open/increase
// long, market sell = open/increase short; close with reduce_only + opposite side.
type CreateOrderRequest struct {
	Symbol      string `json:"symbol"`
	Side        string `json:"side"` // "buy" | "sell"
	Type        string `json:"type"` // "market" | "limit" | ...
	Size        string `json:"size"`
	Price       string `json:"price,omitempty"`
	TimeInForce string `json:"time_in_force,omitempty"`
	ReduceOnly  bool   `json:"reduce_only,omitempty"`
	ClientID    string `json:"client_order_id,omitempty"`
}

// SetLeverage sets leverage for a symbol (1..125), as the user.
func (c *Client) SetLeverage(ctx context.Context, cred *Credential, symbol string, leverage int) (json.RawMessage, error) {
	body := map[string]any{"symbol": symbol, "leverage": leverage}
	return c.do(ctx, http.MethodPost, "/v2/leverage", body, cred, 0)
}

// CreateOrder places an order as the user, carrying the builder fee.
func (c *Client) CreateOrder(ctx context.Context, cred *Credential, req CreateOrderRequest) (json.RawMessage, error) {
	if req.Symbol == "" || req.Side == "" || req.Type == "" || req.Size == "" {
		return nil, fmt.Errorf("strike: order needs symbol, side, type, size")
	}
	return c.do(ctx, http.MethodPost, "/v2/order", req, cred, c.feeBps)
}

// CancelOrder cancels an open order as the user.
func (c *Client) CancelOrder(ctx context.Context, cred *Credential, orderID int64, symbol string) (json.RawMessage, error) {
	body := map[string]any{"order_id": orderID, "symbol": symbol}
	return c.do(ctx, http.MethodPost, "/v2/order/cancel", body, cred, 0)
}

// --- deposit (3-step: quote → build-tx → confirm) ----------------------------

// DepositQuote requests a deposit quote (asset_amount in lovelace, as string).
func (c *Client) DepositQuote(ctx context.Context, cred *Credential, lovelace string) (json.RawMessage, error) {
	body := map[string]any{"blockchain": ChainCardano, "asset_symbol": "ADA", "asset_amount": lovelace}
	return c.do(ctx, http.MethodPost, "/v2/deposit/quote", body, cred, 0)
}

// DepositBuildTx builds the unsigned deposit tx (returns {unsigned_tx: cbor hex}).
func (c *Client) DepositBuildTx(ctx context.Context, cred *Credential, requestID, userAddress string, utxos []string) (json.RawMessage, error) {
	body := map[string]any{"request_id": requestID, "user_address": userAddress, "utxos": utxos}
	return c.do(ctx, http.MethodPost, "/v2/deposit/build-tx", body, cred, 0)
}

// DepositConfirm confirms a deposit by its on-chain tx hash.
func (c *Client) DepositConfirm(ctx context.Context, cred *Credential, requestID, txHash string) (json.RawMessage, error) {
	body := map[string]any{"request_id": requestID, "tx_hash": txHash}
	return c.do(ctx, http.MethodPost, "/v2/deposit", body, cred, 0)
}

// --- balances & withdraw -----------------------------------------------------

// Balances returns the connected user's account balances (walletBalance,
// marginBalance, unrealizedPnl, …). Authenticated.
func (c *Client) Balances(ctx context.Context, cred *Credential) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v2/balances", nil, cred, 0)
}

// WithdrawQuote quotes a withdrawal of usdValue (USD, string). asset is the
// settlement asset on Cardano (e.g. "USDM"/"USDC"/"ADA") — a USD-margined account
// withdraws a stablecoin; the live API rejects ADA as "unsupported asset".
// recipientAddress is the user's wallet (the live app sends it, despite the docs
// saying it's not accepted — the backend locks the registered address regardless).
// Returns {withdraw_id, fee, message_to_sign}. Authenticated.
func (c *Client) WithdrawQuote(ctx context.Context, cred *Credential, usdValue, asset, recipientAddress string) (json.RawMessage, error) {
	body := map[string]any{
		"usd_value":         usdValue,
		"blockchain":        ChainCardano,
		"recipient_address": recipientAddress,
	}
	if asset != "" {
		body["asset"] = asset
	}
	return c.do(ctx, http.MethodPost, "/v2/withdraw/quote", body, cred, 0)
}

// WithdrawConfirm submits the signed withdrawal. walletSignature is the user's
// CIP-30 signature over the quote's message_to_sign. Authenticated.
func (c *Client) WithdrawConfirm(ctx context.Context, cred *Credential, withdrawID, walletSignature string) (json.RawMessage, error) {
	body := map[string]any{"withdraw_id": withdrawID, "wallet_signature": walletSignature}
	return c.do(ctx, http.MethodPost, "/v2/withdraw", body, cred, 0)
}

// appBase is Strike's app host, which serves the perpetuals batcher endpoint used
// to build the on-chain withdrawal tx (this lives on app.strikefinance.org, NOT
// the api.strikefinance.org v2 host).
const appBase = "https://app.strikefinance.org"

// WithdrawBatcher builds the on-chain batcher tx that settles a confirmed
// withdrawal: POST app.strikefinance.org/api/perpetuals/withdraw-batcher with the
// user's address + the batcher leaderAddress + the user's CIP-30 UTxOs. Returns
// the response (contains txData: an unsigned tx CBOR the user signs and submits).
// Unauthenticated (v1-style endpoint).
func (c *Client) WithdrawBatcher(ctx context.Context, address, leaderAddress string, utxos []string) (json.RawMessage, error) {
	body := map[string]any{"address": address, "leaderAddress": leaderAddress, "utxos": utxos}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := appBase + "/api/perpetuals/withdraw-batcher"
	log.Printf("strike: POST %s body=%s", u, truncate(buf, 600))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("strike withdraw-batcher: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("strike withdraw-batcher: status %d: %s", resp.StatusCode, truncate(respBody, 400))
	}
	return json.RawMessage(respBody), nil
}

// --- transport ---------------------------------------------------------------

// do performs a request, signing with cred (when non-nil) and adding the builder
// fee header when feeBps > 0 (orders). Returns the raw response body.
func (c *Client) do(ctx context.Context, method, path string, body any, cred *Credential, feeBps int) (json.RawMessage, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("strike: marshal: %w", err)
		}
		bodyBytes = b
	}
	u := c.base + path
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if len(bodyBytes) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if cred != nil {
		for k, v := range signHeaders(cred, method, path, bodyBytes) {
			req.Header.Set(k, v)
		}
	}
	if feeBps > 0 {
		req.Header.Set("X-Builder-Fee-Bps", strconv.Itoa(feeBps))
	}
	log.Printf("strike: %s %s body=%s", method, u, truncate(bodyBytes, 2000))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("strike: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("strike: %s %s -> %d resp=%s", method, u, resp.StatusCode, truncate(respBody, 600))
		return nil, fmt.Errorf("strike: %s %s status %d: %s", method, path, resp.StatusCode, truncate(respBody, 400))
	}
	return json.RawMessage(respBody), nil
}

// signHeaders builds the X-API-Wallet-* headers for a credential.
func signHeaders(cred *Credential, method, path string, bodyBytes []byte) map[string]string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuidV4()
	bodyHash := sha256.Sum256(bodyBytes) // sha256 of "" for empty bodies
	msg := strings.ToUpper(method) + ":" + path + ":" + ts + ":" + nonce + ":" + hex.EncodeToString(bodyHash[:])
	sig := ed25519.Sign(cred.PrivateKey, []byte(msg))
	return map[string]string{
		"X-API-Wallet-Public-Key": cred.PublicKey,
		"X-API-Wallet-Signature":  hex.EncodeToString(sig),
		"X-API-Wallet-Timestamp":  ts,
		"X-API-Wallet-Nonce":      nonce,
	}
}

// uuidV4 returns a random UUID v4 (the unique per-request nonce).
func uuidV4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
