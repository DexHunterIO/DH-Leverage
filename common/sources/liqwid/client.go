// Package liqwid is the data-gathering pipe for the Liqwid lending protocol.
//
// Liqwid v2 exposes a public GraphQL API at https://v2.api.liqwid.finance/graphql.
// The schema is namespaced under the `liqwid` root field; the markets list
// lives at `liqwid.data.markets.results`. Per-asset prices, supply/borrow
// depth and APYs come back already denominated in whole units of the asset
// (NOT smallest units), so the Cardano decimals don't need to be applied.
//
// Schema discovered via introspection on 2026-04. We use the transactions
// namespace for supply/withdraw/borrow/modifyBorrow tx building, and the
// root submitTransaction mutation for tx submission (with Koios fallback).
package liqwid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Name             = "liqwid"
	defaultEndpoint  = "https://v2.api.liqwid.finance/graphql"
	defaultMarketURL = "https://app.liqwid.finance/markets"
)

type Client struct {
	endpoint string
	http     *http.Client
}

func New() *Client {
	return &Client{
		endpoint: defaultEndpoint,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// WithEndpoint overrides the GraphQL endpoint (e.g. preview).
func (c *Client) WithEndpoint(url string) *Client {
	c.endpoint = url
	return c
}

func (c *Client) Name() string { return Name }

const loansQuery = `query Loans($input: LoansInput) {
  liqwid {
    data {
      loans(input: $input) {
        totalCount
        results {
          id
          transactionId
          transactionIndex
          marketId
          publicKey
          amount
          interest
          APY
          LTV
          healthFactor
          time
          asset {
            symbol
            decimals
            policyId
            hexName
            price
          }
          collaterals {
            asset {
              symbol
              decimals
              policyId
              hexName
            }
            market {
              id
            }
            amount
            amountUSD
            qTokenAmount
          }
        }
      }
    }
  }
}`

const marketsQuery = `query Markets {
  liqwid {
    data {
      markets {
        totalCount
        results {
          id
          symbol
          asset {
            symbol
            decimals
            policyId
            hexName
            price
          }
          receiptAsset {
            symbol
            decimals
            policyId
            hexName
          }
          supply
          borrow
          liquidity
          supplyAPY
          borrowAPY
          utilization
          exchangeRate
          batching
          frozen
          delisting
          parameters {
            minValue
            collateralParameters {
              maxLoanToValue
              liquidationThreshold
              collateral {
                id
                market {
                  id
                  asset {
                    symbol
                    decimals
                    policyId
                    hexName
                    price
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

type gqlRequest struct {
	Query     string `json:"query"`
	Variables any    `json:"variables,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type marketsEnvelope struct {
	Liqwid struct {
		Data struct {
			Markets struct {
				TotalCount int         `json:"totalCount"`
				Results    []rawMarket `json:"results"`
			} `json:"markets"`
		} `json:"data"`
	} `json:"liqwid"`
}

type rawMarket struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
	Asset  struct {
		Symbol   string  `json:"symbol"`
		Decimals int     `json:"decimals"`
		PolicyID string  `json:"policyId"`
		HexName  string  `json:"hexName"`
		Price    float64 `json:"price"`
	} `json:"asset"`
	ReceiptAsset struct {
		Symbol   string `json:"symbol"`
		Decimals int    `json:"decimals"`
		PolicyID string `json:"policyId"`
		HexName  string `json:"hexName"`
	} `json:"receiptAsset"`
	Supply       float64 `json:"supply"`
	Borrow       float64 `json:"borrow"`
	Liquidity    float64 `json:"liquidity"`
	SupplyAPY    float64 `json:"supplyAPY"`
	BorrowAPY    float64 `json:"borrowAPY"`
	Utilization  float64 `json:"utilization"`
	ExchangeRate float64 `json:"exchangeRate"`
	Batching     bool    `json:"batching"`
	Frozen      bool    `json:"frozen"`
	Delisting   bool    `json:"delisting"`
	Parameters  struct {
		MinValue             float64              `json:"minValue"`
		CollateralParameters []rawCollateralParam `json:"collateralParameters"`
	} `json:"parameters"`
}

type rawCollateralParam struct {
	MaxLoanToValue       float64 `json:"maxLoanToValue"`
	LiquidationThreshold float64 `json:"liquidationThreshold"`
	Collateral           struct {
		ID     string `json:"id"`
		Market struct {
			ID    string `json:"id"`
			Asset struct {
				Symbol   string  `json:"symbol"`
				Decimals int     `json:"decimals"`
				PolicyID string  `json:"policyId"`
				HexName  string  `json:"hexName"`
				Price    float64 `json:"price"`
			} `json:"asset"`
		} `json:"market"`
	} `json:"collateral"`
}

func (c *Client) post(ctx context.Context, query string, vars any) (json.RawMessage, error) {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		return nil, err
	}
	logAPICall("→ liqwid POST "+c.endpoint, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("liqwid graphql: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	logAPICall(fmt.Sprintf("← liqwid %d", resp.StatusCode), respBody)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("liqwid graphql: status %d: %s", resp.StatusCode, string(respBody))
	}
	var gql gqlResponse
	if err := json.Unmarshal(respBody, &gql); err != nil {
		return nil, fmt.Errorf("liqwid graphql decode: %w", err)
	}
	if len(gql.Errors) > 0 {
		return nil, errors.New("liqwid graphql error: " + gql.Errors[0].Message)
	}
	return gql.Data, nil
}

// logAPICall prints an outgoing/incoming protocol payload to stderr with
// truncation, so dev can verify the bodies our backend sends to upstream.
func logAPICall(tag string, body []byte) {
	const maxLen = 2000
	s := string(body)
	if len(s) > maxLen {
		s = s[:maxLen] + fmt.Sprintf("…(+%d bytes)", len(body)-maxLen)
	}
	log.Printf("%s body=%s", tag, s)
}

func (c *Client) FetchMarkets(ctx context.Context) ([]sources.Market, error) {
	data, err := c.post(ctx, marketsQuery, nil)
	if err != nil {
		return nil, err
	}
	var env marketsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("liqwid markets decode: %w", err)
	}

	results := env.Liqwid.Data.Markets.Results
	// Liqwid is a pooled lender: every supplied asset (as its qToken) can be
	// used as collateral against any other market's borrow asset. Fan each
	// market out into one row per accepted collateral so the table shows the
	// real (collateral, borrow) pairs the user can open. The depth fields
	// (supply/borrow/liquidity) are shared across rows of the same market.
	out := make([]sources.Market, 0, len(results)*8)
	for _, m := range results {
		borrow := sources.Asset{
			PolicyID:  m.Asset.PolicyID,
			AssetName: m.Asset.HexName,
			Symbol:    m.Asset.Symbol,
			Decimals:  m.Asset.Decimals,
			PriceUsd:  m.Asset.Price,
		}
		receipt := sources.Asset{
			PolicyID:  m.ReceiptAsset.PolicyID,
			AssetName: m.ReceiptAsset.HexName,
			Symbol:    m.ReceiptAsset.Symbol,
			Decimals:  m.ReceiptAsset.Decimals,
		}
		active := !(m.Batching || m.Frozen || m.Delisting)
		base := sources.Market{
			Source:                Name,
			Borrow:                borrow,
			ReceiptAsset:          receipt,
			SupplyExchangeRate:    m.ExchangeRate, // underlying ADA per qADA, whole units
			TotalSuppliedUSD:      m.Supply * m.Asset.Price,
			TotalBorrowedUSD:      m.Borrow * m.Asset.Price,
			AvailableLiquidityUSD: m.Liquidity * m.Asset.Price,
			SupplyAPY:             m.SupplyAPY,
			BorrowAPY:             m.BorrowAPY,
			MinSupply:             m.Parameters.MinValue,
			Active:                active,
			URL:                   defaultMarketURL,
		}

		params := m.Parameters.CollateralParameters
		if len(params) == 0 {
			// No collateral metadata — keep a single row with the asset as
			// both sides so the market still shows up.
			row := base
			row.PoolID = m.ID
			out = append(out, row)
			continue
		}

		for _, cp := range params {
			cm := cp.Collateral.Market
			// Skip same-token pairs (e.g. ADA→ADA, SNEK→SNEK) — these are
			// technically valid in Compound-style protocols but they're
			// confusing in the UI and don't represent a real leverage path.
			if cm.Asset.PolicyID == m.Asset.PolicyID && cm.Asset.HexName == m.Asset.HexName {
				continue
			}
			row := base
			row.PoolID = m.ID + ":" + cm.ID
			row.Collateral = sources.Asset{
				PolicyID:  cm.Asset.PolicyID,
				AssetName: cm.Asset.HexName,
				Symbol:    cm.Asset.Symbol,
				Decimals:  cm.Asset.Decimals,
				PriceUsd:  cm.Asset.Price,
			}
			// The id the borrow mutation wants is "q" + the qToken's
			// market id (e.g. "qAda" for qADA / the Ada market). NOT
			// the cp.Collateral.id the query returns — that's a
			// compound id scoped to the borrow market (e.g.
			// "NIGHT.<policy>") which the mutation does not accept.
			// Liqwid's error message literally spells out the valid
			// format: "Valid collaterals: qAda,Ada,qIAG,IAG,...".
			row.CollateralID = "q" + cm.ID
			row.LTV = cp.MaxLoanToValue
			row.LiquidationThreshold = cp.LiquidationThreshold
			out = append(out, row)
		}
	}
	return out, nil
}

// --- orders ------------------------------------------------------------------

type loansEnvelope struct {
	Liqwid struct {
		Data struct {
			Loans struct {
				TotalCount int        `json:"totalCount"`
				Results    []rawLoan `json:"results"`
			} `json:"loans"`
		} `json:"data"`
	} `json:"liqwid"`
}

type rawLoan struct {
	ID               string  `json:"id"`
	TransactionID    string  `json:"transactionId"`
	TransactionIndex int     `json:"transactionIndex"`
	MarketID         string  `json:"marketId"`
	PublicKey        string  `json:"publicKey"`
	Amount           float64 `json:"amount"`
	Interest         float64 `json:"interest"`
	APY              float64 `json:"APY"`
	LTV              float64 `json:"LTV"`
	HealthFactor     float64 `json:"healthFactor"`
	Time             int64   `json:"time"`
	Asset            struct {
		Symbol   string  `json:"symbol"`
		Decimals int     `json:"decimals"`
		PolicyID string  `json:"policyId"`
		HexName  string  `json:"hexName"`
		Price    float64 `json:"price"`
	} `json:"asset"`
	Collaterals []rawLoanCollateral `json:"collaterals"`
}

type rawLoanCollateral struct {
	Asset struct {
		Symbol   string `json:"symbol"`
		Decimals int    `json:"decimals"`
		PolicyID string `json:"policyId"`
		HexName  string `json:"hexName"`
	} `json:"asset"`
	Market struct {
		ID string `json:"id"`
	} `json:"market"`
	Amount       float64 `json:"amount"`
	AmountUSD    float64 `json:"amountUSD"`
	QTokenAmount float64 `json:"qTokenAmount"`
}

// FetchOrders returns active Liqwid loans. Address (q.Address) is treated as
// a payment-key hash and forwarded via the LoansInput.paymentKeys filter.
func (c *Client) FetchOrders(ctx context.Context, q sources.OrderQuery) ([]sources.Order, error) {
	input := map[string]any{}
	if q.Address != "" {
		input["paymentKeys"] = []string{q.Address}
	}
	if q.Limit > 0 {
		input["perPage"] = q.Limit
	}
	vars := map[string]any{"input": input}
	if len(input) == 0 {
		vars = map[string]any{"input": nil}
	}

	data, err := c.post(ctx, loansQuery, vars)
	if err != nil {
		return nil, err
	}
	var env loansEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("liqwid loans decode: %w", err)
	}

	out := make([]sources.Order, 0, len(env.Liqwid.Data.Loans.Results))
	for _, l := range env.Liqwid.Data.Loans.Results {
		// Skip fully repaid loans — Liqwid keeps them in the list
		// with amount ≈ 0 (or a tiny dust residual) but they clutter
		// the My Orders view. Use a small threshold to catch both
		// exact-zero and near-zero residuals.
		if l.Amount < 0.000001 {
			continue
		}
		order := sources.Order{
			Source: Name,
			ID:     l.ID,
			Type:   sources.TypeBorrow,
			Status: sources.OrderActive,
			Owner:  l.PublicKey,
			// The close-flow needs the txId (not the loan id which is
			// "txId-outIdx"), so surface it as TxHash so the frontend
			// can pass it straight back to /api/tx/close.
			TxHash:      l.TransactionID,
			OutputIndex: l.TransactionIndex,
			MarketID:    l.MarketID,
			Asset: sources.Asset{
				PolicyID:  l.Asset.PolicyID,
				AssetName: l.Asset.HexName,
				Symbol:    l.Asset.Symbol,
				Decimals:  l.Asset.Decimals,
			},
			Amount:       l.Amount,
			AmountUSD:    l.Amount * l.Asset.Price,
			Interest:     l.Interest,
			APY:          l.APY,
			LTV:          l.LTV,
			HealthFactor: l.HealthFactor,
			TimeMs:       l.Time,
		}
		// Liqwid loans can have multiple collaterals. Collapse to the largest
		// USD entry so the routing engine sees the dominant collateral.
		if len(l.Collaterals) > 0 {
			best := l.Collaterals[0]
			for _, c := range l.Collaterals[1:] {
				if c.AmountUSD > best.AmountUSD {
					best = c
				}
			}
			order.CollateralAsset = sources.Asset{
				PolicyID:  best.Asset.PolicyID,
				AssetName: best.Asset.HexName,
				Symbol:    best.Asset.Symbol,
				Decimals:  best.Asset.Decimals,
			}
			order.CollateralAmount = best.Amount
		}
		out = append(out, order)
	}
	return out, nil
}

// =====================================================================
// TxBuilder — Liqwid builds via the GraphQL `liqwid.transactions.*`
// mutations. Each returns `Transaction { cbor }` which the frontend
// signs via CIP-30 and submits.
//
// The `wallet` enum on Liqwid currently lists only ETERNL/BEGIN; we
// default to ETERNL for everything else (Lace, Nami, etc.) since the CBOR
// produced is wallet-agnostic at the protocol layer.
// =====================================================================

// Liqwid exposes tx-building under the Query root (not Mutation) — these
// are queries that *return* an unsigned CBOR. The only real mutation in
// the schema is `submitTransaction`, which we don't use here (the wallet
// submits via CIP-30).
const txSupplyMutation = `query Supply($input: SupplyTransactionInput!) {
  liqwid { transactions { supply(input: $input) { cbor } } }
}`

const txWithdrawMutation = `query Withdraw($input: WithdrawTransactionInput!) {
  liqwid { transactions { withdraw(input: $input) { cbor } } }
}`

const txBorrowMutation = `query Borrow($input: BorrowTransactionInput!) {
  liqwid { transactions { borrow(input: $input) { cbor } } }
}`

type txEnvelope struct {
	Liqwid struct {
		Transactions map[string]struct {
			CBOR string `json:"cbor"`
		} `json:"transactions"`
	} `json:"liqwid"`
}

func liqwidWalletEnum(name string) string {
	switch strings.ToLower(name) {
	case "begin":
		return "BEGIN"
	}
	return "ETERNL"
}

func fallback(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// stripFanout returns the bare Liqwid market id. The Pools view fans each
// market out to "<market>:<collateral>" rows but the protocol GraphQL only
// knows the underlying market id ("Ada", "SNEK", …).
func stripFanout(marketID string) string {
	if i := strings.IndexByte(marketID, ':'); i > 0 {
		return marketID[:i]
	}
	return marketID
}

func (c *Client) buildTxCommonInput(p sources.TxParams, decimals int) map[string]any {
	other := p.OtherAddresses
	if other == nil {
		other = []string{}
	}
	utxos := p.UTXOs
	if utxos == nil {
		utxos = []string{}
	}
	return map[string]any{
		"address":        p.Address,
		"changeAddress":  fallback(p.ChangeAddress, p.Address),
		"otherAddresses": other,
		"amount":         int64(math.Round(p.Amount * math.Pow(10, float64(decimals)))),
		"marketId":       stripFanout(p.MarketID),
		"utxos":          utxos,
		"wallet":         liqwidWalletEnum(p.Wallet),
	}
}

// liqwidMarketDecimals re-uses the markets cache to find the market's
// underlying asset decimals (so a "100 ADA" amount becomes the right
// number of lovelace).
func (c *Client) liqwidMarketDecimals(ctx context.Context, marketID string) int {
	ms, err := c.FetchMarkets(ctx)
	if err != nil {
		return 6
	}
	for _, m := range ms {
		if m.PoolID == marketID || strings.HasPrefix(m.PoolID, marketID+":") {
			return m.Borrow.Decimals
		}
	}
	return 6
}

func (c *Client) callTxMutation(ctx context.Context, query string, input map[string]any) (string, error) {
	data, err := c.post(ctx, query, map[string]any{"input": input})
	if err != nil {
		return "", err
	}
	var env txEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("liqwid tx decode: %w", err)
	}
	for _, v := range env.Liqwid.Transactions {
		if v.CBOR != "" {
			return v.CBOR, nil
		}
	}
	return "", errors.New("liqwid tx: empty cbor in response")
}

func (c *Client) BuildSupply(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	dec := c.liqwidMarketDecimals(ctx, p.MarketID)
	cbor, err := c.callTxMutation(ctx, txSupplyMutation, c.buildTxCommonInput(p, dec))
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{Source: Name, Action: "supply", CBOR: cbor,
		Hint: fmt.Sprintf("Supply %.6f to Liqwid %s market", p.Amount, p.MarketID)}, nil
}

func (c *Client) BuildWithdraw(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	dec := c.liqwidMarketDecimals(ctx, p.MarketID)
	input := c.buildTxCommonInput(p, dec)
	raw := int64(math.Round(p.Amount * math.Pow(10, float64(dec))))
	if p.Full && raw > 0 {
		// Full withdraw: subtract 1 raw unit as a safety buffer. The
		// frontend computes the max from receiptQty × exchangeRate,
		// but the limited precision can overshoot by 1 smallest unit,
		// leaving dust under the protocol's minimum supply threshold.
		raw--
	}
	input["amount"] = raw
	cbor, err := c.callTxMutation(ctx, txWithdrawMutation, input)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{Source: Name, Action: "withdraw", CBOR: cbor,
		Hint: fmt.Sprintf("Withdraw %.6f from Liqwid %s market", p.Amount, p.MarketID)}, nil
}

// SubmitTx uses Liqwid's root `submitTransaction(input: {transaction,
// signature})` mutation which takes the unsigned tx CBOR + the witness
// set separately, does the merge server-side, and broadcasts to the
// chain. Returns the on-chain tx hash as a hex string.
//
// This replaces the fragile client-side witness-set merging that was
// producing `NoRedeemer` / `PPViewHashesDontMatch` / TxSendError on
// supply — the protocol knows exactly how to assemble its own scripts.
const submitTxMutation = `mutation SubmitTx($input: SubmitTransactionInput!) {
  submitTransaction(input: $input)
}`

func (c *Client) SubmitTx(ctx context.Context, p sources.SubmitParams) (string, error) {
	if p.CBOR == "" || p.WitnessSet == "" {
		return "", errors.New("liqwid submit: cbor and witnessSet required")
	}

	// Attempt 1: Liqwid's own submitTransaction mutation. It takes the
	// unsigned cbor + the witness set separately and does the merge
	// server-side. Usually works. Falls into Attempt 2 if Liqwid's
	// resolver returns a garbled error (Ogmios JSON-WSP response
	// stuffed into a String scalar) — that's usually an EvaluateTx
	// SubmitFail where the detail is truncated.
	data, err := c.post(ctx, submitTxMutation, map[string]any{
		"input": map[string]any{
			"transaction": p.CBOR,
			"signature":   p.WitnessSet,
		},
	})
	if err == nil {
		var env struct {
			SubmitTransaction string `json:"submitTransaction"`
		}
		if uerr := json.Unmarshal(data, &env); uerr == nil && env.SubmitTransaction != "" {
			return env.SubmitTransaction, nil
		}
	}
	log.Printf("liqwid submit: liqwid mutation failed (%v), falling back to Koios submittx", err)

	// Attempt 2: merge the witness set locally and submit directly via
	// Koios. Koios returns a clean, untruncated error string when the
	// node rejects the tx — which is what we need to actually diagnose
	// the real failure instead of "SubmitFail: [Array]".
	signed, merr := sources.MergeTxWitnesses(p.CBOR, p.WitnessSet)
	if merr != nil {
		return "", fmt.Errorf("liqwid submit: merge witnesses failed: %w (liqwid error: %v)", merr, err)
	}
	hash, kerr := submitViaKoios(ctx, c.http, signed)
	if kerr != nil {
		// Surface both errors so the user (and the logs) have full context.
		return "", fmt.Errorf("liqwid submit failed: %v; koios fallback: %v", err, kerr)
	}
	log.Printf("liqwid submit: succeeded via Koios fallback, txHash=%s", hash)
	return hash, nil
}

// submitViaKoios POSTs a CBOR-hex signed transaction to Koios' public
// /submittx endpoint and returns the tx hash on success. Content type
// matters: Koios wants application/cbor with raw bytes, not JSON.
func submitViaKoios(ctx context.Context, hc *http.Client, signedHex string) (string, error) {
	raw := make([]byte, len(signedHex)/2)
	for i := 0; i < len(raw); i++ {
		var b byte
		for j := 0; j < 2; j++ {
			c := signedHex[i*2+j]
			switch {
			case c >= '0' && c <= '9':
				b = b*16 + (c - '0')
			case c >= 'a' && c <= 'f':
				b = b*16 + 10 + (c - 'a')
			case c >= 'A' && c <= 'F':
				b = b*16 + 10 + (c - 'A')
			default:
				return "", fmt.Errorf("koios submit: invalid hex byte %q at %d", c, i*2+j)
			}
		}
		raw[i] = b
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.koios.rest/api/v1/submittx", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Accept", "application/json")
	logAPICall("→ koios POST /api/v1/submittx", []byte(fmt.Sprintf("<%d raw cbor bytes>", len(raw))))
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("koios submittx: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	logAPICall(fmt.Sprintf("← koios %d submittx", resp.StatusCode), body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("koios submittx: status %d: %s", resp.StatusCode, string(body))
	}
	// Koios returns the tx hash as a JSON string, e.g. "abcdef...".
	txHash := strings.Trim(string(body), " \"\r\n\t")
	if txHash == "" {
		return "", fmt.Errorf("koios submittx: empty response body")
	}
	return txHash, nil
}

// BuildClose builds a Liqwid full-repay transaction via the
// modifyBorrow GraphQL mutation (it's actually exposed as a query
// under `liqwid.transactions.modifyBorrow`, same pattern as the other
// tx builders).
//
// Flow:
//  1. Query the user's active loans to find the one matching TxHash.
//     We need the fresh outstanding amount + collateral list because
//     both grow with interest and collateral can be multi-asset.
//  2. Build modifyBorrow with:
//       - txId: loan.id (compound "<txHash>-<index>")
//       - amount: 0 (absolute target = zero remaining debt)
//       - redeemCollateral: true        (release the collateral)
//       - collaterals: [{id: "<market>.<policyId>", amount: qTokenRaw}, ...]
//  3. Return the unsigned CBOR for CIP-30 signing.
const txModifyBorrowMutation = `query ModifyBorrow($input: ModifyBorrowTransactionInput!) {
  liqwid { transactions { modifyBorrow(input: $input) { cbor } } }
}`

func (c *Client) BuildClose(ctx context.Context, p sources.TxCloseParams) (*sources.BuiltTx, error) {
	if p.Kind != "" && p.Kind != "repay" {
		return nil, fmt.Errorf("liqwid close: only 'repay' is supported (got %q)", p.Kind)
	}
	if p.Address == "" || p.TxHash == "" {
		return nil, errors.New("liqwid close: address and txHash required")
	}
	if len(p.UTXOs) == 0 {
		return nil, errors.New("liqwid close: utxos required (frontend must call CIP-30 getUtxos)")
	}

	// 1. Find the loan via the paymentKeys filter. Falls back to an
	//    unfiltered scan if no pkh was supplied (legacy callers).
	loan, err := c.findLoanByTxHash(ctx, p.Pkh, p.TxHash)
	if err != nil {
		return nil, fmt.Errorf("liqwid close: lookup loan: %w", err)
	}
	if loan == nil {
		return nil, fmt.Errorf("liqwid close: no active loan found for tx %s (pkh %s)", p.TxHash, p.Pkh)
	}

	// 2. Build the collaterals array. ModifyBorrow expects the compound
	//    collateral id from the borrow market's collateralParameters, of
	//    the form "<borrowMarketId>.<collateralPolicyId>" (e.g.
	//    "SNEK.a04ce7a52545..." for a SNEK borrow with ADA collateral).
	//    Neither "qAda" (BorrowTransactionInput form) nor the bare
	//    underlying market id ("Ada") are accepted here — both produce
	//    "Collateral not found".
	//
	//    Also: Liqwid's loan query returns `qTokenAmount` as a Float in
	//    WHOLE qToken units (e.g. 3955.489 qAda). The mutation needs an
	//    INTEGER in raw qToken units — scale by 10^decimals and round,
	//    otherwise Liqwid's BigInt coercion rejects with "NaN cannot be
	//    converted to a BigInt".
	collIDs, err := c.fetchCollateralIDsForMarket(ctx, loan.MarketID)
	if err != nil {
		return nil, fmt.Errorf("liqwid close: resolve collateral ids: %w", err)
	}
	collaterals := make([]map[string]any, 0, len(loan.Collaterals))
	for _, lc := range loan.Collaterals {
		if lc.Market.ID == "" {
			return nil, errors.New("liqwid close: loan collateral missing market id")
		}
		cid, ok := collIDs[lc.Market.ID]
		if !ok || cid == "" {
			return nil, fmt.Errorf("liqwid close: no collateral id for %s in market %s", lc.Market.ID, loan.MarketID)
		}
		// Cast to int64 after rounding so JSON serializes as a clean
		// integer (e.g. 3955489026) and never as a float with trailing
		// decimals (e.g. 3955489025.9999995) which Liqwid's BigInt
		// coercion would reject.
		qRaw := int64(math.Round(lc.QTokenAmount * math.Pow(10, float64(lc.Asset.Decimals))))
		log.Printf("liqwid close: collateral %s qTokenAmount=%.10f decimals=%d → qRaw=%d",
			lc.Market.ID, lc.QTokenAmount, lc.Asset.Decimals, qRaw)
		collaterals = append(collaterals, map[string]any{
			"id":     cid,
			"amount": qRaw,
		})
	}
	if len(collaterals) == 0 {
		return nil, errors.New("liqwid close: loan has no collaterals in the response")
	}

	other := p.OtherAddresses
	if other == nil {
		other = []string{}
	}
	// txId in ModifyBorrowTransactionInput is Liqwid's compound loan id
	// ("<txHash>-<outputIndex>"), not just the bare tx hash. Passing the
	// bare hash makes the server's loan lookup return undefined, which
	// it then tries to BigInt() and throws "NaN cannot be converted to
	// a BigInt". loan.ID is already in that compound format.
	txID := loan.ID
	if txID == "" {
		txID = fmt.Sprintf("%s-%d", loan.TransactionID, loan.TransactionIndex)
	}
	input := map[string]any{
		"txId":           txID,
		"address":        p.Address,
		"changeAddress":  fallback(p.ChangeAddress, p.Address),
		"otherAddresses": other,
		// amount: 0 = absolute target (zero remaining debt).
		"amount":     0,
		"collaterals": collaterals,
		"utxos":       p.UTXOs,
	}
	// redeemCollateral: true releases the locked collateral back to
	// the user's wallet. false (or absent) keeps it supplied in the
	// protocol — the user repays the loan but their qTokens stay
	// locked, continuing to earn supply APY. Liqwid's own app omits
	// the field entirely for "keep collateral" mode.
	if p.RedeemCollateral {
		input["redeemCollateral"] = true
	}
	log.Printf("liqwid modifyBorrow: txId=%s amount=%v loan.Amount=%.6f loan.Interest=%.6f collaterals=%d",
		txID, input["amount"], loan.Amount, loan.Interest, len(collaterals))

	data, err := c.post(ctx, txModifyBorrowMutation, map[string]any{"input": input})
	if err != nil {
		return nil, err
	}
	var env txEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("liqwid modifyBorrow decode: %w", err)
	}
	for _, v := range env.Liqwid.Transactions {
		if v.CBOR != "" {
			return &sources.BuiltTx{
				Source: Name,
				Action: "repay",
				CBOR:   v.CBOR,
				Hint:   fmt.Sprintf("Repay full Liqwid %s loan (%.6f + interest)", loan.MarketID, loan.Amount),
			}, nil
		}
	}
	return nil, errors.New("liqwid modifyBorrow: empty cbor in response")
}

// fetchCollateralIDsForMarket returns a map from collateral-market-id
// (e.g. "Ada") to the compound collateral id that ModifyBorrow expects
// (e.g. "SNEK.a04ce7a52545..."). It queries only the one borrow
// market's collateralParameters to keep the round-trip small.
func (c *Client) fetchCollateralIDsForMarket(ctx context.Context, borrowMarketID string) (map[string]string, error) {
	const q = `query MarketCollaterals {
  liqwid {
    data {
      markets {
        results {
          id
          parameters {
            collateralParameters {
              collateral {
                id
                market { id }
              }
            }
          }
        }
      }
    }
  }
}`
	data, err := c.post(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	var env marketsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, m := range env.Liqwid.Data.Markets.Results {
		if m.ID != borrowMarketID {
			continue
		}
		for _, cp := range m.Parameters.CollateralParameters {
			out[cp.Collateral.Market.ID] = cp.Collateral.ID
		}
		return out, nil
	}
	return nil, fmt.Errorf("borrow market %q not found", borrowMarketID)
}

// findLoanByTxHash queries the user's active loans (filtered by pkh
// when supplied) and returns the one whose transactionId matches.
// loanIdMatches handles both the bare "<txHash>" form and Liqwid's
// compound "<txHash>-<index>" loan id format.
func (c *Client) findLoanByTxHash(ctx context.Context, pkh, txHash string) (*rawLoan, error) {
	input := map[string]any{"perPage": 100}
	if pkh != "" {
		input["paymentKeys"] = []string{pkh}
	}
	data, err := c.post(ctx, loansQuery, map[string]any{"input": input})
	if err != nil {
		return nil, err
	}
	var env loansEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	log.Printf("liqwid findLoan: got %d loans for pkh=%s, looking for tx=%s",
		len(env.Liqwid.Data.Loans.Results), pkh, txHash)
	for i := range env.Liqwid.Data.Loans.Results {
		l := &env.Liqwid.Data.Loans.Results[i]
		if loanIdMatches(l, txHash) {
			return l, nil
		}
	}
	return nil, nil
}

// loanIdMatches returns true when the loan references the given tx
// hash. Liqwid exposes the transactionId in multiple shapes:
//   - loan.transactionId = "<hash>"
//   - loan.id = "<hash>-<index>" (compound)
//   - loan.id = "<hash>" (bare — some older loans)
// Match any of them so the frontend can pass whichever we stored.
func loanIdMatches(l *rawLoan, txHash string) bool {
	if l.TransactionID == txHash {
		return true
	}
	if l.ID == txHash {
		return true
	}
	if strings.HasPrefix(l.ID, txHash+"-") {
		return true
	}
	return false
}

func (c *Client) BuildBorrow(ctx context.Context, p sources.TxParams) (*sources.BuiltTx, error) {
	dec := c.liqwidMarketDecimals(ctx, p.MarketID)
	input := c.buildTxCommonInput(p, dec)
	// BorrowTransactionInput takes `collaterals: [{id, amount}!]!` and
	// does NOT use `wallet` / `mintedQTokensDestination`. Empty array
	// causes `Got Infinity weighted LTV` — we must explicitly tell
	// Liqwid which Collateral (= qToken) to lock and how much of it.
	//
	// IMPORTANT: amount in BorrowTransactionInputCollateral is in
	// **qToken** units (raw), NOT underlying asset units. Passing 100
	// ADA worth as 100_000_000 would look like 100 qAda ≈ 2 ADA to
	// Liqwid's LTV calculator and trip the LTV check. Convert via the
	// collateral market's supplyExchangeRate:
	//
	//     qToken_whole = underlying_whole / supplyExchangeRate
	//     qToken_raw   = qToken_whole × 10^qTokenDecimals
	delete(input, "wallet")

	coll, err := c.liqwidCollateralForRow(ctx, p.MarketID)
	if err != nil {
		return nil, err
	}
	if coll == nil || coll.id == "" {
		return nil, errors.New("liqwid borrow: missing collateral id for pool " + p.MarketID)
	}
	if p.CollateralAmount <= 0 {
		return nil, errors.New("liqwid borrow: collateralAmount required (whole units of the underlying asset, e.g. 100 = 100 ADA)")
	}

	qTokenRaw := int64(underlyingToQTokenRaw(p.CollateralAmount, coll.exchangeRate, coll.qTokenDec))
	log.Printf("liqwid borrow: collateral %.6f %s underlying → %d raw %s (rate=%.9f, qDec=%d)",
		p.CollateralAmount, coll.underlyingSymbol, qTokenRaw, coll.id, coll.exchangeRate, coll.qTokenDec)

	input["collaterals"] = []map[string]any{{
		"id":     coll.id,
		"amount": qTokenRaw,
	}}

	cbor, err := c.callTxMutation(ctx, txBorrowMutation, input)
	if err != nil {
		return nil, err
	}
	return &sources.BuiltTx{Source: Name, Action: "borrow", CBOR: cbor,
		Hint: fmt.Sprintf("Borrow %.6f from Liqwid %s market", p.Amount, p.MarketID)}, nil
}

// underlyingToQTokenRaw converts a user-entered amount in whole units
// of the underlying asset (e.g. 100 ADA) into Liqwid's expected qToken
// raw-unit amount (what BorrowTransactionInputCollateral.amount wants).
//
//  qToken_whole = underlying_whole / exchangeRate      // rate = ADA per qAda whole
//  qToken_raw   = qToken_whole × 10^qTokenDecimals
func underlyingToQTokenRaw(underlyingWhole, exchangeRate float64, qTokenDec int) float64 {
	if exchangeRate <= 0 {
		// Unknown rate — fall back to 1:1 and let Liqwid's LTV check decide.
		exchangeRate = 1
	}
	qTokenWhole := underlyingWhole / exchangeRate
	return math.Round(qTokenWhole * math.Pow(10, float64(qTokenDec)))
}

// liqwidCollateral carries everything we need to fill in a
// BorrowTransactionInputCollateral: the collateral id Liqwid accepts
// (e.g. "qAda"), the qToken's decimals, its exchangeRate, and the
// underlying asset symbol for logging.
type liqwidCollateral struct {
	id               string
	qTokenDec        int
	exchangeRate     float64
	underlyingSymbol string
}

// liqwidCollateralForRow resolves everything needed to build a Liqwid
// borrow's collateral entry for a fanned-out pool id like "NIGHT:Ada".
// Uses the cached markets list: the SNEK:Ada row carries the borrow
// context, and we look up the collateral's own market (Ada) to get
// its receiptAsset (qAda) decimals and supplyExchangeRate.
func (c *Client) liqwidCollateralForRow(ctx context.Context, poolID string) (*liqwidCollateral, error) {
	ms, err := c.FetchMarkets(ctx)
	if err != nil {
		return nil, err
	}
	var row *sources.Market
	for i := range ms {
		m := &ms[i]
		if m.Source != Name {
			continue
		}
		if m.PoolID == poolID && m.CollateralID != "" {
			row = m
			break
		}
	}
	if row == nil {
		// Bare borrow-market id (no fan-out suffix) — grab any row
		// prefixed with "<poolID>:".
		for i := range ms {
			m := &ms[i]
			if m.Source == Name && strings.HasPrefix(m.PoolID, poolID+":") && m.CollateralID != "" {
				row = m
				break
			}
		}
	}
	if row == nil {
		return nil, nil
	}
	// Find the collateral's own market to pull the receiptAsset
	// decimals and the supply exchange rate. The collateral market id
	// is the second half of the fan-out PoolID: "NIGHT:Ada" → "Ada".
	collateralMarketID := ""
	if idx := strings.IndexByte(row.PoolID, ':'); idx > 0 {
		collateralMarketID = row.PoolID[idx+1:]
	}
	var collMarket *sources.Market
	for i := range ms {
		m := &ms[i]
		if m.Source == Name && m.PoolID != "" && !strings.Contains(m.PoolID, ":") {
			// Fallback: pre-fan-out rows don't exist — every Liqwid row
			// is fanned out. Skip.
			continue
		}
		if m.Source == Name && strings.HasPrefix(m.PoolID, collateralMarketID+":") {
			collMarket = m
			break
		}
	}
	out := &liqwidCollateral{
		id:               row.CollateralID,
		qTokenDec:        row.Collateral.Decimals,
		exchangeRate:     1, // default 1:1 when we can't find the rate
		underlyingSymbol: row.Collateral.Symbol,
	}
	if collMarket != nil && collMarket.SupplyExchangeRate > 0 {
		out.exchangeRate = collMarket.SupplyExchangeRate
		out.qTokenDec = collMarket.ReceiptAsset.Decimals
	}
	return out, nil
}

