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
		keywordDescription:      true,
		keywordTitle:            true,
		keywordType:             true,
		keywordPattern:          true,
		keywordFormat:           true,
		keywordDeprecated:       true,
		keywordReadOnly:         true,
		keywordWriteOnly:        true,
		keywordUniqueItems:      true,
		keywordMinimum:          true,
		keywordMaximum:          true,
		keywordExclusiveMinimum: true,
		keywordExclusiveMaximum: true,
		keywordMultipleOf:       true,
		keywordMinLength:        true,
		keywordMaxLength:        true,
		keywordMinItems:         true,
		keywordMaxItems:         true,
		keywordMinProperties:    true,
		keywordMaxProperties:    true,
		keywordDefault:          true,
		keywordConst:            true,
		keywordEnum:             true,
		keywordExamples:         true,
	}
)

// isKeyValueTag reports whether a jsonschema tag should be parsed as
// comma-separated key=value pairs (as opposed to a bare description).
//
// A tag is key-value when its first segment is WORD=VALUE (no space before the
// '=') and either the key is a recognized keyword, or the value is space-free.
// This keeps recognized keywords with spaced values (e.g.
// "description=Hello World,minimum=1") in key-value mode, surfaces typos like
// "descrption=typo" as unrecognized-key errors, yet treats prose such as
// "a=b is the formula" as a bare description.
func isKeyValueTag(tag string) bool {
	if !kvPrefixRegexp.MatchString(tag) {
		return false
	}

	// Inspect only the first key=value segment, honoring the same escaped-comma
	// rules splitTagPairs applies so an escaped comma in the value does not
	// prematurely end the segment.
	first := splitTagPairs(tag)[0]

	key, value, found := strings.Cut(first, "=")
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
func applyJSONSchemaTag(tag string, fieldType reflect.Type, s *Schema) error {
	if tag == "" {
		return nil
	}

	if !isKeyValueTag(tag) {
		s.Description = tag
		return nil
	}

	pairs := splitTagPairs(tag)
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return fmt.Errorf("jsonschema tag: segment %q missing '='", pair)
		}

		if key == "" {
			return fmt.Errorf("jsonschema tag: empty key in %q", pair)
		}

		err := applyTagKeyValue(key, value, fieldType, s)
		if err != nil {
			return err
		}
	}

	return nil
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
func applyTagKeyValue(key, value string, fieldType reflect.Type, s *Schema) error {
	switch key {
	case keywordDescription:
		s.Description = value
	case keywordTitle:
		s.Title = value

	case keywordType:
		if !validTypeName(value) {
			return fmt.Errorf("jsonschema tag: key %q: %w: %q", key, ErrInvalidType, value)
		}

		applyTypeOverride(s, value)

	case keywordPattern:
		s.Pattern = value
	case keywordFormat:
		s.Format = value

	case keywordDeprecated:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.Deprecated = b

	case keywordReadOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.ReadOnly = b

	case keywordWriteOnly:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.WriteOnly = b

	case keywordUniqueItems:
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.UniqueItems = b

	case keywordMinimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Minimum = &n

	case keywordMaximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Maximum = &n

	case keywordExclusiveMinimum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMinimum = &n

	case keywordExclusiveMaximum:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMaximum = &n

	case keywordMultipleOf:
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		if n <= 0 {
			return fmt.Errorf("jsonschema tag: key %q must be greater than 0, got %v", key, n)
		}

		s.MultipleOf = &n

	case keywordMinLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinLength = &n

	case keywordMaxLength:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxLength = &n

	case keywordMinItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinItems = &n

	case keywordMaxItems:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxItems = &n

	case keywordMinProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinProperties = &n

	case keywordMaxProperties:
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxProperties = &n

	case keywordDefault:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, fieldType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Default = raw

	case keywordConst:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, fieldType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Const = &v

	case keywordEnum:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		// On a slice or array field the enum constrains each element, not the
		// array value itself, so the values parse against the element type and
		// land on the item schemas ("array of enum values").
		if base := derefType(fieldType); base.Kind() == reflect.Slice || base.Kind() == reflect.Array {
			return applyEnumToItems(key, value, base, s)
		}

		enumVals, err := parseEnumValues(key, value, fieldType)
		if err != nil {
			return err
		}

		s.Enum = enumVals

	case keywordExamples:
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		parts := strings.Split(value, "|")

		examples := make([]any, len(parts))
		for i, p := range parts {
			if p == "" {
				return fmt.Errorf("jsonschema tag: key %q has an empty value segment", key)
			}

			v, err := parseTypedScalar(p, fieldType)
			if err != nil {
				return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
			}

			examples[i] = v
		}

		s.Examples = examples

	default:
		return fmt.Errorf("jsonschema tag: unrecognized key %q", key)
	}

	return nil
}

// applyTypeOverride applies a type= tag value, replacing the reflected type
// assertion: it sets Type, clears a Types array, removes the nullable anyOf
// wrapper a pointer field generates, and — when the new type is not numeric —
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
	if inner := nullableInnerSchema(s); inner != nil {
		s = inner
	}

	switch {
	case s.Items != nil:
		return []*Schema{s.Items}
	case len(s.PrefixItems) > 0:
		return s.PrefixItems
	case len(s.ItemsArray) > 0:
		return s.ItemsArray
	default:
		return nil
	}
}

// derefType follows pointers to the underlying non-pointer type.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
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

// parseFloat parses a float64 tag value.
func parseFloat(key, value string) (float64, error) {
	if value == "" {
		return 0, fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
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

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

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

		// Return as int to preserve integer precision (float64 cannot represent
		// all int64 values exactly) and to match the int-typed values the
		// validate-tag interpreter produces, so both tag dialects yield the same
		// const/enum/example type.
		return int(n), nil

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
		// Parse at 64 bits for storage so the stored value is the float64 closest
		// to the decimal the author wrote, not its float32-rounded approximation.
		// Rounding const=0.1 to float32 would store 0.10000000149011612, which a
		// {"v":0.1} instance can never match against its own const.
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, err)
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
