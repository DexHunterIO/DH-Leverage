package sources

import (
	"encoding/hex"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func TestMergeTxWitnesses_InvalidHex(t *testing.T) {
	_, err := MergeTxWitnesses("not-hex", "abcd")
	if err == nil {
		t.Fatal("expected error for invalid tx hex")
	}

	_, err = MergeTxWitnesses("abcd", "not-hex")
	if err == nil {
		t.Fatal("expected error for invalid ws hex")
	}
}

func TestMergeTxWitnesses_RoundTrip(t *testing.T) {
	// Build a minimal valid Cardano tx shape:
	// [body, witness_set, valid, auxiliary_data]
	// body = {} (empty map)
	// witness_set = {0: [[vk1, sig1]]}   (one vkey witness)
	// valid = true
	// auxiliary_data = null
	body, _ := cbor.Marshal(map[int]any{})
	vkey1 := []cbor.RawMessage{mustCBOR([]byte("vk1_placeholder_32bytesAAAAAAAA")), mustCBOR([]byte("sig1_placeholder_64bytesAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))}
	ws1 := map[uint64]cbor.RawMessage{
		0: mustCBOR([]cbor.RawMessage{mustCBOR(vkey1)}),
	}
	ws1Bytes, _ := cbor.Marshal(ws1)

	tx := struct {
		_    struct{} `cbor:",toarray"`
		Body cbor.RawMessage
		WS   cbor.RawMessage
		V    bool
		Aux  cbor.RawMessage
	}{
		Body: body,
		WS:   ws1Bytes,
		V:    true,
		Aux:  mustCBOR(nil),
	}
	txBytes, _ := cbor.Marshal(&tx)
	txHex := hex.EncodeToString(txBytes)

	// Fresh witness set with one new vkey.
	vkey2 := []cbor.RawMessage{mustCBOR([]byte("vk2_placeholder_32bytesBBBBBBBB")), mustCBOR([]byte("sig2_placeholder_64bytesBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"))}
	freshWS := map[uint64]cbor.RawMessage{
		0: mustCBOR([]cbor.RawMessage{mustCBOR(vkey2)}),
	}
	freshWSBytes, _ := cbor.Marshal(freshWS)
	wsHex := hex.EncodeToString(freshWSBytes)

	merged, err := MergeTxWitnesses(txHex, wsHex)
	if err != nil {
		t.Fatalf("MergeTxWitnesses: %v", err)
	}

	// Decode result and verify two vkey witnesses exist.
	mergedBytes, _ := hex.DecodeString(merged)
	var result rawTx
	if err := cbor.Unmarshal(mergedBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	var resultWS map[uint64]cbor.RawMessage
	if err := cbor.Unmarshal(result.WitnessSet, &resultWS); err != nil {
		t.Fatalf("unmarshal result ws: %v", err)
	}

	vkeySlot, ok := resultWS[0]
	if !ok {
		t.Fatal("missing vkey slot in merged ws")
	}

	var vkeys []cbor.RawMessage
	if err := cbor.Unmarshal(vkeySlot, &vkeys); err != nil {
		t.Fatalf("unmarshal vkeys: %v", err)
	}

	if len(vkeys) != 2 {
		t.Fatalf("expected 2 vkey witnesses, got %d", len(vkeys))
	}
}

func TestMarshalWitnessMap_Empty(t *testing.T) {
	b, err := marshalWitnessMap(map[uint64]cbor.RawMessage{})
	if err != nil {
		t.Fatalf("marshalWitnessMap: %v", err)
	}
	if hex.EncodeToString(b) != "a0" {
		t.Fatalf("expected empty map (a0), got %s", hex.EncodeToString(b))
	}
}

func TestSlotKeys(t *testing.T) {
	m := map[uint64]cbor.RawMessage{
		5: {},
		0: {},
		3: {},
	}
	keys := slotKeys(m)
	if len(keys) != 3 || keys[0] != 0 || keys[1] != 3 || keys[2] != 5 {
		t.Fatalf("expected [0 3 5], got %v", keys)
	}
}

func mustCBOR(v any) cbor.RawMessage {
	b, err := cbor.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
