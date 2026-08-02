package tagmodel

import (
	"cmp"
	"fmt"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

// baseOf returns the target's type-derived schema, or an empty one when it has
// none, so the effective-value reads never dereference nil.
func baseOf(t Target) *jsonschema.Schema {
	if t.Base != nil {
		return t.Base
	}

	return &jsonschema.Schema{}
}

// tighten writes n into *field unless it would loosen the effective bound: a
// floor may only rise and a ceiling may only fall, so bounds intersect
// regardless of the order the rules appeared in. The effective bound is the
// value the target authored, or the type-derived one when it authored none, so
// a tag bound never weakens the type's own.
//
// A bound equal to the effective one is written rather than skipped, because
// writing it is what records that the value was authored. Provenance is not
// bookkeeping here: an enum drops the kind-derived bounds and keeps the authored
// ones, so minimum=0 on a uint8 must survive an enum even though the kind floor
// is already 0. Skipping the equal write would misfile it as kind-derived and
// drop it.
func tighten[T cmp.Ordered](field **T, base *T, n T, ceiling bool) {
	eff := *field
	if eff == nil {
		eff = base
	}

	if eff == nil || (ceiling && n <= *eff) || (!ceiling && n >= *eff) {
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
