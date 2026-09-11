package jsonschema

import (
	"errors"
	"fmt"
	"reflect"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
)

var (
	// ErrNilCanvas reports a write through [Constraints] on a context with no
	// [FieldContext.Canvas] to land on: the zero [Constraints], or a facade a
	// caller-built context handed out before populating its canvas. A context
	// the generator builds always carries one.
	ErrNilCanvas = errors.New("constraints: no canvas to write to")

	// ErrInvalidRule reports a rule [Constraints.Apply] cannot hand to the
	// model at all: an [Op] or [Axis] outside the table, a parameter count the
	// operation does not take, or a uniqueness literal that is not a boolean.
	// It wraps the model's own reason. A rule the field's shape cannot carry
	// is a different refusal, [ErrConstraintUnsupported].
	ErrInvalidRule = errors.New("constraints: invalid rule")

	// ErrConstraintUnsupported reports a rule the field's shape cannot carry:
	// a length on a number, a divisor on a string, any rule on an opaque
	// value such as a nil Type or an unresolved reference. The error names
	// the reason after the sentinel.
	ErrConstraintUnsupported = tagmodel.ErrUnsupported

	// ErrConstraintConflict reports two value constraints an interpreter adds
	// through [Constraints] that can never both hold: a second const pinned to a
	// different value, or an enum sharing no value with one in force. It is
	// the public conflict sentinel a tag
	// interpreter matches with [errors.Is]; the validate interpreter's own
	// conflict sentinel is derived from it, so a conflict from either layer is
	// recognizable through this one.
	ErrConstraintConflict = tagmodel.ErrConflict

	// ErrMarshalPanic reports a panic recovered from a user MarshalText or
	// MarshalJSON method that the coerced tag round-trip ran on the value a
	// tag literal names (a const, default, enum, or validate scalar on a
	// field whose type marshals itself as text), the one place a user
	// marshal method runs during generation. The panic is recovered so
	// Generate returns this error instead of crashing.
	ErrMarshalPanic = tagmodel.ErrMarshalPanic

	// ErrBoundNotRepresentable reports a numeric literal no value of its
	// field renders as: a bound the schema's *float64 cannot ship exactly (an
	// integer the float64's shortest-decimal interpretation, the value the
	// schema renders and the validator enforces, does not reproduce), or a
	// bound or scalar past the width of the field's own kind, such as 200 on
	// an int8 or 1e300 on a float32. Storing either would silently change
	// the constraint. It is the single exact-representability policy every
	// dialect's numeric literals parse through.
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
// overwriting, and a rule the field's shape cannot carry is
// [ErrConstraintUnsupported] rather than an inert keyword.
//
// The facade is the one boundary between a hook and the constraint model,
// and it checks its inputs there so nothing past it does. Every write needs
// a [FieldContext.Canvas] to land on and returns [ErrNilCanvas] without one,
// which is what the zero Constraints and a facade over a caller-built
// context with no canvas both are. Every write checks its rule and returns
// [ErrInvalidRule] for one the model has no row for. Reads never fail: a
// missing canvas reads as nothing set. The generator hands each field-level
// hook a ready facade via [FieldContext.Constraints].
type Constraints struct {
	target tagmodel.Target
}

// Op, Axis, Shape, and Form are the shared constraint model's vocabulary,
// re-exported so an interpreter names an operation, a keyword family, and the
// thing it is constraining rather than translating its dialect into a second
// set of names.
type (
	// Op is one constraint operation: an endpoint, a size, a pinned or forbidden
	// value, an enumeration, and so on.
	Op = tagmodel.Op
	// Axis is the keyword family a bound targets.
	Axis = tagmodel.Axis
	// Shape is what a constraint is written against: the field's declared Go
	// type, that type with its pointer chain followed, the kind a scalar literal
	// parses at, the [Form] its instance takes, and whether the occurrence admits
	// null. [ShapeOf] classifies one and [FieldContext.ConstraintsFor] takes one.
	Shape = tagmodel.Shape
	// Form is the JSON shape an instance actually takes, which is what the model
	// dispatches on. It is deliberately not the Go kind: a field encoding itself
	// as a string, through json:",string" or its own MarshalText, is a coerced
	// form rather than a number every operation has to remember to special-case.
	Form = tagmodel.Form
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

// The forms an instance can take. They are the model's own dispatch column, so
// an interpreter that branches on one branches on the same classification the
// rule is then applied under.
const (
	FormString         = tagmodel.FormString
	FormNumber         = tagmodel.FormNumber
	FormBool           = tagmodel.FormBool
	FormArray          = tagmodel.FormArray
	FormObject         = tagmodel.FormObject
	FormCoercedNumber  = tagmodel.FormCoercedNumber
	FormCoercedBool    = tagmodel.FormCoercedBool
	FormCoercedString  = tagmodel.FormCoercedString
	FormTextString     = tagmodel.FormTextString
	FormByteString     = tagmodel.FormByteString
	FormRawBytes       = tagmodel.FormRawBytes
	FormUnresolvedRef  = tagmodel.FormUnresolvedRef
	FormDeclaredObject = tagmodel.FormDeclaredObject
	FormOpaque         = tagmodel.FormOpaque
)

// ShapeOf classifies a field or element from its Go type and the type-derived
// base schema the generator built for it, which is the classification
// [FieldContext.Constraints] performs internally. It is exported for an
// interpreter that dispatches on the shape itself: classify once, branch on the
// result, and hand the same value to [FieldContext.ConstraintsFor] rather than
// paying to classify a second time.
//
// The base decides the form wherever the Go type alone understates what the
// instance is, as it does for a field whose type serializes itself as a string;
// pass [FieldContext.Base]. A nil base classifies from the Go type alone, and
// a nil type classifies as [FormOpaque], the form every rule reports on. Two
// inputs the type and base cannot express reach only [FieldContext.Shape]:
// a json:",string" flag on an [encoding/json.Number] field (string Go kind,
// numeric instance), and the definition a bare $ref base names, which this
// function cannot read and so classifies as [FormUnresolvedRef], the other
// form every rule reports on. Prefer the context's method when one is
// available.
func ShapeOf(t reflect.Type, base *Schema) Shape {
	return tagmodel.ShapeOf(t, base)
}

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
//
// A rule the model has no row for (an operation or axis outside the table, a
// parameter count the operation does not take, a uniqueness literal that is
// not a boolean) is [ErrInvalidRule], and a facade with no canvas returns
// [ErrNilCanvas]. Neither leaves a trace on the field.
func (c *Constraints) Apply(op Op, axis Axis, params ...string) error {
	err := c.ready()
	if err != nil {
		return err
	}

	rule := tagmodel.Rule{Op: op, Axis: axis, Params: tagmodel.ParamsOf(params...)}

	err = rule.Validate()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}

	//nolint:wrapcheck // The model owns the message; the interpreter adds its own dialect prefix.
	return tagmodel.Apply(c.target, rule, c.policy())
}

// ready reports whether the facade has a canvas to write to, checked on
// every write so the zero Constraints and a facade over a canvas-less
// context refuse the same way.
func (c *Constraints) ready() error {
	if c == nil || c.target.Canvas == nil {
		return ErrNilCanvas
	}

	return nil
}

// SetMultipleOf records a multipleOf value on the field, reporting an error for
// a non-positive value, which JSON Schema forbids, and for a field whose shape
// has no number to divide. A divisor already in force (from the field's type,
// the jsonschema tag, or an earlier rule) intersects with it to their least
// common multiple, so an inferred divisor never loosens a stated one. A
// least common multiple the schema's float64 cannot spell exactly is
// [ErrBoundNotRepresentable], as any bound the float64 would not reproduce
// is, since neither divisor alone enforces both. It is the named form of
// [Constraints.Apply] with [OpMultipleOf].
func (c *Constraints) SetMultipleOf(value float64) error {
	err := c.ready()
	if err != nil {
		return err
	}

	//nolint:wrapcheck // The model owns the rule and its wording.
	return tagmodel.SetMultipleOf(c.target, value, c.policy())
}

// Const returns the value the field's const pins and whether one is set, so an
// interpreter can run its own conflict check with its own wording before pinning
// a value of its own.
func (c *Constraints) Const() (any, bool) {
	if c == nil || c.target.Canvas == nil || c.target.Canvas.Const == nil {
		return nil, false
	}

	return *c.target.Canvas.Const, true
}

// Enum returns the field's enum members and whether an enum is set, the analog of
// [Constraints.Const] for the enumerated-value case.
func (c *Constraints) Enum() ([]any, bool) {
	if c == nil || c.target.Canvas == nil || c.target.Canvas.Enum == nil {
		return nil, false
	}

	return c.target.Canvas.Enum, true
}

// SetConst pins the field's const, reporting [ErrConstraintConflict] rather than
// overwriting a const already pinned to a different (numeric-aware) value --
// whether a previous rule pinned it on the canvas or the field's type supplies
// it on an inline base, where reconcile overlays the canvas const and a
// disagreeing type-pinned value would otherwise be silently overwritten. A
// value outside an enum in force, or outside a numeric bound the type
// declares, is the same conflict, since generation drops the type's bounds
// under a const and the value must satisfy them for that to be safe. For a
// $defs-extracted type the check reads the referenced definition, so the
// answer is the one the inline schema gives; the canvas const then rides
// beside the $ref. An interpreter that needs its own conflict wording checks
// [Constraints.Const] first; this call is the shared backstop for the
// overlay path.
func (c *Constraints) SetConst(value any) error {
	err := c.ready()
	if err != nil {
		return err
	}

	//nolint:wrapcheck // The model owns the conflict sentinel and its wording.
	return tagmodel.SetConst(c.target, value)
}

// SetEnum sets the field's enum, intersecting with an enum already in force
// -- on the canvas from a previous rule, or on the type-derived base, which
// reconcile would otherwise overwrite with the canvas value -- so two
// enumerations compose conjunctively rather than one shadowing the other. A
// numeric bound the type declares narrows the enum the same way, to the
// members the bound admits, since generation drops the type's bounds under
// an enum and every member must satisfy them for that to be safe. An empty
// intersection is [ErrConstraintConflict]. For a $defs-extracted type the
// definition's enum and bounds are read through the reference, so the
// narrowed set is the one the inline schema gives, and it rides beside the
// $ref. An empty values is [ErrInvalidRule],
// as it is through [Constraints.Apply] with [OpOneOf]: it would admit nothing,
// and the JSON form omits an empty enum, so the generated schema could not
// express it. An interpreter that needs its own wording checks
// [Constraints.Enum] first.
func (c *Constraints) SetEnum(values []any) error {
	err := c.ready()
	if err != nil {
		return err
	}

	if len(values) == 0 {
		return fmt.Errorf("%w: an enumeration needs at least one value", ErrInvalidRule)
	}

	//nolint:wrapcheck // The model owns the conflict sentinel and its wording.
	return tagmodel.SetEnum(c.target, values)
}

// Forbid records that the field must not equal value, composing with any value
// already forbidden through the shared not.const -> not.enum -> allOf
// escalation. It fails only for a facade with no canvas ([ErrNilCanvas]).
func (c *Constraints) Forbid(value any) error {
	err := c.ready()
	if err != nil {
		return err
	}

	tagmodel.Forbid(c.target.Canvas, value)

	return nil
}

// ForbidSchema forbids a whole subschema (a length range, say). It lands as a
// not branch under allOf on the value side, so a nullable field's null is never
// judged by it and an existing not keeps applying beside it; [Constraints.Forbid]
// is the way to forbid the null itself. A nil subschema is [ErrInvalidRule],
// and a facade with no canvas returns [ErrNilCanvas].
func (c *Constraints) ForbidSchema(forbidden *Schema) error {
	err := c.ready()
	if err != nil {
		return err
	}

	if forbidden == nil {
		return fmt.Errorf("%w: no subschema to forbid", ErrInvalidRule)
	}

	tagmodel.ForbidSchema(c.target.Canvas, forbidden)

	return nil
}
