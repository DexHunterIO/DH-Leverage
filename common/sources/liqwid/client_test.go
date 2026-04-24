package liqwid

import (
	"math"
	"testing"
)

func TestStripFanout(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"SNEK:Ada", "SNEK"},
		{"Ada", "Ada"},
		{"NIGHT:SNEK", "NIGHT"},
		{"", ""},
		{"A:B:C", "A"}, // only strips at first colon
	}
	for _, tc := range cases {
		got := stripFanout(tc.in)
		if got != tc.want {
			t.Errorf("stripFanout(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLiqwidWalletEnum(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"begin", "BEGIN"},
		{"BEGIN", "BEGIN"},
		{"lace", "ETERNL"},
		{"eternl", "ETERNL"},
		{"nami", "ETERNL"},
		{"", "ETERNL"},
	}
	for _, tc := range cases {
		got := liqwidWalletEnum(tc.in)
		if got != tc.want {
			t.Errorf("liqwidWalletEnum(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFallback(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		{"hello", "world", "hello"},
		{"", "world", "world"},
		{"", "", ""},
		{"a", "", "a"},
	}
	for _, tc := range cases {
		got := fallback(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("fallback(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestUnderlyingToQTokenRaw(t *testing.T) {
	cases := []struct {
		name            string
		underlyingWhole float64
		exchangeRate    float64
		qTokenDec       int
		want            float64
	}{
		{
			name:            "100 ADA at rate 0.025 with 6 decimals",
			underlyingWhole: 100,
			exchangeRate:    0.025,
			qTokenDec:       6,
			want:            math.Round(100 / 0.025 * 1e6),
		},
		{
			name:            "50 SNEK at rate 1.0 with 0 decimals",
			underlyingWhole: 50,
			exchangeRate:    1.0,
			qTokenDec:       0,
			want:            50,
		},
		{
			name:            "zero exchange rate falls back to 1:1",
			underlyingWhole: 100,
			exchangeRate:    0,
			qTokenDec:       6,
			want:            100_000_000,
		},
		{
			name:            "negative exchange rate falls back to 1:1",
			underlyingWhole: 100,
			exchangeRate:    -5,
			qTokenDec:       6,
			want:            100_000_000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := underlyingToQTokenRaw(tc.underlyingWhole, tc.exchangeRate, tc.qTokenDec)
			if got != tc.want {
				t.Errorf("got %f, want %f", got, tc.want)
			}
		})
	}
}

func TestLoanIdMatches(t *testing.T) {
	loan := &rawLoan{
		ID:            "abc123def456-0",
		TransactionID: "abc123def456",
	}

	cases := []struct {
		txHash string
		want   bool
	}{
		{"abc123def456", true},       // matches transactionId
		{"abc123def456-0", true},     // matches full id
		{"abc123def456-1", false},    // different index
		{"xyz789", false},            // no match
		{"abc123", false},            // partial — no match
		{"", false},                  // empty
	}
	for _, tc := range cases {
		got := loanIdMatches(loan, tc.txHash)
		if got != tc.want {
			t.Errorf("loanIdMatches(loan, %q) = %v, want %v", tc.txHash, got, tc.want)
		}
	}
}

func TestLoanIdMatches_BareId(t *testing.T) {
	// Some older loans have id == transactionId (no "-index" suffix)
	loan := &rawLoan{
		ID:            "abc123def456",
		TransactionID: "abc123def456",
	}
	if !loanIdMatches(loan, "abc123def456") {
		t.Error("should match bare id")
	}
}

func TestNewClient(t *testing.T) {
	c := New()
	if c.Name() != "liqwid" {
		t.Errorf("Name() = %q, want liqwid", c.Name())
	}
	if c.endpoint != defaultEndpoint {
		t.Errorf("endpoint = %q", c.endpoint)
	}
}

func TestWithEndpoint(t *testing.T) {
	c := New().WithEndpoint("https://test.example.com/graphql")
	if c.endpoint != "https://test.example.com/graphql" {
		t.Errorf("endpoint = %q", c.endpoint)
	}
}
