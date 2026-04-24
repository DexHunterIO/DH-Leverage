package wallet

import (
	"context"
	"testing"
	"time"
)

func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"123", 123},
		{"1000000", 1000000},
		{"", 0},
		{"abc", 0},
		{"12abc", 12},
		{"123456789012345", 123456789012345},
	}
	for _, tc := range cases {
		got := parseInt64(tc.in)
		if got != tc.want {
			t.Errorf("parseInt64(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestHexToAscii(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"534e454b", "SNEK"},
		{"414441", "ADA"},
		{"4e49474854", "NIGHT"},
		// Non-printable byte → empty string.
		{"00ff", ""},
		// Odd-length → last nibble ignored, "4" alone is skipped.
		{"41424", "AB"},
		// Invalid hex chars.
		{"ZZZZ", ""},
	}
	for _, tc := range cases {
		got := hexToAscii(tc.in)
		if got != tc.want {
			t.Errorf("hexToAscii(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHexNibble(t *testing.T) {
	cases := []struct {
		in     byte
		want   byte
		wantOK bool
	}{
		{'0', 0, true},
		{'9', 9, true},
		{'a', 10, true},
		{'f', 15, true},
		{'A', 10, true},
		{'F', 15, true},
		{'g', 0, false},
		{'z', 0, false},
	}
	for _, tc := range cases {
		got, ok := hexNibble(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("hexNibble(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestMemoryBalanceCache_GetMiss(t *testing.T) {
	c := NewMemoryCache()
	_, ok, err := c.Get(context.Background(), "addr1xxx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestMemoryBalanceCache_SetAndGet(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	bal := &Balance{Address: "addr1xxx", Lovelace: 5000000}

	if err := c.Set(ctx, "addr1xxx", bal, 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := c.Get(ctx, "addr1xxx")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Lovelace != 5000000 {
		t.Fatalf("unexpected lovelace: %d", got.Lovelace)
	}
}

func TestMemoryBalanceCache_Expiry(t *testing.T) {
	c := NewMemoryCache()
	ctx := context.Background()
	bal := &Balance{Address: "addr1xxx", Lovelace: 1}

	if err := c.Set(ctx, "addr1xxx", bal, 1*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	_, ok, err := c.Get(ctx, "addr1xxx")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestFetchBalance_EmptyAddress(t *testing.T) {
	client := New(nil, 0)
	_, err := client.FetchBalance(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty address")
	}
}
