// Package tagparse parses and applies the jsonschema struct tag DSL onto a
// generated [jsonschema.Schema]. The reflection generator calls [Apply] once per
// field that carries a jsonschema tag; the tag's comma-separated key=value pairs
// (or a bare description) translate into schema keywords. The keyword names are
// shared with the public package through internal/keyword, so this logic lives
// here without importing the main package.
package tagparse

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

var (
	// ErrInvalidType is returned when a type= tag value names something other
	// than the seven JSON Schema type names. The jsonschema package maps it onto
	// its own public ErrInvalidType so callers can match it with [errors.Is].
	ErrInvalidType = errors.New("invalid type name")

	// Pattern matching the WORD= prefix that signals key-value mode, mirroring
	// the upstream reserved prefix (^[^ \t\n]*=).
	kvPrefixRegexp = regexp.MustCompile(`^[^ \t\n]*=`)

	// Recognized jsonschema struct tag keys. A tag enters key-value mode only
	// when its leading WORD= prefix names one of these keys; otherwise it is
	// treated as a bare description. This prevents a plain description such as
	// "a=b is the formula" from being misparsed as key-value.
	jsonSchemaTagKeys = map[string]bool{
		keyword.Description:      true,
		keyword.Title:            true,
		keyword.Type:             true,
		keyword.Pattern:          true,
		keyword.Format:           true,
		keyword.Deprecated:       true,
		keyword.ReadOnly:         true,
		keyword.WriteOnly:        true,
		keyword.UniqueItems:      true,
		keyword.Minimum:          true,
		keyword.Maximum:          true,
		keyword.ExclusiveMinimum: true,
		keyword.ExclusiveMaximum: true,
		keyword.MultipleOf:       true,
		keyword.MinLength:        true,
		keyword.MaxLength:        true,
		keyword.MinItems:         true,
		keyword.MaxItems:         true,
		keyword.MinProperties:    true,
		keyword.MaxProperties:    true,
		keyword.Default:          true,
		keyword.Const:            true,
		keyword.Enum:             true,
		keyword.Examples:         true,
	}
)

// isKeyValueTag reports whether a jsonschema tag, already split into pairs by
// [splitTagPairs], should be parsed as comma-separated key=value pairs (as
// opposed to a bare description). The caller gates on [kvPrefixRegexp] first,
// so a tag with no "WORD=" prefix never reaches here.
//
// A tag is key-value when its first segment is WORD=VALUE and either the key is
// a recognized keyword, or the value is space-free. This keeps recognized
// keywords with spaced values (e.g. "description=Hello World,minimum=1") in
// key-value mode, surfaces typos like "descrption=typo" as unrecognized-key
// errors, yet treats prose such as "a=b is the formula" as a bare description.
func isKeyValueTag(pairs []string) bool {
	// Inspect only the first key=value segment.
	key, value, found := strings.Cut(pairs[0], "=")
	if !found {
		return false
	}

	if jsonSchemaTagKeys[key] {
		return true
	}

	// Unknown key: prose (a value containing whitespace) is a description;
	// a space-free value is a likely key=value typo that should error.
	return !strings.ContainsAny(value, " \t")
}

// Apply parses and applies a jsonschema struct tag to a schema.
//
// Pairs apply strictly in order. The scalar keys (default, const, enum,
// examples) parse their values against the effective scalar type: the field's
// Go type until a type= pair overrides it, and afterward a stand-in Go type
// for the overridden JSON type (see [standInTypeFor]), so a scalar key before
// type= keeps Go-kind parsing while one after it parses as the overridden
// type. The non-scalar overrides (array, object, null) have no stand-in, so a
// scalar key following one is an error.
func Apply(tag string, fieldType reflect.Type, s *jsonschema.Schema) (bool, error) {
	if tag == "" {
		return false, nil
	}

	// Gate on the cheap regex before paying for splitTagPairs, then split once
	// and reuse the pairs for both the key-value decision and the apply loop.
	if !kvPrefixRegexp.MatchString(tag) {
		s.Description = tag

		return false, nil
	}

	pairs := splitTagPairs(tag)
	if !isKeyValueTag(pairs) {
		s.Description = tag

		return false, nil
	}

	scalarType := fieldType

	var (
		overriddenType string
		groupsSet      = map[string]bool{}
		boundAuthored  bool
	)

	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return false, fmt.Errorf("jsonschema tag: segment %q missing '='", pair)
		}

		if key == "" {
			return false, fmt.Errorf("jsonschema tag: empty key in %q", pair)
		}

		if scalarType == nil && isScalarValueKey(key) {
			return false, fmt.Errorf("jsonschema tag: key %q cannot follow type=%s", key, overriddenType)
		}

		// A type= override drops the constraint groups the new type cannot use.
		// Dropping the keywords derived from the Go kind is intended, but a
		// keyword the tag set explicitly is the author's input, so report the
		// conflict rather than discarding it silently. This in-loop check catches
		// a conflicting keyword set before type=; one set after type= records its
		// group only on the next iteration, so a post-loop check covers it.
		if key == keyword.Type && typename.Valid(value) {
			if g := conflictingGroup(groupsSet, value); g != "" {
				return false, fmt.Errorf("jsonschema tag: %s constraint conflicts with type=%s", g, value)
			}
		}

		err := applyTagKeyValue(key, value, scalarType, s)
		if err != nil {
			return false, err
		}

		if g := constraintGroup(key); g != "" {
			groupsSet[g] = true
		}

		if isNumericBoundKey(key) {
			boundAuthored = true
		}

		if key == keyword.Type {
			scalarType = standInTypeFor(value)
			overriddenType = value
		}
	}

	// Re-check the final override against every constraint group set across the
	// whole tag, so a conflicting keyword placed after type= is reported with the
	// same error as one placed before it (the in-loop guard runs before the group
	// is recorded, so it cannot see the later ordering).
	if overriddenType != "" {
		if g := conflictingGroup(groupsSet, overriddenType); g != "" {
			return false, fmt.Errorf("jsonschema tag: %s constraint conflicts with type=%s", g, overriddenType)
		}
	}

	return boundAuthored, nil
}

// isNumericBoundKey reports whether key is one of the four range-bound keywords
// that [schemashape.ClearNumericBounds] drops, used to tell an author-set bound
// (kept when it narrows an enum) from a kind-derived one (always redundant once
// pinned).
func isNumericBoundKey(key string) bool {
	switch key {
	case keyword.Minimum, keyword.Maximum, keyword.ExclusiveMinimum, keyword.ExclusiveMaximum:
		return true
	default:
		return false
	}
}

// isScalarValueKey reports whether a jsonschema tag key carries scalar values
// parsed against the effective scalar type (see [Apply]).
func isScalarValueKey(key string) bool {
	switch key {
	case keyword.Default, keyword.Const, keyword.Enum, keyword.Examples:
		return true
	default:
		return false
	}
}

// Constraint group names: the JSON type family whose keywords a type= override
// keeps. A keyword outside the override's family is dropped, so an explicitly
// tagged keyword in a dropped family is a conflict.
const (
	groupNumeric = "numeric"
	groupString  = "string"
	groupArray   = "array"
	groupObject  = "object"
)

// constraintGroup returns the constraint group a jsonschema tag key belongs to,
// or "" for an annotation key such as description or default that survives any
// type. Only the tag-settable constraint keywords are classified; the
// kind-derived keywords a type= override also drops never originate from a tag.
func constraintGroup(key string) string {
	switch key {
	case keyword.Minimum, keyword.Maximum, keyword.ExclusiveMinimum, keyword.ExclusiveMaximum, keyword.MultipleOf:
		return groupNumeric
	case keyword.MinLength, keyword.MaxLength, keyword.Pattern, keyword.Format:
		return groupString
	case keyword.UniqueItems, keyword.MinItems, keyword.MaxItems:
		return groupArray
	case keyword.MinProperties, keyword.MaxProperties:
		return groupObject
	default:
		return ""
	}
}

// typeConstraintGroup returns the one constraint group whose keywords a type=
// value keeps: integer and number both keep the numeric group, the others keep
// their own, and boolean or null keep none.
func typeConstraintGroup(typeName string) string {
	switch typeName {
	case typename.Integer, typename.Number:
		return groupNumeric
	case typename.String:
		return groupString
	case typename.Array:
		return groupArray
	case typename.Object:
		return groupObject
	default:
		return ""
	}
}

// conflictingGroup returns the first constraint group in groupsSet that a type=
// override to typeName would drop, or "" when every set group survives. The
// fixed iteration order keeps the reported conflict deterministic.
func conflictingGroup(groupsSet map[string]bool, typeName string) string {
	kept := typeConstraintGroup(typeName)

	for _, g := range []string{groupNumeric, groupString, groupArray, groupObject} {
		if groupsSet[g] && g != kept {
			return g
		}
	}

	return ""
}

// standInTypeFor returns the Go type that scalar tag values parse against
// after a type= pair overrides the field's reflected type: the override
// replaces the schema's type, so subsequent scalar values must parse as the
// overridden JSON type rather than the field's Go kind. The stand-ins are
// never pointers, so "null" scalar values are rejected after an override, and
// never sequences, so the enum-to-items redirection a slice or array field
// normally gets turns off: the enum constrains the overridden value schema
// itself. The non-scalar JSON types (array, object, null) have no scalar
// stand-in and return nil; scalar keys following such an override are an
// error.
func standInTypeFor(typeName string) reflect.Type {
	switch typeName {
	case typename.String:
		return reflect.TypeFor[string]()
	case typename.Integer:
		return reflect.TypeFor[int64]()
	case typename.Number:
		return reflect.TypeFor[float64]()
	case typename.Boolean:
		return reflect.TypeFor[bool]()
	default: // array, object, null
		return nil
	}
}

// splitTagPairs splits a tag string into key=value segments on unescaped
// commas. A comma can be included in a value by escaping it with a backslash
// (`\,`), and a literal backslash by doubling it (`\\`); the escapes are
// resolved in the returned segments. This lets values such as
// `description=Hello\, World` carry commas without being truncated.
func splitTagPairs(tag string) []string {
	var (
		pairs   []string
		segment strings.Builder
		escaped bool
	)

	for i := range len(tag) {
		c := tag[i]
		if escaped {
			// Preserve recognized escapes (\, and \\) literally; pass any other
			// escaped byte through unchanged so unrelated backslashes survive.
			switch c {
			case ',', '\\':
				segment.WriteByte(c)
			default:
				segment.WriteByte('\\')
				segment.WriteByte(c)
			}

			escaped = false

			continue
		}

		switch c {
		case '\\':
			escaped = true
		case ',':
			pairs = append(pairs, segment.String())
			segment.Reset()

		default:
			segment.WriteByte(c)
		}
	}

	// A trailing backslash has no following byte to escape; keep it literal.
	if escaped {
		segment.WriteByte('\\')
	}

	pairs = append(pairs, segment.String())

	return pairs
}

// applyTagKeyValue applies a single key=value pair from the jsonschema tag.
// ScalarType is the effective type the scalar keys (default, const, enum,
// examples) parse against: the field's Go type, or the stand-in for an
// earlier type= override (see [Apply]). Only those keys consult it.
func applyTagKeyValue(key, value string, scalarType reflect.Type, s *jsonschema.Schema) error {
	switch key {
	case keyword.Description:
		s.Description = value
	case keyword.Title:
		s.Title = value

	case keyword.Type:
		if !typename.Valid(value) {
			return fmt.Errorf("jsonschema tag: key %q: %w: %q", key, ErrInvalidType, value)
		}

		applyTypeOverride(s, value)

	case keyword.Pattern:
		s.Pattern = value
	case keyword.Format:
		s.Format = value

	case keyword.Deprecated:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.Deprecated = b

	case keyword.ReadOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.ReadOnly = b

	case keyword.WriteOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.WriteOnly = b

	case keyword.UniqueItems:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.UniqueItems = b

	case keyword.Minimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Minimum = &n

	case keyword.Maximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Maximum = &n

	case keyword.ExclusiveMinimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMinimum = &n

	case keyword.ExclusiveMaximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMaximum = &n

	case keyword.MultipleOf:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		if n <= 0 {
			return fmt.Errorf("jsonschema tag: key %q must be greater than 0, got %v", key, n)
		}

		s.MultipleOf = &n

	case keyword.MinLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinLength = &n

	case keyword.MaxLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxLength = &n

	case keyword.MinItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinItems = &n

	case keyword.MaxItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxItems = &n

	case keyword.MinProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinProperties = &n

	case keyword.MaxProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxProperties = &n

	case keyword.Default:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, scalarType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Default = raw

	case keyword.Const:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, scalarType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Const = &v

	case keyword.Enum:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		// On a slice or array field the enum constrains each element, not the
		// array value itself, so the values parse against the element type and
		// land on the item schemas ("array of enum values").
		if base := numkind.DerefType(scalarType); base.Kind() == reflect.Slice || base.Kind() == reflect.Array {
			return applyEnumToItems(key, value, base, s)
		}

		enumVals, err := parseEnumValues(key, value, scalarType)
		if err != nil {
			return err
		}

		s.Enum = enumVals

	case keyword.Examples:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		examples, err := parseEnumValues(key, value, scalarType)
		if err != nil {
			return err
		}

		s.Examples = examples

	default:
		return fmt.Errorf("jsonschema tag: unrecognized key %q", key)
	}

	return nil
}

// applyTypeOverride applies a type= tag value, replacing the reflected type
// assertion: it sets Type, clears a Types array, drops a bare $ref to a
// definition, removes the nullable anyOf wrapper a pointer field generates,
// and drops the keyword groups the new type cannot use. The numeric bounds,
// array keywords, object keywords, and string constraints each derive from the
// original Go kind (an int64-reflected field such as [time.Duration] carries
// range bounds, a slice carries items, a struct carries properties, and a
// string-reflected field such as [time.Time] or [big.Rat] carries a
// format/pattern); left on a schema of a different type they are vacuous but
// emit as confusing dead structure. Tag pairs apply in order, so keys after
// type= still take effect.
func applyTypeOverride(s *jsonschema.Schema, typeName string) {
	// A nullable pointer field wraps the value schema in anyOf[value, null];
	// an explicit type replaces the whole construct, including the wrapped
	// value branch and its kind-derived constraints.
	if schemashape.NullableInnerSchema(s) != nil {
		s.AnyOf = nil
	}

	// A field whose type was extracted to $defs reflects to a bare {$ref}; the
	// explicit type replaces that assertion, so drop the ref. Leaving it would
	// emit {$ref, type}, which under 2020-12 requires both to hold and is
	// unsatisfiable when the referenced definition is a different type.
	s.Ref = ""

	s.Type = typeName
	s.Types = nil

	if typeName != typename.Integer && typeName != typename.Number {
		s.Minimum = nil
		s.Maximum = nil
		s.ExclusiveMinimum = nil
		s.ExclusiveMaximum = nil
		s.MultipleOf = nil
	}

	if typeName != typename.String {
		s.Format = ""
		s.Pattern = ""
		s.MinLength = nil
		s.MaxLength = nil
	}

	if typeName != typename.Array {
		s.Items = nil
		s.PrefixItems = nil
		s.ItemsArray = nil
		s.AdditionalItems = nil
		s.UnevaluatedItems = nil
		s.MinItems = nil
		s.MaxItems = nil
		s.UniqueItems = false
		s.Contains = nil
		s.MinContains = nil
		s.MaxContains = nil
	}

	if typeName != typename.Object {
		s.Properties = nil
		s.PatternProperties = nil
		s.AdditionalProperties = nil
		s.PropertyNames = nil
		s.UnevaluatedProperties = nil
		s.Required = nil
		s.MinProperties = nil
		s.MaxProperties = nil
		s.DependentRequired = nil
		s.DependentSchemas = nil
	}
}

// parseEnumValues parses a pipe-separated enum tag value against t, returning
// the parsed values in tag order.
func parseEnumValues(key, value string, t reflect.Type) ([]any, error) {
	parts := strings.Split(value, "|")

	enumVals := make([]any, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("jsonschema tag: key %q has an empty value segment", key)
		}

		v, err := parseTypedScalar(p, t)
		if err != nil {
			return nil, fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		enumVals[i] = v
	}

	return enumVals, nil
}

// applyEnumToItems applies an enum tag on a slice or array field to the
// field's item schemas, parsing each value against the element type. A nested
// sequence element descends recursively, so the enum always lands on the
// innermost (scalar) item schemas. The other scalar tag keys (const, default,
// examples) remain whole-value constraints and are not redirected this way.
func applyEnumToItems(key, value string, t reflect.Type, s *jsonschema.Schema) error {
	items := schemashape.ItemSchemas(s)
	if len(items) == 0 {
		// A []byte field encodes as a single base64 string, leaving no
		// per-element schema for the enum to constrain.
		return fmt.Errorf("jsonschema tag: key %q: %s field has no item schema to constrain", key, t.Kind())
	}

	// Descend nested sequences on the dereferenced element type, but parse the
	// enum against the pointer-preserving element type: a nullable-pointer
	// element ([]*string) must accept a "null" enum member, which parseTypedScalar
	// only permits when it sees the pointer rather than the dereferenced value.
	elem := t.Elem()
	derefElem := numkind.DerefType(elem)

	// A self-recursive sequence element (type T []*T) dereferences back to the
	// sequence type itself; descending would recurse on the same type and bottom
	// out on the misleading "no item schema" message, so name the real cause.
	if derefElem == t {
		return fmt.Errorf(
			"jsonschema tag: key %q: recursive %s element type %s is not supported for an enum constraint",
			key, t.Kind(), t,
		)
	}

	if derefElem.Kind() == reflect.Slice || derefElem.Kind() == reflect.Array {
		for _, item := range items {
			err := applyEnumToItems(key, value, derefElem, item)
			if err != nil {
				return err
			}
		}

		return nil
	}

	enumVals, err := parseEnumValues(key, value, elem)
	if err != nil {
		return err
	}

	for _, item := range items {
		// Each item schema gets its own value slice so no slice is shared
		// across schema nodes.
		item.Enum = slices.Clone(enumVals)

		// A nullable-pointer element ([]*string) wraps its value schema in
		// anyOf[value, null]; the enum belongs on the value branch, not as a
		// sibling of anyOf where it would reject a valid null element. This is
		// a no-op for a non-nullable item.
		schemashape.RelocateConstEnumToValueBranch(item)
	}

	return nil
}

// parseBoolValue parses a boolean tag value.
func parseBoolValue(key, value string) (bool, error) {
	if value == "" {
		return false, fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
	}

	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("jsonschema tag: key %q: invalid boolean %q", key, value)
	}
}

// nonDecimalFloat reports a value that [strconv.ParseFloat] accepts beyond plain
// decimal notation: the underscore digit separator (1_000.5) and the
// hexadecimal float forms (0x1p-2). The integer keywords parse in base 10 and
// reject both, so the float keywords reject them too, keeping a numeric tag
// value accepted or rejected the same way regardless of the field's type.
func nonDecimalFloat(value string) bool {
	return strings.ContainsAny(value, "_xX")
}

// parseFloat parses a float64 tag value.
func parseFloat(key, value string) (float64, error) {
	if value == "" {
		return 0, fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
	}

	if nonDecimalFloat(value) {
		return 0, fmt.Errorf("jsonschema tag: key %q: %q is not a decimal number", key, value)
	}

	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("jsonschema tag: key %q: %q is not a finite number", key, value)
	}

	// An integer-literal bound the float64 cannot represent exactly (magnitude
	// above 2^53, e.g. an int64 field's own max) silently rounds to a different
	// value when stored as the schema's *float64 bound, loosening the constraint.
	// Reject it rather than ship a bound that differs from the tag, keeping the
	// bound keywords consistent with the exact-precision const/enum parsing on
	// the same field. Fractional and exponent literals are left alone.
	if !strings.ContainsAny(value, ".eE") {
		if exact, ok := new(big.Int).SetString(value, 10); ok {
			if new(big.Float).SetInt(exact).Cmp(big.NewFloat(n)) != 0 {
				return 0, fmt.Errorf(
					"jsonschema tag: key %q: integer bound %q exceeds exact float64 "+
						"precision (>2^53); use const for an exact extreme value",
					key, value,
				)
			}
		}
	}

	return n, nil
}

// parseInt parses a non-negative int tag value, rejecting negatives as required
// for the length and count keywords by JSON Schema.
func parseInt(key, value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	if n < 0 {
		return 0, fmt.Errorf("jsonschema tag: key %q must be non-negative, got %d", key, n)
	}

	return n, nil
}

// parseTypedScalar parses a scalar value according to the field's Go type.
func parseTypedScalar(value string, t reflect.Type) (any, error) {
	// The literal "null" maps to JSON null, but only a field whose Go type can
	// itself hold nil (a pointer) may legitimately carry it. Detect that before
	// following pointers so a non-nullable kind (int/bool/float/string) rejects
	// "null" via its kind switch instead of silently accepting JSON null.
	nullable := t.Kind() == reflect.Pointer

	t = numkind.DerefType(t)

	if value == typename.Null {
		if !nullable {
			return nil, fmt.Errorf("cannot assign null to non-nullable type %s", t.Kind())
		}

		return nil, nil //nolint:nilnil // Intentional: nil represents JSON null.
	}

	switch t.Kind() {
	case reflect.String:
		return value, nil
	case reflect.Bool:
		switch value {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, fmt.Errorf("invalid boolean %q", value)
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Parse at the field kind's bit size so a value the field cannot hold (for
		// example const=200 on an int8) overflows here. Strconv reports overflow as
		// strconv.ErrRange, which surfaces from Generate as a tag value error rather
		// than producing a schema that accepts an out-of-range constant.
		n, err := strconv.ParseInt(value, 10, numkind.IntBitSize(t.Kind()))
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", value, err)
		}

		// Return int64, not a platform int, so a value above 2^31-1 survives on a
		// 32-bit build (float64 also cannot represent all int64 values exactly).
		// The validate-tag interpreter returns int64 too, so both tag dialects
		// yield the same const/enum/example type.
		return n, nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		// Parse at the field kind's bit size so a value the field cannot hold (for
		// example enum=300 on a uint8) overflows here rather than being accepted.
		n, err := strconv.ParseUint(value, 10, numkind.UintBitSize(t.Kind()))
		if err != nil {
			return nil, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}

		// Return as uint64 to preserve integer precision (neither int nor
		// float64 can represent all uint64 values exactly).
		return n, nil

	case reflect.Float32, reflect.Float64:
		// Reject the underscore and hexadecimal float forms [strconv.ParseFloat]
		// would accept, matching the base-10 integer keys so a numeric tag value
		// parses the same way across field types.
		if nonDecimalFloat(value) {
			return nil, fmt.Errorf("invalid number %q: not a decimal number", value)
		}

		// Parse at 64 bits for storage so the stored value is the float64 closest
		// to the decimal the author wrote, not its float32-rounded approximation.
		// Rounding const=0.1 to float32 would store 0.10000000149011612, which a
		// {"v":0.1} instance can never match against its own const.
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, err)
		}

		// NaN and the infinities parse without error but cannot be marshaled into
		// a schema (encoding/json rejects them) and a NaN const matches nothing,
		// so reject them here as parseFloat does for the bound keywords.
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%q is not a finite number", value)
		}

		// A float32 field cannot hold a value outside its range, so reparse at 32
		// bits purely as an overflow check: const=1e300 on a float32 still surfaces
		// strconv.ErrRange as a tag value error rather than a schema that accepts a
		// value the Go type can never hold.
		if t.Kind() == reflect.Float32 {
			_, err = strconv.ParseFloat(value, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q: %w", value, err)
			}
		}

		return n, nil

	default:
		// Scalar tag values (default, const, enum, examples) are only meaningful
		// for primitive kinds. Anything else (struct, slice, map, interface) is
		// an error rather than being silently coerced to a string.
		return nil, fmt.Errorf("cannot assign scalar value %q to type %s", value, t.Kind())
	}
}
