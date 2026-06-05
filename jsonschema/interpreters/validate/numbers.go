package validate

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.jacobcolvin.com/jsonschema"
)

// parseBoundFloat parses a numeric bound, rejecting non-finite values
// ("NaN"/"Inf"/"+Inf"/"-Inf"). [strconv.ParseFloat] accepts those, but a
// non-finite bound cannot constrain any JSON number: the validator converts
// each bound to a [big.Rat] and skips comparison when that conversion yields
// no rational form, so a non-finite Minimum/Maximum is a silent no-op. Such a
// bound is rejected at generation time so it never reaches the schema.
func parseBoundFloat(value string) (float64, error) {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", value, err)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("%q is not a finite number", value)
	}

	return n, nil
}

// applyNumericMinConstraint applies min/gte or gt to a numeric schema.
func applyNumericMinConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	n, err := parseBoundFloat(value)
	if err != nil {
		name := "min"
		if exclusive {
			name = "gt"
		}

		return fmt.Errorf("validate tag: %s: %w", name, err)
	}
	if exclusive {
		s.ExclusiveMinimum = jsonschema.Ptr(n)
	} else {
		s.Minimum = jsonschema.Ptr(n)
	}

	return nil
}

// applyNumericMaxConstraint applies max/lte or lt to a numeric schema.
func applyNumericMaxConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	n, err := parseBoundFloat(value)
	if err != nil {
		name := "max"
		if exclusive {
			name = "lt"
		}

		return fmt.Errorf("validate tag: %s: %w", name, err)
	}
	if exclusive {
		s.ExclusiveMaximum = jsonschema.Ptr(n)
	} else {
		s.Maximum = jsonschema.Ptr(n)
	}

	return nil
}

// applyNumericOneOf applies oneof=1 2 3 to a numeric schema.
func applyNumericOneOf(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	vals, err := parseNumericValues(value, baseType)
	if err != nil {
		return fmt.Errorf("validate tag: oneof: %w", err)
	}

	s.Enum = vals

	return nil
}

// applyNumericEq applies eq=N → const for a numeric schema.
func applyNumericEq(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	parsed, err := parseNumericValue(value, baseType)
	if err != nil {
		return fmt.Errorf("validate tag: eq: %w", err)
	}

	s.Const = &parsed

	return nil
}

// applyNumericNe applies ne=N → not for a numeric schema.
func applyNumericNe(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	parsed, err := parseNumericValue(value, baseType)
	if err != nil {
		return fmt.Errorf("validate tag: ne: %w", err)
	}

	forbidValue(s, parsed)

	return nil
}

// forbidValue records that the schema must not equal v. Several tags can forbid
// values on the same field (for example "required" forbids the zero value while
// "ne" forbids another); rather than clobbering a single not.const, the values
// accumulate into not.enum so every constraint composes.
func forbidValue(s *jsonschema.Schema, v any) {
	switch {
	case s.Not == nil:
		s.Not = &jsonschema.Schema{Const: &v}
	case s.Not.Const != nil:
		if numericEqual(*s.Not.Const, v) {
			// Already forbidden as a single value (e.g. required and ne=0 on a
			// numeric field both forbid 0); nothing to add. The comparison is
			// numeric-aware because the same number can arrive with different
			// dynamic types: the required path forbids the untyped int 0 while
			// ne=0 on an unsigned field parses to uint64(0) and on a float field
			// to float64(0). Plain == treats those as distinct and would emit a
			// duplicate.
			return
		}
		// Promote the existing single forbidden value into an enum set.
		s.Not.Enum = []any{*s.Not.Const, v}
		s.Not.Const = nil

	case s.Not.Enum != nil:
		if slices.ContainsFunc(s.Not.Enum, func(e any) bool { return numericEqual(e, v) }) {
			return
		}

		s.Not.Enum = append(s.Not.Enum, v)

	default:
		// Not carries some other shape (e.g. a type or pattern). Composing the
		// forbidden value onto it directly would silently keep those unrelated
		// constraints; instead move the existing not under allOf and add a
		// separate not for the new value so both apply conjunctively.
		s.AllOf = append(s.AllOf,
			&jsonschema.Schema{Not: s.Not},
			&jsonschema.Schema{Not: &jsonschema.Schema{Const: &v}},
		)
		s.Not = nil
	}
}

// numericEqual reports whether a and b represent the same number, regardless of
// their dynamic Go types. Forbidden values reach [forbidValue] with different
// types for the same number (the required path forbids the untyped int 0 while
// ne=0 on an unsigned field forbids uint64(0) and on a float field float64(0)),
// so an int and a uint64 holding 0 compare equal here even though == reports
// them distinct. Non-numeric values (for example bools or strings) fall back to
// ==.
func numericEqual(a, b any) bool {
	ai, aIsInt := asInt64(a)
	bi, bIsInt := asInt64(b)
	if aIsInt && bIsInt {
		return ai == bi
	}

	au, aIsUint := asUint64(a)
	bu, bIsUint := asUint64(b)
	if aIsUint && bIsUint {
		return au == bu
	}
	// A signed and an unsigned value can still match when the signed value is
	// non-negative and equals the unsigned magnitude.
	if aIsInt && bIsUint {
		return ai >= 0 && uint64(ai) == bu
	}
	if aIsUint && bIsInt {
		return bi >= 0 && uint64(bi) == au
	}

	af, aIsFloat := asFloat64(a)
	bf, bIsFloat := asFloat64(b)
	if (aIsInt || aIsUint || aIsFloat) && (bIsInt || bIsUint || bIsFloat) {
		return af == bf
	}

	return a == b
}

// asInt64 returns the value as an int64 when it holds a signed integer kind.
func asInt64(v any) (int64, bool) {
	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	default:
		return 0, false
	}
}

// asUint64 returns the value as a uint64 when it holds an unsigned integer kind.
func asUint64(v any) (uint64, bool) {
	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint(), true
	default:
		return 0, false
	}
}

// asFloat64 returns the value as a float64 when it holds any numeric kind.
func asFloat64(v any) (float64, bool) {
	rv := reflect.ValueOf(v)

	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	default:
		return 0, false
	}
}

// parseNumericValue parses a single numeric value according to the Go type.
// Signed integer kinds parse with [strconv.ParseInt] and unsigned kinds with
// [strconv.ParseUint], so a bound anywhere in the 64-bit range keeps the
// precision a float64 round-trip would lose. Unsigned kinds are checked first
// because [isIntegerKind] also reports true for them.
func parseNumericValue(value string, t reflect.Type) (any, error) {
	switch {
	case isUnsignedKind(t):
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}

		return n, nil

	case isIntegerKind(t):
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", value, err)
		}

		return int(n), nil
	}

	n, err := parseBoundFloat(value)
	if err != nil {
		return nil, err
	}

	return n, nil
}

// parseNumericValues parses space-separated numeric values.
func parseNumericValues(value string, t reflect.Type) ([]any, error) {
	fields := splitOneOfValues(value)
	if len(fields) == 0 {
		return nil, fmt.Errorf("requires at least one value")
	}

	result := make([]any, len(fields))
	for i, f := range fields {
		parsed, err := parseNumericValue(f, t)
		if err != nil {
			return nil, err
		}

		result[i] = parsed
	}

	return result, nil
}

func isNumericKind(t reflect.Type) bool {
	return isIntegerKind(t) || isFloatKind(t)
}

// isIntegerKind reports whether the type is an integer kind. Uintptr counts as
// an integer here so a uintptr field is treated like the other unsigned kinds
// rather than falling through to the float branch.
func isIntegerKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func isUnsignedKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

func isFloatKind(t reflect.Type) bool {
	return t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64
}

// splitOneOfValues tokenizes a oneof tag value the way go-playground/validator
// does: whitespace separates values, but a single-quoted run is one value even
// when it contains spaces, and the surrounding quotes are stripped. So
// "oneof='New York' Boston" yields ["New York", "Boston"] rather than being
// shattered on every space.
func splitOneOfValues(value string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
		started bool
	)

	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()

			started = false
		}
	}

	for _, r := range value {
		switch {
		case r == '\'':
			// A quote toggles grouping and is itself stripped. Entering a quote
			// starts a value even if it ends up empty (oneof='' -> "").
			inQuote = !inQuote
			started = true

		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)

			started = true
		}
	}

	flush()

	return out
}
