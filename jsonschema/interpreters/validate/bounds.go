package validate

import (
	"fmt"
	"math"
	"strconv"
)

// parseBoundValue parses the integer value of a length/size validate-tag rule,
// wrapping a malformed value with the shared tag-error phrasing.
func parseBoundValue(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}

	return n, nil
}

// raiseFloor stores n in *field when it tightens the lower bound. Rules in a
// validate tag are ANDed, so overlapping floors intersect to their maximum: a
// weaker (lower) floor never lowers a stronger one already set, regardless of
// tag order.
func raiseFloor(field **int, n int) {
	if *field == nil || n > **field {
		*field = new(n)
	}
}

// lowerCeiling stores n in *field when it tightens the upper bound. Rules in a
// validate tag are ANDed, so overlapping ceilings intersect to their minimum: a
// weaker (higher) ceiling never raises a stronger one already set, regardless of
// tag order.
func lowerCeiling(field **int, n int) {
	if *field == nil || n < **field {
		*field = new(n)
	}
}

// applyMinBound raises the floor at field from a min/gte (inclusive) or gt
// (exclusive) rule. Gt=N is the inclusive bound N+1, clamped non-negative as the
// length keywords require.
func applyMinBound(field **int, value string, exclusive bool) error {
	n, err := parseBoundValue(value)
	if err != nil {
		return err
	}

	raiseFloor(field, clampNonNegative(inclusiveLowerBound(n, exclusive)))

	return nil
}

// applyMaxBound lowers the ceiling at maxField from a max/lte (inclusive) or lt
// (exclusive) rule. Lt=N is the inclusive bound N-1. When that inclusive ceiling
// is negative the rule demands a length below zero, which no string or
// collection can have, so the constraint is unsatisfiable. Clamping the ceiling
// to a non-negative zero would instead accept the empty value the rule forbids
// (go-playground's lt=0 rejects every value, including the empty one), so the
// contradiction is expressed as a floor of one against a ceiling of zero,
// mirroring how an incompatible len yields an unsatisfiable min/max range.
func applyMaxBound(minField, maxField **int, value string, exclusive bool) error {
	n, err := parseBoundValue(value)
	if err != nil {
		return err
	}

	ceiling := inclusiveUpperBound(n, exclusive)
	if ceiling < 0 {
		raiseFloor(minField, 1)
		lowerCeiling(maxField, 0)

		return nil
	}

	lowerCeiling(maxField, ceiling)

	return nil
}

// applyLenBound pins both bounds to len=N: it raises the floor and lowers the
// ceiling to N, each only when it tightens an existing bound, so the result is
// the order-independent intersection with any min/max set elsewhere in the tag.
// An incompatible len yields an unsatisfiable range, just as a conflicting
// min/max pair does. A negative len/eq can never be satisfied -- no string or
// collection has a negative length, and go-playground rejects every value for
// len=-1 -- so it is expressed as the unsatisfiable floor-one/ceiling-zero
// range (as applyMaxBound does for a sub-zero ceiling), rather than clamping to
// 0, which would accept the empty value the rule forbids.
func applyLenBound(minField, maxField **int, value string) error {
	n, err := parseBoundValue(value)
	if err != nil {
		return err
	}

	if n < 0 {
		raiseFloor(minField, 1)
		lowerCeiling(maxField, 0)

		return nil
	}

	raiseFloor(minField, n)
	lowerCeiling(maxField, n)

	return nil
}

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
