package provider

import (
	"math/big"
	"regexp"
)

// maxQuantityLength is the longest resource quantity the platform accepts.
const maxQuantityLength = 20

// The quantity formats the platform accepts for database resources. CPU is a
// plain or milli count ("2", "500m", "0.5"); memory and storage need a binary
// or decimal unit suffix ("1Gi", "1.5G"). Neither allows a milli prefix on a
// unit, so "1mGi" is rejected rather than read as a millibyte count. The
// patterns match the ManagedDatabase CRD's (\d is ASCII-only in Go).
var (
	cpuQuantityPattern  = regexp.MustCompile(`^(\d+m?|\d*\.\d+)$`)
	sizeQuantityPattern = regexp.MustCompile(`^(\d+(\.\d+)?)(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)$`)
)

var sizeQuantityMultipliers = map[string]*big.Rat{
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

// parseSizeQuantity returns the exact number of bytes in a memory or storage
// quantity such as "10Gi" or "1.5G". ok is false for anything outside
// sizeQuantityPattern; the schema rejects those values before this runs.
func parseSizeQuantity(s string) (value *big.Rat, ok bool) {
	m := sizeQuantityPattern.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	number, isRat := new(big.Rat).SetString(m[1])
	if !isRat {
		return nil, false
	}
	return number.Mul(number, sizeQuantityMultipliers[m[3]]), true
}
