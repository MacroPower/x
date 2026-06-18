package validate

import (
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
)

// applyStringMinConstraint applies min/gte or gt to a string schema by raising
// its minLength floor.
func applyStringMinConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	return applyMinBound(&s.MinLength, value, exclusive)
}

// applyStringMaxConstraint applies max/lte or lt to a string schema by lowering
// its maxLength ceiling.
func applyStringMaxConstraint(s *jsonschema.Schema, value string, exclusive bool) error {
	return applyMaxBound(&s.MinLength, &s.MaxLength, value, exclusive)
}

// applyStringLenConstraint applies len=N to a string schema by pinning minLength
// and maxLength to the intersected bound.
func applyStringLenConstraint(s *jsonschema.Schema, value string) error {
	return applyLenBound(&s.MinLength, &s.MaxLength, value)
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
