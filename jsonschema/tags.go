package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
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
		"description":      true,
		"title":            true,
		"pattern":          true,
		"format":           true,
		"deprecated":       true,
		"readOnly":         true,
		"writeOnly":        true,
		"uniqueItems":      true,
		"minimum":          true,
		"maximum":          true,
		"exclusiveMinimum": true,
		"exclusiveMaximum": true,
		"multipleOf":       true,
		"minLength":        true,
		"maxLength":        true,
		"minItems":         true,
		"maxItems":         true,
		"minProperties":    true,
		"maxProperties":    true,
		"default":          true,
		"const":            true,
		"enum":             true,
		"examples":         true,
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

	// Check if this is a key-value tag (starts with a recognized WORD=).
	if !isKeyValueTag(tag) {
		// Bare description.
		s.Description = tag
		return nil
	}

	// Parse as comma-separated key=value pairs.
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

	for i := 0; i < len(tag); i++ {
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
	case "description":
		s.Description = value
	case "title":
		s.Title = value
	case "pattern":
		s.Pattern = value
	case "format":
		s.Format = value

	case "deprecated":
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.Deprecated = b

	case "readOnly":
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.ReadOnly = b

	case "writeOnly":
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.WriteOnly = b

	case "uniqueItems":
		b, err := parseBoolValue(key, value)
		if err != nil {
			return err
		}

		s.UniqueItems = b

	case "minimum":
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Minimum = &n

	case "maximum":
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.Maximum = &n

	case "exclusiveMinimum":
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMinimum = &n

	case "exclusiveMaximum":
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}

		s.ExclusiveMaximum = &n

	case "multipleOf":
		n, err := parseFloat(key, value)
		if err != nil {
			return err
		}
		if n <= 0 {
			return fmt.Errorf("jsonschema tag: key %q must be greater than 0, got %v", key, n)
		}

		s.MultipleOf = &n

	case "minLength":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinLength = &n

	case "maxLength":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxLength = &n

	case "minItems":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinItems = &n

	case "maxItems":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxItems = &n

	case "minProperties":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MinProperties = &n

	case "maxProperties":
		n, err := parseInt(key, value)
		if err != nil {
			return err
		}

		s.MaxProperties = &n

	case "default":
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

	case "const":
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		v, err := parseTypedScalar(value, fieldType)
		if err != nil {
			return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
		}

		s.Const = &v

	case "enum":
		if value == "" {
			return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
		}

		parts := strings.Split(value, "|")

		enumVals := make([]any, len(parts))
		for i, p := range parts {
			if p == "" {
				return fmt.Errorf("jsonschema tag: key %q has an empty value segment", key)
			}

			v, err := parseTypedScalar(p, fieldType)
			if err != nil {
				return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
			}

			enumVals[i] = v
		}

		s.Enum = enumVals

	case "examples":
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

// parseInt parses a non-negative int tag value. Every keyword that uses it
// (minLength, maxLength, minItems, maxItems, minProperties, maxProperties)
// requires a non-negative integer per JSON Schema.
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

	// Follow pointers.
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
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", value, err)
		}
		// Return as int to preserve integer precision (float64 cannot represent
		// all int64 values exactly) and to match the int-typed values the
		// validate-tag interpreter produces, so both tag dialects yield the same
		// const/enum/example type.
		return int(n), nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid unsigned integer %q: %w", value, err)
		}
		// Return as uint64 to preserve integer precision (neither int nor
		// float64 can represent all uint64 values exactly).
		return n, nil

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", value, err)
		}

		return n, nil

	default:
		// Scalar tag values (default, const, enum, examples) are only meaningful
		// for primitive kinds. Anything else (struct, slice, map, interface) is
		// an error rather than being silently coerced to a string.
		return nil, fmt.Errorf("cannot assign scalar value %q to type %s", value, t.Kind())
	}
}
