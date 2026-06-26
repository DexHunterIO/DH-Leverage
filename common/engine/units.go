package engine

import (
	"math"
	"strings"

	"dh-leverage/common/sources"

	"github.com/Salvionied/apollo/serialization/AssetName"
	"github.com/Salvionied/apollo/serialization/Policy"
)

// A "unit" is the canonical asset id used internally: policyHex+assetNameHex
// concatenated, or the empty string for ADA. This matches how sources.Asset
// concatenates (PolicyID + AssetName) and how Koios reports assets.

// unitOf returns the canonical unit string for a source asset.
func unitOf(a sources.Asset) string {
	if a.PolicyID == "" && a.AssetName == "" {
		return ""
	}
	return a.PolicyID + a.AssetName
}

// splitUnit breaks a unit into its policy-id hex (first 56 chars) and asset
// name hex (remainder). ADA ("") yields two empty strings.
func splitUnit(unit string) (policyHex, nameHex string) {
	if len(unit) < 56 {
		return unit, ""
	}
	return unit[:56], unit[56:]
}

func toPolicy(policyHex string) Policy.PolicyId {
	p, err := Policy.New(policyHex)
	if err != nil || p == nil {
		return Policy.PolicyId{}
	}
	return *p
}

func toAssetName(nameHex string) AssetName.AssetName {
	an := AssetName.NewAssetNameFromHexString(nameHex)
	if an == nil {
		return AssetName.AssetName{}
	}
	return *an
}

// dexToken formats a unit the way DexHunter expects it: the policy id and
// asset-name hex CONCATENATED with no separator (e.g. "279c…d927a3f534e454b"),
// or the empty string for ADA. (DexHunter rejects the dotted "policyId.hexName"
// form with a 400.)
func dexToken(unit string) string {
	return unit
}

// sameUnit compares two units case-insensitively.
func sameUnit(a, b string) bool { return strings.EqualFold(a, b) }

// toWhole / toRaw convert between whole units and smallest units for a given
// decimals count.
func toWhole(raw int64, decimals int) float64 {
	return float64(raw) / math.Pow(10, float64(decimals))
}

func toRaw(whole float64, decimals int) int64 {
	return int64(math.Round(whole * math.Pow(10, float64(decimals))))
}
