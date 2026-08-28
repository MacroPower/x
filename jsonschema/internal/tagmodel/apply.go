package tagmodel

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
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

	// Arity is checked here as well as in Bind, so a caller constructing a
	// Rule directly (the interpreter facade) cannot hand an applier a
	// parameter count it would silently misread: a missing single value would
	// pin the empty string, an extra one would be dropped, and an empty
	// enumeration would forbid every instance.
	err := checkParams(r.Op, r.Params)
	if err != nil {
		return err
	}

	form := t.Shape.Form
	if form == FormUnset || form >= formCount {
		return fmt.Errorf("tagmodel: unclassified shape for %s", t.Shape.Type)
	}

	c := matrix[r.Op][form]
	switch c.status {
	case statusApply:
		return c.apply(t, r, pol)
	case statusIgnore:
		// An ignored cell is a real rule with nothing faithful to emit, which
		// only a rule-shaped dialect can drop: its constraint still holds at
		// runtime. A dialect that names keywords outright gets the rejection
		// instead, per its policy of no inert keywords.
		if pol.NamedKeywords {
			return fmt.Errorf("%w: %s", ErrUnsupported, c.why)
		}

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
		return fmt.Errorf("%w: %s", ErrUnsupported, NoElementsReason(t.Shape.Form))
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

// NoElementsReason is why a shape has no element schema to constrain. Every path
// that has to say so -- a sequence-wide rule, a dive, and the static cells for
// the shapes that never have elements -- says it from here, so the two dialects
// cannot drift on the wording of a fact they share.
func NoElementsReason(form Form) string {
	switch form {
	case FormByteString, FormRawBytes:
		return "a []byte field has no item schema to constrain (it encodes as a base64 string)"
	case FormArray, FormObject:
		return "the schema has no item or value sub-schema to constrain"
	default:
		return fmt.Sprintf("a %s has no elements to constrain", form)
	}
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

	if axis != AxisNumeric {
		return applySizeBound(t, r, pol)
	}

	// A number has no "exact size": pinning is what an exact rule means there.
	// The table reroutes the plain numeric form already, but a shape whose type
	// is only known through a reference reaches this with the axis named
	// outright.
	if r.Op == OpExactSize {
		return applyEqual(t, r, pol)
	}

	return applyNumericBound(t, r, pol)
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
		tighten(&t.Canvas.Minimum, base.Minimum, n, false)
	case OpFloorExcl:
		tighten(&t.Canvas.ExclusiveMinimum, base.ExclusiveMinimum, n, false)
	case OpCeilIncl:
		tighten(&t.Canvas.Maximum, base.Maximum, n, true)
	case OpCeilExcl:
		tighten(&t.Canvas.ExclusiveMaximum, base.ExclusiveMaximum, n, true)
	default:
		// The four endpoint operations are the only ones applyBound routes to
		// the numeric axis; an exact size is a pin and was handled there.
		return fmt.Errorf("%w: %s has no numeric endpoint", ErrUnsupported, r.Op)
	}

	return nil
}

// applySizeBound folds a length or count rule through the shared size algebra --
// the exclusive-to-inclusive fold, the non-negative clamp, the negative-literal
// domain the dialect chose, and the unsatisfiable floor-one/ceiling-zero
// construction -- and intersects each resulting endpoint against the effective
// one.
func applySizeBound(t Target, r Rule, pol Policy) error {
	bounds, err := constraint.ParseSizeBound(
		r.Params.One(), sizeRuleFor(r.Op), pol.Sizes, constraint.Intersect, constraint.Authored)
	if err != nil {
		//nolint:wrapcheck // The shared algebra owns the message dialect.
		return err
	}

	minField, maxField, baseMin, baseMax := sizeSlots(t, r.Axis)

	for _, b := range bounds {
		n := int(b.End.Rat.Num().Int64())
		if b.Lower {
			tighten(minField, baseMin, n, false)
		} else {
			tighten(maxField, baseMax, n, true)
		}
	}

	return nil
}

// forbidSizeOn builds the applier that forbids one length or entry count on a
// given axis, expressed as a not subschema pinning that size. The axis comes
// from the table rather than from the target's form, so the form-to-axis fact
// stays in one place.
//
// The subschema is gated on the instance type, because a size keyword is inert
// against a non-array (or non-object) instance: without the gate it would
// vacuously validate against the null a nilable field's schema deliberately
// admits, and the outer not would reject it.
func forbidSizeOn(axis Axis) func(Target, Rule, Policy) error {
	return func(t Target, r Rule, _ Policy) error {
		n, err := parseSizeLiteral(r.Params.One())
		if err != nil {
			return err
		}

		// No instance has a negative size, so forbidding one excludes nothing.
		if n < 0 {
			return nil
		}

		forbidden := &jsonschema.Schema{}

		if axis == AxisProperties {
			forbidden.Type = typename.Object
			forbidden.MinProperties = &n
			forbidden.MaxProperties = &n
		} else {
			forbidden.Type = typename.Array
			forbidden.MinItems = &n
			forbidden.MaxItems = &n
		}

		ForbidSchema(t.Canvas, forbidden)

		return nil
	}
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
	on, err := ParseBoolLiteral(r.Params.One())
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
	return applyDivisor(t, r.Params.One())
}

// applyDivisor records a divisor from its literal, shared with the facade's
// named setter so the positivity rule lives in one place.
func applyDivisor(t Target, lit string) error {
	// A divisor is a keyword value, not a field value, so it takes the
	// keyword-shaped literal domain regardless of the target's kind.
	end, err := constraint.ParseNumericBound(lit, reflect.Invalid)
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

// applyStringKeyword sets one of the string keywords under the dialect's
// composition rule ([Policy.Keywords]).
//
// Unlike a const, two patterns are not contradictory: they are two constraints
// the package resolves by a documented precedence, under which a jsonschema tag
// replaces what the type declared and an interpreter defers to both. So this
// reports nothing either way. What is a mistake is one tag naming the same
// keyword twice, where there is no precedence to appeal to and dropping either
// value would be silent; that is a repeated key in one tag, which the front-end
// owning that grammar detects.
func applyStringKeyword(t Target, r Rule, pol Policy) error {
	slot, typeValue := stringKeywordSlots(t, r.Op)

	if pol.Keywords == KeywordFirstWins && (*slot != "" || typeValue != "") {
		return nil
	}

	*slot = r.Params.One()

	return nil
}

// stringKeywordSlots returns the canvas slot a string keyword writes and the
// value the type already declares for it. A $defs-extracted type declares its
// keywords on the definition rather than on the provisional $ref payload, so
// the type value reads through the target's refBase seam too: without it a
// first-wins interpreter could not see the definition's format and would
// conjoin its inferred one as a $ref sibling.
func stringKeywordSlots(t Target, op Op) (*string, string) {
	base, ref := baseOf(t), refBaseOf(t)

	switch op {
	case OpFormat:
		return &t.Canvas.Format, cmp.Or(base.Format, ref.Format)
	case OpPattern:
		return &t.Canvas.Pattern, cmp.Or(base.Pattern, ref.Pattern)
	case OpContentEncoding:
		return &t.Canvas.ContentEncoding, cmp.Or(base.ContentEncoding, ref.ContentEncoding)
	default:
		return &t.Canvas.ContentMediaType, cmp.Or(base.ContentMediaType, ref.ContentMediaType)
	}
}

// refBaseOf returns the schema of the definition the target's base defers to,
// or an empty one when the base is not a reference (or the seam was not
// supplied), so the effective-value reads never dereference nil.
func refBaseOf(t Target) *jsonschema.Schema {
	if t.refBase != nil {
		if s := t.refBase(); s != nil {
			return s
		}
	}

	return &jsonschema.Schema{}
}

// nonZeroNullable is the non-zero assertion on a nullable occurrence, a
// forbidden null and nothing else.
//
// A pointer field's emptiness is its nil, which go-playground's required reads
// as "must be non-nil" and says nothing about the pointed-to value, so the
// pointed-to zero stays valid. The schema's required entry cannot express that
// on its own, since a property whose value is null is still present, so the
// assertion needs the null forbidden outright.
//
// The applier forbids null as a value rather than as a subschema, which keeps
// it on the null wrapper. A forbidden value rides the not.const to not.enum
// accumulation and holds the single not slot, which the keyword table scopes to
// the wrapper. A forbidden subschema cannot join that accumulation, so it gives
// way to allOf, and allOf is scoped to the value branch of the null encoding,
// where a null instance never reaches it.
func nonZeroNullable(t Target) error {
	Forbid(t.Canvas, nil)

	return nil
}

// nonZeroFloor is the non-zero assertion on a shape whose emptiness is a size:
// a floor of one on the axis that measures it. It is an ordinary intersect-only
// floor, so a stronger bound another rule set is never lowered.
func nonZeroFloor(axis Axis) func(Target, Rule, Policy) error {
	return func(t Target, _ Rule, pol Policy) error {
		if t.Shape.Nullable {
			return nonZeroNullable(t)
		}

		return applySizeBound(t, Rule{Op: OpFloorIncl, Axis: axis, Params: ParamsOf("1")}, pol)
	}
}

// nonZeroForbidCoerced is the non-zero assertion on a shape whose schema is a
// string because it serializes itself as one: forbid the serialized zero, taken
// from the same scalar constructor every other coerced operation calls, so a
// string-marshaling type forbids the text it actually writes rather than a
// hardcoded "0" it never emits.
func nonZeroForbidCoerced(t Target, _ Rule, pol Policy) error {
	if t.Shape.Nullable {
		return nonZeroNullable(t)
	}

	v, err := t.Shape.ParseScalar(t.Shape.zeroLiteral(), pol)
	if err != nil {
		return err
	}

	Forbid(t.Canvas, v)

	return nil
}

// nonZeroForbidNumber is the non-zero assertion on a native number: forbid the
// zero literal directly. The untyped literals are what the numeric-aware dedup
// folds together with any other spelling of zero the same target forbids.
func nonZeroForbidNumber(t Target, _ Rule, _ Policy) error {
	if t.Shape.Nullable {
		return nonZeroNullable(t)
	}

	if numkind.IsInteger(t.Shape.Kind) {
		Forbid(t.Canvas, 0)
	} else {
		Forbid(t.Canvas, 0.0)
	}

	return nil
}

// nonZeroTrue is the non-zero assertion on a boolean, where the only non-zero
// value is true. A const another rule (or the type) pinned to false can never
// satisfy it, so the impossible pair is reported rather than resolved by
// whichever rule ran last.
func nonZeroTrue(t Target, _ Rule, _ Policy) error {
	if t.Shape.Nullable {
		return nonZeroNullable(t)
	}

	return SetConst(t, true)
}

// SetMultipleOf records a divisor on the target, going through the same cell
// the tag keyword does so a shape with no number to divide is reported rather
// than stamped.
func SetMultipleOf(t Target, value float64) error {
	return Apply(t, Rule{
		Op:     OpMultipleOf,
		Params: ParamsOf(strconv.FormatFloat(value, 'g', -1, 64)),
	}, Policy{})
}

// SetConst pins the target's const, reporting [ErrConflict] rather than
// overwriting a value already pinned differently -- by another rule on the
// canvas, or by the type on its base, where a disagreeing value would otherwise
// be silently overwritten when the canvas is overlaid. An enumeration already
// in force is checked too: a const outside it can never hold, and both keywords
// fully describe the allowed set, so the impossible pair is reported rather
// than composed into a schema no instance satisfies.
func SetConst(t Target, v any) error {
	if t.Canvas.Const != nil && !constraint.ValuesEqual(*t.Canvas.Const, v) {
		return fmt.Errorf("%w: a different value is already pinned", ErrConflict)
	}

	if base := baseOf(t); base.Const != nil && !constraint.ValuesEqual(*base.Const, v) {
		return fmt.Errorf("%w: the type already pins a different value", ErrConflict)
	}

	if t.Canvas.Enum != nil && !enumAdmits(t.Canvas.Enum, v) {
		return fmt.Errorf("%w: an enumeration already in force excludes the value", ErrConflict)
	}

	if base := baseOf(t); base.Enum != nil && !enumAdmits(base.Enum, v) {
		return fmt.Errorf("%w: the type's enumeration excludes the value", ErrConflict)
	}

	value := v
	t.Canvas.Const = &value

	return nil
}

// SetEnum sets the target's enum, reporting [ErrConflict] rather than shadowing
// one already in force on the canvas or supplied by the type. A const already
// pinned -- by another rule or by the type -- must be a member, for the same
// reason [SetConst] checks the enumeration: the two keywords assert
// conjunctively, so an excluded pin makes the schema unsatisfiable.
func SetEnum(t Target, vals []any) error {
	if t.Canvas.Enum != nil {
		return fmt.Errorf("%w: an enumeration is already set", ErrConflict)
	}

	if baseOf(t).Enum != nil {
		return fmt.Errorf("%w: the type already sets an enumeration", ErrConflict)
	}

	if c := t.Canvas.Const; c != nil && !enumAdmits(vals, *c) {
		return fmt.Errorf("%w: the enumeration excludes a value already pinned", ErrConflict)
	}

	if c := baseOf(t).Const; c != nil && !enumAdmits(vals, *c) {
		return fmt.Errorf("%w: the enumeration excludes the value the type pins", ErrConflict)
	}

	// Copy rather than alias: this is public through the Constraints facade, so
	// vals may be a caller's slice that it goes on to reuse or mutate.
	t.Canvas.Enum = slices.Clone(vals)

	return nil
}

// enumAdmits reports whether the enumeration contains v, under the same
// numeric-aware equality [SetConst] uses for a repeated pin, so 7 and 7.0
// count as the same member.
func enumAdmits(vals []any, v any) bool {
	return slices.ContainsFunc(vals, func(m any) bool {
		return constraint.ValuesEqual(m, v)
	})
}

// Forbid records that the schema must not equal v, composing with anything
// already forbidden.
func Forbid(s *jsonschema.Schema, v any) {
	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.Forbid(v)
	vs.WriteForbidden(s)
}

// ForbidSchema forbids a whole subschema, taking the free not slot or moving an
// existing one under allOf so both apply conjunctively.
func ForbidSchema(s, forbidden *jsonschema.Schema) {
	var vs constraint.ValueSet

	vs.SeedNot(s.Not)
	vs.ForbidSchema(forbidden)
	vs.WriteForbidden(s)
}
