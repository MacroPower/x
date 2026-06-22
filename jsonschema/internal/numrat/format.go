package numrat

import (
	"fmt"
	"math"
	"math/big"
)

// TruncateNumber shortens an over-length number literal for use in an error
// message so the message stays bounded regardless of the instance size.
func TruncateNumber(s string) string {
	const keep = 32

	if len(s) <= keep {
		return s
	}

	return fmt.Sprintf("%s... (%d chars)", s[:keep], len(s))
}

// RatString returns a compact string representation of a [big.Rat]. An integer
// renders exactly; a fraction renders through its shortest float64 decimal,
// except when the float64 conversion loses the value: a magnitude above the
// float64 range overflows to a meaningless +Inf, and a tiny magnitude below the
// smallest subnormal underflows to 0. The non-integer guarantee means the value
// is nonzero, so an f of 0 can only be underflow; both cases fall back to the
// exact rational form instead of a misleading "0" or "+Inf". A finite, in-range
// value whose shortest-float decimal does not round-trip to the exact rational
// (more precision than a float64 holds) falls back too, so the rendering is the
// true value rather than a rounded one.
func RatString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}

	f, _ := r.Float64()
	if math.IsInf(f, 0) || f == 0 {
		return r.RatString()
	}

	// The shortest-float decimal is faithful only when it parses back to the
	// exact rational. Ordinary decimals such as 0.1 and 0.3 round-trip and keep
	// their readable form; a value carrying more precision than a float64 holds
	// does not, so fall back to the exact p/q form rather than report a rounded
	// value.
	s := fmt.Sprintf("%v", f)

	back, ok := new(big.Rat).SetString(s)
	if ok && back.Cmp(r) == 0 {
		return s
	}

	return r.RatString()
}
