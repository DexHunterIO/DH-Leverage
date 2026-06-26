package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	dexhunter "dh-leverage/common/dexhunter-sdk"
)

const (
	// healthPollInterval is how often the monitor refreshes open positions.
	healthPollInterval = 60 * time.Second
	// liqWarnHealth is the health factor below which the monitor logs a warning.
	liqWarnHealth = 1.15
)

// priceProbeAda is the notional used to quote the target's price. A fixed ADA
// amount (rather than 1 token) gives a stable quote for low-decimal / low-value
// tokens and a real route, and price impact cancels out in the entry/current
// ratio since both use the same probe.
const priceProbeAda = 50.0

// priceOf returns the DexHunter price of one whole target token in ADA (ADA per
// target), so it rises when the target appreciates. It quotes buying the target
// with priceProbeAda ADA and inverts; DexHunter's averagePrice endpoint isn't
// used because it doesn't pair against ADA (empty token id → 404), and the live
// swap quote is the better signal for liquidation anyway.
func (e *Engine) priceOf(ctx context.Context, targetUnit string) (float64, error) {
	if e.dex == nil {
		return 0, fmt.Errorf("dexhunter client not configured")
	}
	if targetUnit == "" {
		return 1, nil // ADA priced in ADA
	}
	est, err := e.dex.EstimateSwap(ctx, dexhunter.EstimateRequest{
		AmountIn: priceProbeAda, TokenIn: "", TokenOut: dexToken(targetUnit), Slippage: defaultSlippage,
	})
	if err != nil {
		return 0, fmt.Errorf("price probe: %w", err)
	}
	if est.TotalOutput <= 0 {
		return 0, fmt.Errorf("price probe: zero output")
	}
	return priceProbeAda / est.TotalOutput, nil // ADA per target
}

// StartHealthMonitor launches a background loop that refreshes the health of
// every open position from DexHunter's average price until ctx is cancelled.
// Call once at startup.
func (e *Engine) StartHealthMonitor(ctx context.Context) {
	if e.dex == nil {
		log.Printf("engine: health monitor disabled (no dexhunter client)")
		return
	}
	go func() {
		t := time.NewTicker(healthPollInterval)
		defer t.Stop()
		e.pollHealth(ctx) // once at startup
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.pollHealth(ctx)
			}
		}
	}()
	log.Printf("engine: health monitor started (every %s)", healthPollInterval)
}

// pollHealth refreshes every open position once.
func (e *Engine) pollHealth(ctx context.Context) {
	jobs, err := e.store.ListByStatus(ctx, StatusOpen)
	if err != nil {
		log.Printf("engine: health poll: list open jobs: %v", err)
		return
	}
	for _, job := range jobs {
		if err := e.RefreshHealth(ctx, job); err != nil {
			log.Printf("engine[%s]: health refresh failed: %v", job.ID, err)
		}
	}
}

// RefreshHealth recomputes a position's health factor and P&L from the current
// DexHunter price and persists it. Health scales with the price ratio since
// entry: a long's collateral (the target) gains value as the price rises, while
// a short's debt (the borrowed target) grows — so the ratio is applied in
// opposite directions. Liquidation is health <= 1.
func (e *Engine) RefreshHealth(ctx context.Context, job *Job) error {
	if job.Plan == nil || job.Plan.TotalBorrowed <= 0 || job.EntryPrice <= 0 {
		return nil // nothing to value yet
	}
	price, err := e.priceOf(ctx, job.TargetUnit)
	if err != nil {
		return err
	}
	ratio := price / job.EntryPrice

	// Health at entry: collateral value * liqThreshold / debt, both ADA-valued
	// from the plan. Then scale by the price move.
	liqT := job.LiqThreshold
	if liqT <= 0 {
		liqT = 0.8
	}
	entryHealth := liqT * job.Plan.TotalExposure / job.Plan.TotalBorrowed

	var health, pnlPct float64
	if job.Direction == Long {
		health = entryHealth * ratio
		// P&L: leveraged exposure to the price move, on the user's capital.
		pnlPct = (ratio - 1) * (job.Plan.TotalExposure / job.InitialAmount) * 100
	} else {
		health = entryHealth / ratio
		pnlPct = (1 - ratio) * (job.Plan.TotalExposure / job.InitialAmount) * 100
	}

	job.CurrentPrice = price
	job.HealthFactor = health
	job.PnLPct = pnlPct
	job.HealthUpdatedAt = time.Now().UTC()
	if err := e.store.Save(ctx, job); err != nil {
		return err
	}
	if health <= liqWarnHealth {
		e.levLog(job, "⚠ health %.2f (price %.6f ADA, %.1f%% to liquidation) — consider reversing", health, price, (health-1)*100)
	}
	return nil
}
