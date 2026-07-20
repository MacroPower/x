package validate

import (
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
)

// applyStringMinConstraint applies min/gte or gt to a string field by raising
// its minLength floor on the canvas, reading the effective floor.
func applyStringMinConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	return applyMinBound(&field.Schema.MinLength, field.EffectiveMinLength(), value, exclusive)
}

// applyStringMaxConstraint applies max/lte or lt to a string field by lowering
// its maxLength ceiling on the canvas, reading the effective bounds.
func applyStringMaxConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	return applyMaxBound(&field.Schema.MinLength, &field.Schema.MaxLength,
		field.EffectiveMinLength(), field.EffectiveMaxLength(), value, exclusive)
}

// applyStringLenConstraint applies len=N to a string field by pinning minLength
// and maxLength on the canvas to the intersected bound.
func applyStringLenConstraint(field jsonschema.FieldContext, value string) error {
	return applyLenBound(&field.Schema.MinLength, &field.Schema.MaxLength,
		field.EffectiveMinLength(), field.EffectiveMaxLength(), value)
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

	return setOneOfEnum(s, enum)
}

// applyStringEq applies eq=val → const for a string schema. A const already
// pinned to a different value by an earlier rule is a conflict the two rules can
// never both satisfy, so it is reported rather than silently overwritten. This
// keeps the result independent of tag order and matches setNumericConst and
// applyBoolEq.
func applyStringEq(s *jsonschema.Schema, value string) error {
	if s.Const != nil {
		if existing, ok := (*s.Const).(string); ok && existing != value {
			return fmt.Errorf("%w: eq=%q conflicts with an existing value constraint",
				ErrConflictingConstraints, value)
		}
	}

	var v any = value

	s.Const = &v

	return nil
}

// applyStringNe applies ne=val → not for a string schema.
func applyStringNe(s *jsonschema.Schema, value string) {
	forbidValue(s, value)
}

// isStringKind reports whether the type is a string kind.
func isStringKind(t reflect.Type) bool {
	return t.Kind() == reflect.String
}
