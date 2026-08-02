package jsonschema

import (
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

var (
	// ErrConstraintConflict reports two value constraints an interpreter adds
	// through [Constraints] that can never both hold: a second const pinned to a
	// different value, or a second enum. It is the public conflict sentinel a tag
	// interpreter matches with [errors.Is]; the validate interpreter's own
	// conflict sentinel is derived from it, so a conflict from either layer is
	// recognizable through this one.
	ErrConstraintConflict = tagmodel.ErrConflict

	// ErrBoundNotRepresentable reports a numeric bound the schema's *float64
	// cannot ship exactly: an integer the float64's shortest-decimal
	// interpretation (the value the schema renders and the validator enforces)
	// does not reproduce, so storing it would silently change the constraint.
	// It is the single exact-representability policy shared by
	// [Constraints.AddNumericBound] and the jsonschema tag.
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
	target tagmodel.Target
	kind   reflect.Kind
}

// Op and Axis are the shared constraint model's vocabulary, re-exported so an
// interpreter names an operation and a keyword family rather than translating
// its dialect into a second set of names.
type (
	// Op is one constraint operation: an endpoint, a size, a pinned or forbidden
	// value, an enumeration, and so on.
	Op = tagmodel.Op
	// Axis is the keyword family a bound targets.
	Axis = tagmodel.Axis
)

// The operations an interpreter contributes. They are the shared model's, so a
// rule means the same thing here as it does in the jsonschema tag.
const (
	OpFloorIncl        = tagmodel.OpFloorIncl
	OpFloorExcl        = tagmodel.OpFloorExcl
	OpCeilIncl         = tagmodel.OpCeilIncl
	OpCeilExcl         = tagmodel.OpCeilExcl
	OpExactSize        = tagmodel.OpExactSize
	OpForbidSize       = tagmodel.OpForbidSize
	OpEqual            = tagmodel.OpEqual
	OpNotEqual         = tagmodel.OpNotEqual
	OpOneOf            = tagmodel.OpOneOf
	OpNonZero          = tagmodel.OpNonZero
	OpUnique           = tagmodel.OpUnique
	OpMultipleOf       = tagmodel.OpMultipleOf
	OpFormat           = tagmodel.OpFormat
	OpPattern          = tagmodel.OpPattern
	OpContentEncoding  = tagmodel.OpContentEncoding
	OpContentMediaType = tagmodel.OpContentMediaType
)

// The keyword families a bound can target. AxisAuto lets the field's shape
// choose, which is what a rule-shaped tag (min, max) means; naming a family
// pins it, so a shape with no such keyword is an error rather than an inert
// keyword nothing enforces.
const (
	AxisAuto       = tagmodel.AxisAuto
	AxisNumeric    = tagmodel.AxisNumeric
	AxisLength     = tagmodel.AxisLength
	AxisItems      = tagmodel.AxisItems
	AxisProperties = tagmodel.AxisProperties
)

// interpreterPolicy is the dialect policy every tag interpreter runs under.
//
// A tag interpreter reads a rule-shaped dialect: min=5 describes a predicate on
// the Go value, so the literal parses at the field's own kind (gte=1.5 on an int
// is an error, as go-playground has it) and a negative size folds to the
// unsatisfiable range rather than being rejected outright. The jsonschema tag,
// which names JSON Schema keywords directly, runs under the opposite settings;
// both are the same implementation with different parameters.
func interpreterPolicy(kind reflect.Kind) tagmodel.Policy {
	return tagmodel.Policy{BoundKind: kind, Sizes: tagmodel.SizeFold}
}

// Apply contributes one constraint in the shared model's vocabulary: the
// operation, the keyword family it targets (or [AxisAuto] to let the field's
// shape choose), and the rule's parameters.
//
// It is the whole contribution surface. Which operations the field's shape can
// carry, whether a scalar parameter compares against the Go value or against
// the text a json:",string" field serializes, and whether a rule retargets onto
// element schemas are all decided by the shared model from the field's shape, so
// an interpreter neither repeats those decisions nor can get them wrong. A rule
// the shape cannot carry reports an error naming the reason rather than emitting
// a keyword nothing enforces.
//
// Bounds intersect: each is written only when it tightens the value already in
// effect (the canvas value, or the type-derived one), so a weaker rule never
// loosens a stronger one and repeated rules compose order-independently. A
// second const or enum that disagrees with one already in force is
// [ErrConstraintConflict] rather than a silent overwrite.
func (c *Constraints) Apply(op Op, axis Axis, params ...string) error {
	//nolint:wrapcheck // The model owns the message; the interpreter adds its own dialect prefix.
	return tagmodel.Apply(
		c.target,
		tagmodel.Rule{Op: op, Axis: axis, Params: tagmodel.ParamsOf(params...)},
		interpreterPolicy(c.kind),
	)
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
// field's kind, applying the shared exact-representability policy (a bound the
// shipped float64 would not reproduce exactly is rejected as
// [ErrBoundNotRepresentable] rather than silently rounded).
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
// each inclusive floor or ceiling intersects the canvas's current count. A field
// of any other kind has no count keyword to target, so the call reports an error
// rather than stamping minItems onto a non-container schema.
func (c *Constraints) AddCountBound(rule LenRule, value string) error {
	base := c.baseSchema()

	switch c.kind {
	case reflect.Map:
		return c.addSizeBound(rule, value,
			&c.canvas.MinProperties, &c.canvas.MaxProperties, base.MinProperties, base.MaxProperties)

	case reflect.Slice, reflect.Array:
		return c.addSizeBound(rule, value,
			&c.canvas.MinItems, &c.canvas.MaxItems, base.MinItems, base.MaxItems)

	default:
		return fmt.Errorf("count bound: field kind %s is not a slice, array, or map", c.kind)
	}
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
// overwriting a const already pinned to a different (numeric-aware) value --
// whether a previous rule pinned it on the canvas or the field's type supplies
// it on an inline base, where reconcile overlays the canvas const and a
// disagreeing type-pinned value would otherwise be silently overwritten. For a
// $defs-extracted type the base is the field's provisional {$ref} payload, so a
// const the referenced definition pins is not visible here and no conflict is
// reported; nothing is overwritten either -- the canvas const rides beside the
// $ref and both apply conjunctively, so disagreeing values compose to a
// faithfully unsatisfiable schema rather than aborting generation. An
// interpreter that needs its own conflict wording checks [Constraints.Const]
// first; this call is the shared backstop for the overlay path.
func (c *Constraints) SetConst(value any) error {
	if c.canvas.Const != nil && !constraint.ValuesEqual(*c.canvas.Const, value) {
		return fmt.Errorf("%w: a different const is already set", ErrConstraintConflict)
	}

	if base := c.baseSchema(); base.Const != nil && !constraint.ValuesEqual(*base.Const, value) {
		return fmt.Errorf("%w: the type already pins a different const", ErrConstraintConflict)
	}

	v := value
	c.canvas.Const = &v

	return nil
}

// SetEnum sets the field's enum, reporting [ErrConstraintConflict] when an enum
// is already set -- on the canvas by a previous rule, or on an inline
// type-derived base, which reconcile would silently overwrite with the canvas
// value -- so two enumerations cannot shadow one another. For a $defs-extracted
// type the definition's enum is not visible on the base (the provisional {$ref}
// payload) and no conflict is reported; the canvas enum rides beside the $ref
// and the conjunction intersects the two sets, which only tightens. An
// interpreter that needs its own wording checks [Constraints.Enum] first.
func (c *Constraints) SetEnum(values []any) error {
	if c.canvas.Enum != nil {
		return fmt.Errorf("%w: an enum is already set", ErrConstraintConflict)
	}

	if c.baseSchema().Enum != nil {
		return fmt.Errorf("%w: the type already sets an enum", ErrConstraintConflict)
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
