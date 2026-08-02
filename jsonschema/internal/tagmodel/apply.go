package tagmodel

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// Apply executes one rule against one target. It is the single entry point:
// resolve the bound axis if the operation needs one, look up the (Op, Form)
// cell, and run it. No path reaches an applier without a Form, so no applier
// asks what shape it is looking at.
func Apply(t Target, r Rule, pol Policy) error {
	if r.Op == OpUnset || r.Op >= opCount {
		return fmt.Errorf("tagmodel: %s", r.Op)
	}

	form := t.Shape.Form
	if form == FormUnset || form >= formCount {
		return fmt.Errorf("tagmodel: unclassified shape for %s", t.Shape.Type)
	}

	c := matrix[r.Op][form]
	switch c.status {
	case StatusApply:
		return c.apply(t, r, pol)
	case StatusIgnore:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnsupported, c.why)
	}
}

// resolveAxis settles which keyword family a bound targets, then checks the
// form has that family at all. An AxisAuto rule takes the form's natural axis,
// which is what a dialect naming a rule rather than a keyword means. A pinned
// axis the form does not carry is an error rather than an inert keyword, so
// minItems=3 on a string surfaces instead of emitting dead structure.
func resolveAxis(form Form, axis Axis) (Axis, error) {
	if axis == AxisAuto {
		axis = formAxis[form]
	}

	if axis == AxisAuto || !formCarriesAxis[form][axis] {
		return AxisAuto, fmt.Errorf("%w: %s", ErrUnsupported, axisRejection(form, axis))
	}

	return axis, nil
}

// retargetToElements applies a rule to each element target, re-entering Apply so
// the element's own Shape selects the cell. It is the single path a
// sequence-wide rule and a dive-scoped rule both take, which is why an element's
// coercion can no longer be invisible to one of them; nesting falls out, since
// an element whose own form is an array reaches this same cell again.
func retargetToElements(t Target, r Rule, pol Policy) error {
	elems := t.Elements()
	if len(elems) == 0 {
		return fmt.Errorf("%w: %s has no item schema to constrain", ErrUnsupported, t.Shape.Form)
	}

	for _, elem := range elems {
		if t.recursesIntoItself(elem) {
			return fmt.Errorf(
				"%w: recursive element type %s cannot carry an element constraint",
				ErrUnsupported, t.Shape.Type)
		}

		err := Apply(elem, r, pol)
		if err != nil {
			return err
		}
	}

	return nil
}

// applyBound settles which keyword family the bound targets, then routes it
// there. Each contribution intersects the effective endpoint -- the value
// already authored on the canvas, or the type-derived one -- and lands only when
// it tightens, so a weaker rule never loosens a stronger one and the result does
// not depend on the order the rules appeared in.
//
// The axis is resolved here rather than before the cell lookup because not every
// operation that a dialect spells as a bound is one: an exact size on a number
// pins the value, and its cell is the pinning applier, which has no axis to
// resolve and must not be rejected for lacking one.
func applyBound(t Target, r Rule, pol Policy) error {
	axis, err := resolveAxis(t.Shape.Form, r.Axis)
	if err != nil {
		return err
	}

	r.Axis = axis

	if axis == AxisNumeric {
		return applyNumericBound(t, r, pol)
	}

	return applySizeBound(t, r, pol)
}

// applyNumericBound parses under the dialect's literal domain
// ([Policy.BoundKind]) and the shared exact-representability policy, then
// tightens the endpoint its operation names.
func applyNumericBound(t Target, r Rule, pol Policy) error {
	end, err := constraint.ParseNumericBound(r.Params.One(), pol.BoundKind)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the message and the sentinel.
		return err
	}

	n := end.Val
	base := baseOf(t)

	switch r.Op {
	case OpFloorIncl:
		tightenBound(&t.Canvas.Minimum, coalesce(t.Canvas.Minimum, base.Minimum), n, false)
	case OpFloorExcl:
		tightenBound(&t.Canvas.ExclusiveMinimum, coalesce(t.Canvas.ExclusiveMinimum, base.ExclusiveMinimum), n, false)
	case OpCeilIncl:
		tightenBound(&t.Canvas.Maximum, coalesce(t.Canvas.Maximum, base.Maximum), n, true)
	case OpCeilExcl:
		tightenBound(&t.Canvas.ExclusiveMaximum, coalesce(t.Canvas.ExclusiveMaximum, base.ExclusiveMaximum), n, true)
	default:
		// Only the four endpoint operations resolve to the numeric axis; an
		// exact size on a number pins the value and never reaches here.
		return fmt.Errorf("%w: %s has no numeric endpoint", ErrUnsupported, r.Op)
	}

	return nil
}

// applySizeBound folds a length or count rule through the shared size algebra --
// the exclusive-to-inclusive fold, the non-negative clamp, and the
// unsatisfiable floor-one/ceiling-zero construction -- and intersects each
// resulting endpoint against the effective one. A strict-size dialect rejects a
// negative literal before the fold, since it is writing a keyword whose value
// domain excludes one.
func applySizeBound(t Target, r Rule, pol Policy) error {
	value := r.Params.One()

	if pol.Sizes == SizeStrict {
		n, err := parseSizeLiteral(value)
		if err != nil {
			return err
		}

		if n < 0 {
			return fmt.Errorf("must be non-negative, got %d", n)
		}
	}

	bounds, err := constraint.ParseSizeBound(value, sizeRuleFor(r.Op), constraint.Intersect, constraint.Authored)
	if err != nil {
		//nolint:wrapcheck // The shared algebra owns the message dialect.
		return err
	}

	minField, maxField, baseMin, baseMax := sizeSlots(t, r.Axis)

	for _, b := range bounds {
		n := int(b.End.Rat.Num().Int64())
		if b.Lower {
			raiseFloor(minField, coalesce(*minField, baseMin), n)
		} else {
			lowerCeiling(maxField, coalesce(*maxField, baseMax), n)
		}
	}

	return nil
}

// applyForbidSize forbids one length or entry count, expressed as a not
// subschema pinning that size. The subschema is gated on the instance type,
// because a size keyword is inert against a non-array (or non-object) instance:
// without the gate it would vacuously validate against the null a nilable
// field's schema deliberately admits, and the outer not would reject it.
func applyForbidSize(t Target, r Rule, _ Policy) error {
	n, err := parseSizeLiteral(r.Params.One())
	if err != nil {
		return err
	}

	// No instance has a negative size, so forbidding one excludes nothing.
	if n < 0 {
		return nil
	}

	forbidden := &jsonschema.Schema{}

	// The size a map has is its entry count; every other sized shape's is its
	// item count. Reading it from the form keeps this independent of whether the
	// dialect named an axis at all.
	if t.Shape.Form == FormObject {
		forbidden.Type = typename.Object
		forbidden.MinProperties = &n
		forbidden.MaxProperties = &n
	} else {
		forbidden.Type = typename.Array
		forbidden.MinItems = &n
		forbidden.MaxItems = &n
	}

	forbidSchema(t.Canvas, forbidden)

	return nil
}

// applyEqual pins the target's const, reporting a conflict rather than
// overwriting a value another rule or the type itself already pinned. Two pins
// that disagree can never both hold, so reporting keeps the result independent
// of the order the rules appeared in.
func applyEqual(t Target, r Rule, pol Policy) error {
	v, err := t.Shape.ParseScalar(r.Params.One(), pol)
	if err != nil {
		return err
	}

	return SetConst(t, v)
}

// applyNotEqual forbids one value, composing with anything already forbidden
// through the shared not.const -> not.enum -> allOf escalation: several rules
// can forbid values on one target, and the numeric-aware dedup treats the same
// number arriving with different dynamic types as one value.
func applyNotEqual(t Target, r Rule, pol Policy) error {
	v, err := t.Shape.ParseScalar(r.Params.One(), pol)
	if err != nil {
		return err
	}

	Forbid(t.Canvas, v)

	return nil
}

// applyOneOf enumerates the allowed values, reporting a conflict rather than
// shadowing an enumeration already in force. Two enumerations of the same value
// fully describe the allowed set, so neither can silently win.
func applyOneOf(t Target, r Rule, pol Policy) error {
	vals, err := t.Shape.ParseScalars(r.Params.Values(), pol)
	if err != nil {
		return err
	}

	return SetEnum(t, vals)
}

// applyUnique sets uniqueItems when the bound boolean asks for it. A false
// literal leaves the keyword unset rather than emitting the vacuous default.
func applyUnique(t Target, r Rule, _ Policy) error {
	on, err := parseBoolLiteral(r.Params.One())
	if err != nil {
		return err
	}

	if on {
		t.Canvas.UniqueItems = true
	}

	return nil
}

// applyMultipleOf records a divisor, rejecting the non-positive value JSON
// Schema forbids.
func applyMultipleOf(t Target, r Rule, _ Policy) error {
	// A divisor is a keyword value, not a field value, so it takes the
	// keyword-shaped literal domain regardless of the target's kind.
	end, err := constraint.ParseNumericBound(r.Params.One(), reflect.Invalid)
	if err != nil {
		//nolint:wrapcheck // The shared policy owns the message and the sentinel.
		return err
	}

	if end.Val <= 0 {
		return fmt.Errorf("must be greater than 0, got %v", end.Val)
	}

	n := end.Val
	t.Canvas.MultipleOf = &n

	return nil
}

// applyStringKeyword sets one of the string keywords, first-wins: a value
// already in force stands, whether the field's type supplied it or an earlier
// rule authored it.
//
// Unlike a const, two patterns are not contradictory -- they are two constraints
// the package resolves by a documented precedence, under which an explicit
// jsonschema struct tag wins over an interpreter and the type wins over both. So
// this reports nothing. What is a mistake is one tag naming the same keyword
// twice, where there is no precedence to appeal to and dropping either value
// would be silent; that is a repeated key in one tag, which the front-end owning
// that grammar detects and reports.
func applyStringKeyword(t Target, r Rule, _ Policy) error {
	slot, typeValue := stringKeywordSlots(t, r.Op)

	if *slot != "" || typeValue != "" {
		return nil
	}

	*slot = r.Params.One()

	return nil
}

// stringKeywordSlots returns the canvas slot a string keyword writes and the
// value the type already declares for it.
func stringKeywordSlots(t Target, op Op) (*string, string) {
	base := baseOf(t)

	switch op {
	case OpFormat:
		return &t.Canvas.Format, base.Format
	case OpPattern:
		return &t.Canvas.Pattern, base.Pattern
	case OpContentEncoding:
		return &t.Canvas.ContentEncoding, base.ContentEncoding
	default:
		return &t.Canvas.ContentMediaType, base.ContentMediaType
	}
}

// nonZeroFloor is the non-zero assertion on a shape whose emptiness is a size:
// a floor of one on the axis that measures it. It is an ordinary intersect-only
// floor, so a stronger bound another rule set is never lowered.
func nonZeroFloor(axis Axis) func(Target, Rule, Policy) error {
	return func(t Target, _ Rule, pol Policy) error {
		if t.Shape.Nullable {
			return nil
		}

		return applySizeBound(t, Rule{Op: OpFloorIncl, Axis: axis, Params: ParamsOf("1")}, pol)
	}
}

// nonZeroForbid is the non-zero assertion on a shape whose emptiness is a value:
// forbid the zero the field emits. On a coerced shape that is the serialized
// text, taken from the same scalar constructor every other coerced operation
// calls, so a string-marshaling type forbids what it actually writes.
func nonZeroForbid(t Target, _ Rule, pol Policy) error {
	if t.Shape.Nullable {
		return nil
	}

	switch t.Shape.Form {
	case FormCoercedNumber, FormCoercedBool:
		v, err := t.Shape.ParseScalar(t.Shape.zeroLiteral(), pol)
		if err != nil {
			return err
		}

		Forbid(t.Canvas, v)

	case FormNumber:
		// The untyped literals the numeric-aware dedup folds together with any
		// other spelling of zero the same target forbids.
		if numericIsIntegral(t.Shape.Kind) {
			Forbid(t.Canvas, 0)
		} else {
			Forbid(t.Canvas, 0.0)
		}

	default:
		// Only the value-shaped forms route here; the matrix sends every other
		// form to a size floor or a documented no-op.
	}

	return nil
}

// nonZeroTrue is the non-zero assertion on a boolean, where the only non-zero
// value is true. A const another rule (or the type) pinned to false can never
// satisfy it, so the impossible pair is reported rather than resolved by
// whichever rule ran last.
func nonZeroTrue(t Target, _ Rule, _ Policy) error {
	if t.Shape.Nullable {
		return nil
	}

	return SetConst(t, true)
}

// SetConst pins the target's const, reporting [ErrConflict] rather than
// overwriting a value already pinned differently -- by another rule on the
// canvas, or by the type on its base, where a disagreeing value would otherwise
// be silently overwritten when the canvas is overlaid.
func SetConst(t Target, v any) error {
	if t.Canvas.Const != nil && !constraint.ValuesEqual(*t.Canvas.Const, v) {
		return fmt.Errorf("%w: a different value is already pinned", ErrConflict)
	}

	if base := baseOf(t); base.Const != nil && !constraint.ValuesEqual(*base.Const, v) {
		return fmt.Errorf("%w: the type already pins a different value", ErrConflict)
	}

	value := v
	t.Canvas.Const = &value

	return nil
}

// SetEnum sets the target's enum, reporting [ErrConflict] rather than shadowing
// one already in force on the canvas or supplied by the type.
func SetEnum(t Target, vals []any) error {
	if t.Canvas.Enum != nil {
		return fmt.Errorf("%w: an enumeration is already set", ErrConflict)
	}

	if baseOf(t).Enum != nil {
		return fmt.Errorf("%w: the type already sets an enumeration", ErrConflict)
	}

	// Each target gets its own slice, so no two share one backing array.
	t.Canvas.Enum = slices.Clone(vals)

	return nil
}

// Forbid records that the schema must not equal v, composing with anything
// already forbidden.
func Forbid(s *jsonschema.Schema, v any) {
	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.Forbid(v)
	vs.WriteForbidden(s)
}

// forbidSchema forbids a whole subschema, taking the free not slot or moving an
// existing one under allOf so both apply conjunctively.
func forbidSchema(s, forbidden *jsonschema.Schema) {
	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.ForbidSchema(forbidden)
	vs.WriteForbidden(s)
}
