package jsonschema

import (
	"errors"
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
)

var (
	// ErrConstraintConflict reports two value constraints an interpreter adds
	// through [Constraints] that can never both hold: a second const pinned to a
	// different value, or a second enum. It is the public conflict sentinel a tag
	// interpreter matches with [errors.Is]; the validate interpreter's own
	// conflict sentinel is derived from it, so a conflict from either layer is
	// recognizable through this one.
	ErrConstraintConflict = errors.New("conflicting value constraints")

	// ErrBoundNotRepresentable reports a numeric bound whose exact value a float64
	// cannot hold (an integer magnitude beyond 2^53), so storing it as the schema's
	// *float64 bound would silently round and change the constraint. It is the
	// single exact-representability policy shared by [Constraints.AddNumericBound]
	// and the jsonschema tag.
	ErrBoundNotRepresentable = constraint.ErrNotRepresentable
)

// BoundRule identifies which numeric bound a [Constraints.AddNumericBound]
// contribution expresses. The exclusive rules render as exclusiveMinimum and
// exclusiveMaximum; the inclusive ones as minimum and maximum.
type BoundRule uint8

const (
	// BoundMin is an inclusive floor (minimum), the target of a min or gte rule.
	BoundMin BoundRule = iota
	// BoundMax is an inclusive ceiling (maximum), the target of a max or lte rule.
	BoundMax
	// BoundGt is an exclusive floor (exclusiveMinimum), the target of a gt rule.
	BoundGt
	// BoundLt is an exclusive ceiling (exclusiveMaximum), the target of a lt rule.
	BoundLt
)

// LenRule identifies which length or count bound a [Constraints.AddLengthBound]
// or [Constraints.AddCountBound] contribution expresses. AddLengthBound targets
// the string length keywords (minLength/maxLength); AddCountBound targets the
// container count keywords (minItems/maxItems for a slice or array,
// minProperties/maxProperties for a map), chosen from the field's kind.
type LenRule uint8

const (
	// LenMin is an inclusive floor (min or gte).
	LenMin LenRule = iota
	// LenGt is an exclusive floor folded to the inclusive floor N+1 (gt).
	LenGt
	// LenMax is an inclusive ceiling (max or lte).
	LenMax
	// LenLt is an exclusive ceiling folded to the inclusive ceiling N-1 (lt).
	LenLt
	// LenExact pins both the floor and the ceiling to N (len).
	LenExact
)

// Constraints is the contribution surface a [TagInterpreter] uses to add value
// constraints to a field. It hides the shared constraint algebra behind a
// curated set of methods, so an interpreter contributes numeric bounds, string
// length, container counts, multipleOf, and const/enum/forbidden values without
// naming the internal model, and the single 2^53 exact-representability policy
// applies to its numeric bounds automatically.
//
// The bound methods are intersect-only: each parses its value, reads the field's
// effective endpoint (the value already authored on the canvas, or the
// type-derived one from the base), and writes the result back only when it
// tightens that endpoint. A bound that would widen or clear a kind bound is a
// no-op, so a bound never loosens a stronger one set by the field's type or an
// earlier rule, regardless of order. The value set (const/enum/forbidden) is
// composed on the field's canvas through the same shared helpers the jsonschema
// tag uses. The zero value is not usable; the generator hands each field-level
// hook a ready facade via [FieldContext.Constraints].
type Constraints struct {
	canvas *Schema
	base   *Schema
	kind   reflect.Kind
}

// baseSchema returns the type-derived base, or an empty schema when the facade
// was built without one, so the effective-endpoint reads never dereference nil.
func (c *Constraints) baseSchema() *Schema {
	if c.base != nil {
		return c.base
	}

	return &Schema{}
}

// AddNumericBound contributes one numeric bound parsed from value under the
// field's kind, applying the shared exact-representability policy (a bound beyond
// 2^53 is rejected as [ErrBoundNotRepresentable] rather than silently rounded).
// The bound intersects the field's effective bound: it is written to the canvas
// only when it tightens the value already in effect (the canvas value, or the
// type-derived one), so a weaker bound never loosens a stronger one and the kind
// bound survives a tag that cannot reach past it.
//
// The tighten check compares within one keyword slot (a gt against the effective
// exclusiveMinimum, a min against the effective minimum), so a bound looser than
// the opposite-inclusivity kind bound on the same side (a gt below an inclusive
// kind minimum) can still reach the canvas. That is harmless: reconcile takes the
// tighter of both slots when it resolves the axis, so the kind bound wins in the
// ordinary case, and under a value enum -- the one case the looser bound survives
// -- the enum already restricts the value to within the type range.
func (c *Constraints) AddNumericBound(rule BoundRule, value string) error {
	end, err := constraint.ParseNumericBound(value, c.kind)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the message; its sentinel is re-exported as ErrBoundNotRepresentable.
		return err
	}

	base := c.baseSchema()
	n := end.Val

	switch rule {
	case BoundMin:
		tightenBound(&c.canvas.Minimum, coalesceField(c.canvas.Minimum, base.Minimum), n, false)

	case BoundGt:
		eff := coalesceField(c.canvas.ExclusiveMinimum, base.ExclusiveMinimum)
		tightenBound(&c.canvas.ExclusiveMinimum, eff, n, false)

	case BoundMax:
		tightenBound(&c.canvas.Maximum, coalesceField(c.canvas.Maximum, base.Maximum), n, true)

	case BoundLt:
		eff := coalesceField(c.canvas.ExclusiveMaximum, base.ExclusiveMaximum)
		tightenBound(&c.canvas.ExclusiveMaximum, eff, n, true)
	}

	return nil
}

// AddLengthBound contributes one string-length bound parsed from value: the
// exclusive-to-inclusive fold, the non-negative clamp, and the
// unsatisfiable-range construction (a floor of one against a ceiling of zero)
// all happen in the shared size-bound algebra. Each resulting inclusive floor or
// ceiling intersects the canvas's current minLength/maxLength, writing only when
// it tightens the effective bound.
func (c *Constraints) AddLengthBound(rule LenRule, value string) error {
	base := c.baseSchema()

	return c.addSizeBound(rule, value,
		&c.canvas.MinLength, &c.canvas.MaxLength, base.MinLength, base.MaxLength)
}

// AddCountBound contributes one container-count bound parsed from value, folding
// and clamping through the same size-bound algebra as [Constraints.AddLengthBound]
// but targeting the count keywords the field's kind selects: minItems/maxItems
// for a slice or array, minProperties/maxProperties for a map. The exclusive
// fold happens here (there is no exclusive-items keyword to defer it to), and
// each inclusive floor or ceiling intersects the canvas's current count.
func (c *Constraints) AddCountBound(rule LenRule, value string) error {
	base := c.baseSchema()

	if c.kind == reflect.Map {
		return c.addSizeBound(rule, value,
			&c.canvas.MinProperties, &c.canvas.MaxProperties, base.MinProperties, base.MaxProperties)
	}

	return c.addSizeBound(rule, value,
		&c.canvas.MinItems, &c.canvas.MaxItems, base.MinItems, base.MaxItems)
}

// addSizeBound parses a length/count rule through the shared size algebra and
// intersects each resulting inclusive floor/ceiling against the canvas's current
// bound, reading the effective value (canvas or base) so a weaker bound never
// loosens a stronger one.
func (c *Constraints) addSizeBound(
	rule LenRule, value string, minField, maxField **int, baseMin, baseMax *int,
) error {
	bounds, err := constraint.ParseSizeBound(value, sizeRule(rule), constraint.Intersect, constraint.Authored)
	if err != nil {
		//nolint:wrapcheck // The shared algebra owns the message dialect.
		return err
	}

	for _, b := range bounds {
		n := int(b.End.Rat.Num().Int64())
		if b.Lower {
			raiseFloor(minField, coalesceField(*minField, baseMin), n)
		} else {
			lowerCeiling(maxField, coalesceField(*maxField, baseMax), n)
		}
	}

	return nil
}

// SetMultipleOf records a multipleOf value on the field, reporting an error for a
// non-positive value, which JSON Schema forbids.
func (c *Constraints) SetMultipleOf(value float64) error {
	if value <= 0 {
		return fmt.Errorf("multipleOf must be positive, got %v", value)
	}

	v := value
	c.canvas.MultipleOf = &v

	return nil
}

// Const returns the value the field's const pins and whether one is set, so an
// interpreter can run its own conflict check with its own wording before pinning
// a value of its own.
func (c *Constraints) Const() (any, bool) {
	if c.canvas.Const == nil {
		return nil, false
	}

	return *c.canvas.Const, true
}

// Enum returns the field's enum members and whether an enum is set, the analog of
// [Constraints.Const] for the enumerated-value case.
func (c *Constraints) Enum() ([]any, bool) {
	if c.canvas.Enum == nil {
		return nil, false
	}

	return c.canvas.Enum, true
}

// SetConst pins the field's const, reporting [ErrConstraintConflict] rather than
// overwriting a const a previous rule already pinned to a different
// (numeric-aware) value. An interpreter that needs its own conflict wording
// checks [Constraints.Const] first; this call is the shared backstop.
func (c *Constraints) SetConst(value any) error {
	if c.canvas.Const != nil && !constraint.ValuesEqual(*c.canvas.Const, value) {
		return fmt.Errorf("%w: a different const is already set", ErrConstraintConflict)
	}

	v := value
	c.canvas.Const = &v

	return nil
}

// SetEnum sets the field's enum, reporting [ErrConstraintConflict] when an enum
// is already set, so two enumerations cannot silently overwrite one another. An
// interpreter that needs its own wording checks [Constraints.Enum] first.
func (c *Constraints) SetEnum(values []any) error {
	if c.canvas.Enum != nil {
		return fmt.Errorf("%w: an enum is already set", ErrConstraintConflict)
	}

	c.canvas.Enum = values

	return nil
}

// Forbid records that the field must not equal value, composing with any value
// already forbidden through the shared not.const -> not.enum -> allOf escalation.
func (c *Constraints) Forbid(value any) {
	var vs constraint.ValueSet

	vs.SeedNot(c.canvas.Not)
	vs.Forbid(value)
	vs.WriteForbidden(c.canvas)
}

// ForbidSchema forbids a whole subschema (a length range, say), taking the free
// not slot or moving an existing not under allOf so both apply conjunctively.
func (c *Constraints) ForbidSchema(forbidden *Schema) {
	var vs constraint.ValueSet

	vs.SeedNot(c.canvas.Not)
	vs.ForbidSchema(forbidden)
	vs.WriteForbidden(c.canvas)
}

// tightenBound writes n into *field when it tightens the effective bound: a
// higher value for a floor, a lower value for a ceiling. A widening or equal
// value is a no-op, so the bound methods intersect and never loosen.
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

// sizeRule maps a facade [LenRule] to the shared algebra's [constraint.SizeRule].
func sizeRule(rule LenRule) constraint.SizeRule {
	switch rule {
	case LenGt:
		return constraint.RuleGt
	case LenMax:
		return constraint.RuleMax
	case LenLt:
		return constraint.RuleLt
	case LenExact:
		return constraint.RuleLen
	default:
		return constraint.RuleMin
	}
}
