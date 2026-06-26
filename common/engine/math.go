package engine

import (
	"errors"
	"math"
)

// MaxLeverage is the hard ceiling the engine will build to. Looping borrows
// compounds risk quickly; 1.8x is the agreed cap for this build.
const MaxLeverage = 1.8

// safetyBuffer keeps each borrow comfortably under the market's MAX LTV — near
// the protocol's recommended-borrow level — so (a) the borrow order is within
// the lender's fillable range and (b) a small price move between legs doesn't
// immediately threaten liquidation. e.g. a 0.5 max-LTV market is borrowed
// against at ~0.375.
const safetyBuffer = 0.75

// Direction is the side of the leveraged trade.
type Direction string

const (
	Long  Direction = "long"  // amplified upside to the target token
	Short Direction = "short" // amplified downside to the target token
)

// Leg is one iteration of the loop: trade the input asset into the held
// asset, supply it as collateral, and borrow against it. Amounts are in whole
// units of their respective assets. The borrowed proceeds feed the next leg's
// trade.
type Leg struct {
	Index       int     `json:"index" bson:"index"`
	TradeInAda  float64 `json:"tradeInAda" bson:"tradeInAda"`   // base asset put into this leg's trade
	SupplyAmt   float64 `json:"supplyAmt" bson:"supplyAmt"`     // collateral supplied (in collateral units, est.)
	BorrowAmt   float64 `json:"borrowAmt" bson:"borrowAmt"`     // borrowed against the supply (base-asset units)
	CumExposure float64 `json:"cumExposure" bson:"cumExposure"` // running exposure as a multiple of initial
}

// Plan is the full sizing for a leverage position: the ordered legs plus the
// totals the quote surfaces to the user.
type Plan struct {
	Direction        Direction `json:"direction" bson:"direction"`
	InitialAmount    float64   `json:"initialAmount" bson:"initialAmount"`       // user capital, base-asset units
	TargetLeverage   float64   `json:"targetLeverage" bson:"targetLeverage"`     // requested, clamped to [1, MaxLeverage]
	AchievedLeverage float64   `json:"achievedLeverage" bson:"achievedLeverage"` // what the legs actually reach
	LegLTV           float64   `json:"legLtv" bson:"legLtv"`                     // effective per-leg borrow LTV used
	Legs             []Leg     `json:"legs" bson:"legs"`
	TotalExposure    float64   `json:"totalExposure" bson:"totalExposure"` // total base-asset exposure built
	TotalBorrowed    float64   `json:"totalBorrowed" bson:"totalBorrowed"` // sum of borrows (the debt)
}

// BuildPlan sizes the loop. Starting from the user's initial capital, each leg
// supplies the currently-held amount as collateral and borrows
// effectiveLTV * supplied against it; the borrow is traded into more of the
// held asset, which becomes the next leg's collateral. Cumulative exposure
// follows the geometric series 1 + L + L^2 + ... and converges to 1/(1-L).
//
// minBorrow is the protocol's minimum executable borrow (market units, 0 = no
// minimum). A leg below it can't be submitted, and since the legs shrink
// geometrically every later leg is smaller too — so we stop there and accept
// the lower achievable leverage rather than emitting an un-executable dust leg.
// The caller decides what to do when zero legs are executable (position too
// small). To avoid a tiny tapered final leg dipping under the minimum, the last
// leg is only tapered to the target when the tapered amount still clears the
// minimum; otherwise we take the full-LTV borrow (slightly under target).
//
// marketLTV is the max borrow LTV of the market(s) being used (0..1). target
// is clamped to [1, MaxLeverage].
func BuildPlan(dir Direction, initial, marketLTV, target, minBorrow float64) (*Plan, error) {
	if initial <= 0 {
		return nil, errors.New("plan: initial amount must be > 0")
	}
	if dir != Long && dir != Short {
		return nil, errors.New("plan: direction must be long or short")
	}
	if marketLTV <= 0 || marketLTV >= 1 {
		return nil, errors.New("plan: market LTV must be in (0,1)")
	}
	if target < 1 {
		target = 1
	}
	if target > MaxLeverage {
		target = MaxLeverage
	}

	ltv := marketLTV * safetyBuffer
	// Absolute ceiling reachable by infinite looping at this LTV. If the user
	// asked for more than the LTV can deliver, cap the target there.
	ceiling := 1 / (1 - ltv)
	if target > ceiling {
		target = ceiling
	}

	plan := &Plan{
		Direction:      dir,
		InitialAmount:  initial,
		TargetLeverage: target,
		LegLTV:         ltv,
	}

	cumExposure := 1.0
	held := initial // currently-held amount available as collateral for the next leg
	const maxLegs = 8

	for i := 0; i < maxLegs && cumExposure < target-1e-9; i++ {
		remaining := target - cumExposure // exposure still to add, as multiple of initial
		fullBorrow := held * ltv          // most we could borrow against current holding

		borrow := fullBorrow
		if borrow > remaining*initial {
			// We'd overshoot the target — taper to land on it, but only if the
			// tapered borrow still clears the protocol minimum. If tapering
			// would drop us under the minimum, stop instead of emitting a dust
			// leg (accepts a hair under the target).
			tapered := remaining * initial
			if minBorrow > 0 && tapered < minBorrow {
				break
			}
			borrow = tapered
		}
		if borrow <= 0 {
			break
		}
		// Can't execute a borrow below the protocol minimum; every later leg is
		// smaller, so stop here and accept the lower achieved leverage.
		if minBorrow > 0 && borrow < minBorrow {
			break
		}
		cumExposure += borrow / initial
		plan.Legs = append(plan.Legs, Leg{
			Index:       i,
			TradeInAda:  borrow, // borrowed base asset traded into the held asset
			SupplyAmt:   held,
			BorrowAmt:   borrow,
			CumExposure: cumExposure,
		})
		plan.TotalBorrowed += borrow
		held = borrow // the traded proceeds become next leg's collateral
	}

	plan.AchievedLeverage = cumExposure
	plan.TotalExposure = cumExposure * initial
	return plan, nil
}

// LiquidationPrice returns the price move (relative to entry, as a fraction)
// that would push the position to its liquidation threshold. For a long, this
// is how far the target token can fall; for a short, how far it can rise.
//
// At liquidation: debt = liqThreshold * collateralValue. With exposure E (as
// a multiple of initial) and debt D (also relative to initial), and entry
// price normalized to 1, the collateral value scales with price p:
//
//	D = liqThreshold * E * p  =>  p_liq = D / (liqThreshold * E)
//
// Returns the fractional move from entry (e.g. -0.35 means a 35% adverse move
// triggers liquidation). Sign is negative for long (price down) and positive
// for short (price up).
func LiquidationPrice(dir Direction, exposure, debt, liqThreshold float64) float64 {
	if exposure <= 0 || liqThreshold <= 0 {
		return 0
	}
	pLiq := debt / (liqThreshold * exposure)
	move := pLiq - 1
	if dir == Short {
		return -move // a short is liquidated when price rises
	}
	return move
}

// HealthFactor is collateralValue * liqThreshold / debt. > 1 is safe; <= 1 is
// liquidatable. Both values are in the same (USD or base-asset) unit.
func HealthFactor(collateralValue, debt, liqThreshold float64) float64 {
	if debt <= 0 {
		return math.Inf(1)
	}
	return (collateralValue * liqThreshold) / debt
}
