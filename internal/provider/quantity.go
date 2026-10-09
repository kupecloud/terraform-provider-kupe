package provider

import (
	"math/big"
	"regexp"
)

// quantityPattern matches the resource quantity format kupe-api accepts: an
// integer (optionally milli, "500m") or a decimal, followed by an optional
// binary or decimal SI suffix.
var quantityPattern = regexp.MustCompile(`^(\d+|\d*\.\d+)(m?)(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$`)

var quantityMultipliers = map[string]*big.Rat{
	"":   big.NewRat(1, 1),
	"Ki": new(big.Rat).SetInt64(1 << 10),
	"Mi": new(big.Rat).SetInt64(1 << 20),
	"Gi": new(big.Rat).SetInt64(1 << 30),
	"Ti": new(big.Rat).SetInt64(1 << 40),
	"Pi": new(big.Rat).SetInt64(1 << 50),
	"Ei": new(big.Rat).SetInt64(1 << 60),
	"k":  new(big.Rat).SetInt64(1e3),
	"M":  new(big.Rat).SetInt64(1e6),
	"G":  new(big.Rat).SetInt64(1e9),
	"T":  new(big.Rat).SetInt64(1e12),
	"P":  new(big.Rat).SetInt64(1e15),
	"E":  new(big.Rat).SetInt64(1e18),
}

// parseQuantity returns the exact value of a Kubernetes-style quantity such
// as "10Gi", "1.5G" or "500m". ok is false for anything outside the format
// kupe-api accepts; callers then leave validation to the API.
func parseQuantity(s string) (value *big.Rat, ok bool) {
	m := quantityPattern.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	number, isRat := new(big.Rat).SetString(m[1])
	if !isRat {
		return nil, false
	}
	if m[2] == "m" {
		number.Quo(number, big.NewRat(1000, 1))
	}
	return number.Mul(number, quantityMultipliers[m[3]]), true
}
