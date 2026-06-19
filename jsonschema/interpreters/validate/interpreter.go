package validate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

// ErrConflictingConstraints reports two tag rules on one field that can never
// both hold, such as required and eq=false on a bool: required means the value
// must be true while eq=false pins it to false, so no value satisfies both.
var ErrConflictingConstraints = errors.New("validate tag: conflicting constraints")

// Interpreter implements [jsonschema.TagInterpreter] for go-playground/validator
// tag syntax. Create one with [NewInterpreter] and register it under the
// "validate" tag key:
//
//	jsonschema.WithTagInterpreter("validate", validate.NewInterpreter())
type Interpreter struct{}

// NewInterpreter returns a new validate tag interpreter.
func NewInterpreter() *Interpreter {
	return &Interpreter{}
}

// Interpret parses the validate tag value from [jsonschema.Tag] and applies
// constraints to the field schema. Interpretation is pure tag parsing, so
// the context is unused.
func (i *Interpreter) Interpret(_ context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
	// Split on commas first, exactly as go-playground/validator does; the OR
	// operator is then handled per comma group inside applyParts. Splitting on
	// the first pipe up front would discard every later comma-separated
	// constraint (e.g. "oneof=a|b,required" would drop required).
	parts := strings.Split(tag.Value, ",")

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
		part := parts[idx]
		// The | OR operator is not modeled. The go-playground/validator parser
		// splits a comma group on the pipe and treats the alternatives as OR;
		// here only the first alternative (the group before the first pipe) is
		// interpreted, matching the documented behavior. Stripping per part
		// rather than across the whole tag keeps later comma-separated
		// constraints intact. A literal pipe in a param is written 0x7C and
		// survives, since unescapeParam runs after this split.
		if i := strings.IndexByte(part, '|'); i >= 0 {
			part = part[:i]
		}

		part = strings.TrimSpace(part)
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

	// A json:",string" field serializes its numeric or bool value as a quoted
	// string, so the generator emits a string schema. Value-equality constraints
	// must compare against that serialized form; dispatching on the raw Go kind
	// would stamp a numeric or bool const/enum onto a string schema that no
	// instance can match. Route eq/ne/oneof through the string path. Bound and
	// length constraints keep their kind dispatch.
	if isStringCoercedValue(s, baseType) {
		switch key {
		case "eq", "ne", "oneof":
			baseType = reflect.TypeFor[string]()
		}
	}

	switch key {
	case "required":
		if parent != nil && fieldName != "" {
			addRequired(parent, fieldName)
		}

		if !isPointer {
			return applyRequiredConstraint(s, baseType)
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
		if isByteSliceField(baseType) {
			return errByteSliceLengthConstraint
		}

		// UniqueItems is only meaningful for array/slice types. Maps are
		// excluded: JSON Schema's uniqueItems is array-only, and
		// go-playground/validator's unique-on-map checks unique values, which
		// has no object-schema equivalent. So unique on a map is a no-op.
		if isSequenceKind(baseType) {
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
		if p == "" || p == "-" {
			continue
		}

		// A control tag such as omitempty or structonly, or a cross-field
		// validator, governs when validation runs rather than constraining a
		// value, so it does not satisfy a trailing dive. Match on the key before
		// any equals sign.
		key, _, _ := strings.Cut(p, "=")
		if isControlTag(key) || isCrossFieldValidator(key) {
			continue
		}

		return true
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
func applyRequiredConstraint(s *jsonschema.Schema, baseType reflect.Type) error {
	switch {
	case baseType.Kind() == reflect.String:
		if s.MinLength == nil || *s.MinLength < 1 {
			s.MinLength = new(1)
		}

	case baseType.Kind() == reflect.Slice, baseType.Kind() == reflect.Array:
		if s.MinItems == nil || *s.MinItems < 1 {
			s.MinItems = new(1)
		}

	case baseType.Kind() == reflect.Map:
		if s.MinProperties == nil || *s.MinProperties < 1 {
			s.MinProperties = new(1)
		}

	case baseType.Kind() == reflect.Bool:
		// Required on bool means the value must be true. An eq tag elsewhere on the
		// field may already have pinned the const: eq=true agrees and needs no
		// change, but eq=false pins it to false, which required can never satisfy.
		// Overwriting it would silently discard the eq=false rule, so the impossible
		// combination is reported rather than resolved by precedence.
		if s.Const != nil {
			if b, ok := (*s.Const).(bool); ok && !b {
				return fmt.Errorf("%w: required on a bool already constrained to false", ErrConflictingConstraints)
			}
		}

		s.Const = new(any(true))

	case isIntegerKind(baseType):
		// Required on a numeric type means the value must not be zero.
		forbidValue(s, 0)
	case isFloatKind(baseType):
		forbidValue(s, 0.0)
	}

	return nil
}

// applyMinConstraint applies min/gte or gt constraint based on the type.
func applyMinConstraint(s *jsonschema.Schema, value string, baseType reflect.Type, exclusive bool) error {
	switch {
	case isStringKind(baseType):
		return applyStringMinConstraint(s, value, exclusive)
	case isNumericKind(baseType):
		return applyNumericMinConstraint(s, value, baseType, exclusive)
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
		return applyNumericMaxConstraint(s, value, baseType, exclusive)
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

		return setNumericConst(s, parsed)

	default:
		return fmt.Errorf("validate tag: len not supported for type %s", baseType.Kind())
	}
}

// applyOneOf applies oneof constraint based on the type. On a slice or array
// the constraint applies to each element (mirroring the jsonschema tag's enum
// behavior for sequence fields), so it lands on the item schemas parsed
// against the element type. A field whose base type is none of these (for
// example a struct, [time.Time], or map) cannot carry a string enum without
// producing an unsatisfiable schema, so it is rejected rather than silently
// mis-stamped.
func applyOneOf(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	switch {
	case isNumericKind(baseType):
		return applyNumericOneOf(s, value, baseType)
	case isBoolKind(baseType):
		return applyBoolOneOf(s, value)
	case isStringKind(baseType):
		return applyStringOneOf(s, value)
	case isSequenceKind(baseType):
		return applySequenceOneOf(s, value, baseType)
	default:
		return fmt.Errorf("validate tag: oneof not supported for type %s", baseType.Kind())
	}
}

// applySequenceOneOf applies oneof on a slice or array field to its item
// schemas, parsing the values against the element type. The element schemas
// mirror diveIntoSequence's shapes: Items for slices, prefixItems
// (Draft 2020-12) or the items-as-array form (Draft-07) for fixed arrays. A
// []byte field encodes as a single base64 string with no element schema, so
// oneof on it is rejected rather than silently dropped.
func applySequenceOneOf(s *jsonschema.Schema, value string, baseType reflect.Type) error {
	items := schemashape.ItemSchemas(s)
	if len(items) == 0 {
		return fmt.Errorf("validate tag: oneof: %s field has no item schema to constrain", baseType.Kind())
	}

	elem := baseType.Elem()
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}

	for _, item := range items {
		err := applyOneOf(item, value, elem)
		if err != nil {
			return err
		}

		relocateNullableValueConstraint(item)
	}

	return nil
}

// relocateNullableValueConstraint moves a Const or Enum stamped on a nullable
// wrapper schema (anyOf[value, null], the shape a pointer element generates)
// onto its value branch. A type-agnostic const/enum left as a sibling of anyOf
// is evaluated against the instance directly and so rejects a valid null
// element; on the value branch it constrains the value alone. It is a no-op for
// a schema that is not a nullable wrapper or carries neither keyword.
func relocateNullableValueConstraint(s *jsonschema.Schema) {
	inner := schemashape.NullableInnerSchema(s)
	if inner == nil {
		return
	}

	if s.Const != nil {
		inner.Const, s.Const = s.Const, nil
	}

	if s.Enum != nil {
		inner.Enum, s.Enum = s.Enum, nil
	}
}

// isStringCoercedValue reports whether the generated schema is a string while
// the Go kind is numeric or bool, the shape a json:",string" field (or a
// string-marshaling type) produces. A value-equality constraint then compares
// against the serialized string, not the underlying numeric or bool value.
func isStringCoercedValue(s *jsonschema.Schema, baseType reflect.Type) bool {
	return schemaPermitsString(s) && (isNumericKind(baseType) || isBoolKind(baseType))
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
		return applyStringEq(s, value)

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

// applyBoolEq applies eq=true/false → const for a bool schema. A const already
// pinned to the opposite value by another rule (for example required, which pins
// it to true) is a conflict the two rules can never both satisfy, so it is
// reported rather than silently overwritten. This keeps the result independent
// of tag order.
func applyBoolEq(s *jsonschema.Schema, value string) error {
	b, err := parseBool(value)
	if err != nil {
		return err
	}

	if s.Const != nil {
		if existing, ok := (*s.Const).(bool); ok && existing != b {
			return fmt.Errorf("%w: eq=%t conflicts with an existing bool constraint", ErrConflictingConstraints, b)
		}
	}

	s.Const = new(any(b))

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

	return setOneOfEnum(s, enum)
}

// setOneOfEnum pins the schema's enum to a oneof value list, reporting a
// conflict rather than silently overwriting an enum an earlier rule (such as a
// jsonschema enum tag) already set. Both oneof and enum fully enumerate the
// allowed values, so two different enumerations can never both hold; this
// mirrors the const family (eq) instead of letting whichever rule runs last win.
func setOneOfEnum(s *jsonschema.Schema, vals []any) error {
	if s.Enum != nil {
		return fmt.Errorf("%w: oneof conflicts with an existing enum constraint", ErrConflictingConstraints)
	}

	s.Enum = vals

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
