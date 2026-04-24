package sources

import "context"

// TxParams is the request shape for any of the Build* methods on TxBuilder.
//
// All numeric Amount fields are in WHOLE units of the user-facing asset
// (e.g. 100 means "100 ADA"). Each source converts to the protocol's
// expected units (lovelace, raw asset name, …).
//
// Address fields are bech32 mainnet addresses sourced from CIP-30 calls
// (`getUsedAddresses` / `getChangeAddress`). Wallet/UTXOs come from the
// frontend; the backend doesn't talk to the wallet directly.
type TxParams struct {
	Source           string  `json:"source"`           // "liqwid" / "surf"
	Action           string  `json:"action"`           // "supply" / "withdraw" / "borrow" (set by handler)
	MarketID         string  `json:"marketId"`         // protocol-internal id
	Amount           float64 `json:"amount"`           // whole units of the borrow/supply asset
	CollateralAmount float64 `json:"collateralAmount"` // borrow only — whole units of collateral asset
	Full             bool    `json:"full"`             // withdraw only — subtract 1 raw unit to avoid exchange-rate overshoot

	// Wallet context — provided by the frontend after CIP-30 enable.
	Wallet         string   `json:"wallet"`         // "lace", "eternl", "nami", …
	Address        string   `json:"address"`        // bech32 first used address
	ChangeAddress  string   `json:"changeAddress"`  // bech32
	OtherAddresses []string `json:"otherAddresses"` // bech32, may be empty
	UTXOs          []string `json:"utxos"`          // CIP-30 cbor-hex; required by Liqwid
}

// BuiltTx is the unsigned transaction returned to the frontend, ready to
// be signed via CIP-30 and submitted.
type BuiltTx struct {
	Source string `json:"source"`
	Action string `json:"action"`
	CBOR   string `json:"cbor"`           // hex-encoded unsigned tx
	Hint   string `json:"hint,omitempty"` // human description for the modal
}

// SubmitParams is the request shape for submitting a signed transaction
// via a protocol's remote submit endpoint. Both Liqwid and Surf expose
// such endpoints, which lets us skip fragile client-side witness-set
// merging: the protocol merges the wallet witnesses into the original
// build-time tx on its own server and broadcasts to the chain.
type SubmitParams struct {
	Source     string `json:"source"`     // "liqwid" / "surf"
	CBOR       string `json:"cbor"`       // unsigned tx hex from the Build* step
	WitnessSet string `json:"witnessSet"` // CIP-30 signTx result (witness set hex)
	Address    string `json:"address"`    // bech32 first used address (Surf needs it)
	Wallet     string `json:"wallet"`     // "lace" / "eternl" (Surf's `canonical` field)
}

// TxCloseParams identifies a specific borrow position or pending order
// to be closed (repaid or cancelled). Surf uses an outRef
// (txHash + outputIndex) to look up the on-chain UTxO; Liqwid uses the
// loan id + the user's UTxOs to build a modifyBorrow tx.
type TxCloseParams struct {
	Source      string `json:"source"`      // "liqwid" / "surf"
	MarketID    string `json:"marketId"`    // Surf poolId or Liqwid market id
	Address     string `json:"address"`     // bech32
	Wallet      string `json:"wallet"`      // "lace", "eternl", …
	TxHash      string `json:"txHash"`      // outRef tx hash (Surf) or loan tx id (Liqwid)
	OutputIndex int    `json:"outputIndex"` // outRef output index (Surf)
	// Kind is "repay" for settled (active) positions and "cancel" for
	// pending batcher orders. Surf routes to /api/repay or
	// /api/cancelOrder accordingly. Liqwid only supports "repay".
	Kind string `json:"kind"`
	// Wallet context — needed by Liqwid's modifyBorrow mutation to
	// select inputs and pay fees. Surf infers these from the address.
	ChangeAddress  string   `json:"changeAddress"`
	OtherAddresses []string `json:"otherAddresses"`
	UTXOs          []string `json:"utxos"`
	// Pkh is the user's payment-key hash (hex). Liqwid's LoansInput
	// uses it as the paymentKeys filter so we can find the right loan
	// among potentially many. Surf doesn't need it.
	Pkh string `json:"pkh"`
	// RedeemCollateral controls whether Liqwid's modifyBorrow releases
	// the locked collateral back to the user (true) or keeps it
	// supplied in the protocol (false). Surf always redeems.
	RedeemCollateral bool `json:"redeemCollateral"`
}

// TxBuilder is implemented by sources that can build supply/withdraw/borrow
// transactions on behalf of the user AND submit them via the protocol's
// own submit endpoint. Cache and persistence wrappers delegate through
// to the inner source so the type assertion works at the API layer.
type TxBuilder interface {
	BuildSupply(ctx context.Context, p TxParams) (*BuiltTx, error)
	BuildWithdraw(ctx context.Context, p TxParams) (*BuiltTx, error)
	BuildBorrow(ctx context.Context, p TxParams) (*BuiltTx, error)

	// BuildClose builds a full-repay or cancel-pending-order tx,
	// identified by an outRef on the position/order. No user-entered
	// amount — the protocol computes the exact repayment amount from
	// the on-chain state itself.
	BuildClose(ctx context.Context, p TxCloseParams) (*BuiltTx, error)

	// SubmitTx combines the unsigned tx with the wallet's witness set
	// and submits it via the protocol's own endpoint. Returns the
	// on-chain transaction hash.
	SubmitTx(ctx context.Context, p SubmitParams) (string, error)
}
