package surf

import (
	"testing"

	"dh-leverage/common/sources"
)

func TestPow10(t *testing.T) {
	cases := []struct {
		n    int
		want float64
	}{
		{0, 1},
		{1, 10},
		{6, 1_000_000},
		{8, 100_000_000},
	}
	for _, tc := range cases {
		got := pow10(tc.n)
		if got != tc.want {
			t.Errorf("pow10(%d) = %f, want %f", tc.n, got, tc.want)
		}
	}
}

func TestSplitUnit(t *testing.T) {
	cases := []struct {
		unit       string
		wantPolicy string
		wantHex    string
	}{
		{"", "", ""},
		// 56-char policy only
		{"a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68", "a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68", ""},
		// policy + asset name
		{"a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68534e454b", "a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68", "534e454b"},
		// short string (< 56)
		{"abc", "abc", ""},
	}
	for _, tc := range cases {
		p, h := splitUnit(tc.unit)
		if p != tc.wantPolicy || h != tc.wantHex {
			t.Errorf("splitUnit(%q) = (%q, %q), want (%q, %q)", tc.unit, p, h, tc.wantPolicy, tc.wantHex)
		}
	}
}

func TestScaleByDecimals(t *testing.T) {
	cases := []struct {
		amt      float64
		decimals int
		want     float64
	}{
		{1_000_000, 6, 1.0},
		{100_000_000, 6, 100.0},
		{35028, 0, 35028},
		{0, 6, 0},
		{500, -1, 500}, // negative decimals = no scaling
	}
	for _, tc := range cases {
		got := scaleByDecimals(tc.amt, tc.decimals)
		if got != tc.want {
			t.Errorf("scaleByDecimals(%f, %d) = %f, want %f", tc.amt, tc.decimals, got, tc.want)
		}
	}
}

func TestNormalizeSurfType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Borrow", sources.TypeBorrow},
		{"Repay", sources.TypeRepay},
		{"Repay With Collateral", sources.TypeRepayCollateral},
		{"Deposit", sources.TypeDeposit},
		{"Withdraw", sources.TypeWithdraw},
		{"Cancel Withdraw", sources.TypeCancelWithdraw},
		{"Leveraged Borrow", sources.TypeLeveragedBorrow},
		{"Liquidation", sources.TypeLiquidation},
		{"SomethingElse", sources.TypeUnknown},
		{"", sources.TypeUnknown},
	}
	for _, tc := range cases {
		got := normalizeSurfType(tc.in)
		if got != tc.want {
			t.Errorf("normalizeSurfType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello…"},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
	}
	for _, tc := range cases {
		got := truncate(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

func TestIsAlreadySubmittedError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"All inputs are spent", true},
		{"Transaction has probably already been included", true},
		{"BadInputsUTxO", true},
		{"some other error", false},
		{"", false},
		{"ConwayMempoolFailure All inputs are spent. Transaction has probably already been included", true},
	}
	for _, tc := range cases {
		got := isAlreadySubmittedError(tc.msg)
		if got != tc.want {
			t.Errorf("isAlreadySubmittedError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestSurfBlockfrostRetryable(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Could not fetch UTxOs from Blockfrost. Try again", true},
		{"Blockfrost rate limit. Try again later", true},
		{"some other error", false},
		{"", false},
	}
	for _, tc := range cases {
		got := surfBlockfrostRetryable(tc.msg)
		if got != tc.want {
			t.Errorf("surfBlockfrostRetryable(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestBuildAssetIndex(t *testing.T) {
	pools := poolInfosResponse{
		PoolInfos: map[string]surfPool{
			"pool1": {
				Asset: surfAsset{Ticker: "ADA", PolicyID: "", AssetName: "", Decimals: 6},
				CollateralAssets: []surfCollateral{
					{Asset: surfAsset{Ticker: "SNEK", PolicyID: "abc123", AssetName: "534e454b", Decimals: 0}},
				},
			},
		},
	}
	idx := buildAssetIndex(pools)

	// ADA has empty unit — should NOT be in the index (empty policyId + assetName skipped)
	if _, ok := idx[""]; ok {
		t.Error("ADA (empty unit) should not be in the asset index")
	}

	// SNEK should be present
	snek := idx["abc123534e454b"]
	if snek.Symbol != "SNEK" {
		t.Errorf("expected SNEK, got %q", snek.Symbol)
	}
	if snek.Decimals != 0 {
		t.Errorf("expected 0 decimals, got %d", snek.Decimals)
	}
}

func TestAssetIndexLookup(t *testing.T) {
	idx := assetIndex{
		"abc123534e454b": sources.Asset{Symbol: "SNEK", Decimals: 0, PolicyID: "abc123", AssetName: "534e454b"},
	}

	// Empty unit = ADA
	ada := idx.lookup("")
	if ada.Symbol != "ADA" || ada.Decimals != 6 {
		t.Errorf("lookup('') = %+v, want ADA/6", ada)
	}

	// Known unit
	snek := idx.lookup("abc123534e454b")
	if snek.Symbol != "SNEK" {
		t.Errorf("lookup SNEK = %+v", snek)
	}

	// Unknown unit — should split at 56 chars
	unknown := idx.lookup("a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68aabbccdd")
	if unknown.PolicyID != "a04ce7a52545e5e33c2867e148898d9e667a69602285f6a1298f9d68" {
		t.Errorf("unknown lookup policy = %q", unknown.PolicyID)
	}
	if unknown.AssetName != "aabbccdd" {
		t.Errorf("unknown lookup assetName = %q", unknown.AssetName)
	}
}

func TestNewClient(t *testing.T) {
	c := New()
	if c.Name() != "surf" {
		t.Errorf("Name() = %q, want surf", c.Name())
	}
	if c.base != defaultBase {
		t.Errorf("base = %q, want %q", c.base, defaultBase)
	}
}

func TestWithBase(t *testing.T) {
	c := New().WithBase("https://test.example.com")
	if c.base != "https://test.example.com" {
		t.Errorf("base = %q", c.base)
	}
}
