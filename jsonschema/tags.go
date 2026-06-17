package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

var (
	// Pattern matching the WORD= prefix that signals key-value mode, mirroring
	// the upstream reserved prefix (^[^ \t\n]*=).
	kvPrefixRegexp = regexp.MustCompile(`^[^ \t\n]*=`)

	// Recognized jsonschema struct tag keys. A tag enters key-value mode only
	// when its leading WORD= prefix names one of these keys; otherwise it is
	// treated as a bare description. This prevents a plain description such as
	// "a=b is the formula" from being misparsed as key-value.
	jsonSchemaTagKeys = map[string]bool{
		KeywordDescription:      true,
		KeywordTitle:            true,
		KeywordType:             true,
		KeywordPattern:          true,
		KeywordFormat:           true,
		KeywordDeprecated:       true,
		KeywordReadOnly:         true,
		KeywordWriteOnly:        true,
		KeywordUniqueItems:      true,
		KeywordMinimum:          true,
		KeywordMaximum:          true,
		KeywordExclusiveMinimum: true,
		KeywordExclusiveMaximum: true,
		KeywordMultipleOf:       true,
		KeywordMinLength:        true,
		KeywordMaxLength:        true,
		KeywordMinItems:         true,
		KeywordMaxItems:         true,
		KeywordMinProperties:    true,
		KeywordMaxProperties:    true,
		KeywordDefault:          true,
		KeywordConst:            true,
		KeywordEnum:             true,
		KeywordExamples:         true,
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

// applyJSONSchemaTag parses and applies a jsonschema struct tag to a schema.
//
// Pairs apply strictly in order. The scalar keys (default, const, enum,
// examples) parse their values against the effective scalar type: the field's
// Go type until a type= pair overrides it, and afterward a stand-in Go type
// for the overridden JSON type (see [standInTypeFor]), so a scalar key before
// type= keeps Go-kind parsing while one after it parses as the overridden
// type. The non-scalar overrides (array, object, null) have no stand-in, so a
// scalar key following one is an error.
func applyJSONSchemaTag(tag string, fieldType reflect.Type, s *Schema) error {
	if tag == "" {
		return nil
	}

	// Gate on the cheap regex before paying for splitTagPairs, then split once
	// and reuse the pairs for both the key-value decision and the apply loop.
	if !kvPrefixRegexp.MatchString(tag) {
		s.Description = tag
		return nil
	}

	pairs := splitTagPairs(tag)
	if !isKeyValueTag(pairs) {
		s.Description = tag
		return nil
	}

	scalarType := fieldType

	var overriddenType string

	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return fmt.Errorf("jsonschema tag: segment %q missing '='", pair)
		}

		if key == "" {
			return fmt.Errorf("jsonschema tag: empty key in %q", pair)
		}

		if scalarType == nil && isScalarValueKey(key) {
			return fmt.Errorf("jsonschema tag: key %q cannot follow type=%s", key, overriddenType)
		}

		err := applyTagKeyValue(key, value, scalarType, s)
		if err != nil {
			return err
		}

		if key == KeywordType {
			scalarType = standInTypeFor(value)
			overriddenType = value
		}
	}

	return nil
}

// isScalarValueKey reports whether a jsonschema tag key carries scalar values
// parsed against the effective scalar type (see [applyJSONSchemaTag]).
func isScalarValueKey(key string) bool {
	switch key {
	case KeywordDefault, KeywordConst, KeywordEnum, KeywordExamples:
		return true
	default:
		return false
	}
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
	case typeNameString:
		return reflect.TypeFor[string]()
	case typeNameInteger:
		return reflect.TypeFor[int64]()
	case typeNameNumber:
		return reflect.TypeFor[float64]()
	case typeNameBoolean:
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
// earlier type= override (see [applyJSONSchemaTag]). Only those keys consult
// it.
func applyTagKeyValue(key, value string, scalarType reflect.Type, s *Schema) error {
	switch key {
	case KeywordDescription:
		s.Description = value
	case KeywordTitle:
		s.Title = value

	case KeywordType:
		if !validTypeName(value) {
			return fmt.Errorf("jsonschema tag: key %q: %w: %q", key, ErrInvalidType, value)
		}

		applyTypeOverride(s, value)

	case KeywordPattern:
		s.Pattern = value
	case KeywordFormat:
		s.Format = value

	case KeywordDeprecated:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.Deprecated = b

	case KeywordReadOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.ReadOnly = b

	case KeywordWriteOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.WriteOnly = b

	case KeywordUniqueItems:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.UniqueItems = b

	case KeywordMinimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Minimum = &n

	case KeywordMaximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Maximum = &n

	case KeywordExclusiveMinimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMinimum = &n

	case KeywordExclusiveMaximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMaximum = &n

	case KeywordMultipleOf:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		if n <= 0 {
			return fmt.Errorf("jsonschema tag: key %q must be greater than 0, got %v", key, n)
		}

		s.MultipleOf = &n

	case KeywordMinLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinLength = &n

	case KeywordMaxLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxLength = &n

	case KeywordMinItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinItems = &n

	case KeywordMaxItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxItems = &n

	case KeywordMinProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinProperties = &n

	case KeywordMaxProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxProperties = &n

	case KeywordDefault:
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

	case KeywordConst:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, scalarType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Const = &v

	case KeywordEnum:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		// On a slice or array field the enum constrains each element, not the
		// array value itself, so the values parse against the element type and
		// land on the item schemas ("array of enum values").
		if base := derefType(scalarType); base.Kind() == reflect.Slice || base.Kind() == reflect.Array {
			return applyEnumToItems(key, value, base, s)
		}

		enumVals, err := parseEnumValues(key, value, scalarType)
		if err != nil {
			return err
		}

		s.Enum = enumVals

	case KeywordExamples:
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
// assertion: it sets Type, clears a Types array, removes the nullable anyOf
// wrapper a pointer field generates, and, when the new type is not numeric,
// drops the numeric bounds derived from the Go kind (an int64-reflected field
// such as [time.Duration] carries range bounds that would otherwise survive
// as noise on a string schema). Tag pairs apply in order, so keys after type=
// still take effect.
func applyTypeOverride(s *Schema, typeName string) {
	// A nullable pointer field wraps the value schema in anyOf[value, null];
	// an explicit type replaces the whole construct, including the wrapped
	// value branch and its kind-derived constraints.
	if nullableInnerSchema(s) != nil {
		s.AnyOf = nil
	}

	s.Type = typeName
	s.Types = nil

	if typeName != typeNameInteger && typeName != typeNameNumber {
		s.Minimum = nil
		s.Maximum = nil
		s.ExclusiveMinimum = nil
		s.ExclusiveMaximum = nil
		s.MultipleOf = nil
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
func applyEnumToItems(key, value string, t reflect.Type, s *Schema) error {
	items := itemSchemas(s)
	if len(items) == 0 {
		// A []byte field encodes as a single base64 string, leaving no
		// per-element schema for the enum to constrain.
		return fmt.Errorf("jsonschema tag: key %q: %s field has no item schema to constrain", key, t.Kind())
	}

	elem := derefType(t.Elem())
	if elem.Kind() == reflect.Slice || elem.Kind() == reflect.Array {
		for _, item := range items {
			err := applyEnumToItems(key, value, elem, item)
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
	}

	return nil
}

// itemSchemas returns the per-element schemas of a generated slice or array
// field schema: Items for slices, prefixItems (Draft 2020-12) or the
// items-as-array form (Draft-07) for fixed arrays. A nullable pointer field
// wraps the value schema in anyOf[value, null]; the lookup follows that
// wrapper. A []byte field (a base64 string) has no element schema and yields
// nil.
func itemSchemas(s *Schema) []*Schema {
	return schemashape.ItemSchemas(s)
}

// derefType follows pointers to the underlying non-pointer type.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		elem := t.Elem()
		// A self-referential pointer type (type T *T) has t.Elem() == t, which
		// would spin this loop forever; stop at it.
		if elem == t {
			break
		}

		t = elem
	}

	return t
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

	t = derefType(t)

	if value == typeNameNull {
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
		n, err := strconv.ParseInt(value, 10, intBitSize(t.Kind()))
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
		n, err := strconv.ParseUint(value, 10, uintBitSize(t.Kind()))
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

// intBitSize returns the bit size to parse a signed-integer tag value at, so an
// out-of-range constant overflows during parsing. Plain int is platform-
// dependent ([strconv.IntSize]); the sized kinds map to their fixed widths.
func intBitSize(k reflect.Kind) int {
	switch k {
	case reflect.Int8:
		return 8
	case reflect.Int16:
		return 16
	case reflect.Int32:
		return 32
	case reflect.Int64:
		return 64
	default: // reflect.Int
		return strconv.IntSize
	}
}

// uintBitSize returns the bit size to parse an unsigned-integer tag value at, so
// an out-of-range constant overflows during parsing. Plain uint and uintptr are
// platform-dependent ([strconv.IntSize]); the sized kinds map to their fixed
// widths.
func uintBitSize(k reflect.Kind) int {
	switch k {
	case reflect.Uint8:
		return 8
	case reflect.Uint16:
		return 16
	case reflect.Uint32:
		return 32
	case reflect.Uint64:
		return 64
	default: // reflect.Uint, reflect.Uintptr
		return strconv.IntSize
	}
}
