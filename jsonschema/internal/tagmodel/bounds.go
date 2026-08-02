package tagmodel

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
)

// baseOf returns the target's type-derived schema, or an empty one when it has
// none, so the effective-value reads never dereference nil.
func baseOf(t Target) *jsonschema.Schema {
	if t.Base != nil {
		return t.Base
	}

	return &jsonschema.Schema{}
}

// coalesce returns the canvas value when the target authored one, otherwise the
// type-derived value: the effective keyword a rule tightens against, so a tag
// bound never weakens the type's own and repeated tag bounds intersect
// order-independently.
func coalesce[T any](canvas, base *T) *T {
	if canvas != nil {
		return canvas
	}

	return base
}

// tightenBound writes n into *field when it tightens the effective bound: a
// higher value for a floor, a lower one for a ceiling. A widening or equal value
// is a no-op, so bounds intersect and never loosen.
func tightenBound(field **float64, eff *float64, n float64, ceiling bool) {
	if eff == nil || (ceiling && n < *eff) || (!ceiling && n > *eff) {
		*field = &n
	}
}

// raiseFloor writes n into *field when it tightens the effective lower bound.
func raiseFloor(field **int, eff *int, n int) {
	if eff == nil || n > *eff {
		*field = &n
	}
}

// lowerCeiling writes n into *field when it tightens the effective upper bound.
func lowerCeiling(field **int, eff *int, n int) {
	if eff == nil || n < *eff {
		*field = &n
	}
}

// parseSizeLiteral parses a length or count literal, which is always a base-10
// integer in both dialects.
func parseSizeLiteral(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", value, err)
	}

	return n, nil
}

// sizeRuleFor maps a bound operation onto the shared algebra's size rule.
func sizeRuleFor(op Op) constraint.SizeRule {
	switch op {
	case OpFloorExcl:
		return constraint.RuleGt
	case OpCeilIncl:
		return constraint.RuleMax
	case OpCeilExcl:
		return constraint.RuleLt
	case OpExactSize:
		return constraint.RuleLen
	default:
		return constraint.RuleMin
	}
}

// sizeSlots returns the canvas floor and ceiling fields an axis writes, paired
// with the type-derived values they tighten against.
func sizeSlots(t Target, axis Axis) (**int, **int, *int, *int) {
	base := baseOf(t)

	switch axis {
	case AxisItems:
		return &t.Canvas.MinItems, &t.Canvas.MaxItems, base.MinItems, base.MaxItems
	case AxisProperties:
		return &t.Canvas.MinProperties, &t.Canvas.MaxProperties, base.MinProperties, base.MaxProperties
	default:
		return &t.Canvas.MinLength, &t.Canvas.MaxLength, base.MinLength, base.MaxLength
	}
}

// numericIsIntegral reports whether a numeric kind's zero is an integer, so the
// non-zero assertion forbids the literal the schema's own values compare as.
func numericIsIntegral(kind reflect.Kind) bool {
	return numkind.IsInteger(kind)
}
