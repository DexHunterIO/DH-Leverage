package strike

import (
	"context"
	"sort"
	"sync"
	"time"
)

// HistoryEntry is one recorded deposit or withdrawal for a user address. It is a
// local record (Strike's builder API exposes no transaction-history endpoint), so
// we capture each flow as it completes to power the UI history view.
type HistoryEntry struct {
	ID         string    `json:"id" bson:"id"`
	Address    string    `json:"address" bson:"address"`
	Type       string    `json:"type" bson:"type"`     // "deposit" | "withdraw"
	Asset      string    `json:"asset" bson:"asset"`   // ADA, USDM, …
	Amount     string    `json:"amount" bson:"amount"` // human amount of Asset (optional)
	UsdValue   string    `json:"usdValue" bson:"usdValue"`
	TxHash     string    `json:"txHash" bson:"txHash"`         // on-chain tx (deposit tx / withdraw settlement)
	WithdrawID string    `json:"withdrawId" bson:"withdrawId"` // withdraw quote id (withdrawals)
	RequestID  string    `json:"requestId" bson:"requestId"`   // deposit request id (deposits)
	Status     string    `json:"status" bson:"status"`         // confirmed | pending | sent | failed
	CreatedAt  time.Time `json:"createdAt" bson:"createdAt"`
}

// History records and lists a user's deposit/withdrawal entries.
type History interface {
	Record(ctx context.Context, e HistoryEntry) error
	List(ctx context.Context, address string) ([]HistoryEntry, error)
	// SettleWithdraw attaches the on-chain settlement tx hash (and a new status)
	// to a previously-recorded pending withdrawal, matched by withdrawID.
	SettleWithdraw(ctx context.Context, address, withdrawID, txHash, status string) error
}

// MemoryHistory is an in-memory History (lost on restart). Used as a fallback
// when Mongo is unavailable, mirroring the rest of the app's degradation pattern.
type MemoryHistory struct {
	mu     sync.Mutex
	byAddr map[string][]HistoryEntry
}

// NewMemoryHistory creates an empty in-memory history store.
func NewMemoryHistory() *MemoryHistory {
	return &MemoryHistory{byAddr: map[string][]HistoryEntry{}}
}

func (h *MemoryHistory) Record(_ context.Context, e HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	h.byAddr[e.Address] = append(h.byAddr[e.Address], e)
	return nil
}

func (h *MemoryHistory) List(_ context.Context, address string) ([]HistoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HistoryEntry, len(h.byAddr[address]))
	copy(out, h.byAddr[address])
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (h *MemoryHistory) SettleWithdraw(_ context.Context, address, withdrawID, txHash, status string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.byAddr[address]
	for i := range entries {
		if entries[i].Type == "withdraw" && entries[i].WithdrawID == withdrawID {
			entries[i].TxHash = txHash
			if status != "" {
				entries[i].Status = status
			}
		}
	}
	return nil
}
