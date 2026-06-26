package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	dexhunter "dh-leverage/common/dexhunter-sdk"
	"dh-leverage/common/sources"

	"github.com/Salvionied/apollo/constants"
)

// Tunables for the live loop. Mainnet batches are variable, so the waits are
// generous; the engine persists state after every tx so a timeout never loses
// track of funds (they stay at the temp address, recoverable via Reverse).
const (
	confirmTimeout  = 4 * time.Minute
	batchTimeout    = 12 * time.Minute
	batchPoll       = 8 * time.Second
	defaultSlippage = 3.0 // percent, for the swap legs

	// How long the backend waits for the user's funding tx to confirm before
	// giving up and leaving the job awaiting_funding.
	fundingWaitTimeout  = 10 * time.Minute
	fundingPollInterval = 15 * time.Second

	// Fee reserve sizing. A leverage position is many sequential txs — for each
	// loop: a DEX swap (network fee + ~1-2 ADA batcher fee + ~2 ADA refundable
	// deposit) plus a lend/borrow tx, and on close the matching
	// repay/withdraw/sweep. A typical 1.8x position is ~2 lend/borrows and 2-4
	// swaps, which needs ~30 ADA of fee coverage; we hold enough ADA at the temp
	// address to cover the whole round-trip so it never stalls mid-loop for want
	// of fees. Sized as base + perLoop*loops, floored at the minimum.
	baseFeeReserveLovelace = 10_000_000 //  10 ADA: funding/sweep + buffer
	perLoopFeeLovelace     = 10_000_000 //  10 ADA per loop (swap + lend + unwind legs)
	minFeeReserveLovelace  = 30_000_000 //  30 ADA floor — covers a typical position

	// minLegBorrowAda is the smallest borrow worth doing in a loop: each leg
	// costs a swap + a lend tx (and their unwind counterparts) in batcher/network
	// fees (~5-10 ADA round-trip), so a leg borrowing less than this adds less
	// exposure than it costs. Since legs shrink geometrically, this is what caps
	// how high small capital can lever (e.g. 120 ADA → ~1 leg).
	minLegBorrowAda = 20.0
)

// feeReserveFor returns the ADA (lovelace) the temp address must hold, beyond
// the user's collateral, to pay for every tx across building and unwinding a
// position with the given number of loops.
func feeReserveFor(loops int) int64 {
	r := baseFeeReserveLovelace + int64(loops)*perLoopFeeLovelace
	if r < minFeeReserveLovelace {
		r = minFeeReserveLovelace
	}
	return r
}

// Engine builds and manages leveraged positions. It is safe for concurrent
// use; per-job goroutines coordinate through the store and the cancels map.
type Engine struct {
	sources   []sources.Source
	dex       *dexhunter.Client
	chain     *Chain
	store     JobStore
	encSecret string
	network   constants.Network

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // jobID -> cancel its loop context (reversal signal)
}

// New constructs an Engine. dex may be nil only if no swap legs will run.
func New(srcs []sources.Source, dex *dexhunter.Client, chain *Chain, store JobStore, encSecret string, network constants.Network) *Engine {
	return &Engine{
		sources:   srcs,
		dex:       dex,
		chain:     chain,
		store:     store,
		encSecret: encSecret,
		network:   network,
		cancels:   make(map[string]context.CancelFunc),
	}
}

// SubmitRaw broadcasts an already-signed tx (CBOR hex) to the chain. The
// leverage flow uses it to submit the user-signed funding tx, which isn't
// owned by any single source.
func (e *Engine) SubmitRaw(ctx context.Context, signedHex string) (string, error) {
	return e.chain.SubmitRaw(ctx, signedHex)
}

// --- request / response shapes ---------------------------------------------

// OpenRequest is the user-facing leverage request. CollateralUnit is the base
// asset the user funds with ("" = ADA); TargetUnit is the token being
// longed/shorted. Amount is in whole units of CollateralUnit.
type OpenRequest struct {
	Owner          string    `json:"owner"`          // user's bech32 address (sweep destination)
	Direction      Direction `json:"direction"`      // long|short
	CollateralUnit string    `json:"collateralUnit"` // base asset, "" = ADA
	TargetUnit     string    `json:"targetUnit"`     // token to long/short
	Amount         float64   `json:"amount"`         // whole units of CollateralUnit
	Leverage       float64   `json:"leverage"`       // requested, clamped to <= 1.8
	// Wallet context for the funding tx (Open only) — the user's CIP-30 UTxOs
	// and change address, so coin selection sees the whole wallet rather than
	// a single address. Optional; falls back to BlockFrost's view of Owner.
	Utxos         []string `json:"utxos,omitempty"`
	ChangeAddress string   `json:"changeAddress,omitempty"`
}

// Quote is the non-mutating preview of a position.
type Quote struct {
	Plan           *Plan   `json:"plan"`
	BorrowSource   string  `json:"borrowSource"`
	BorrowMarket   string  `json:"borrowMarket"`
	BorrowAPY      float64 `json:"borrowApy"`
	LegLTV         float64 `json:"legLtv"`
	Loops          int     `json:"loops"`
	TxCount        int     `json:"txCount"`
	LiqMovePct     float64 `json:"liqMovePct"`          // adverse % move to liquidation
	LiqThreshold   float64 `json:"liqThreshold"`        // market liquidation threshold (0..1)
	FeeReserveAda  float64 `json:"feeReserveAda"`       // ADA held for fees, on top of capital
	TotalAdaNeeded float64 `json:"totalAdaNeeded"`      // capital + fee reserve the wallet must hold
	MinSupply      float64 `json:"minSupply,omitempty"` // protocol minimum for this market
	Note           string  `json:"note,omitempty"`
}

// OpenResult is returned from Open; the frontend signs FundingCBOR via CIP-30,
// submits it, then calls Start with the JobID.
type OpenResult struct {
	JobID       string `json:"jobId"`
	TempAddress string `json:"tempAddress"`
	FundingCBOR string `json:"fundingCbor"`
}

// --- quote ------------------------------------------------------------------

// Quote sizes a position and picks the borrow market without touching the
// chain.
func (e *Engine) Quote(ctx context.Context, req OpenRequest) (*Quote, error) {
	colUnit, borrowUnit := e.legUnits(req)
	market, err := e.pickBorrowMarket(ctx, colUnit, borrowUnit)
	if err != nil {
		return nil, err
	}
	ltv := market.LTV
	if ltv <= 0 || ltv >= 1 {
		ltv = 0.5 // conservative default if the market doesn't report one
	}
	// Minimum viable borrow per leg: the protocol's own minimum if it reports
	// one, but never below the engine's economic floor — a leg that borrows less
	// than the batcher/network fees it incurs isn't worth doing, and the legs
	// shrink geometrically, so this caps how far small capital can lever.
	minBorrow := market.MinSupply
	if minBorrow < minLegBorrowAda {
		minBorrow = minLegBorrowAda
	}
	plan, err := BuildPlan(req.Direction, req.Amount, ltv, req.Leverage, minBorrow)
	if err != nil {
		return nil, err
	}
	loops := len(plan.Legs)

	// If even the first leg is below the minimum viable borrow, the position is
	// too small / leverage too low to do anything.
	if loops == 0 && plan.TargetLeverage > 1.0+1e-9 {
		fullFirstBorrow := req.Amount * plan.LegLTV
		if fullFirstBorrow < minBorrow {
			return nil, fmt.Errorf(
				"position too small: even a full first borrow ≈ %.2f ADA is below the %.0f-ADA minimum viable loop — add capital",
				fullFirstBorrow, minBorrow)
		}
		minLev := 1.0 + minBorrow/req.Amount
		return nil, fmt.Errorf(
			"leverage too low: the smallest viable loop borrows %.0f ADA, so the smallest position at %.0f ADA is ≈ %.2fx — raise leverage to at least %.2fx",
			minBorrow, req.Amount, minLev, minLev)
	}

	// Geometric legs shrink each loop, so the minimum-viable-borrow floor (and
	// the market LTV ceiling) commonly cap the reachable leverage below the
	// request — surface that so the user isn't surprised by the achieved value.
	var note string
	if plan.AchievedLeverage < req.Leverage-1e-3 {
		note = fmt.Sprintf("requested %.2fx not reachable for %.0f ADA on %s (LTV ceiling + each loop must borrow ≥ %.0f ADA to beat fees); achievable %.2fx — add capital for more",
			req.Leverage, req.Amount, market.Source, minBorrow, plan.AchievedLeverage)
	}

	liq := LiquidationPrice(req.Direction, plan.AchievedLeverage, plan.TotalBorrowed/req.Amount, market.LiquidationThreshold)
	// tx count: (long has 1 initial trade) + per leg (lend + trade) + final sweep on close.
	txc := loops * 2
	if req.Direction == Long {
		txc++
	}
	feeReserve := toWhole(feeReserveFor(loops), 6)
	return &Quote{
		Plan:           plan,
		BorrowSource:   market.Source,
		BorrowMarket:   market.PoolID,
		BorrowAPY:      market.BorrowAPY,
		LegLTV:         plan.LegLTV,
		Loops:          loops,
		TxCount:        txc,
		LiqMovePct:     liq * 100,
		LiqThreshold:   market.LiquidationThreshold,
		FeeReserveAda:  feeReserve,
		TotalAdaNeeded: req.Amount + feeReserve,
		MinSupply:      market.MinSupply,
		Note:           note,
	}, nil
}

// --- open -------------------------------------------------------------------

// Open creates the temp wallet, persists the job, and builds the funding tx
// the user must sign. It does NOT move funds.
func (e *Engine) Open(ctx context.Context, req OpenRequest) (*OpenResult, error) {
	if e.encSecret == "" {
		return nil, fmt.Errorf("engine: ENGINE_ENC_KEY not configured")
	}
	if req.Owner == "" {
		return nil, fmt.Errorf("engine: owner address required")
	}
	q, err := e.Quote(ctx, req)
	if err != nil {
		return nil, err
	}
	w, err := NewTempWallet(e.network)
	if err != nil {
		return nil, err
	}
	sealed, err := w.Seal(e.encSecret)
	if err != nil {
		return nil, err
	}

	// Funding amount: the user's capital in lovelace (ADA collateral) plus a
	// fee reserve sized to cover every tx across the loop and the eventual
	// unwind, so the temp address can pay for itself end to end.
	feeReserve := feeReserveFor(len(q.Plan.Legs))
	fundingLovelace := toRaw(req.Amount, 6) + feeReserve
	fundingCbor, err := e.chain.BuildFundingTx(req.Owner, req.ChangeAddress, w.Address, fundingLovelace, req.Utxos)
	if err != nil {
		return nil, fmt.Errorf("engine: build funding tx: %w", err)
	}

	// Capture the entry price (ADA per target) so the health monitor can track
	// the position against the current price. Best-effort — a price-probe
	// failure must not block opening.
	entryPrice, perr := e.priceOf(ctx, req.TargetUnit)
	if perr != nil {
		log.Printf("engine: entry price probe failed (%v) — health tracking will start once a price is available", perr)
	}

	now := time.Now().UTC()
	job := &Job{
		ID:                 newID(),
		Owner:              req.Owner,
		Direction:          req.Direction,
		CollateralUnit:     req.CollateralUnit,
		TargetUnit:         req.TargetUnit,
		InitialAmount:      req.Amount,
		Leverage:           q.Plan.TargetLeverage,
		Plan:               q.Plan,
		TempAddress:        w.Address,
		Seal:               sealed,
		FundingLovelace:    fundingLovelace,
		FeeReserveLovelace: feeReserve,
		EntryPrice:         entryPrice,
		LiqThreshold:       q.LiqThreshold,
		Status:             StatusAwaitingFunding,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := e.store.Save(ctx, job); err != nil {
		return nil, err
	}
	// Hand the job to the backend lifecycle immediately: it watches for the
	// funding tx and auto-starts the loop once it lands, so the position
	// progresses even if the frontend never calls Start (e.g. the tab closed).
	e.beginLifecycle(job)
	return &OpenResult{JobID: job.ID, TempAddress: w.Address, FundingCBOR: fundingCbor}, nil
}

// Start begins the position. If the funding tx has already confirmed it kicks
// off the loop immediately; otherwise it waits for the funding in the
// background and starts as soon as the temp address is funded. Either way it
// returns right away, so the loop survives the user closing the browser tab.
func (e *Engine) Start(ctx context.Context, jobID string) error {
	job, err := e.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != StatusAwaitingFunding && job.Status != StatusFailed {
		return fmt.Errorf("engine: job %s not startable (status %s)", jobID, job.Status)
	}
	if isLegacyWallet(job) {
		return errLegacyWallet
	}
	e.beginLifecycle(job)
	return nil
}

// isLegacyWallet reports whether a job's temp wallet was sealed before the
// two-key (payment + stake) change — such wallets can't produce the stake
// witness DexHunter requires, so they can sweep (Reverse) but not trade.
func isLegacyWallet(job *Job) bool {
	return job.Seal != nil && job.Seal.StakeVKeyHex == ""
}

var errLegacyWallet = fmt.Errorf("this position predates the two-key wallet fix and can't run swaps (its stake key wasn't saved) — Reverse it to recover funds, then open a new position")

// beginLifecycle takes ownership of a job: it registers the cancel channel
// (rejecting a duplicate watcher), then either runs the loop immediately if the
// temp address is already funded, or waits for the funding in the background
// and starts as soon as it lands. Open calls this so the position progresses
// even if the frontend never calls Start; Start and recovery call it too.
func (e *Engine) beginLifecycle(job *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if _, exists := e.cancels[job.ID]; exists {
		e.mu.Unlock()
		cancel()
		return // already being watched/run
	}
	e.cancels[job.ID] = cancel
	e.mu.Unlock()

	if e.funded(job) {
		e.levLog(job, "funding confirmed — starting loop")
		job.Status = StatusRunning
		e.save(job)
		go e.run(job, ctx)
		return
	}
	e.levLog(job, "watching for funding — loop will start automatically once the temp address is funded")
	go e.waitFundingThenRun(job, ctx)
}

// RecoverInflight resumes jobs that were mid-flight when the process last
// stopped (goroutines don't survive a restart): awaiting-funding and running
// jobs re-enter the lifecycle (the loop is resume-aware), and reversing jobs
// finish unwinding. Call once at startup.
func (e *Engine) RecoverInflight(ctx context.Context) {
	for _, st := range []string{StatusAwaitingFunding, StatusRunning} {
		jobs, err := e.store.ListByStatus(ctx, st)
		if err != nil {
			log.Printf("engine: recover %s: %v", st, err)
			continue
		}
		for _, job := range jobs {
			if isLegacyWallet(job) {
				e.levLog(job, "skipping recovery — legacy wallet (Reverse to recover funds)")
				continue
			}
			e.levLog(job, "recovering %s position after restart", st)
			e.beginLifecycle(job)
		}
	}
	reversing, err := e.store.ListByStatus(ctx, StatusReversing)
	if err == nil {
		for _, job := range reversing {
			e.levLog(job, "resuming reversal after restart")
			go e.unwind(job)
		}
	}
}

// funded reports whether the temp address holds at least half the expected
// funding (collateral + fee reserve) — enough to confirm the funding tx landed.
func (e *Engine) funded(job *Job) bool {
	bal, err := e.chain.LovelaceAt(job.TempAddress)
	if err != nil {
		log.Printf("engine[%s]: balance check failed: %v", job.ID, err)
		return false
	}
	return bal >= job.FundingLovelace/2
}

// waitFundingThenRun polls for the funding tx to land, then runs the loop. It
// gives up after fundingWaitTimeout, leaving the job awaiting_funding so Start
// can be retried. A reversal during the wait sweeps anything already at the
// temp address back to the user.
func (e *Engine) waitFundingThenRun(job *Job, ctx context.Context) {
	deadline := time.Now().Add(fundingWaitTimeout)
	for {
		if e.cancelled(ctx) {
			e.sweepOnCancel(job)
			return
		}
		if e.funded(job) {
			e.levLog(job, "funding confirmed — starting loop")
			job.Status = StatusRunning
			e.save(job)
			e.run(job, ctx)
			return
		}
		if time.Now().After(deadline) {
			e.levLog(job, "funding not received after %s — still awaiting_funding (retry Start once funded)", fundingWaitTimeout)
			e.mu.Lock()
			delete(e.cancels, job.ID)
			e.mu.Unlock()
			return
		}
		select {
		case <-ctx.Done():
			e.sweepOnCancel(job)
			return
		case <-time.After(fundingPollInterval):
		}
	}
}

// sweepOnCancel handles a reversal requested before the loop began: it sweeps
// any funds already at the temp address back to the user (there may be none if
// the funding never landed) and marks the job reversed.
func (e *Engine) sweepOnCancel(job *Job) {
	ctx := context.Background()
	e.mu.Lock()
	delete(e.cancels, job.ID)
	e.mu.Unlock()
	if w, err := job.Seal.Open(e.encSecret, e.network); err == nil {
		e.sweepAll(ctx, job, w)
	}
	job.Status = StatusReversed
	e.record(ctx, job, Step{Kind: "status", Note: "reversed — cancelled before funding"})
}

// Status returns the current job snapshot (without the sealed key).
func (e *Engine) Status(ctx context.Context, jobID string) (*Job, error) {
	job, err := e.store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	job.Seal = nil
	return job, nil
}

// ListJobs returns an owner's tracked positions (sealed keys stripped).
func (e *Engine) ListJobs(ctx context.Context, owner string) ([]*Job, error) {
	jobs, err := e.store.ListByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		j.Seal = nil
	}
	return jobs, nil
}

// Reverse interrupts a running loop (or unwinds an open position) and sweeps
// the temp address back to the user.
func (e *Engine) Reverse(ctx context.Context, jobID string) error {
	job, err := e.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	// Reversing in progress can't be re-triggered, but an already-reversed job
	// CAN be reversed again — a no-op unwind plus a sweep recovers any funds
	// that trickled back to the temp address after the first sweep.
	if job.Status == StatusReversing {
		return fmt.Errorf("engine: job %s is already reversing", jobID)
	}
	// Mark reversing BEFORE signaling so the run goroutine (if any) doesn't
	// race us to a terminal status.
	job.Status = StatusReversing
	_ = e.store.Save(ctx, job)

	// If a loop is actively running, cancel its context and let IT perform the
	// unwind when its current step aborts — running two unwinds against the same
	// temp UTxOs concurrently would produce conflicting txs. Only when no
	// goroutine owns the job (open / failed) do we unwind here.
	e.mu.Lock()
	cancel, running := e.cancels[jobID]
	if running {
		cancel()
		delete(e.cancels, jobID)
	}
	e.mu.Unlock()

	if !running {
		go e.unwind(job)
	}
	return nil
}

// Sweep is the SMART recovery: it first checks whether the temp address still
// has any open lending positions/orders. If it does, it runs the full unwind
// (sell → repay → sell → sweep); if it doesn't, it just sweeps the loose
// remainder straight back to the user — no needless sell/repay/cancel attempts.
// Refuses while a loop or unwind already owns the job (that would race the sweep
// against in-flight txs).
func (e *Engine) Sweep(ctx context.Context, jobID string) error {
	job, err := e.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == StatusRunning || job.Status == StatusReversing {
		return fmt.Errorf("engine: job %s is %s — wait for it to settle before sweeping", jobID, job.Status)
	}
	e.mu.Lock()
	_, running := e.cancels[jobID]
	e.mu.Unlock()
	if running {
		return fmt.Errorf("engine: job %s is active — cannot sweep now", jobID)
	}
	if job.Seal == nil {
		return fmt.Errorf("engine: job %s has no sealed key to sweep", jobID)
	}
	w, err := job.Seal.Open(e.encSecret, e.network)
	if err != nil {
		return fmt.Errorf("engine: open temp wallet: %w", err)
	}
	go func() {
		bg := context.Background()
		if e.hasOpenPositions(bg, w) {
			// Open positions remain — fall through to the full unwind so the
			// collateral isn't stranded.
			e.record(bg, job, Step{Kind: "status", Note: "sweep: open positions found — unwinding first"})
			job.Status = StatusReversing
			_ = e.store.Save(bg, job)
			e.unwindWith(bg, job, w) // sets StatusReversed when done
			return
		}
		// Nothing open — just sweep the remainder.
		e.record(bg, job, Step{Kind: "status", Note: "sweeping remaining funds (no open positions)"})
		e.sweepAll(bg, job, w)
		job.Status = StatusReversed
		e.record(bg, job, Step{Kind: "status", Note: "swept remaining → " + short(job.Owner)})
	}()
	return nil
}

// hasOpenPositions reports whether the temp address still holds any pending or
// active orders/positions at the lending source. On a query error it returns
// true (conservative: prefer running a no-op unwind over stranding collateral).
func (e *Engine) hasOpenPositions(ctx context.Context, w *TempWallet) bool {
	src := e.sourceByName(engineSource)
	if src == nil {
		return false
	}
	orders, err := src.FetchOrders(ctx, sources.OrderQuery{Address: w.Address, Refresh: true})
	if err != nil {
		log.Printf("[LEVERAGE] sweep: fetch orders failed (%v) — assuming positions may be open", err)
		return true
	}
	for _, o := range orders {
		if o.Status == sources.OrderPending || o.Status == sources.OrderActive {
			return true
		}
	}
	return false
}

// Retry resumes a FAILED position from its current on-chain state, re-driving
// the loop toward the target. Funds are still at the temp address, so a retry
// continues building from where it stopped rather than starting over (the run
// loop skips completed legs and the already-done initial trade). Use this after
// a transient failure (RPC hiccup, batch timeout, the swap-endpoint bug, …).
func (e *Engine) Retry(ctx context.Context, jobID string) error {
	job, err := e.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != StatusFailed {
		return fmt.Errorf("engine: only failed positions can be retried (job %s is %s)", jobID, job.Status)
	}
	if isLegacyWallet(job) {
		return errLegacyWallet
	}
	// Register the cancel func atomically and reject a duplicate run.
	loopCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if _, exists := e.cancels[jobID]; exists {
		e.mu.Unlock()
		cancel()
		return fmt.Errorf("engine: job %s is already running", jobID)
	}
	e.cancels[jobID] = cancel
	e.mu.Unlock()

	job.Status = StatusRunning
	job.Error = ""
	e.record(ctx, job, Step{Kind: "status", Note: "retry requested — resuming"})
	go e.run(job, loopCtx)
	return nil
}

// --- the loop ---------------------------------------------------------------

// run drives the build loop. ctx is the job's cancellable loop context: a
// Reverse cancels it, which aborts the in-flight wait/HTTP call so the loop
// interrupts promptly and unwinds (with a fresh context, since ctx is now
// cancelled). bg is that fresh context used for fail/unwind persistence.
func (e *Engine) run(job *Job, ctx context.Context) {
	bg := context.Background()
	w, err := job.Seal.Open(e.encSecret, e.network)
	if err != nil {
		e.fail(bg, job, fmt.Errorf("open temp key: %w", err))
		return
	}
	colUnit, borrowUnit := e.legUnits(OpenRequest{Direction: job.Direction, CollateralUnit: job.CollateralUnit, TargetUnit: job.TargetUnit})

	// failOrUnwind decides, after an operation error, whether a reversal caused
	// it (ctx cancelled → unwind) or it's a genuine failure (→ fail).
	failOrUnwind := func(err error) {
		if e.cancelled(ctx) {
			e.levLog(job, "reversal requested — unwinding")
			e.unwindWith(bg, job, w)
			return
		}
		e.fail(bg, job, err)
	}

	// Resume support (used by Retry after a failure): the loop replays from the
	// recorded progress. A leg's borrow step means that leg's collateral is
	// already lent, so skip completed legs; a recorded trade means the initial
	// buy already happened, so skip it. Anything still in flight is re-driven
	// from live on-chain balances.
	tradedAlready := e.countStepKind(job, "trade") > 0
	completedLegs := e.countStepKind(job, "borrow")
	if completedLegs > len(job.Plan.Legs) {
		completedLegs = len(job.Plan.Legs)
	}
	resuming := tradedAlready || completedLegs > 0
	if resuming {
		e.record(ctx, job, Step{Kind: "status", Note: fmt.Sprintf("resuming %s position from leg %d/%d", job.Direction, completedLegs+1, len(job.Plan.Legs))})
	} else {
		e.record(ctx, job, Step{Kind: "status", Note: fmt.Sprintf("building %s position, target %.2fx, %d legs", job.Direction, job.Leverage, len(job.Plan.Legs))})
	}

	// LONG establishes base exposure by buying the target with the initial
	// capital first; SHORT lends first (borrow target, sell it).
	if job.Direction == Long && !tradedAlready {
		if e.cancelled(ctx) {
			e.unwindWith(bg, job, w)
			return
		}
		if err := e.tradeAll(ctx, job, w, job.CollateralUnit, job.TargetUnit); err != nil {
			failOrUnwind(fmt.Errorf("initial trade: %w", err))
			return
		}
	} else if resuming && job.Direction == Long {
		// On resume, convert any base asset stranded by a previously-failed
		// post-borrow trade so it isn't left behind.
		if baseRaw, err := e.chain.AssetAt(w.Address, job.CollateralUnit); err == nil && baseRaw-job.FeeReserveLovelace > 2_000_000 {
			if err := e.tradeAll(ctx, job, w, job.CollateralUnit, job.TargetUnit); err != nil {
				failOrUnwind(fmt.Errorf("resume catch-up trade: %w", err))
				return
			}
		}
	}

	for i := completedLegs; i < len(job.Plan.Legs); i++ {
		if e.cancelled(ctx) {
			e.levLog(job, "reversal requested, unwinding at leg %d", i)
			e.unwindWith(bg, job, w)
			return
		}
		e.record(ctx, job, Step{Kind: "leg", Note: fmt.Sprintf("leg %d/%d start", i+1, len(job.Plan.Legs))})
		market, err := e.pickBorrowMarket(ctx, colUnit, borrowUnit)
		if err != nil {
			failOrUnwind(fmt.Errorf("leg %d pick market: %w", i, err))
			return
		}
		if err := e.openLeg(ctx, job, w, market, colUnit, borrowUnit); err != nil {
			failOrUnwind(fmt.Errorf("leg %d: %w", i, err))
			return
		}
		// Trade the borrowed proceeds back into the held asset to extend exposure.
		if job.Direction == Long {
			err = e.tradeAll(ctx, job, w, job.CollateralUnit, job.TargetUnit)
		} else {
			err = e.tradeAll(ctx, job, w, job.TargetUnit, job.CollateralUnit)
		}
		if err != nil {
			failOrUnwind(fmt.Errorf("leg %d trade: %w", i, err))
			return
		}
	}

	// Finalize atomically against Reverse: check for a late cancel and drop
	// our cancel registration under the same lock Reverse takes, so exactly
	// one of {this goroutine unwinds, Reverse spawns the unwind} happens.
	e.mu.Lock()
	cancelled := e.cancelled(ctx)
	if !cancelled {
		delete(e.cancels, job.ID)
	}
	e.mu.Unlock()
	if cancelled {
		e.levLog(job, "reversal requested after last leg, unwinding")
		e.unwindWith(bg, job, w)
		return
	}
	job.Status = StatusOpen
	job.Exposure = job.Plan.TotalExposure
	e.record(bg, job, Step{Kind: "status", Note: fmt.Sprintf("position OPEN — %.2fx exposure", job.Plan.AchievedLeverage)})
}

// openLeg supplies the held collateral and borrows against it. Surf borrows
// with inline collateral (one tx); Liqwid supplies then borrows (two txs).
func (e *Engine) openLeg(ctx context.Context, job *Job, w *TempWallet, market sources.Market, colUnit, borrowUnit string) error {
	builder, ok := e.builderFor(market.Source)
	if !ok {
		return fmt.Errorf("source %s has no tx builder", market.Source)
	}
	colDec := decimalsForUnit(market, colUnit)

	// How much collateral do we currently hold at the temp address?
	heldRaw, err := e.chain.AssetAt(w.Address, colUnit)
	if err != nil {
		return fmt.Errorf("read collateral: %w", err)
	}
	supply := toWhole(heldRaw, colDec)
	if colUnit == "" { // keep a fee reserve when collateral is ADA
		supply = toWhole(heldRaw-job.FeeReserveLovelace, colDec)
	}
	if supply <= 0 {
		return fmt.Errorf("no collateral held to supply")
	}
	// Borrow up to the safety LTV, valued via a DexHunter price probe.
	borrowWhole, err := e.borrowAmount(ctx, market, colUnit, borrowUnit, supply, job.Plan.LegLTV)
	if err != nil {
		return fmt.Errorf("size borrow: %w", err)
	}
	if borrowWhole <= 0 {
		return fmt.Errorf("computed borrow amount <= 0")
	}
	e.levLog(job, "leg sizing on %s/%s: supply %.4f %s, borrow %.4f %s (legLTV %.3f)",
		market.Source, short(market.PoolID), supply, short(colUnit), borrowWhole, short(borrowUnit), job.Plan.LegLTV)

	utxos, _ := e.chain.UtxosCBOR(w.Address)
	common := sources.TxParams{
		Source:         market.Source,
		MarketID:       market.PoolID,
		Address:        w.Address,
		ChangeAddress:  w.Address,
		Wallet:         "engine",
		UTXOs:          utxos,
		CollateralUnit: colUnit, // which collateral asset we're supplying (ADA="")
	}

	if strings.EqualFold(market.Source, "surf") {
		// Surf borrow is batched — the borrowed asset arrives at the temp
		// address only after the Surf batcher settles, so snapshot the balance
		// first and wait for the proceeds before returning.
		beforeBorrow, _ := e.chain.AssetAt(w.Address, borrowUnit)
		p := common
		p.Action = "borrow"
		p.Amount = borrowWhole
		p.CollateralAmount = supply
		tx, err := builder.BuildBorrow(ctx, p)
		if err != nil {
			return fmt.Errorf("surf borrow build: %w", err)
		}
		if _, err := e.signSubmitConfirm(ctx, job, w, tx.CBOR, "borrow", market, borrowWhole); err != nil {
			return err
		}
		e.record(ctx, job, Step{Kind: "batch", Note: fmt.Sprintf("waiting for Surf to deliver borrowed %s", short(borrowUnit))})
		if err := e.waitForBalanceIncrease(ctx, w.Address, borrowUnit, beforeBorrow); err != nil {
			return fmt.Errorf("wait borrow proceeds: %w", err)
		}
		e.record(ctx, job, Step{Kind: "batch", Note: fmt.Sprintf("borrow proceeds received (%s)", short(borrowUnit))})
		return nil
	}

	// Pooled (Liqwid) two-market path: supply then borrow. NOTE: the engine
	// currently selects Surf-only markets (see pickBorrowMarket), so this branch
	// is not exercised; it's left as the basis for wiring Liqwid's two-market
	// collateral model later.
	sp := common
	sp.Action = "supply"
	sp.Amount = supply
	supplyTx, err := builder.BuildSupply(ctx, sp)
	if err != nil {
		return fmt.Errorf("supply build: %w", err)
	}
	if _, err := e.signSubmitConfirm(ctx, job, w, supplyTx.CBOR, "supply", market, supply); err != nil {
		return err
	}

	utxos2, _ := e.chain.UtxosCBOR(w.Address)
	bp := common
	bp.UTXOs = utxos2
	bp.Action = "borrow"
	bp.Amount = borrowWhole
	bp.CollateralAmount = supply
	borrowTx, err := builder.BuildBorrow(ctx, bp)
	if err != nil {
		return fmt.Errorf("borrow build: %w", err)
	}
	if _, err := e.signSubmitConfirm(ctx, job, w, borrowTx.CBOR, "borrow", market, borrowWhole); err != nil {
		return err
	}
	return nil
}

// tradeAll swaps the entire held balance of fromUnit into toUnit and waits for
// the DEX batch to deliver toUnit to the temp address.
func (e *Engine) tradeAll(ctx context.Context, job *Job, w *TempWallet, fromUnit, toUnit string) error {
	if e.dex == nil {
		return fmt.Errorf("dexhunter client not configured")
	}
	fromDec := e.unitDecimals(ctx, fromUnit)
	heldRaw, err := e.chain.AssetAt(w.Address, fromUnit)
	if err != nil {
		return err
	}
	if fromUnit == "" {
		heldRaw -= job.FeeReserveLovelace
	}
	amount := toWhole(heldRaw, fromDec)
	if amount <= 0 {
		return fmt.Errorf("nothing to trade for %s", short(fromUnit))
	}

	beforeRaw, _ := e.chain.AssetAt(w.Address, toUnit)
	utxos, _ := e.chain.UtxosCBOR(w.Address)
	resp, err := e.dex.BuildSwap(ctx, dexhunter.SwapRequest{
		AmountIn:     amount,
		TokenIn:      dexToken(fromUnit),
		TokenOut:     dexToken(toUnit),
		BuyerAddress: w.Address,
		Slippage:     defaultSlippage,
		Inputs:       utxos,
	})
	if err != nil {
		return fmt.Errorf("build swap: %w", err)
	}
	// DexHunter swaps must be assembled by DexHunter: sign the built tx locally
	// (both payment + stake witnesses), hand the witness set to /swap/sign, and
	// submit the properly-assembled tx it returns.
	witnessHex, err := w.WitnessSetHex(resp.CBOR)
	if err != nil {
		return fmt.Errorf("swap sign (local): %w", err)
	}
	signed, err := e.dex.Sign(ctx, dexhunter.SignRequest{TxCBOR: resp.CBOR, Signatures: witnessHex})
	if err != nil {
		return fmt.Errorf("swap /sign: %w", err)
	}
	if signed.CBOR == "" {
		return fmt.Errorf("swap /sign: empty signed cbor")
	}
	hash, err := e.chain.SubmitRaw(ctx, signed.CBOR)
	if err != nil {
		return fmt.Errorf("swap submit: %w", err)
	}
	e.record(ctx, job, Step{Kind: "trade", Source: "dexhunter", TxHash: hash, Amount: amount})
	// Wait for the batcher to settle: the proceeds appear at the temp address.
	e.record(ctx, job, Step{Kind: "batch", Note: fmt.Sprintf("waiting for DEX batch to deliver %s", short(toUnit))})
	if err := e.waitForBalanceIncrease(ctx, w.Address, toUnit, beforeRaw); err != nil {
		return fmt.Errorf("wait batch: %w", err)
	}
	e.record(ctx, job, Step{Kind: "batch", Note: fmt.Sprintf("batch filled — received %s", short(toUnit))})
	return nil
}

// --- unwind / reverse -------------------------------------------------------

// unwind reopens the temp key and unwinds the position. Used by Reverse for
// jobs with no active loop goroutine (open / failed / awaiting).
func (e *Engine) unwind(job *Job) {
	w, err := job.Seal.Open(e.encSecret, e.network)
	if err != nil {
		e.fail(context.Background(), job, fmt.Errorf("unwind open key: %w", err))
		return
	}
	e.unwindWith(context.Background(), job, w)
}

// unwindWith reverses a position in the proper order:
//
//  1. REPAY settled positions via repayWithCollateral (BOTH directions) / CANCEL
//     stuck unfilled borrows. repayWithCollateral repays the debt straight from
//     the locked collateral, so nothing is needed in the wallet — no pre-sell —
//     and it's submitted THROUGH Surf (submitTrackedRepay → assemble +
//     submitTrackedOrder) so the order registers and FILLS instead of orphaning
//     and locking ADA. (A LONG owes ADA, repaid from its target collateral; a
//     SHORT owes the target, repaid from its ADA collateral.)
//  2. WAIT for the released collateral to come back (Surf settles via batcher).
//  3. SELL any returned collateral / residual target back to ADA.
//  4. RETURN: sweep everything to the user.
//
// It queries the source's live state (not recorded steps) so a partially-built
// position unwinds correctly, and always runs on a fresh context so a reversal
// that cancelled the loop can't abort the unwind itself.
func (e *Engine) unwindWith(_ context.Context, job *Job, w *TempWallet) {
	ctx := context.Background()
	e.record(ctx, job, Step{Kind: "status", Note: "unwinding — repay (from collateral) → sell → sweep"})

	colUnit, _ := e.legUnits(OpenRequest{Direction: job.Direction, CollateralUnit: job.CollateralUnit, TargetUnit: job.TargetUnit})

	// 1. Repay (from collateral) settled positions / cancel pending orders. This
	//    releases the collateral back to the temp address. No pre-sell: the debt
	//    is covered by the locked collateral itself, for both long and short.
	beforeCol, _ := e.chain.AssetAt(w.Address, colUnit)
	closedAny := e.closeOpenOrders(ctx, job, w)

	// 2. Wait for the released collateral to return via the batcher.
	if closedAny {
		e.record(ctx, job, Step{Kind: "batch", Note: fmt.Sprintf("waiting for released collateral (%s)", short(colUnit))})
		if err := e.waitForBalanceIncrease(ctx, w.Address, colUnit, beforeCol); err != nil {
			e.levLog(job, "unwind: collateral-return wait: %v — sweeping what's available", err)
		}
	}

	// 3. Sell all held target (returned collateral + any residual) back to ADA.
	_ = e.tradeAll(ctx, job, w, job.TargetUnit, job.CollateralUnit)

	// 4. Sweep everything back to the user (waits for settlement, single tx).
	e.sweepAll(ctx, job, w)
	job.Status = StatusReversed
	e.record(ctx, job, Step{Kind: "status", Note: "REVERSED — swept back to " + short(job.Owner)})
}

const (
	maxCloseRounds = 6 // enough to cancel stale orders, wait for them to revert, then repay
	closeRoundWait = 30 * time.Second

	// repayWithCollateralAutoSubmit gates whether the unwind submits NEW
	// repayWithCollateral orders. The order must be submitted THROUGH Surf
	// (submitTrackedRepay → /api/wallet/assemble + /api/wallet/submitTrackedOrder)
	// so its processor registers and fills it; a raw chain broadcast orphans it.
	// CONFIRMED working on mainnet — tx 02039c02 filled and closed a short,
	// returning the collateral — so submission is ON. Orphan RECOVERY
	// (cancelOrphanRepayOrders, via /api/wallet/submit so the order key is
	// co-signed) runs on every reversal regardless.
	repayWithCollateralAutoSubmit = true
)

// closeOpenOrders closes everything the temp address has open at the lending
// source, LOOPING until nothing new is left to submit:
//
//   - ACTIVE position → repay. A SHORT repays straight from its ADA collateral
//     (repayWithCollateral) since its debt token can't be reacquired once the
//     wallet's drained; a LONG repays plainly with the ADA pre-sold in unwindWith.
//   - PENDING unfilled BORROW → cancel (undo a half-built leg). A pending repay /
//     withdraw is a close already in flight — left to settle, never cancelled
//     (that would undo our own repay).
//
// Each position is closed at most once (tracked by market) so a repay still
// settling in the batcher isn't submitted twice. Returns whether anything was
// closed; failures are logged and skipped.
func (e *Engine) closeOpenOrders(ctx context.Context, job *Job, w *TempWallet) bool {
	src := e.sourceByName(engineSource)
	builder, ok := e.builderFor(engineSource)
	if src == nil || !ok {
		return false
	}
	// Both directions repay straight from the locked collateral (no borrow asset
	// needed in the wallet) and submit THROUGH Surf via submitTrackedRepay, so the
	// order registers and fills instead of orphaning. (LONG owes ADA, repaid from
	// its target collateral; SHORT owes the target, repaid from its ADA collateral.)
	activeKind := "repayWithCollateral"

	closedMarkets := map[string]bool{}         // positions we've already submitted a repay for
	cancelledOrders := map[string]bool{}       // pending orders we've already cancelled
	cancelledRepayMarkets := map[string]bool{} // markets whose stale repay we cancelled, awaiting revert-to-active
	closedAny := false
	deferredRepay := false // logged once when a repayWithCollateral is gated off
	for round := 0; round < maxCloseRounds; round++ {
		orders, err := src.FetchOrders(ctx, sources.OrderQuery{Address: w.Address, Refresh: true})
		if err != nil {
			e.levLog(job, "unwind: fetch %s orders failed: %v — continuing", src.Name(), err)
			break
		}

		openLeft := 0 // orders/positions still open at the source
		didSubmit := false
		for _, o := range orders {
			oKey := o.TxHash + ":" + fmt.Sprint(o.OutputIndex)
			var kind string
			switch o.Status {
			case sources.OrderPending:
				openLeft++
				if cancelledOrders[oKey] {
					continue // already cancelled, settling
				}
				// Leave a fresh repay WE submitted this unwind to fill. Cancel
				// anything else pending: an unfilled borrow, or a stale/stuck
				// repay order (e.g. one built before the slippage fix) that the
				// batcher will never fill and that locks the position so we can't
				// resubmit a corrected one.
				if o.Type != sources.TypeBorrow && closedMarkets[o.MarketID] {
					continue
				}
				kind = "cancel"
			case sources.OrderActive:
				openLeft++
				if closedMarkets[o.MarketID] {
					continue // repay already submitted; it's settling
				}
				if activeKind == "repayWithCollateral" && !repayWithCollateralAutoSubmit {
					// Submitting would orphan the order (never registered with
					// Surf's processor), locking more ADA. Skip until registration
					// is wired — the position stays open, orphans are recovered
					// separately by cancelOrphanRepayOrders.
					if !deferredRepay {
						e.record(ctx, job, Step{Kind: "status", Note: "repayWithCollateral deferred (Surf order-registration not yet wired) — cancelling orphan orders instead"})
						deferredRepay = true
					}
					continue
				}
				kind = activeKind
			default:
				continue
			}
			utxos, _ := e.chain.UtxosCBOR(w.Address)
			closeTx, err := builder.BuildClose(ctx, sources.TxCloseParams{
				Source:           src.Name(),
				MarketID:         o.MarketID,
				Address:          w.Address,
				ChangeAddress:    w.Address,
				Wallet:           "engine",
				TxHash:           o.TxHash,
				OutputIndex:      o.OutputIndex,
				Kind:             kind,
				Pkh:              w.Pkh,
				UTXOs:            utxos,
				RedeemCollateral: true,
			})
			if err != nil {
				e.levLog(job, "unwind %s build failed (%v) — continuing", kind, err)
				continue
			}
			var submitErr error
			switch kind {
			case "repayWithCollateral":
				// Must go THROUGH Surf (assemble + submitTrackedOrder) so its
				// processor registers and fills the order; a raw chain broadcast
				// would orphan it.
				_, submitErr = e.submitTrackedRepay(ctx, job, w, src, closeTx, o.MarketID)
			case "cancel":
				_, submitErr = e.submitCancel(ctx, job, w, src, closeTx, o.MarketID)
			default:
				_, submitErr = e.signSubmitConfirm(ctx, job, w, closeTx.CBOR, kind, sources.Market{Source: src.Name(), PoolID: o.MarketID}, 0)
			}
			if submitErr != nil {
				e.levLog(job, "unwind %s submit failed (%v) — continuing", kind, submitErr)
				continue
			}
			closedAny = true
			didSubmit = true
			if kind == "cancel" {
				cancelledOrders[oKey] = true
				if o.Type != sources.TypeBorrow {
					// A cancelled repay reverts its position to active — we still
					// owe it a repay once the cancel settles.
					cancelledRepayMarkets[o.MarketID] = true
				}
			} else {
				closedMarkets[o.MarketID] = true
				delete(cancelledRepayMarkets, o.MarketID)
			}
		}

		if openLeft == 0 {
			break // nothing open — done
		}
		// Keep looping while a cancelled repay still owes us a repay once it
		// reverts to active. Otherwise, if we submitted nothing this round,
		// everything left is settling and the collateral-return wait handles it.
		awaitingRevert := false
		for m := range cancelledRepayMarkets {
			if !closedMarkets[m] {
				awaitingRevert = true
				break
			}
		}
		if !didSubmit && !awaitingRevert {
			break
		}
		// Give the batcher time to settle (a cancelled order may revert a
		// position to active that we repay next round).
		if round < maxCloseRounds-1 {
			e.record(ctx, job, Step{Kind: "batch", Note: "waiting for closes to settle, then re-checking"})
			time.Sleep(closeRoundWait)
		}
	}
	return closedAny
}

// NOTE: there is deliberately no "cancel orphan repay orders" pass. A
// repayWithCollateral order that the engine fails to register lands at a Surf
// CONTROLLED address ([Surf order key][owner stake]); only Surf can spend it
// (its `cancelOrder` returns witnesses:null and even /api/wallet/submit won't
// co-sign for us). Such orders are cleaned up by Surf's own
// cleanupRepayWithCollateralOrders worker, which refunds the owner per the order
// datum — we must not try to spend them ourselves. With the tracked-submit flow
// in place, properly-built repay orders now register and fill, so this case
// shouldn't recur.

// submitTrackedRepay finishes a repayWithCollateral through Surf's tracked-order
// flow: it signs the build-time tx with the payment key and hands the witness +
// trackedOrder to the source's SubmitTracked (assemble + submitTrackedOrder), so
// Surf's processor registers and fills the order. Records the resulting tx as a
// repayWithCollateral step. Without this the order would orphan.
func (e *Engine) submitTrackedRepay(ctx context.Context, job *Job, w *TempWallet, src sources.Source, built *sources.BuiltTx, marketID string) (string, error) {
	ts, ok := src.(sources.TrackedSubmitter)
	if !ok {
		return "", fmt.Errorf("source %s does not support tracked submit", src.Name())
	}
	if len(built.TrackedOrder) == 0 {
		return "", fmt.Errorf("repayWithCollateral build returned no trackedOrder")
	}
	witness, err := w.PaymentWitness(built.CBOR)
	if err != nil {
		return "", fmt.Errorf("repayWithCollateral witness: %w", err)
	}
	hash, err := ts.SubmitTracked(ctx, w.Address, built.CBOR, witness, built.TrackedOrder)
	if err != nil {
		return "", err
	}
	e.record(ctx, job, Step{Kind: "repayWithCollateral", Source: src.Name(), MarketID: marketID, TxHash: hash})
	return hash, nil
}

// submitCancel cancels a Surf order. The order key is held SERVER-SIDE by Surf
// (the cancelOrder response's witnesses is null), so the cancel must be submitted
// THROUGH Surf's /api/wallet/submit, which co-signs the order key — broadcasting
// to BlockFrost ourselves fails MissingVKeyWitnesses for that key. We assemble the
// build-time tx with the temp wallet's payment witness (plus any extra witnesses
// Surf did return), then hand it to Surf to co-sign + submit. Confirms before
// returning.
func (e *Engine) submitCancel(ctx context.Context, job *Job, w *TempWallet, src sources.Source, built *sources.BuiltTx, marketID string) (string, error) {
	ts, ok := src.(sources.TrackedSubmitter)
	if !ok {
		return e.signSubmitConfirm(ctx, job, w, built.CBOR, "cancel", sources.Market{Source: src.Name(), PoolID: marketID}, 0)
	}
	tempWit, err := w.PaymentWitness(built.CBOR)
	if err != nil {
		return "", fmt.Errorf("cancel witness: %w", err)
	}
	wits := append([]string{tempWit}, built.Witnesses...)
	signedTx, err := ts.Assemble(ctx, w.Address, built.CBOR, wits, false)
	if err != nil {
		return "", err
	}
	hash, err := ts.Submit(ctx, w.Address, signedTx)
	if err != nil {
		return "", fmt.Errorf("cancel submit: %w", err)
	}
	e.record(ctx, job, Step{Kind: "cancel", Source: src.Name(), MarketID: marketID, TxHash: hash})
	if err := e.chain.WaitConfirmed(ctx, hash, confirmTimeout); err != nil {
		return hash, err
	}
	return hash, nil
}

// sourceByName returns the registered source with the given name.
func (e *Engine) sourceByName(name string) sources.Source {
	for _, s := range e.sources {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func (e *Engine) sweep(ctx context.Context, job *Job, w *TempWallet) error {
	signed, err := e.chain.BuildSweepTx(w, job.Owner)
	if err != nil {
		return err
	}
	hash, err := e.chain.SubmitRaw(ctx, signed)
	if err != nil {
		return err
	}
	e.record(ctx, job, Step{Kind: "sweep", Note: "swept funds back to user", TxHash: hash})
	return nil
}

const (
	settlePollWait = 15 * time.Second
	settleStableN  = 3 // balance unchanged for this many polls = fully settled
	settleMaxWait  = 4 * time.Minute
)

// sweepAll waits for the temp balance to stop changing — all trailing
// settlements (DEX deposit refunds, late-settling batch outputs, BlockFrost
// indexing lag) have landed — and then sweeps ONCE. This recovers everything in
// a single tx (one fee) instead of chasing trickling funds with multiple sweeps.
func (e *Engine) sweepAll(ctx context.Context, job *Job, w *TempWallet) {
	e.waitBalanceStable(ctx, job, w.Address)
	if err := e.sweep(ctx, job, w); err != nil {
		e.levLog(job, "sweep: %v", err)
	}
}

// waitBalanceStable polls until the address' lovelace balance is unchanged for
// settleStableN consecutive polls (or settleMaxWait elapses), i.e. no more funds
// are trickling in.
func (e *Engine) waitBalanceStable(ctx context.Context, job *Job, addr string) {
	var last int64 = -1
	stable := 0
	deadline := time.Now().Add(settleMaxWait)
	for {
		bal, err := e.chain.LovelaceAt(addr)
		if err == nil {
			if bal == last {
				if stable++; stable >= settleStableN {
					return
				}
			} else {
				stable = 0
				last = bal
			}
		}
		if time.Now().After(deadline) {
			e.levLog(job, "sweep: balance not settled after %s — sweeping current %.2f ADA", settleMaxWait, float64(last)/1e6)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settlePollWait):
		}
	}
}

// --- tx helpers -------------------------------------------------------------

// signSubmitConfirm signs locally, submits, waits for confirmation, and records
// the step.
func (e *Engine) signSubmitConfirm(ctx context.Context, job *Job, w *TempWallet, unsignedCbor, kind string, market sources.Market, amount float64) (string, error) {
	hash, err := e.signSubmit(ctx, job, w, unsignedCbor, kind, market, amount)
	if err != nil {
		return "", err
	}
	if err := e.chain.WaitConfirmed(ctx, hash, confirmTimeout); err != nil {
		return hash, err
	}
	return hash, nil
}

func (e *Engine) signSubmit(ctx context.Context, job *Job, w *TempWallet, unsignedCbor, kind string, market sources.Market, amount float64) (string, error) {
	signed, err := w.SignTx(unsignedCbor)
	if err != nil {
		return "", fmt.Errorf("%s sign: %w", kind, err)
	}
	hash, err := e.chain.SubmitRaw(ctx, signed)
	if err != nil {
		return "", fmt.Errorf("%s submit: %w", kind, err)
	}
	e.record(ctx, job, Step{
		Kind: kind, Source: market.Source, MarketID: market.PoolID,
		TxHash: hash, Amount: amount,
	})
	return hash, nil
}

func (e *Engine) waitForBalanceIncrease(ctx context.Context, addr, unit string, before int64) error {
	deadline := time.Now().Add(batchTimeout)
	for {
		cur, err := e.chain.AssetAt(addr, unit)
		if err == nil && cur > before {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s to arrive", short(unit))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(batchPoll):
		}
	}
}

// --- market selection / sizing ----------------------------------------------

// legUnits resolves which asset is collateral and which is borrowed for the
// position's direction. Long target T: supply T, borrow base. Short target T:
// supply base, borrow T.
func (e *Engine) legUnits(req OpenRequest) (colUnit, borrowUnit string) {
	if req.Direction == Long {
		return req.TargetUnit, req.CollateralUnit
	}
	return req.CollateralUnit, req.TargetUnit
}

// engineSource restricts which lending sources the engine loops against.
// Currently Surf only: Surf's isolated pools supply+borrow atomically with the
// right collateral model, whereas Liqwid's two-market collateral flow isn't
// wired into the loop yet. Widen this once Liqwid is supported.
const engineSource = "surf"

// pickBorrowMarket returns the best market to borrow borrowUnit against
// colUnit collateral: lowest borrow APY, with the collateral supported. Only
// engineSource markets are considered.
func (e *Engine) pickBorrowMarket(ctx context.Context, colUnit, borrowUnit string) (sources.Market, error) {
	all := e.fetchAllMarkets(ctx)
	var candidates []sources.Market
	for _, m := range all {
		if !m.Active || m.Source != engineSource {
			continue
		}
		if !sameUnit(unitOf(m.Borrow), borrowUnit) {
			continue
		}
		// Surf is pair-isolated: the collateral asset must match.
		if unitOf(m.Collateral) != "" && !sameUnit(unitOf(m.Collateral), colUnit) {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return sources.Market{}, fmt.Errorf("no %s market to borrow %s against %s (try a different token/direction)", engineSource, short(borrowUnit), short(colUnit))
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].BorrowAPY < candidates[j].BorrowAPY
	})
	return candidates[0], nil
}

func (e *Engine) fetchAllMarkets(ctx context.Context) []sources.Market {
	var (
		mu  sync.Mutex
		all []sources.Market
		wg  sync.WaitGroup
	)
	for _, src := range e.sources {
		wg.Add(1)
		go func(s sources.Source) {
			defer wg.Done()
			ms, err := s.FetchMarkets(ctx)
			if err != nil {
				log.Printf("engine: markets from %s failed: %v", s.Name(), err)
				return
			}
			mu.Lock()
			all = append(all, ms...)
			mu.Unlock()
		}(src)
	}
	wg.Wait()
	return all
}

// borrowAmount sizes a borrow at legLTV of the supplied collateral's value.
//
// It values the collateral in borrow-asset units using the LENDER's own prices
// (market.Collateral/Borrow.PriceUsd) — the same prices the protocol uses to
// compute its max borrow — so the requested borrow lands inside the protocol's
// fillable range. (Using an independent DexHunter quote can diverge from the
// lender's oracle and push the borrow above the protocol's max, so the order
// can't be filled.) Falls back to a DexHunter quote only when the market
// carries no usable prices.
func (e *Engine) borrowAmount(ctx context.Context, market sources.Market, colUnit, borrowUnit string, supply, legLTV float64) (float64, error) {
	if sameUnit(colUnit, borrowUnit) {
		return supply * legLTV, nil
	}
	colPx := market.Collateral.PriceUsd
	borPx := market.Borrow.PriceUsd
	if colPx > 0 && borPx > 0 {
		return supply * (colPx / borPx) * legLTV, nil
	}
	est, err := e.dex.EstimateSwap(ctx, dexhunter.EstimateRequest{
		AmountIn: supply,
		TokenIn:  dexToken(colUnit),
		TokenOut: dexToken(borrowUnit),
		Slippage: defaultSlippage,
	})
	if err != nil {
		return 0, err
	}
	return est.TotalOutput * legLTV, nil
}

// unitDecimals looks up an asset's decimals from any market that references
// it, defaulting to 6 (ADA / common stablecoins) when unknown.
func (e *Engine) unitDecimals(ctx context.Context, unit string) int {
	if unit == "" {
		return 6
	}
	for _, m := range e.fetchAllMarkets(ctx) {
		if sameUnit(unitOf(m.Borrow), unit) {
			return m.Borrow.Decimals
		}
		if sameUnit(unitOf(m.Collateral), unit) {
			return m.Collateral.Decimals
		}
	}
	return 6
}

func decimalsForUnit(m sources.Market, unit string) int {
	if sameUnit(unitOf(m.Borrow), unit) {
		return m.Borrow.Decimals
	}
	if sameUnit(unitOf(m.Collateral), unit) {
		return m.Collateral.Decimals
	}
	if unit == "" {
		return 6
	}
	return 0
}

func (e *Engine) builderFor(name string) (sources.TxBuilder, bool) {
	for _, s := range e.sources {
		if s.Name() == name {
			b, ok := s.(sources.TxBuilder)
			return b, ok
		}
	}
	return nil, false
}

// --- logging & step recording ----------------------------------------------

// levLog emits a prominent, leverage-scoped log line. The [LEVERAGE <id>]
// prefix keeps the engine's progress visible above lower-level source logs.
func (e *Engine) levLog(job *Job, format string, args ...any) {
	log.Printf("[LEVERAGE %s] %s", job.ID, fmt.Sprintf(format, args...))
}

// scanURL returns the Cardanoscan link for a tx hash on the engine's network,
// so every on-chain step can be followed from the logs, the DB, or the UI.
func (e *Engine) scanURL(hash string) string {
	if hash == "" {
		return ""
	}
	host := "cardanoscan.io"
	switch e.network {
	case constants.PREPROD:
		host = "preprod.cardanoscan.io"
	case constants.PREVIEW:
		host = "preview.cardanoscan.io"
	case constants.TESTNET:
		host = "testnet.cardanoscan.io"
	}
	return "https://" + host + "/transaction/" + hash
}

// record appends a step to the job, persists it (so the full timeline lives in
// the DB), fills in the Cardanoscan URL for tx steps, and logs it prominently.
// Every meaningful action — each tx and each milestone — goes through here.
func (e *Engine) record(ctx context.Context, job *Job, st Step) {
	if st.At.IsZero() {
		st.At = time.Now().UTC()
	}
	if st.TxHash != "" && st.URL == "" {
		st.URL = e.scanURL(st.TxHash)
	}
	job.Steps = append(job.Steps, st)
	e.save(job)

	msg := st.Kind
	if st.Source != "" {
		msg += " (" + st.Source + ")"
	}
	if st.Note != "" {
		msg += ": " + st.Note
	}
	if st.URL != "" {
		msg += " → " + st.URL
	} else if st.TxHash != "" {
		msg += " tx " + st.TxHash
	}
	e.levLog(job, "%s", msg)
}

// countStepKind counts recorded steps of a given kind, used by run() to resume
// a retried position from its recorded progress.
func (e *Engine) countStepKind(job *Job, kind string) int {
	n := 0
	for _, s := range job.Steps {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

func (e *Engine) fail(_ context.Context, job *Job, err error) {
	job.Status = StatusFailed
	job.Error = err.Error()
	job.Steps = append(job.Steps, Step{Kind: "status", Note: "FAILED: " + err.Error(), At: time.Now().UTC()})
	e.save(job)
	e.levLog(job, "FAILED: %v (funds recoverable at temp address via Reverse)", err)
	e.mu.Lock()
	delete(e.cancels, job.ID)
	e.mu.Unlock()
}

// cancelled reports whether the loop's context has been cancelled (a Reverse).
func (e *Engine) cancelled(ctx context.Context) bool { return ctx.Err() != nil }

// save persists a job with a background context so a reversal (which cancels the
// loop context) never aborts the write — losing a status/step update would
// leave the job in a wrong state.
func (e *Engine) save(job *Job) { _ = e.store.Save(context.Background(), job) }

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}
