package validate

import "math"

// inclusiveLowerBound converts a lower bound to its inclusive form. A gt=N tag
// is an exclusive lower bound equivalent to an inclusive bound of N+1, so the
// value is incremented when exclusive. The increment saturates at [math.MaxInt]
// so gt=MaxInt yields the largest representable bound instead of wrapping
// negative and collapsing to a permissive bound of 0.
func inclusiveLowerBound(n int, exclusive bool) int {
	if exclusive && n != math.MaxInt {
		n++
	}

	return n
}

// inclusiveUpperBound converts an upper bound to its inclusive form. An lt=N tag
// is an exclusive upper bound equivalent to an inclusive bound of N-1, so the
// value is decremented when exclusive. The decrement saturates at [math.MinInt]
// so lt=MinInt does not wrap to a large positive (permissive) bound before the
// non-negative clamp.
func inclusiveUpperBound(n int, exclusive bool) int {
	if exclusive && n != math.MinInt {
		n--
	}

	return n
}

// clampNonNegative floors n at 0. Length keywords (minLength, maxLength,
// minItems, maxItems, minProperties, maxProperties) MUST be non-negative per
// JSON Schema, so a negative bound collapses to 0.
func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}

	return n
}
