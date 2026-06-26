package engine

import (
	"math"
	"testing"
)

func TestBuildPlanCapsAtMaxLeverage(t *testing.T) {
	// Asking for 5x against a high-LTV market must be clamped to MaxLeverage.
	p, err := BuildPlan(Long, 1000, 0.8, 5.0, 0)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if p.TargetLeverage > MaxLeverage+1e-9 {
		t.Fatalf("target leverage %.3f exceeds cap %.3f", p.TargetLeverage, MaxLeverage)
	}
	if p.AchievedLeverage > MaxLeverage+1e-6 {
		t.Fatalf("achieved leverage %.4f exceeds cap %.3f", p.AchievedLeverage, MaxLeverage)
	}
}

func TestBuildPlanReachesTarget(t *testing.T) {
	// 1.5x against a 0.5 LTV market (0.45 after buffer) should be reachable
	// and land very close to the target.
	p, err := BuildPlan(Long, 1000, 0.5, 1.5, 0)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if math.Abs(p.AchievedLeverage-1.5) > 1e-6 {
		t.Fatalf("achieved %.6f, want ~1.5", p.AchievedLeverage)
	}
	if p.TotalExposure != p.AchievedLeverage*1000 {
		t.Fatalf("total exposure %.2f inconsistent with achieved %.4f", p.TotalExposure, p.AchievedLeverage)
	}
	if len(p.Legs) == 0 {
		t.Fatalf("expected at least one leg")
	}
	// Final leg's cumulative exposure must equal the achieved leverage.
	last := p.Legs[len(p.Legs)-1]
	if math.Abs(last.CumExposure-p.AchievedLeverage) > 1e-9 {
		t.Fatalf("last leg cumExposure %.6f != achieved %.6f", last.CumExposure, p.AchievedLeverage)
	}
}

func TestBuildPlanCappedByLTVCeiling(t *testing.T) {
	// 0.4 LTV -> 0.36 after buffer -> ceiling 1/(1-0.36) ≈ 1.5625, below 1.8.
	p, err := BuildPlan(Short, 500, 0.4, 1.8, 0)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	ceiling := 1 / (1 - 0.4*safetyBuffer)
	if p.AchievedLeverage > ceiling+1e-6 {
		t.Fatalf("achieved %.4f exceeds LTV ceiling %.4f", p.AchievedLeverage, ceiling)
	}
}

func TestBuildPlanRejectsBadInput(t *testing.T) {
	if _, err := BuildPlan(Long, 0, 0.5, 1.5, 0); err == nil {
		t.Fatal("expected error for zero initial amount")
	}
	if _, err := BuildPlan("sideways", 100, 0.5, 1.5, 0); err == nil {
		t.Fatal("expected error for invalid direction")
	}
	if _, err := BuildPlan(Long, 100, 1.2, 1.5, 0); err == nil {
		t.Fatal("expected error for out-of-range LTV")
	}
}

func TestFeeReserveFor(t *testing.T) {
	// Floor of 30 ADA even for a single loop.
	if got := feeReserveFor(1); got != minFeeReserveLovelace {
		t.Fatalf("1 loop: got %d, want floor %d", got, minFeeReserveLovelace)
	}
	// 2 loops = base(10) + 2*10 = 30 ADA, still the floor.
	if got := feeReserveFor(2); got != 30_000_000 {
		t.Fatalf("2 loops: got %d, want 30_000_000", got)
	}
	// 3 loops = 10 + 30 = 40 ADA, above the floor.
	if got := feeReserveFor(3); got != 40_000_000 {
		t.Fatalf("3 loops: got %d, want 40_000_000", got)
	}
}

func TestBuildPlanDropsLegsBelowMinimum(t *testing.T) {
	// 250 ADA, 0.5 LTV (0.375 buffered), min borrow 81. First borrow is ~94
	// (>= 81), but the next leg (~35) is below the minimum, so only one leg
	// should execute and no leg may be < 81.
	p, err := BuildPlan(Long, 250, 0.5, 1.8, 81)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Legs) != 1 {
		t.Fatalf("expected exactly 1 executable leg, got %d", len(p.Legs))
	}
	for _, leg := range p.Legs {
		if leg.BorrowAmt < 81 {
			t.Fatalf("leg borrow %.2f is below the minimum 81", leg.BorrowAmt)
		}
	}
	if p.AchievedLeverage >= p.TargetLeverage {
		t.Fatalf("expected achieved (%.3f) below target (%.3f) when minimum caps it", p.AchievedLeverage, p.TargetLeverage)
	}
}

func TestBuildPlan120AdaCapsAtOneLeg(t *testing.T) {
	// The user's real case: 120 ADA, Surf max LTV ~0.44 (0.33 after the 0.75
	// buffer), engine min viable borrow 20 ADA. The first leg borrows ~40 (>=20)
	// but the second would borrow ~13 (<20), so only one leg executes — capping
	// leverage at ~1.33x. 1.7x is NOT reachable here.
	p, err := BuildPlan(Long, 120, 0.44, 1.7, minLegBorrowAda)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Legs) != 1 {
		t.Fatalf("expected exactly 1 viable leg for 120 ADA, got %d", len(p.Legs))
	}
	if p.AchievedLeverage > 1.4 {
		t.Fatalf("expected ~1.33x achievable, got %.3f", p.AchievedLeverage)
	}
	if p.AchievedLeverage >= 1.7 {
		t.Fatalf("1.7x should NOT be reachable for 120 ADA, got %.3f", p.AchievedLeverage)
	}
}

func TestBuildPlanZeroLegsWhenFirstBorrowBelowMinimum(t *testing.T) {
	// 50 ADA, 0.5 LTV -> first borrow ~22.5, below a 81 minimum: no executable
	// legs at all (caller treats this as "position too small").
	p, err := BuildPlan(Long, 50, 0.5, 1.8, 81)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(p.Legs) != 0 {
		t.Fatalf("expected 0 executable legs, got %d", len(p.Legs))
	}
}

func TestLiquidationPriceSign(t *testing.T) {
	// A long is liquidated on a price drop (negative move); a short on a rise.
	longMove := LiquidationPrice(Long, 1.5, 0.5, 0.8)
	if longMove >= 0 {
		t.Fatalf("expected negative liquidation move for long, got %.4f", longMove)
	}
	shortMove := LiquidationPrice(Short, 1.5, 0.5, 0.8)
	if shortMove <= 0 {
		t.Fatalf("expected positive liquidation move for short, got %.4f", shortMove)
	}
}
