package constraint

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// ErrNotRepresentable marks a numeric bound whose exact value cannot be stored
// as the schema's *float64 without rounding, so the stored bound would differ
// from the tag. It is the single exact-representability policy, shared by every
// dialect: an integer bound beyond 2^53 rounds when converted to float64,
// silently loosening (or tightening) the constraint, so it is rejected rather
// than shipped. Callers wrap this with their own dialect-specific phrasing.
var ErrNotRepresentable = errors.New("not exactly representable as a JSON Schema number")

// maxExactInt is the largest integer magnitude a float64 represents exactly;
// beyond it consecutive integers share a float64 and a bound would round.
const maxExactInt = int64(1) << 53

// ParseNumericBound parses a numeric min/max/gt/lt value under the one
// exact-representability policy, returning the inclusive-by-default endpoint the
// caller then marks exclusive and tiers. It is the single entry point through
// which both the jsonschema tag and the validate tag parse numeric bounds: an
// integer-kind field parses exactly (rejecting a fractional or exponent spelling
// the type cannot hold) and checks the 2^53 magnitude directly; every other kind
// parses as a decimal float and applies the same 2^53 check to an integer-valued
// literal, so a float-kind bound beyond 2^53 is rejected rather than silently
// rounded.
func ParseNumericBound(value string, kind reflect.Kind) (Endpoint, error) {
	switch {
	case numkind.IsUnsigned(kind):
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}

		if n > uint64(maxExactInt) {
			return Endpoint{}, notRepresentable(value)
		}

		return numericEndpoint(float64(n), new(big.Rat).SetUint64(n)), nil

	case numkind.IsInteger(kind):
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid integer %q: %w", value, err)
		}

		if n < -maxExactInt || n > maxExactInt {
			return Endpoint{}, notRepresentable(value)
		}

		return numericEndpoint(float64(n), new(big.Rat).SetInt64(n)), nil

	default:
		return parseFloatBound(value)
	}
}

// parseFloatBound parses a decimal-float bound: it rejects the non-decimal float
// forms strconv accepts (underscore separators, hexadecimal floats), rejects
// non-finite values (which cannot constrain any JSON number), and rejects an
// integer-valued literal beyond 2^53 whose float64 form rounds away from the
// exact value.
func parseFloatBound(value string) (Endpoint, error) {
	if strings.ContainsAny(value, "_xX") {
		return Endpoint{}, fmt.Errorf("%q is not a decimal number", value)
	}

	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return Endpoint{}, fmt.Errorf("invalid number %q: %w", value, err)
	}

	if math.IsNaN(n) || math.IsInf(n, 0) {
		return Endpoint{}, fmt.Errorf("%q is not a finite number", value)
	}

	// Compare the float64's exact binary value against the exact integer, not its
	// shortest decimal: a power of two above 2^53 (e.g. 2^60) is stored exactly
	// and must be accepted, while 2^53+1 rounds and must be rejected. Float64ToRat
	// would use the shortest decimal (2^60 -> ...847000), which differs from the
	// exact integer and would wrongly reject it.
	if dn, ok := numrat.ParseDecNumber(value); ok && dn.IsIntegral() && dn.ExactlyComparable() {
		if new(big.Rat).SetFloat64(n).Cmp(dn.Rat()) != 0 {
			return Endpoint{}, notRepresentable(value)
		}
	}

	return numericEndpoint(n, numrat.Float64ToRat(n)), nil
}

// notRepresentable wraps [ErrNotRepresentable] with the value, so both the
// sentinel and the offending literal reach the caller for its own phrasing.
func notRepresentable(value string) error {
	return fmt.Errorf("bound %s is %w", value, ErrNotRepresentable)
}

// SizeRule is the kind of length/count bound a value expresses, so
// [ParseSizeBound] can perform the exclusive-to-inclusive fold and the
// unsatisfiable-range construction once.
type SizeRule uint8

const (
	// RuleMin is an inclusive floor (min, gte).
	RuleMin SizeRule = iota
	// RuleGt is an exclusive floor (gt): the inclusive floor N+1.
	RuleGt
	// RuleMax is an inclusive ceiling (max, lte).
	RuleMax
	// RuleLt is an exclusive ceiling (lt): the inclusive ceiling N-1.
	RuleLt
	// RuleLen pins both bounds to N (len, and eq on a collection).
	RuleLen
)

// ParseSizeBound parses a length/count bound into inclusive integer
// contributions at the given tier. It folds an exclusive rule to its inclusive
// integer form (saturating at MaxInt/MinInt so a boundary value does not wrap),
// clamps a floor to non-negative, and expresses an unsatisfiable rule -- a
// sub-zero ceiling from lt=0 or a negative max, or a negative len -- as a floor
// of one against a ceiling of zero rather than a permissive zero ceiling, since
// such a rule rejects even the empty value.
func ParseSizeBound(value string, rule SizeRule, mode Mode, prov Provenance) ([]Bound, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q: %w", value, err)
	}

	switch rule {
	case RuleMin:
		return []Bound{floorBound(clampNonNegative(n), mode, prov)}, nil
	case RuleGt:
		return []Bound{floorBound(clampNonNegative(incSaturating(n)), mode, prov)}, nil
	case RuleMax:
		return ceilingBounds(n, mode, prov), nil
	case RuleLt:
		return ceilingBounds(decSaturating(n), mode, prov), nil
	case RuleLen:
		if n < 0 {
			return unsatisfiableBounds(mode, prov), nil
		}

		return []Bound{floorBound(n, mode, prov), ceilingBound(n, mode, prov)}, nil

	default:
		return nil, fmt.Errorf("unknown size rule %d", rule)
	}
}

// ceilingBounds lowers the ceiling, or -- when the inclusive ceiling is negative
// -- builds the unsatisfiable floor-one/ceiling-zero range.
func ceilingBounds(ceiling int, mode Mode, prov Provenance) []Bound {
	if ceiling < 0 {
		return unsatisfiableBounds(mode, prov)
	}

	return []Bound{ceilingBound(ceiling, mode, prov)}
}

// unsatisfiableBounds is the floor-one/ceiling-zero range no length satisfies.
func unsatisfiableBounds(mode Mode, prov Provenance) []Bound {
	return []Bound{floorBound(1, mode, prov), ceilingBound(0, mode, prov)}
}

func floorBound(n int, mode Mode, prov Provenance) Bound {
	return Bound{End: intEndpoint(n), Lower: true, Mode: mode, Provenance: prov}
}

func ceilingBound(n int, mode Mode, prov Provenance) Bound {
	return Bound{End: intEndpoint(n), Lower: false, Mode: mode, Provenance: prov}
}

// incSaturating returns n+1, saturating at MaxInt so gt=MaxInt yields the
// largest representable floor instead of wrapping to a permissive negative.
func incSaturating(n int) int {
	if n == math.MaxInt {
		return n
	}

	return n + 1
}

// decSaturating returns n-1, saturating at MinInt so lt=MinInt does not wrap to
// a large positive ceiling before the sub-zero check.
func decSaturating(n int) int {
	if n == math.MinInt {
		return n
	}

	return n - 1
}

// clampNonNegative floors n at 0, since a length keyword must be non-negative.
func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}

	return n
}
