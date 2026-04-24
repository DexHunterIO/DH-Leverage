package sources

import (
	"encoding/hex"
	"fmt"
	"log"
	"sort"

	"github.com/fxamacker/cbor/v2"
)

// rawTx mirrors the on-wire Cardano transaction array shape but keeps
// every field except the witness set as raw CBOR. The wallet signs the
// BODY HASH (= blake2b(body bytes)); round-tripping the body through a
// typed Go struct would mutate the bytes (field ordering, dropped tag
// 258, added defaults) and invalidate every signature.
type rawTx struct {
	_             struct{} `cbor:",toarray"`
	Body          cbor.RawMessage
	WitnessSet    cbor.RawMessage
	Valid         bool
	AuxiliaryData cbor.RawMessage
}

// Witness-set slot numbers per Cardano CDDL. See conway.cddl in the
// ledger repo. Key 5 (redeemers) changed shape from an array to a map
// in Conway — decoding as cbor.RawMessage keeps whichever form the
// protocol produced.
const (
	wsKeyVkeyWitnesses uint64 = 0
	wsKeyRedeemer      uint64 = 5
)

// MergeTxWitnesses takes an unsigned tx CBOR (as returned by a protocol's
// tx-builder) and a freshly signed witness set CBOR (as returned by a
// CIP-30 wallet's signTx call) and produces the combined signed tx CBOR
// ready to broadcast via wallet.submitTx() or any submit endpoint.
//
// Surgical merge strategy — decode the witness set as a plain
// map[uint64]cbor.RawMessage (NOT a typed struct with keyasint tags)
// so that slots we never touch pass through byte-identical. The only
// mutation is at slot 0 (vkey witnesses): append the wallet's fresh
// vkey witnesses to whatever the protocol already had.
//
// The body, auxiliary data, plutus scripts, plutus data, and redeemers
// all survive untouched, which keeps both body_hash (signature
// validity) and script_data_hash (Plutus script validation) intact.
func MergeTxWitnesses(txHex, wsHex string) (string, error) {
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("decode tx hex: %w", err)
	}
	wsBytes, err := hex.DecodeString(wsHex)
	if err != nil {
		return "", fmt.Errorf("decode witness hex: %w", err)
	}

	var tx rawTx
	if err := cbor.Unmarshal(txBytes, &tx); err != nil {
		return "", fmt.Errorf("unmarshal tx: %w", err)
	}

	log.Printf("tx-finalize: in body=%d ws=%d aux=%d valid=%v",
		len(tx.Body), len(tx.WitnessSet), len(tx.AuxiliaryData), tx.Valid)
	log.Printf("tx-finalize: in ws hex=%s", hex.EncodeToString(tx.WitnessSet))

	existing := map[uint64]cbor.RawMessage{}
	if len(tx.WitnessSet) > 0 {
		if err := cbor.Unmarshal(tx.WitnessSet, &existing); err != nil {
			return "", fmt.Errorf("unmarshal existing witness set: %w", err)
		}
	}
	log.Printf("tx-finalize: existing ws slots=%v", slotKeys(existing))

	fresh := map[uint64]cbor.RawMessage{}
	if err := cbor.Unmarshal(wsBytes, &fresh); err != nil {
		return "", fmt.Errorf("unmarshal new witness set: %w", err)
	}
	log.Printf("tx-finalize: fresh ws slots=%v", slotKeys(fresh))

	// Merge vkey witnesses (slot 0). Decode each side as a CBOR array
	// of raw messages so we can concatenate without touching the
	// individual witness bytes.
	if freshVkey, ok := fresh[wsKeyVkeyWitnesses]; ok && len(freshVkey) > 0 {
		var existingList []cbor.RawMessage
		if ex, ok := existing[wsKeyVkeyWitnesses]; ok && len(ex) > 0 {
			if err := cbor.Unmarshal(ex, &existingList); err != nil {
				return "", fmt.Errorf("unmarshal existing vkey witnesses: %w", err)
			}
		}
		var newList []cbor.RawMessage
		if err := cbor.Unmarshal(freshVkey, &newList); err != nil {
			return "", fmt.Errorf("unmarshal fresh vkey witnesses: %w", err)
		}
		combined := append(existingList, newList...)

		// Canonical encoder — produces definite-length arrays, which is
		// what Cardano nodes accept for the final signed tx.
		encOpts := cbor.CanonicalEncOptions()
		encOpts.TagsMd = cbor.TagsAllowed
		enc, err := encOpts.EncMode()
		if err != nil {
			return "", fmt.Errorf("vkey enc mode: %w", err)
		}
		vkeyBytes, err := enc.Marshal(combined)
		if err != nil {
			return "", fmt.Errorf("marshal merged vkey witnesses: %w", err)
		}
		existing[wsKeyVkeyWitnesses] = vkeyBytes
		log.Printf("tx-finalize: merged vkeys existing=%d fresh=%d total=%d",
			len(existingList), len(newList), len(combined))
	}

	// Sanity check: the redeemer slot we preserve MUST still be there
	// after the merge. If it vanished, surface it loudly.
	if red, ok := existing[wsKeyRedeemer]; ok {
		log.Printf("tx-finalize: preserved redeemer slot bytes=%d hex=%s",
			len(red), hex.EncodeToString(red))
	} else {
		log.Printf("tx-finalize: WARNING no redeemer in existing witness set")
	}

	mergedWS, err := marshalWitnessMap(existing)
	if err != nil {
		return "", fmt.Errorf("marshal merged witness set: %w", err)
	}
	tx.WitnessSet = mergedWS
	log.Printf("tx-finalize: out ws hex=%s", hex.EncodeToString(mergedWS))

	encOpts := cbor.CTAP2EncOptions()
	encOpts.TagsMd = cbor.TagsAllowed
	encOpts.IndefLength = cbor.IndefLengthAllowed
	enc, err := encOpts.EncMode()
	if err != nil {
		return "", fmt.Errorf("enc mode: %w", err)
	}
	signed, err := enc.Marshal(&tx)
	if err != nil {
		return "", fmt.Errorf("marshal signed tx: %w", err)
	}
	return hex.EncodeToString(signed), nil
}

// marshalWitnessMap emits the witness-set map with integer keys in
// ascending order (canonical CBOR for Cardano). map[uint64]any
// serialization via fxamacker sorts numerically when using
// CanonicalEncOptions, which is what we want.
func marshalWitnessMap(m map[uint64]cbor.RawMessage) ([]byte, error) {
	if len(m) == 0 {
		return []byte{0xa0}, nil // empty map
	}
	encOpts := cbor.CanonicalEncOptions()
	encOpts.TagsMd = cbor.TagsAllowed
	encOpts.IndefLength = cbor.IndefLengthAllowed
	enc, err := encOpts.EncMode()
	if err != nil {
		return nil, err
	}
	return enc.Marshal(m)
}

// slotKeys returns the sorted list of keys in a witness-set map, used
// only for debug logging.
func slotKeys(m map[uint64]cbor.RawMessage) []uint64 {
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
