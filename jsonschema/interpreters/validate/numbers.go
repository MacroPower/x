package validate

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"go.jacobcolvin.com/jsonschema"
)

// parseBoundFloat parses a numeric bound, rejecting non-finite values
// ("NaN"/"Inf"/"+Inf"/"-Inf"). strconv.ParseFloat accepts those, but a
// non-finite bound stored in Minimum/Maximum panics the core validator, so the
// stricter behavior of the jsonschema-tag float parser is mirrored here.
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
		return fmt.Errorf("validate tag: %w", err)
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
		return fmt.Errorf("validate tag: %w", err)
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
		// Promote the existing single forbidden value into an enum set.
		s.Not.Enum = []any{*s.Not.Const, v}
		s.Not.Const = nil

	case s.Not.Enum != nil:
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

// parseNumericValue parses a single numeric value according to the Go type.
// Integer types are parsed with [strconv.ParseInt] to preserve precision for
// values beyond float64's exact integer range.
func parseNumericValue(value string, t reflect.Type) (any, error) {
	if isIntegerKind(t) {
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
		return nil, fmt.Errorf("oneof requires at least one value")
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

// isNumericKind reports whether the type is a numeric kind.
func isNumericKind(t reflect.Type) bool {
	return isIntegerKind(t) || isFloatKind(t)
}

// isIntegerKind reports whether the type is an integer kind.
func isIntegerKind(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

// isFloatKind reports whether the type is a float kind.
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
