package jsonschema

import (
	"fmt"

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
	// It is the single exact-representability policy every dialect's numeric
	// bounds parse through.
	ErrBoundNotRepresentable = constraint.ErrNotRepresentable
)

// Constraints is the contribution surface a [TagInterpreter] uses to add value
// constraints to a field. Its vocabulary is the shared constraint model's: an
// interpreter names an [Op] and, for a bound, the [Axis] it targets, and the
// model decides from the field's shape what that means. There is no second set
// of rule names to translate into, which is what keeps an interpreter from
// re-deriving a policy the model already owns.
//
// [Constraints.Apply] is the whole surface for constraints; the named methods
// below are conveniences for the value set, where an interpreter usually wants
// to run its own conflict check with its own wording first. Bounds are
// intersect-only, const and enum report [ErrConstraintConflict] rather than
// overwriting, and a rule the field's shape cannot carry is an error rather
// than an inert keyword.
//
// The zero value is not usable; the generator hands each field-level hook a
// ready facade via [FieldContext.Constraints].
type Constraints struct {
	target tagmodel.Target
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
func (c *Constraints) policy() tagmodel.Policy {
	return tagmodel.Policy{
		BoundKind: c.target.Shape.Kind,
		Sizes:     tagmodel.SizeFold,
		Keywords:  tagmodel.KeywordFirstWins,
	}
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
		c.policy(),
	)
}

// SetMultipleOf records a multipleOf value on the field, reporting an error for a
// non-positive value, which JSON Schema forbids.
func (c *Constraints) SetMultipleOf(value float64) error {
	if value <= 0 {
		return fmt.Errorf("multipleOf must be positive, got %v", value)
	}

	v := value
	c.target.Canvas.MultipleOf = &v

	return nil
}

// Const returns the value the field's const pins and whether one is set, so an
// interpreter can run its own conflict check with its own wording before pinning
// a value of its own.
func (c *Constraints) Const() (any, bool) {
	if c.target.Canvas.Const == nil {
		return nil, false
	}

	return *c.target.Canvas.Const, true
}

// Enum returns the field's enum members and whether an enum is set, the analog of
// [Constraints.Const] for the enumerated-value case.
func (c *Constraints) Enum() ([]any, bool) {
	if c.target.Canvas.Enum == nil {
		return nil, false
	}

	return c.target.Canvas.Enum, true
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
	//nolint:wrapcheck // The model owns the conflict sentinel and its wording.
	return tagmodel.SetConst(c.target, value)
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
	//nolint:wrapcheck // The model owns the conflict sentinel and its wording.
	return tagmodel.SetEnum(c.target, values)
}

// Forbid records that the field must not equal value, composing with any value
// already forbidden through the shared not.const -> not.enum -> allOf escalation.
func (c *Constraints) Forbid(value any) {
	tagmodel.Forbid(c.target.Canvas, value)
}

// ForbidSchema forbids a whole subschema (a length range, say), taking the free
// not slot or moving an existing not under allOf so both apply conjunctively.
func (c *Constraints) ForbidSchema(forbidden *Schema) {
	tagmodel.ForbidSchema(c.target.Canvas, forbidden)
}
