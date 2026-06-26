package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Salvionied/apollo"
	"github.com/Salvionied/apollo/constants"
	"github.com/Salvionied/apollo/serialization/Address"
	"github.com/Salvionied/apollo/serialization/UTxO"
	"github.com/Salvionied/apollo/txBuilding/Backend/BlockFrostChainContext"
	"github.com/fxamacker/cbor/v2"
)

const (
	koiosBase       = "https://api.koios.rest/api/v1"
	confirmPollWait = 5 * time.Second
)

// Chain gives the engine its own view of the chain for the temp address: it
// fetches UTxOs / builds funding & sweep txs through apollo's BlockFrost
// context, and submits + confirms transactions through Koios (which returns
// clean node errors and accepts raw signed CBOR without a re-marshal that
// could mutate script txs).
type Chain struct {
	cc        BlockFrostChainContext.BlockFrostChainContext
	network   constants.Network
	http      *http.Client
	bfBase    string // BlockFrost API base incl. /v0 (era-aware submit)
	projectID string
}

// NewChain builds a Chain from a BlockFrost project id + base url. The
// network selects mainnet vs a testnet for address/header derivation.
func NewChain(projectID, baseURL string, network constants.Network) (*Chain, error) {
	if projectID == "" {
		return nil, errors.New("engine: BLOCKFROST_PROJECT_ID is required")
	}
	cc, err := BlockFrostChainContext.NewBlockfrostChainContext(baseURL, int(network), projectID)
	if err != nil {
		return nil, fmt.Errorf("engine: blockfrost ctx: %w", err)
	}
	// apollo appends /v0 for blockfrost.io hosts; mirror that for our own
	// raw submit calls.
	bfBase := strings.TrimRight(baseURL, "/")
	if strings.Contains(bfBase, "blockfrost.io") && !strings.HasSuffix(bfBase, "/v0") {
		bfBase += "/v0"
	}
	return &Chain{
		cc:        cc,
		network:   network,
		http:      &http.Client{Timeout: 30 * time.Second},
		bfBase:    bfBase,
		projectID: projectID,
	}, nil
}

// Context exposes the underlying apollo chain context (read-only use).
func (ch *Chain) Context() *BlockFrostChainContext.BlockFrostChainContext { return &ch.cc }

// UtxosCBOR returns the address' UTxOs encoded as CIP-30 cbor-hex strings
// (each a TransactionUnspentOutput = [input, output]). The Liqwid and
// DexHunter tx builders accept exactly this shape in their `utxos` field, so
// the temp address can drive those builders just like a CIP-30 wallet would.
func (ch *Chain) UtxosCBOR(addr string) ([]string, error) {
	decoded, err := Address.DecodeAddress(addr)
	if err != nil {
		return nil, fmt.Errorf("utxos: decode address: %w", err)
	}
	utxos, err := ch.cc.Utxos(decoded)
	if err != nil {
		return nil, fmt.Errorf("utxos: %w", err)
	}
	out := make([]string, 0, len(utxos))
	for _, u := range utxos {
		b, err := cbor.Marshal(u)
		if err != nil {
			return nil, fmt.Errorf("utxos: marshal: %w", err)
		}
		out = append(out, hex.EncodeToString(b))
	}
	return out, nil
}

// LovelaceAt returns the total lovelace held at an address.
func (ch *Chain) LovelaceAt(addr string) (int64, error) {
	decoded, err := Address.DecodeAddress(addr)
	if err != nil {
		return 0, fmt.Errorf("balance: decode address: %w", err)
	}
	utxos, err := ch.cc.Utxos(decoded)
	if err != nil {
		return 0, fmt.Errorf("balance: %w", err)
	}
	var total int64
	for _, u := range utxos {
		total += u.Output.GetValue().GetCoin()
	}
	return total, nil
}

// AssetAt returns the total quantity of a native asset (policyHex+nameHex)
// held at an address, summed across its UTxOs. An empty unit means ADA and
// returns the lovelace total.
func (ch *Chain) AssetAt(addr, unit string) (int64, error) {
	if unit == "" {
		return ch.LovelaceAt(addr)
	}
	decoded, err := Address.DecodeAddress(addr)
	if err != nil {
		return 0, fmt.Errorf("asset: decode address: %w", err)
	}
	utxos, err := ch.cc.Utxos(decoded)
	if err != nil {
		return 0, fmt.Errorf("asset: %w", err)
	}
	policy, name := splitUnit(unit)
	var total int64
	for _, u := range utxos {
		total += u.Output.GetValue().GetAssets().GetByPolicyAndId(
			toPolicy(policy), toAssetName(name),
		)
	}
	return total, nil
}

// BuildFundingTx builds the unsigned tx that moves `lovelace` from the user's
// wallet into the temp address. The user signs it via CIP-30 (this is the one
// and only signature the engine asks the user for) and submits it.
//
// Coin selection prefers the wallet's actual CIP-30 UTxOs (utxosHex, passed
// from the frontend) because a CIP-30 wallet spreads funds across many
// addresses — selecting only from the first used address (BlockFrost's view of
// userAddr) frequently fails with "not enough funds" even when the wallet is
// well funded. When no UTxOs are supplied we fall back to the single-address
// BlockFrost path. Change goes to changeAddr (the wallet's change address).
func (ch *Chain) BuildFundingTx(userAddr, changeAddr, tempAddr string, lovelace int64, utxosHex []string) (string, error) {
	dest, err := Address.DecodeAddress(tempAddr)
	if err != nil {
		return "", fmt.Errorf("funding: decode temp address: %w", err)
	}
	if changeAddr == "" {
		changeAddr = userAddr
	}
	b := apollo.New(&ch.cc).
		SetChangeAddressBech32(changeAddr).
		PayToAddress(dest, int(lovelace))
	if len(utxosHex) > 0 {
		utxos, err := decodeUtxos(utxosHex)
		if err != nil {
			return "", fmt.Errorf("funding: decode wallet utxos: %w", err)
		}
		b = b.AddLoadedUTxOs(utxos...)
	} else {
		b = b.AddInputAddressFromBech32(userAddr)
	}
	built, err := b.Complete()
	if err != nil {
		return "", fmt.Errorf("funding: complete: %w", err)
	}
	return txToHex(built)
}

// decodeUtxos parses CIP-30 cbor-hex TransactionUnspentOutputs into apollo
// UTxOs for coin selection.
func decodeUtxos(utxosHex []string) ([]UTxO.UTxO, error) {
	out := make([]UTxO.UTxO, 0, len(utxosHex))
	for _, h := range utxosHex {
		raw, err := hex.DecodeString(h)
		if err != nil {
			return nil, err
		}
		var u UTxO.UTxO
		if err := cbor.Unmarshal(raw, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// BuildSweepTx consumes every UTxO at the temp address and pays the remainder
// (minus fee) back to the user as change — i.e. "send all". It signs locally
// with the temp key and returns the signed CBOR ready to submit. Used to
// recover funds when a position is closed, reversed, or fails mid-build.
func (ch *Chain) BuildSweepTx(w *TempWallet, userAddr string) (string, error) {
	decoded, err := Address.DecodeAddress(w.Address)
	if err != nil {
		return "", fmt.Errorf("sweep: decode temp address: %w", err)
	}
	utxos, err := ch.cc.Utxos(decoded)
	if err != nil {
		return "", fmt.Errorf("sweep: utxos: %w", err)
	}
	if len(utxos) == 0 {
		return "", errors.New("sweep: temp address holds no utxos")
	}
	// Force EVERY utxo in as an input (AddInput → preselected) rather than
	// AddLoadedUTxOs (a selectable pool). A sweep has no payment output, so coin
	// selection would otherwise pull only enough utxos to cover the fee and leave
	// the rest stranded at the temp address. Preselecting all of them sends the
	// entire balance, minus fee, to the user as change.
	b := apollo.New(&ch.cc).
		AddInput(utxos...).
		SetChangeAddressBech32(userAddr)
	built, err := b.Complete()
	if err != nil {
		return "", fmt.Errorf("sweep: complete: %w", err)
	}
	signed, err := built.SignWithSkey(w.VKey(), w.SKey())
	if err != nil {
		return "", fmt.Errorf("sweep: sign: %w", err)
	}
	return txToHex(signed)
}

// txToHex marshals an apollo-built transaction to CBOR hex using the same
// encoder options the rest of the codebase uses for on-wire txs.
func txToHex(b *apollo.Apollo) (string, error) {
	encOpts := cbor.CTAP2EncOptions()
	encOpts.TagsMd = cbor.TagsAllowed
	encOpts.IndefLength = cbor.IndefLengthAllowed
	enc, err := encOpts.EncMode()
	if err != nil {
		return "", err
	}
	out, err := enc.Marshal(b.GetTx())
	if err != nil {
		return "", fmt.Errorf("marshal tx: %w", err)
	}
	return hex.EncodeToString(out), nil
}

// SubmitRaw submits an already-signed transaction (CBOR hex) to the chain and
// returns the on-chain tx hash. It submits raw bytes (no re-marshal, so script
// txs keep their script-data hash) to BlockFrost first — BlockFrost's submit
// endpoint tracks the current ledger era, whereas Koios' cardano-submit-api
// rejected apollo-built txs with a Conway/Babbage EraMismatch. Koios is the
// fallback because it returns clearer node errors.
func (ch *Chain) SubmitRaw(ctx context.Context, signedHex string) (string, error) {
	raw, err := hex.DecodeString(signedHex)
	if err != nil {
		return "", fmt.Errorf("submit: decode hex: %w", err)
	}
	hash, bfErr := ch.submitTo(ctx, ch.bfBase+"/tx/submit", raw, true)
	if bfErr == nil {
		return hash, nil
	}
	log.Printf("engine: blockfrost submit failed (%v) — trying koios", bfErr)
	hash, koErr := ch.submitTo(ctx, koiosBase+"/submittx", raw, false)
	if koErr == nil {
		return hash, nil
	}
	return "", fmt.Errorf("submit failed: blockfrost: %v; koios: %v", bfErr, koErr)
}

// submitTo POSTs raw tx bytes to a cardano submit endpoint. blockfrost adds the
// project_id header; both want application/cbor and return the tx hash as a
// (possibly quoted) JSON string.
func (ch *Chain) submitTo(ctx context.Context, url string, raw []byte, blockfrost bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Accept", "application/json")
	if blockfrost {
		req.Header.Set("project_id", ch.projectID)
	}
	resp, err := ch.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return strings.Trim(string(body), " \"\r\n\t"), nil
}

// WaitConfirmed polls Koios /tx_status until the transaction has at least one
// confirmation or the context/timeout fires.
func (ch *Chain) WaitConfirmed(ctx context.Context, txHash string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conf, err := ch.confirmations(ctx, txHash)
		if err == nil && conf > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("confirm: timed out waiting for %s", txHash)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(confirmPollWait):
		}
	}
}

func (ch *Chain) confirmations(ctx context.Context, txHash string) (int, error) {
	payload, _ := json.Marshal(map[string]any{"_tx_hashes": []string{txHash}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, koiosBase+"/tx_status", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := ch.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tx_status: %d", resp.StatusCode)
	}
	var rows []struct {
		NumConfirmations int `json:"num_confirmations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].NumConfirmations, nil
}
