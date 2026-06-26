// Package engine implements the leveraged long/short trading engine.
//
// A leverage position is built by looping "trade -> wait for batch -> lend
// against the proceeds -> trade again" until a target leverage (capped at
// 1.8x) is reached. Because each leg is a separate on-chain transaction that
// must clear a DEX batcher before the next can run, the loop cannot be a
// single atomic transaction. Instead the engine routes the user's capital
// through an ephemeral "temp address" whose key it holds: the user signs
// exactly once (a funding tx into the temp address) and the backend drives
// every subsequent leg by signing locally with the temp key.
//
// Custody model: the temp signing key is persisted (encrypted) alongside the
// job so the engine can keep driving / unwinding the position across restarts.
// Closing or reversing a position unwinds it and sweeps the remainder back to
// the user's own wallet.
package engine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"dh-leverage/common/sources"

	"github.com/Salvionied/apollo/constants"
	"github.com/Salvionied/apollo/serialization/Address"
	"github.com/Salvionied/apollo/serialization/Key"
	"github.com/Salvionied/apollo/serialization/TransactionWitnessSet"
	"github.com/Salvionied/apollo/serialization/VerificationKeyWitness"
	"github.com/fxamacker/cbor/v2"
	"golang.org/x/crypto/blake2b"
)

// TempWallet is an ephemeral, backend-controlled wallet used to hold a single
// leverage position while it is being built and managed. It is a full BASE
// address (payment + staking credential): DexHunter rejects enterprise
// (payment-only) addresses, so the temp wallet carries a staking part too. The
// stake key is never registered/delegated — it only shapes the address; spends
// are authorized by the payment key alone.
type TempWallet struct {
	Address   string // bech32 base address (payment + staking)
	Pkh       string // payment-key hash (hex) — Liqwid's builders need it
	vkey      Key.VerificationKey
	skey      Key.SigningKey
	stakeVkey Key.VerificationKey
	stakeSkey Key.SigningKey
}

// NewTempWallet generates a fresh random temp wallet on the given network: a
// payment keypair for spending and a stake keypair for the address' staking
// part. DexHunter's swap tx requires witnesses from BOTH credentials, so both
// keys are kept and used to sign.
func NewTempWallet(network constants.Network) (*TempWallet, error) {
	payKP, err := Key.PaymentKeyPairGenerate()
	if err != nil {
		return nil, fmt.Errorf("temp wallet payment keygen: %w", err)
	}
	stakeKP, err := Key.PaymentKeyPairGenerate()
	if err != nil {
		return nil, fmt.Errorf("temp wallet stake keygen: %w", err)
	}
	stakeHash, err := stakeKP.VerificationKey.Hash()
	if err != nil {
		return nil, fmt.Errorf("temp wallet stake hash: %w", err)
	}
	return walletFromKeys(payKP.VerificationKey, payKP.SigningKey, stakeKP.VerificationKey, stakeKP.SigningKey, stakeHash[:], network)
}

// walletFromKeys builds a base address from the payment key hash + the explicit
// stakeHash, and keeps both keypairs for signing. The stakeHash is passed in
// (rather than derived) so a legacy wallet — sealed before the stake key was
// persisted — reconstructs its original address from the stored hash.
func walletFromKeys(vk Key.VerificationKey, sk Key.SigningKey, stakeVk Key.VerificationKey, stakeSk Key.SigningKey, stakeHash []byte, network constants.Network) (*TempWallet, error) {
	pkh, err := vk.Hash()
	if err != nil {
		return nil, fmt.Errorf("temp wallet pkh: %w", err)
	}
	addr := Address.WalletAddressFromBytes(pkh[:], stakeHash, network)
	if addr == nil {
		return nil, errors.New("temp wallet: nil address from key hashes")
	}
	return &TempWallet{
		Address:   addr.String(),
		Pkh:       hex.EncodeToString(pkh[:]),
		vkey:      vk,
		skey:      sk,
		stakeVkey: stakeVk,
		stakeSkey: stakeSk,
	}, nil
}

// rawTxForSign mirrors the on-wire tx array but keeps the body as raw CBOR, so
// we can hash the EXACT body bytes the node will hash. Re-marshaling the body
// through a typed struct changes the bytes (field order / set tags) and yields
// the wrong tx id, producing InvalidWitnesses on submit.
type rawTxForSign struct {
	_             struct{} `cbor:",toarray"`
	Body          cbor.RawMessage
	WitnessSet    cbor.RawMessage
	Valid         bool
	AuxiliaryData cbor.RawMessage
}

// SignTx signs an externally-built protocol tx (Surf/Liqwid) with the PAYMENT
// key only and merges the witness in. A normal UTxO spend needs only the
// payment credential's witness; adding the stake witness would both be
// unnecessary and break the protocol's fee budget (FeeTooSmall).
func (w *TempWallet) SignTx(txHex string) (string, error) {
	witnessSetHex, err := w.witnessFor(txHex, false)
	if err != nil {
		return "", err
	}
	return sources.MergeTxWitnesses(txHex, witnessSetHex)
}

// WitnessSetHex produces a witness-set CBOR (hex) with BOTH the payment and
// stake vkey witnesses, for DexHunter's /swap/sign (its swap tx requires
// witnesses from both credentials of the base address).
func (w *TempWallet) WitnessSetHex(txHex string) (string, error) { return w.witnessFor(txHex, true) }

// PaymentWitness produces the PAYMENT-only witness-set CBOR (hex), as Surf's
// /api/wallet/assemble expects — it merges this witness into the build-time tx
// itself, so we hand it the witness set rather than a pre-merged signed tx.
func (w *TempWallet) PaymentWitness(txHex string) (string, error) { return w.witnessFor(txHex, false) }

// witnessFor signs the tx body hash (blake2b-256 of the RAW body bytes) with
// the payment key and, when includeStake is set, the stake key too.
func (w *TempWallet) witnessFor(txHex string, includeStake bool) (string, error) {
	raw, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("sign: decode tx hex: %w", err)
	}
	var rtx rawTxForSign
	if err := cbor.Unmarshal(raw, &rtx); err != nil {
		return "", fmt.Errorf("sign: unmarshal tx: %w", err)
	}
	h, err := blake2b.New256(nil)
	if err != nil {
		return "", err
	}
	h.Write(rtx.Body)
	bodyHash := h.Sum(nil)

	paySig, err := w.skey.Sign(bodyHash)
	if err != nil {
		return "", fmt.Errorf("sign payment: %w", err)
	}
	witnesses := []VerificationKeyWitness.VerificationKeyWitness{
		{Vkey: w.vkey, Signature: paySig},
	}
	if includeStake {
		stakeSig, serr := w.stakeSkey.Sign(bodyHash)
		if serr != nil {
			return "", fmt.Errorf("sign stake: %w", serr)
		}
		witnesses = append(witnesses, VerificationKeyWitness.VerificationKeyWitness{Vkey: w.stakeVkey, Signature: stakeSig})
	}
	ws := TransactionWitnessSet.TransactionWitnessSet{VkeyWitnesses: witnesses}
	enc, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		return "", err
	}
	out, err := enc.Marshal(ws)
	if err != nil {
		return "", fmt.Errorf("sign: marshal witness set: %w", err)
	}
	return hex.EncodeToString(out), nil
}

// VKey/SKey expose the underlying keys for callers that build txs with apollo
// directly (funding/sweep) and need to sign in-builder.
func (w *TempWallet) VKey() Key.VerificationKey { return w.vkey }
func (w *TempWallet) SKey() Key.SigningKey      { return w.skey }

// --- persistence: encrypt the signing key at rest ---------------------------

// SealedKey is the encrypted form of a temp wallet's signing key, safe to
// persist in Mongo. Address/Pkh are kept in clear so the engine can read
// position state without decrypting.
type SealedKey struct {
	Address      string `bson:"address" json:"address"`
	Pkh          string `bson:"pkh" json:"pkh"`
	VKeyHex      string `bson:"vkeyHex" json:"vkeyHex"`           // payment vkey
	StakeVKeyHex string `bson:"stakeVkeyHex" json:"stakeVkeyHex"` // stake vkey (new format)
	StakeHashHex string `bson:"stakeHashHex" json:"stakeHashHex"` // staking part, for address reconstruction
	CipherText   string `bson:"cipherText" json:"cipherText"`     // hex(nonce||ciphertext)
}

// sealedKeys is the JSON plaintext encrypted in CipherText for the current
// format — both signing keys.
type sealedKeys struct {
	Pay   []byte `json:"pay"`
	Stake []byte `json:"stake"`
}

// Seal encrypts BOTH signing keys (payment + stake) with AES-256-GCM using a
// key derived from the engine encryption secret.
func (w *TempWallet) Seal(encSecret string) (*SealedKey, error) {
	gcm, err := newGCM(encSecret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(sealedKeys{Pay: w.skey.Payload, Stake: w.stakeSkey.Payload})
	if err != nil {
		return nil, err
	}
	stakeHash, err := w.stakeVkey.Hash()
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	return &SealedKey{
		Address:      w.Address,
		Pkh:          w.Pkh,
		VKeyHex:      hex.EncodeToString(w.vkey.Payload),
		StakeVKeyHex: hex.EncodeToString(w.stakeVkey.Payload),
		StakeHashHex: hex.EncodeToString(stakeHash[:]),
		CipherText:   hex.EncodeToString(ct),
	}, nil
}

// Open decrypts a SealedKey back into a usable TempWallet. It handles both the
// current two-key format and the legacy single-payment-key format (jobs sealed
// before the stake key was persisted) — legacy wallets reconstruct from the
// stored stake hash and can still be swept (payment-only spends).
func (s *SealedKey) Open(encSecret string, network constants.Network) (*TempWallet, error) {
	gcm, err := newGCM(encSecret)
	if err != nil {
		return nil, err
	}
	blob, err := hex.DecodeString(s.CipherText)
	if err != nil {
		return nil, fmt.Errorf("unseal: decode ciphertext: %w", err)
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("unseal: ciphertext too short")
	}
	plain, err := gcm.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("unseal: %w", err)
	}
	payVkBytes, err := hex.DecodeString(s.VKeyHex)
	if err != nil {
		return nil, fmt.Errorf("unseal: decode vkey: %w", err)
	}

	// Legacy format: no stake vkey was stored, CipherText is the single payment
	// key. Reconstruct with the stored stake hash; reuse the payment key as a
	// stand-in stake key (only used to satisfy the struct — legacy jobs only
	// ever sweep, which spends with the payment key).
	if s.StakeVKeyHex == "" {
		stakeHash, herr := hex.DecodeString(s.StakeHashHex)
		if herr != nil {
			return nil, fmt.Errorf("unseal: decode stake hash: %w", herr)
		}
		payVk := Key.VerificationKey{Payload: payVkBytes}
		paySk := Key.SigningKey{Payload: plain}
		return walletFromKeys(payVk, paySk, payVk, paySk, stakeHash, network)
	}

	var ks sealedKeys
	if err := json.Unmarshal(plain, &ks); err != nil {
		return nil, fmt.Errorf("unseal: decode keys: %w", err)
	}
	stakeVkBytes, err := hex.DecodeString(s.StakeVKeyHex)
	if err != nil {
		return nil, fmt.Errorf("unseal: decode stake vkey: %w", err)
	}
	stakeVk := Key.VerificationKey{Payload: stakeVkBytes}
	stakeHash, err := stakeVk.Hash()
	if err != nil {
		return nil, fmt.Errorf("unseal: stake hash: %w", err)
	}
	return walletFromKeys(
		Key.VerificationKey{Payload: payVkBytes},
		Key.SigningKey{Payload: ks.Pay},
		stakeVk,
		Key.SigningKey{Payload: ks.Stake},
		stakeHash[:],
		network,
	)
}

// newGCM derives a 32-byte AES key from the engine secret (SHA-256) and
// returns an AES-GCM AEAD. An empty secret is rejected so we never silently
// persist temp keys with a zero key.
func newGCM(encSecret string) (cipher.AEAD, error) {
	if encSecret == "" {
		return nil, errors.New("ENGINE_ENC_KEY is required to seal temp wallet keys")
	}
	sum := sha256.Sum256([]byte(encSecret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
