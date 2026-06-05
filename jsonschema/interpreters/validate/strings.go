package validate

import (
	"fmt"
	"reflect"
	"strconv"

	"go.jacobcolvin.com/jsonschema"
)

// applyStringMinConstraint applies min/gte or gt to a string schema.
func applyStringMinConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// Gt=N means minLength N+1, clamped to a non-negative bound as JSON Schema
	// requires.
	n = clampNonNegative(inclusiveLowerBound(n, exclusive))

	s.MinLength = jsonschema.Ptr(n)

	return nil
}

// applyStringMaxConstraint applies max/lte or lt to a string schema.
func applyStringMaxConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// Lt=N means maxLength N-1, clamped to a non-negative bound as JSON Schema
	// requires.
	n = clampNonNegative(inclusiveUpperBound(n, exclusive))

	s.MaxLength = jsonschema.Ptr(n)

	return nil
}

// applyStringLenConstraint applies len=N to a string schema.
func applyStringLenConstraint(s *jsonschema.Schema, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("validate tag: invalid number %q: %w", value, err)
	}
	// MinLength and MaxLength MUST be non-negative per JSON Schema; a negative
	// length collapses to 0.
	n = clampNonNegative(n)

	s.MinLength = jsonschema.Ptr(n)
	s.MaxLength = jsonschema.Ptr(n)

	return nil
}

// applyStringOneOf applies oneof=a b c to a string schema. Single-quoted runs
// group multi-word values (oneof='New York' Boston) per go-playground/validator.
func applyStringOneOf(s *jsonschema.Schema, value string) error {
	vals := splitOneOfValues(value)
	if len(vals) == 0 {
		return fmt.Errorf("validate tag: oneof requires at least one value")
	}

	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = v
	}

	s.Enum = enum

	return nil
}

// applyStringEq applies eq=val → const for a string schema.
func applyStringEq(s *jsonschema.Schema, value string) {
	var v any = value

	s.Const = &v
}

// applyStringNe applies ne=val → not for a string schema.
func applyStringNe(s *jsonschema.Schema, value string) {
	forbidValue(s, value)
}

// isStringKind reports whether the type is a string kind.
func isStringKind(t reflect.Type) bool {
	return t.Kind() == reflect.String
}
