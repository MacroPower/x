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

// ErrNotRepresentable marks a numeric literal whose exact value neither the
// schema's *float64 nor the field's own width stores without changing it, so
// the number enforced would differ from the number written. It is the single
// exact-representability policy, shared by every dialect: a float denotes its
// shortest decimal (the value encoding/json renders and the validator
// enforces), so a literal that interpretation cannot reproduce -- an integer
// beyond 2^53 that rounds, a decimal with more digits than the width keeps,
// or one whose shortest decimal differs from the exact binary value -- would
// silently loosen (or tighten) the constraint and is rejected rather than
// shipped. Callers wrap this with their own dialect-specific phrasing.
var (
	ErrNotRepresentable = errors.New("not exactly representable as a JSON Schema number")

	// ErrIntegerLiteral marks an integer literal that is not a JSON integer:
	// a bound or scalar on an integer-kind field, or a length or count on
	// any field. Every dialect parses such a literal in base ten with no
	// sign but a minus, no leading zero, and no base prefix, which is the one
	// spelling every reader agrees on: go-playground reads its parameter in
	// base 0, where 010 is eight and 0x10 is sixteen, and refuses a leading
	// plus on an unsigned field while accepting it on a signed one, so a
	// spelling outside the JSON grammar would mean one number here and
	// another there.
	ErrIntegerLiteral = errors.New("not a JSON integer")
)

// maxExactInt is the largest integer magnitude a float64 represents exactly;
// beyond it consecutive integers share a float64 and a bound would round.
const maxExactInt = int64(1) << 53

// ParseNumericBound parses a numeric min/max/gt/lt value under the one
// exact-representability policy, returning the inclusive-by-default endpoint the
// caller then marks exclusive and tiers. It is the single entry point through
// which both the jsonschema tag and the validate tag parse numeric bounds: an
// integer-kind field parses exactly (rejecting a fractional or exponent spelling
// the type cannot hold) and checks the 2^53 magnitude directly; every other kind
// parses as a decimal float and accepts a literal only when it is the shortest
// decimal of the float it rounds to at the kind's width ([CheckFloatLiteral]),
// so a float-kind bound the schema or the field cannot hold as authored is
// rejected rather than silently rounded.
func ParseNumericBound(value string, kind reflect.Kind) (Endpoint, error) {
	switch {
	case numkind.IsUnsigned(kind):
		err := CheckIntegerLiteral(value)
		if err != nil {
			return Endpoint{}, err
		}

		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}

		if n > uint64(maxExactInt) {
			return Endpoint{}, notRepresentable(value)
		}

		return numericEndpoint(float64(n), new(big.Rat).SetUint64(n)), nil

	case numkind.IsInteger(kind):
		err := CheckIntegerLiteral(value)
		if err != nil {
			return Endpoint{}, err
		}

		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid integer %q: %w", value, err)
		}

		if n < -maxExactInt || n > maxExactInt {
			return Endpoint{}, notRepresentable(value)
		}

		return numericEndpoint(float64(n), new(big.Rat).SetInt64(n)), nil

	default:
		return parseFloatBound(value, kind)
	}
}

// CheckIntegerLiteral reports whether value spells a JSON integer: an optional
// minus, then a single zero or a digit run with no leading zero. It is the
// spelling half of every integer parse, the numeric bounds and scalars of an
// integer-kind field and the length and count bounds of every field, so a
// literal reads the same way at every kind and in every dialect. The range
// check is the caller's.
func CheckIntegerLiteral(value string) error {
	digits := strings.TrimPrefix(value, "-")

	switch {
	case digits == "":
		return fmt.Errorf("%q is %w: no digits", value, ErrIntegerLiteral)
	case strings.HasPrefix(value, "+"):
		return fmt.Errorf("%q is %w: a leading plus", value, ErrIntegerLiteral)
	case digits[0] == '0' && len(digits) > 1:
		return fmt.Errorf("%q is %w: a leading zero", value, ErrIntegerLiteral)
	}

	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return fmt.Errorf("%q is %w: %q is not a decimal digit", value, ErrIntegerLiteral, digits[i])
		}
	}

	return nil
}

// ParseDecimalFloat parses a decimal float literal under the one spelling policy
// every dialect shares: it rejects the non-decimal forms strconv also accepts
// (underscore digit separators, hexadecimal floats), so a numeric tag value
// reads the same way regardless of the field's type, and rejects the non-finite
// values encoding/json cannot marshal and no JSON number can equal.
//
// It is the spelling half only. A bound and a field value both then apply
// the width check of [CheckFloatLiteral]. Both start here.
func ParseDecimalFloat(value string) (float64, error) {
	if strings.ContainsAny(value, "_xX") {
		return 0, fmt.Errorf("invalid number %q: not a decimal number", value)
	}

	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", value, err)
	}

	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("%q is not a finite number", value)
	}

	return n, nil
}

// parseFloatBound parses a decimal-float bound: the shared spelling policy,
// then the width check, so the endpoint carries the number the tag wrote.
func parseFloatBound(value string, kind reflect.Kind) (Endpoint, error) {
	n, err := ParseDecimalFloat(value)
	if err != nil {
		return Endpoint{}, err
	}

	err = CheckFloatLiteral(value, kind)
	if err != nil {
		return Endpoint{}, err
	}

	return numericEndpoint(n, numrat.Float64ToRat(n)), nil
}

// CheckFloatLiteral reports whether a decimal literal names a value of the
// kind's width exactly: it parses at that width, 32 bits for a float32 kind
// and 64 otherwise, refusing an overflow, and requires the literal to be the
// shortest decimal of the float it parses to, so a value of the kind marshals
// as the literal and the schema's own *float64 ships it unchanged. On a
// float32 field 0.1 passes and 10.0000001 fails, since every float32 near it
// renders as 10, which is also the number go-playground reads there. On a
// float64 field 2^54 and 1e23 pass, each its float64's shortest decimal, and
// 2^60 fails: it is stored exactly in binary yet renders as
// 1152921504606847000, so shipping it would loosen a bound by 24. The
// spelling is the caller's ([ParseDecimalFloat]); a literal that policy
// admits but [numrat.ParseDecNumber] does not read is passed through.
func CheckFloatLiteral(value string, kind reflect.Kind) error {
	width := 64
	if kind == reflect.Float32 {
		width = 32
	}

	f, err := strconv.ParseFloat(value, width)
	if err != nil {
		return fmt.Errorf("invalid number %q: %w", value, err)
	}

	dn, ok := numrat.ParseDecNumber(value)
	if !ok || !dn.ExactlyComparable() {
		return nil
	}

	// The shortest decimal at the width is what a value of the kind renders
	// as, and for a float32 kind it differs from Float64ToRat(f), which would
	// expand the float32's float64 form (0.1 becomes 0.10000000149011612).
	shortest, _ := numrat.ParseDecNumber(strconv.FormatFloat(f, 'f', -1, width))
	if shortest.Rat().Cmp(dn.Rat()) != 0 {
		return notRepresentable(value)
	}

	return nil
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

// SizeDomain is how a dialect reads a negative size literal, the one place the
// two disagree for a defensible reason on each side.
type SizeDomain uint8

const (
	// SizeFold folds a negative literal into the unsatisfiable range below. A
	// dialect naming a rule rather than a keyword is describing a predicate, and
	// go-playground's max=-1 is a predicate no value satisfies.
	SizeFold SizeDomain = iota
	// SizeStrict rejects a negative literal outright, matching the non-negative
	// value domain of minLength and its siblings: a dialect that names the
	// keyword is writing that keyword's value, and -1 is not one.
	SizeStrict
)

// ParseSizeBound parses a length/count bound into inclusive integer
// contributions at the given tier. It folds an exclusive rule to its inclusive
// integer form (saturating at MaxInt/MinInt so a boundary value does not wrap),
// clamps a floor to non-negative, and expresses an unsatisfiable rule -- a
// sub-zero ceiling from lt=0 or a negative max, or a negative len -- as a floor
// of one against a ceiling of zero rather than a permissive zero ceiling, since
// such a rule rejects even the empty value. Under [SizeStrict] a negative
// literal is rejected before any of that.
func ParseSizeBound(
	value string, rule SizeRule, domain SizeDomain, mode Mode, prov Provenance,
) ([]Bound, error) {
	err := CheckIntegerLiteral(value)
	if err != nil {
		return nil, err
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q: %w", value, err)
	}

	if domain == SizeStrict && n < 0 {
		return nil, fmt.Errorf("must be non-negative, got %d", n)
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
