package engine

import (
	"strings"
	"testing"

	"github.com/Salvionied/apollo/constants"
)

func TestTempWalletAddressIsMainnetBase(t *testing.T) {
	w, err := NewTempWallet(constants.MAINNET)
	if err != nil {
		t.Fatalf("NewTempWallet: %v", err)
	}
	if !strings.HasPrefix(w.Address, "addr1") {
		t.Fatalf("expected mainnet address (addr1...), got %s", w.Address)
	}
	// DexHunter rejects enterprise addresses, so the temp wallet must be a base
	// address (payment + staking). A base address bech32 is ~103 chars vs ~58
	// for enterprise — the staking part roughly doubles it.
	if len(w.Address) < 90 {
		t.Fatalf("expected a base address (payment+staking), got a short one (%d chars): %s", len(w.Address), w.Address)
	}
	if len(w.Pkh) != 56 { // 28 bytes hex
		t.Fatalf("expected 28-byte pkh hex, got %d chars", len(w.Pkh))
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	w, err := NewTempWallet(constants.MAINNET)
	if err != nil {
		t.Fatalf("NewTempWallet: %v", err)
	}
	const secret = "test-encryption-secret"
	sealed, err := w.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if strings.Contains(sealed.CipherText, "") && sealed.CipherText == "" {
		t.Fatal("ciphertext is empty")
	}
	reopened, err := sealed.Open(secret, constants.MAINNET)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reopened.Address != w.Address {
		t.Fatalf("address mismatch after round-trip: %s != %s", reopened.Address, w.Address)
	}
	if reopened.Pkh != w.Pkh {
		t.Fatalf("pkh mismatch after round-trip")
	}
	// Wrong secret must fail to decrypt.
	if _, err := sealed.Open("wrong-secret", constants.MAINNET); err == nil {
		t.Fatal("expected decryption failure with wrong secret")
	}
}

func TestSealRequiresSecret(t *testing.T) {
	w, _ := NewTempWallet(constants.MAINNET)
	if _, err := w.Seal(""); err == nil {
		t.Fatal("expected Seal to reject an empty secret")
	}
}
