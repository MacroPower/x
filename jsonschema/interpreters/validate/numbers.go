package validate

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
)

// parseBoundFloat parses a numeric value, rejecting non-finite values
// ("NaN"/"Inf"/"+Inf"/"-Inf"). [strconv.ParseFloat] accepts those, but a
// non-finite value cannot constrain any JSON number: the validator converts each
// bound to a [big.Rat] and skips comparison when that conversion yields no
// rational form, so a non-finite Minimum/Maximum is a silent no-op. Such a value
// is rejected at generation time so it never reaches the schema. It parses the
// scalar eq/ne/oneof values on a float field; the numeric bound keywords parse
// through the shared [constraint.ParseNumericBound] policy in the facade.
func parseBoundFloat(value string) (float64, error) {
	// Reject the non-decimal float forms Go's strconv accepts (underscore digit
	// separators and hexadecimal floats such as 0x1p4) so the validate tag
	// parses decimal numbers only, matching the jsonschema-tag path.
	if strings.ContainsAny(value, "_xX") {
		return 0, fmt.Errorf("%q is not a decimal number", value)
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

// applyNumericMinConstraint applies min/gte or gt to a numeric field by raising
// the minimum (or exclusiveMinimum) floor through the shared constraints facade,
// which parses under the field's kind, applies the single 2^53 policy, and
// intersects the bound against the effective one so a tag bound the type can
// never reach (min=-300 on an int8) does not lower the stronger type floor.
func applyNumericMinConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	rule, name := jsonschema.BoundMin, ruleMin
	if exclusive {
		rule, name = jsonschema.BoundGt, "gt"
	}

	err := field.Constraints().AddNumericBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: %s: %w", name, err)
	}

	return nil
}

// applyNumericMaxConstraint applies max/lte or lt to a numeric field by lowering
// the maximum (or exclusiveMaximum) ceiling through the facade, which intersects
// it so a tag bound the type can never reach (max=200 on an int8) does not raise
// the stronger type ceiling.
func applyNumericMaxConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	rule, name := jsonschema.BoundMax, ruleMax
	if exclusive {
		rule, name = jsonschema.BoundLt, "lt"
	}

	err := field.Constraints().AddNumericBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: %s: %w", name, err)
	}

	return nil
}

// applyNumericOneOf applies oneof=1 2 3 to a numeric field.
func applyNumericOneOf(field jsonschema.FieldContext, value string, baseType reflect.Type) error {
	vals, err := parseNumericValues(value, baseType)
	if err != nil {
		return fmt.Errorf("validate tag: oneof: %w", err)
	}

	return setOneOfEnum(field, vals)
}

// applyNumericEq applies eq=N → const for a numeric field.
func applyNumericEq(field jsonschema.FieldContext, value string, baseType reflect.Type) error {
	parsed, err := parseNumericValue(value, baseType)
	if err != nil {
		return fmt.Errorf("validate tag: eq: %w", err)
	}

	return setNumericConst(field, parsed)
}

// setNumericConst pins the field's const to a numeric value, reporting a
// conflict rather than silently overwriting a const that a previous rule
// already pinned to a different number. A numeric field's eq and len rules
// both pin the value (eq=N and len=N each mean "equals N"), so eq=5,len=10 --
// or eq=3,eq=9 -- can never both hold; rejecting the clash keeps the result
// order-independent and matches applyBoolEq instead of letting whichever rule
// runs last win. The type-derived base is compared too: reconcile overlays the
// canvas const onto it, so a disagreeing const the field's type already
// supplies (a type override, or another hook) would be silently overwritten.
func setNumericConst(field jsonschema.FieldContext, parsed any) error {
	if field.Canvas.Const != nil && !constraint.ValuesEqual(*field.Canvas.Const, parsed) {
		return fmt.Errorf("%w: eq/len=%v conflicts with an existing value constraint",
			ErrConflictingConstraints, parsed)
	}

	if field.Base != nil && field.Base.Const != nil && !constraint.ValuesEqual(*field.Base.Const, parsed) {
		return fmt.Errorf("%w: eq/len=%v conflicts with the type's existing const",
			ErrConflictingConstraints, parsed)
	}

	field.Canvas.Const = &parsed

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

// forbidValue records that the schema must not equal v, composing with any value
// already forbidden through the shared not.const -> not.enum -> allOf escalation
// in the constraint package: several tags can forbid values on one field (for
// example required forbids the zero value while ne forbids another), and the
// numeric-aware dedup treats the same number arriving with different dynamic
// types (the untyped int 0 the required path forbids and the uint64(0) an ne=0
// on an unsigned field forbids) as one value.
func forbidValue(s *jsonschema.Schema, v any) {
	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.Forbid(v)
	vs.WriteForbidden(s)
}

// parseNumericValue parses a single numeric value according to the Go type.
// Unlike a min/max bound, this value (eq/ne/oneof, or len on a numeric field) is
// itself a field value, so it is parsed at the field kind's bit width: a value
// the field can never hold (eq=200 on an int8) overflows here rather than pinning
// the schema to an unsatisfiable const or emitting an inert not. This mirrors the
// jsonschema-tag path, which range-checks const/enum the same way, so both tag
// dialects reject the same out-of-range values. Unsigned kinds are checked first
// because [numkind.IsInteger] also reports true for them.
func parseNumericValue(value string, t reflect.Type) (any, error) {
	switch {
	case numkind.IsUnsigned(t.Kind()):
		n, err := strconv.ParseUint(value, 10, numkind.UintBitSize(t.Kind()))
		if err != nil {
			return nil, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}

		return n, nil

	case numkind.IsInteger(t.Kind()):
		n, err := strconv.ParseInt(value, 10, numkind.IntBitSize(t.Kind()))
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", value, err)
		}

		// Return int64, not a platform int: a value above 2^31-1 would truncate
		// on a 32-bit build. The numeric comparison handles every integer kind.
		return n, nil
	}

	n, err := parseBoundFloat(value)
	if err != nil {
		return nil, err
	}

	// A float32 field cannot hold a value outside its range, so reparse at 32
	// bits purely as an overflow check, mirroring the jsonschema-tag path.
	if t.Kind() == reflect.Float32 {
		_, err := strconv.ParseFloat(value, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, err)
		}
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
	return numkind.IsInteger(t.Kind()) || numkind.IsFloat(t.Kind())
}

// oneOfSplitRegexp matches one oneof token, mirroring go-playground/validator's
// own splitter (`'[^']*'|\S+`): a single-quoted run (one value even with
// spaces) or an unquoted whitespace-delimited run. A quote only opens a group
// when it is the first character of a token; an interior, trailing, or
// unbalanced quote is matched by the \S+ alternative and stripped afterward.
var oneOfSplitRegexp = regexp.MustCompile(`'[^']*'|\S+`)

// splitOneOfValues tokenizes a oneof tag value the way go-playground/validator
// does: whitespace separates values, but a single-quoted run is one value even
// when it contains spaces, and every quote in each token is then stripped. So
// "oneof='New York' Boston" yields ["New York", "Boston"] rather than being
// shattered on every space, and "oneof=ab'cd ef" yields ["abcd", "ef"] -- the
// interior quote does not suppress the separator -- matching the upstream
// tokenize-then-strip exactly.
func splitOneOfValues(value string) []string {
	out := oneOfSplitRegexp.FindAllString(value, -1)
	for i := range out {
		out[i] = strings.ReplaceAll(out[i], "'", "")
	}

	return out
}
