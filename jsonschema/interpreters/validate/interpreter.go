package validate

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"go.jacobcolvin.com/jsonschema"
)

// Interpreter implements [jsonschema.TagInterpreter] for go-playground/validator
// tag syntax. Create one with [NewInterpreter].
type Interpreter struct{}

// NewInterpreter returns a new validate tag interpreter.
func NewInterpreter() *Interpreter {
	return &Interpreter{}
}

// TagKey returns "validate".
func (i *Interpreter) TagKey() string { return "validate" }

// Interpret parses a validate tag and applies constraints to the field schema.
func (i *Interpreter) Interpret(tag string, field jsonschema.FieldContext) error {
	// Strip everything after | (OR operator — unsupported, use first group only).
	if idx := strings.Index(tag, "|"); idx >= 0 {
		tag = tag[:idx]
	}

	parts := strings.Split(tag, ",")

	return applyParts(parts, field.Schema, field.Parent, field.Name, field.Type, false)
}

// applyParts applies a sequence of validator tag parts to a schema.
//
//nolint:unparam // isDive is accepted for consistency with the recursive dive pattern.
func applyParts(
	parts []string,
	s, parent *jsonschema.Schema,
	fieldName string,
	fieldType reflect.Type,
	isDive bool,
) error {
	var inKeys bool
	for idx := range parts {
		part := strings.TrimSpace(parts[idx])
		if part == "" || part == "-" {
			continue
		}

		// A dive inside a keys...endkeys block is a key-side dive (e.g.
		// dive,keys,dive,endkeys for collection-typed map keys), which is not
		// modeled; it must be skipped by the inKeys guard below rather than
		// treated as a value-element dive. Only handle dive outside the block.
		if part == "dive" && !inKeys {
			// Descend into element type. A trailing dive with no subsequent
			// constraints is an error (matches go-playground/validator).
			remaining := parts[idx+1:]
			if !hasConstraint(remaining) {
				return fmt.Errorf("validate tag: dive with no subsequent constraints")
			}

			return applyDive(remaining, s, fieldType)
		}

		key, value, hasValue := strings.Cut(part, "=")
		if hasValue {
			// Tags split blindly on commas, pipes, and equals, then the
			// documented escapes in the param value only are unescaped:
			// "0x2C" -> "," and "0x7C" -> "|". This lets a param carry a literal
			// comma or pipe (e.g. oneof=a0x2Cb yields the enum value "a,b"). The
			// key is never unescaped, matching go-playground/validator cache.go.
			value = unescapeParam(value)
		}

		// Skip cross-field validators.
		if isCrossFieldValidator(key) {
			continue
		}
		// Skip control tags that govern when validation runs rather than
		// expressing a value constraint (e.g. omitempty, structonly). These
		// have no schema representation and must not be treated as unknown
		// validators.
		if isControlTag(key) {
			continue
		}
		// Map key validators: constraints between keys and endkeys apply to
		// the map's keys (not modeled here) and are skipped. A keys without a
		// matching endkeys is malformed; rather than swallowing every later
		// constraint, the keys marker is ignored so the remaining constraints
		// still apply to the value schema.
		if key == "keys" {
			if hasEndkeys(parts[idx+1:]) {
				inKeys = true
			}

			continue
		}
		if key == "endkeys" {
			inKeys = false
			continue
		}
		if inKeys {
			continue
		}

		err := applyValidator(key, value, s, parent, fieldName, fieldType)
		if err != nil {
			return err
		}
	}

	return nil
}

// applyValidator applies a single validator to the schema.
func applyValidator(key, value string, s, parent *jsonschema.Schema, fieldName string, fieldType reflect.Type) error {
	// Follow pointers for type checking.
	baseType := fieldType

	isPointer := false
	for baseType.Kind() == reflect.Pointer {
		isPointer = true
		baseType = baseType.Elem()
	}

	switch key {
	case "required":
		if parent != nil && fieldName != "" {
			addRequired(parent, fieldName)
		}
		if !isPointer {
			applyRequiredConstraint(s, baseType)
		}

		return nil

	case "min", "gte":
		return applyMinConstraint(s, value, baseType, false)
	case "max", "lte":
		return applyMaxConstraint(s, value, baseType, false)
	case "gt":
		return applyMinConstraint(s, value, baseType, true)
	case "lt":
		return applyMaxConstraint(s, value, baseType, true)
	case "len":
		return applyLenConstraint(s, value, baseType)

	case "oneof":
		return applyOneOf(s, value, baseType)
	case "eq":
		return applyEq(s, value, baseType)
	case "ne":
		return applyNe(s, value, baseType)

	case "unique":
		// UniqueItems is only meaningful for array/slice types.
		if isCollectionKind(baseType) {
			s.UniqueItems = true
		}

	default:
		// Try format, pattern, and content tags. These set string-only
		// keywords, so they are gated on a string instance: a recognized keyword
		// is rejected when neither the Go kind nor the generated schema is a
		// string (so it is not stamped on as inert noise), but it is allowed when
		// the schema is a string even if the Go kind is not (e.g. base64 on a
		// []byte field, whose schema is a base64-encoded string). An unrecognized
		// validator is an error rather than being silently consumed.
		if recognized := isStringKeywordTag(key); recognized {
			if !isStringKind(baseType) && !schemaPermitsString(s) {
				return fmt.Errorf("validate tag: %q not supported for type %s", key, baseType.Kind())
			}

			applyStringKeywordTag(key, s)

			return nil
		}

		return fmt.Errorf("validate tag: unrecognized validator %q", key)
	}

	return nil
}

// unescapeParam applies go-playground/validator's documented param escapes:
// "0x2C" becomes a literal comma and "0x7C" a literal pipe. Tags are split on
// commas and pipes before parsing, so these escapes are the only way a param
// value can contain either character. The order matches validator cache.go:
// commas are unescaped before pipes.
func unescapeParam(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "0x2C", ","), "0x7C", "|")
}

// hasConstraint reports whether parts contains at least one meaningful
// (non-empty, non-skip) constraint.
func hasConstraint(parts []string) bool {
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && p != "-" {
			return true
		}
	}

	return false
}

// hasEndkeys reports whether parts contains an "endkeys" marker.
func hasEndkeys(parts []string) bool {
	for _, p := range parts {
		if strings.TrimSpace(p) == "endkeys" {
			return true
		}
	}

	return false
}

// addRequired adds a field to the parent's required list if not already present.
func addRequired(parent *jsonschema.Schema, name string) {
	if slices.Contains(parent.Required, name) {
		return
	}

	parent.Required = append(parent.Required, name)
}

// applyRequiredConstraint adds type-specific "required" constraints, matching
// go-playground/validator semantics where "required" means a non-zero value.
// Validator rules in a single tag compose conjunctively and order-independently,
// so the floors only ever rise: a stronger min/len bound set by another part of
// the tag is never lowered, regardless of where "required" appears.
func applyRequiredConstraint(s *jsonschema.Schema, baseType reflect.Type) {
	switch {
	case baseType.Kind() == reflect.String:
		if s.MinLength == nil || *s.MinLength < 1 {
			s.MinLength = jsonschema.Ptr(1)
		}

	case baseType.Kind() == reflect.Slice, baseType.Kind() == reflect.Array:
		if s.MinItems == nil || *s.MinItems < 1 {
			s.MinItems = jsonschema.Ptr(1)
		}

	case baseType.Kind() == reflect.Map:
		if s.MinProperties == nil || *s.MinProperties < 1 {
			s.MinProperties = jsonschema.Ptr(1)
		}

	case baseType.Kind() == reflect.Bool:
		// Required on bool means the value must be true.
		s.Const = jsonschema.Ptr[any](true)
	case isIntegerKind(baseType):
		// Required on a numeric type means the value must not be zero.
		forbidValue(s, 0)
	case isFloatKind(baseType):
		forbidValue(s, 0.0)
	}
}

// applyMinConstraint applies min/gte or gt constraint based on the type.
func applyMinConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	switch {
	case isStringKind(baseType):
		return applyStringMinConstraint(s, value, exclusive)
	case isNumericKind(baseType):
		return applyNumericMinConstraint(s, value, exclusive)
	case isCollectionKind(baseType):
		return applyCollectionMinConstraint(s, value, baseType, exclusive)
	default:
		return fmt.Errorf("validate tag: min/gt not supported for type %s", baseType.Kind())
	}
}

// applyMaxConstraint applies max/lte or lt constraint based on the type.
func applyMaxConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	switch {
	case isStringKind(baseType):
		return applyStringMaxConstraint(s, value, exclusive)
	case isNumericKind(baseType):
		return applyNumericMaxConstraint(s, value, exclusive)
	case isCollectionKind(baseType):
		return applyCollectionMaxConstraint(s, value, baseType, exclusive)
	default:
		return fmt.Errorf("validate tag: max/lt not supported for type %s", baseType.Kind())
	}
}

// applyLenConstraint applies len=N (sets both min and max) based on the type.
func applyLenConstraint(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	switch {
	case isStringKind(baseType):
		return applyStringLenConstraint(s, value)
	case isCollectionKind(baseType):
		return applyCollectionLenConstraint(s, value, baseType)
	case isNumericKind(baseType):
		// Len=N on a numeric type means the value must equal N.
		parsed, err := parseNumericValue(value, baseType)
		if err != nil {
			return fmt.Errorf("validate tag: len: %w", err)
		}

		s.Const = &parsed

		return nil

	default:
		return fmt.Errorf("validate tag: len not supported for type %s", baseType.Kind())
	}
}

// applyOneOf applies oneof constraint based on the type. A field whose base
// type is neither numeric, bool, nor string (for example a struct, [time.Time],
// slice, or map) cannot carry a string enum without producing an unsatisfiable
// schema, so it is rejected rather than silently mis-stamped.
func applyOneOf(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	switch {
	case isNumericKind(baseType):
		return applyNumericOneOf(s, value, baseType)
	case isBoolKind(baseType):
		return applyBoolOneOf(s, value)
	case isStringKind(baseType):
		return applyStringOneOf(s, value)
	default:
		return fmt.Errorf("validate tag: oneof not supported for type %s", baseType.Kind())
	}
}

// applyEq applies eq constraint based on the type. A non-numeric, non-bool,
// non-collection, non-string base type (for example a struct or [time.Time])
// cannot carry a string const without producing an unsatisfiable schema, so it
// is rejected rather than silently mis-stamped.
func applyEq(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	switch {
	case isNumericKind(baseType):
		return applyNumericEq(s, value, baseType)
	case isBoolKind(baseType):
		return applyBoolEq(s, value)
	case isCollectionKind(baseType):
		// Eq=N on a collection means the length equals N.
		return applyCollectionLenConstraint(s, value, baseType)
	case isStringKind(baseType):
		applyStringEq(s, value)

		return nil

	default:
		return fmt.Errorf("validate tag: eq not supported for type %s", baseType.Kind())
	}
}

// isBoolKind reports whether the type is a bool kind.
func isBoolKind(t reflect.Type) bool { return t.Kind() == reflect.Bool }

// parseBool parses a boolean validator value.
func parseBool(v string) (bool, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("validate tag: invalid boolean %q", v)
	}
}

// applyBoolEq applies eq=true/false → const for a bool schema.
func applyBoolEq(s *jsonschema.Schema, value string) error {
	b, err := parseBool(value)
	if err != nil {
		return err
	}

	s.Const = jsonschema.Ptr[any](b)

	return nil
}

// applyBoolOneOf applies oneof=true false → enum for a bool schema.
func applyBoolOneOf(s *jsonschema.Schema, value string) error {
	vals := splitOneOfValues(value)
	if len(vals) == 0 {
		return fmt.Errorf("validate tag: oneof requires at least one value")
	}

	enum := make([]any, len(vals))
	for i, v := range vals {
		b, err := parseBool(v)
		if err != nil {
			return err
		}

		enum[i] = b
	}

	s.Enum = enum

	return nil
}

// applyNe applies ne constraint based on the type. It mirrors applyEq: ne on a
// bool forbids the boolean value, ne=N on a collection forbids that length, and
// ne on a string forbids the string. A non-numeric, non-bool, non-collection,
// non-string base type is rejected rather than silently mis-stamped.
func applyNe(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	switch {
	case isNumericKind(baseType):
		return applyNumericNe(s, value, baseType)
	case isBoolKind(baseType):
		return applyBoolNe(s, value)
	case isCollectionKind(baseType):
		return applyCollectionNe(s, value, baseType)
	case isStringKind(baseType):
		applyStringNe(s, value)

		return nil

	default:
		return fmt.Errorf("validate tag: ne not supported for type %s", baseType.Kind())
	}
}

// applyBoolNe applies ne=true/false → not for a bool schema, forbidding the
// boolean value rather than a string.
func applyBoolNe(s *jsonschema.Schema, value string) error {
	b, err := parseBool(value)
	if err != nil {
		return err
	}

	forbidValue(s, b)

	return nil
}

// isControlTag reports whether a key is a go-playground/validator control tag
// that governs when validation runs rather than expressing a value constraint.
// These have no JSON Schema representation and are skipped.
func isControlTag(key string) bool {
	switch key {
	case "omitempty", "omitnil", "omitzero", "structonly", "nostructlevel",
		"isdefault":
		return true
	}

	return false
}

// isCrossFieldValidator reports whether a key is a cross-field validator
// that should be silently ignored.
func isCrossFieldValidator(key string) bool {
	switch key {
	case "eqfield", "nefield", "gtfield", "gtefield", "ltfield", "ltefield",
		"eqcsfield", "necsfield", "gtcsfield", "gtecsfield", "ltcsfield", "ltecsfield",
		"required_if", "required_unless", "required_with", "required_with_all",
		"required_without", "required_without_all", "excluded_if", "excluded_unless",
		"excluded_with", "excluded_with_all", "excluded_without", "excluded_without_all",
		"skip_unless", "fieldcontains", "fieldexcludes":
		return true
	}

	return false
}
