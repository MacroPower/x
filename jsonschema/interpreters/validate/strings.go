package validate

import (
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// applyStringMinConstraint applies min/gte or gt to a string field by raising
// its minLength floor through the shared constraints facade, which folds an
// exclusive gt, clamps non-negative, and intersects against the effective floor.
func applyStringMinConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	rule := jsonschema.LenMin
	if exclusive {
		rule = jsonschema.LenGt
	}

	err := field.Constraints().AddLengthBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: min: %w", err)
	}

	return nil
}

// applyStringMaxConstraint applies max/lte or lt to a string field by lowering
// its maxLength ceiling through the facade, which folds an exclusive lt and
// expresses an unsatisfiable sub-zero ceiling as the floor-one/ceiling-zero
// range no string satisfies.
func applyStringMaxConstraint(field jsonschema.FieldContext, value string, exclusive bool) error {
	rule := jsonschema.LenMax
	if exclusive {
		rule = jsonschema.LenLt
	}

	err := field.Constraints().AddLengthBound(rule, value)
	if err != nil {
		return fmt.Errorf("validate tag: max: %w", err)
	}

	return nil
}

// applyStringLenConstraint applies len=N to a string field by pinning minLength
// and maxLength through the facade to the intersected bound (a negative len
// yields the unsatisfiable range).
func applyStringLenConstraint(field jsonschema.FieldContext, value string) error {
	err := field.Constraints().AddLengthBound(jsonschema.LenExact, value)
	if err != nil {
		return fmt.Errorf("validate tag: len: %w", err)
	}

	return nil
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

	if field.Base != nil && field.Base.Const != nil && !constraint.ValuesEqual(*field.Base.Const, any(value)) {
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
