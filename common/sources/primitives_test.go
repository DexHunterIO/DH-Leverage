package sources

import (
	"testing"
)

func TestErrSentinel(t *testing.T) {
	err := ErrUnsupportedTxBuilder
	if err.Error() != "source does not implement TxBuilder" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestOrderConstants(t *testing.T) {
	// Verify constants are distinct non-empty strings.
	statuses := []string{OrderActive, OrderClosed, OrderPending}
	seen := map[string]bool{}
	for _, s := range statuses {
		if s == "" {
			t.Fatal("empty status constant")
		}
		if seen[s] {
			t.Fatalf("duplicate status: %s", s)
		}
		seen[s] = true
	}

	types := []string{TypeBorrow, TypeRepay, TypeLend, TypeDeposit, TypeWithdraw, TypeUnknown}
	seen = map[string]bool{}
	for _, s := range types {
		if s == "" {
			t.Fatal("empty type constant")
		}
		if seen[s] {
			t.Fatalf("duplicate type: %s", s)
		}
		seen[s] = true
	}
}
