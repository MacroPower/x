package validate

import (
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
)

// applyStringMinConstraint applies min/gte or gt to a string field by raising
// its minLength floor on the canvas, intersecting its own repeated rules against
// the canvas value (reconcile intersects against the type bound).
func applyStringMinConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	return applyMinBound(&field.Canvas.MinLength, field.Canvas.MinLength, value, exclusive)
}

// applyStringMaxConstraint applies max/lte or lt to a string field by lowering
// its maxLength ceiling on the canvas, intersecting its own repeated rules
// against the canvas values (reconcile intersects against the type bound).
func applyStringMaxConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	return applyMaxBound(&field.Canvas.MinLength, &field.Canvas.MaxLength,
		field.Canvas.MinLength, field.Canvas.MaxLength, value, exclusive)
}

// applyStringLenConstraint applies len=N to a string field by pinning minLength
// and maxLength on the canvas to the intersected bound.
func applyStringLenConstraint(field jsonschema.FieldContext, value string) error {
	return applyLenBound(&field.Canvas.MinLength, &field.Canvas.MaxLength,
		field.Canvas.MinLength, field.Canvas.MaxLength, value)
}

// applyStringOneOf applies oneof=a b c to a string field. Single-quoted runs
// group multi-word values (oneof='New York' Boston) per go-playground/validator.
func applyStringOneOf(field jsonschema.FieldContext, value string) error {
	vals := splitOneOfValues(value)
	if len(vals) == 0 {
		return fmt.Errorf("validate tag: oneof requires at least one value")
	}

	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = v
	}

	return setOneOfEnum(field, enum)
}

// applyStringEq applies eq=val → const for a string field. A const already
// pinned to a different value by an earlier rule -- or by the field's type
// itself, whose const reconcile would otherwise silently overwrite -- is a
// conflict the two rules can never both satisfy, so it is reported rather than
// silently resolved. This keeps the result independent of tag order and matches
// setNumericConst and applyBoolEq.
func applyStringEq(field jsonschema.FieldContext, value string) error {
	if field.Canvas.Const != nil {
		if existing, ok := (*field.Canvas.Const).(string); ok && existing != value {
			return fmt.Errorf("%w: eq=%q conflicts with an existing value constraint",
				ErrConflictingConstraints, value)
		}
	}

	if field.Base != nil && field.Base.Const != nil && !numericEqual(*field.Base.Const, any(value)) {
		return fmt.Errorf("%w: eq=%q conflicts with the type's existing const",
			ErrConflictingConstraints, value)
	}

	var v any = value

	field.Canvas.Const = &v

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
