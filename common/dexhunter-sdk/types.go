package dexhunter

// Token IDs in DexHunter requests are encoded as `policyId.assetNameHex`,
// or the literal empty string for ADA. Helpers in this package use plain
// strings to keep the call sites obvious.

// Split is one leg of a routed swap: how much of the input goes through
// which pool on which DEX. Returned by every estimate/swap response.
type Split struct {
	AmountIn      float64 `json:"amount_in"`
	ExpectedOut   float64 `json:"expected_output"`
	PoolID        string  `json:"pool_id"`
	DexName       string  `json:"dex"`
	PriceImpact   float64 `json:"price_impact,omitempty"`
	BatcherFee    float64 `json:"batcher_fee,omitempty"`
	DepositAmount float64 `json:"deposit,omitempty"`
}

// Route is one of the candidate routes considered before fee/impact
// scoring. The chosen route is reflected in `Splits` on the parent
// response; `PossibleRoutes` is informational.
type Route struct {
	Path        []string `json:"path"`
	DexName     string   `json:"dex"`
	ExpectedOut float64  `json:"expected_output"`
}

// Fees is the per-swap fee breakdown. Fields not returned by the API
// for a given endpoint stay zero.
type Fees struct {
	BatcherFee  float64 `json:"batcher_fee,omitempty"`
	DepositFee  float64 `json:"deposit_fee,omitempty"`
	PartnerFee  float64 `json:"partner_fee,omitempty"`
	FrontendFee float64 `json:"frontend_fee,omitempty"`
	NetworkFee  float64 `json:"network_fee,omitempty"`
}

// --- /swap/estimate ---------------------------------------------------------

type EstimateRequest struct {
	AmountIn           float64  `json:"amount_in"`
	TokenIn            string   `json:"token_in"`
	TokenOut           string   `json:"token_out"`
	Slippage           float64  `json:"slippage"`
	BlacklistedDexes   []string `json:"blacklisted_dexes,omitempty"`
	SinglePreferredDex string   `json:"single_preferred_dex,omitempty"`
}

type EstimateResponse struct {
	TotalInput     float64 `json:"total_input"`
	TotalOutput    float64 `json:"total_output"`
	NetPrice       float64 `json:"net_price"`
	NetPriceReverse float64 `json:"net_price_reverse,omitempty"`
	PriceImpact    float64 `json:"price_impact,omitempty"`
	Fees           Fees    `json:"fees"`
	Splits         []Split `json:"splits"`
	PossibleRoutes []Route `json:"possible_routes,omitempty"`
}

// --- /swap/reverseEstimate --------------------------------------------------

type ReverseEstimateRequest struct {
	AmountOut        float64  `json:"amount_out"`
	TokenIn          string   `json:"token_in"`
	TokenOut         string   `json:"token_out"`
	Slippage         float64  `json:"slippage"`
	BlacklistedDexes []string `json:"blacklisted_dexes,omitempty"`
	BuyerAddress     string   `json:"buyer_address,omitempty"`
}

type ReverseEstimateResponse struct {
	TotalInput  float64 `json:"total_input"`
	TotalOutput float64 `json:"total_output"`
	PriceAB     float64 `json:"price_ab"`
	PriceBA     float64 `json:"price_ba"`
	Splits      []Split `json:"splits"`
}

// --- /swap/swap -------------------------------------------------------------

// SwapRequest is the executable swap. The CBOR returned in SwapResponse
// is unsigned; sign with the user's wallet then submit via Sign().
type SwapRequest struct {
	AmountIn         float64  `json:"amount_in"`
	TokenIn          string   `json:"token_in"`
	TokenOut         string   `json:"token_out"`
	BuyerAddress     string   `json:"buyer_address"`
	Slippage         float64  `json:"slippage"`
	Inputs           []string `json:"inputs,omitempty"`
	BlacklistedDexes []string `json:"blacklisted_dexes,omitempty"`
}

type SwapResponse struct {
	CBOR           string   `json:"cbor"`
	TotalInput     float64  `json:"total_input"`
	TotalOutput    float64  `json:"total_output"`
	Fees           Fees     `json:"fees"`
	Splits         []Split  `json:"splits"`
	PossibleRoutes []Route  `json:"possible_routes,omitempty"`
	Communications []string `json:"communications,omitempty"`
}

// --- /swap/sign -------------------------------------------------------------

type SignRequest struct {
	TxCBOR     string   `json:"txCbor"`
	Signatures []string `json:"Signatures"`
}

type SignResponse struct {
	CBOR    string `json:"cbor"`
	StratID string `json:"strat_id"`
}

// --- /swap/cancel -----------------------------------------------------------

type CancelRequest struct {
	Address string `json:"address"`
	OrderID string `json:"order_id"`
}

type CancelResponse struct {
	CBOR                     string  `json:"cbor"`
	AdditionalCancellationFee float64 `json:"additional_cancellation_fee"`
}

// --- /swap/limit and /swap/limitEstimate ------------------------------------

type LimitOrderRequest struct {
	AmountIn         float64  `json:"amount_in"`
	WantedPrice      float64  `json:"wanted_price"`
	TokenIn          string   `json:"token_in"`
	TokenOut         string   `json:"token_out"`
	BuyerAddress     string   `json:"buyer_address"`
	Dex              string   `json:"dex,omitempty"`
	Multiples        int      `json:"multiples,omitempty"`
	BlacklistedDexes []string `json:"blacklisted_dexes,omitempty"`
}

type LimitOrderResponse struct {
	CBOR           string  `json:"cbor"`
	TotalInput     float64 `json:"total_input,omitempty"`
	TotalOutput    float64 `json:"total_output"`
	Fees           Fees    `json:"fees"`
	Splits         []Split `json:"splits"`
	PossibleRoutes []Route `json:"possible_routes,omitempty"`
}

// --- /swap/orders/{address} -------------------------------------------------

type Order struct {
	OrderID    string  `json:"order_id"`
	Status     string  `json:"status"`
	TokenIn    string  `json:"token_in"`
	TokenOut   string  `json:"token_out"`
	AmountIn   float64 `json:"amount_in"`
	AmountOut  float64 `json:"amount_out"`
	Dex        string  `json:"dex"`
	TxHash     string  `json:"tx_hash"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
	OrderType  string  `json:"order_type,omitempty"`
}

// --- /swap/wallet -----------------------------------------------------------

type WalletRequest struct {
	Addresses []string `json:"addresses"`
}

type WalletToken struct {
	PolicyID  string  `json:"policy_id"`
	AssetName string  `json:"asset_name"`
	Ticker    string  `json:"ticker,omitempty"`
	Amount    float64 `json:"amount"`
	ValueADA  float64 `json:"value_ada,omitempty"`
	ValueUSD  float64 `json:"value_usd,omitempty"`
	Decimals  int     `json:"decimals,omitempty"`
}

type WalletInfoResponse struct {
	CardanoBalance float64       `json:"cardano"`
	Tokens         []WalletToken `json:"tokens"`
}

// --- /dca/* -----------------------------------------------------------------

type CreateDCARequest struct {
	UserAddress    string   `json:"user_address"`
	TokenIn        string   `json:"token_in"`
	TokenOut       string   `json:"token_out"`
	AmountIn       float64  `json:"amount_in"`
	Cycles         int      `json:"cycles"`
	Interval       int      `json:"interval"`
	IntervalLength string   `json:"interval_length"` // "minute" | "hour" | "day" | "week"
	DexAllowlist   []string `json:"dex_allowlist,omitempty"`
}

type CreateDCAResponse struct {
	DCAID         string  `json:"dca_id"`
	AmountTokenIn float64 `json:"amount_token_in"`
	AmountADAIn   float64 `json:"amount_ada_in"`
	CBOR          string  `json:"cbor"`
	Fees          Fees    `json:"fees"`
	Deposits      float64 `json:"deposits"`
}

type CancelDCARequest struct {
	UserAddress string `json:"user_address"`
	DCAID       string `json:"dca_id"`
}

type DCAOrder struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	TokenIn          string  `json:"token_in"`
	TokenOut         string  `json:"token_out"`
	RemainingCycles  int     `json:"remaining_cycles"`
	NextExecutionMs  int64   `json:"next_execution"`
	LastExecutionMs  int64   `json:"last_execution,omitempty"`
	AmountPerCycle   float64 `json:"amount_per_cycle"`
}

// --- /charts ----------------------------------------------------------------

type ChartRequest struct {
	TokenIn  string `json:"tokenIn"`
	TokenOut string `json:"tokenOut"`
	Period   string `json:"period"` // e.g. "1h", "1d"
	From     int64  `json:"from"`   // unix seconds
	To       int64  `json:"to"`
	IsLast   bool   `json:"isLast,omitempty"`
}

type Candle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type ChartResponse struct {
	Data   []Candle `json:"data"`
	Period string   `json:"period"`
}

// --- /marking/submit --------------------------------------------------------

type MarkerOrderType string

const (
	MarkerLimit    MarkerOrderType = "LIMIT"
	MarkerStopLoss MarkerOrderType = "STOP_LOSS"
	MarkerDCA      MarkerOrderType = "DCA"
	MarkerSwap     MarkerOrderType = "SWAP"
)

type MarkerRequest struct {
	TxHash    string          `json:"tx_hash"`
	OrderType MarkerOrderType `json:"order_type"`
	CBOR      string          `json:"cbor"`
}
