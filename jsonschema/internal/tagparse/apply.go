package tagparse

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

var (
	// ErrInvalidType is returned when a type= tag value names something other
	// than the seven JSON Schema type names. The jsonschema package maps it onto
	// its own public ErrInvalidType so callers can match it with [errors.Is].
	ErrInvalidType = errors.New("invalid type name")

	// The tagPolicy value is this dialect's half of the two legitimate
	// divergences the shared model carries as parameters.
	//
	// A bound literal parses at [reflect.Invalid], the keyword-shaped domain:
	// this tag names JSON Schema keywords directly, and minimum is a keyword
	// whose value may be fractional even on an integer schema, so minimum=1.5 on
	// an int8 is correct. (The validate tag, which names go-playground rules,
	// parses at the field kind instead, where gte=1.5 on an int is correctly an
	// error.) A size literal is likewise a keyword value, and minLength's value
	// domain is non-negative, so a negative one is rejected rather than folded
	// into the unsatisfiable range go-playground's max=-1 means. Null is
	// spellable here, since this tag has a null literal.
	tagPolicy = tagmodel.Policy{
		BoundKind:       reflect.Invalid,
		Sizes:           tagmodel.SizeStrict,
		AllowNullScalar: true,
	}

	// The tagKeys table maps each constraint keyword this tag spells to the shared operation
	// it names. Unlike the validate tag, which names rules and lets the field's
	// shape choose a keyword family, this dialect names the keyword itself, so every
	// bound row pins its axis: that is what makes minItems=3 on a string an error
	// rather than an inert keyword nothing enforces.
	tagKeys = map[string]tagmodel.KeyRule{
		keyword.Minimum:          {Op: tagmodel.OpFloorIncl, Axis: tagmodel.AxisNumeric, Param: tagmodel.ParamRequired},
		keyword.Maximum:          {Op: tagmodel.OpCeilIncl, Axis: tagmodel.AxisNumeric, Param: tagmodel.ParamRequired},
		keyword.ExclusiveMinimum: {Op: tagmodel.OpFloorExcl, Axis: tagmodel.AxisNumeric, Param: tagmodel.ParamRequired},
		keyword.ExclusiveMaximum: {Op: tagmodel.OpCeilExcl, Axis: tagmodel.AxisNumeric, Param: tagmodel.ParamRequired},
		keyword.MultipleOf:       {Op: tagmodel.OpMultipleOf, Param: tagmodel.ParamRequired},

		keyword.MinLength:     {Op: tagmodel.OpFloorIncl, Axis: tagmodel.AxisLength, Param: tagmodel.ParamRequired},
		keyword.MaxLength:     {Op: tagmodel.OpCeilIncl, Axis: tagmodel.AxisLength, Param: tagmodel.ParamRequired},
		keyword.MinItems:      {Op: tagmodel.OpFloorIncl, Axis: tagmodel.AxisItems, Param: tagmodel.ParamRequired},
		keyword.MaxItems:      {Op: tagmodel.OpCeilIncl, Axis: tagmodel.AxisItems, Param: tagmodel.ParamRequired},
		keyword.MinProperties: {Op: tagmodel.OpFloorIncl, Axis: tagmodel.AxisProperties, Param: tagmodel.ParamRequired},
		keyword.MaxProperties: {Op: tagmodel.OpCeilIncl, Axis: tagmodel.AxisProperties, Param: tagmodel.ParamRequired},

		// The keyword carries its own boolean, where the validate tag's unique is
		// bare. Declaring that here is what keeps uniqueItems=false from setting the
		// keyword at all.
		keyword.UniqueItems: {Op: tagmodel.OpUnique, Param: tagmodel.ParamRequired},

		keyword.Const: {Op: tagmodel.OpEqual, Param: tagmodel.ParamRequired},
		keyword.Enum:  {Op: tagmodel.OpOneOf, Param: tagmodel.ParamList, Split: splitEnumValues},

		keyword.Pattern: {Op: tagmodel.OpPattern, Param: tagmodel.ParamRequired},
		keyword.Format:  {Op: tagmodel.OpFormat, Param: tagmodel.ParamRequired},
	}
)

// Result reports what [Apply] did that the generator's render phase needs. It is
// internal, so it is free to widen as more provenance is required.
type Result struct {
	// TypeOverridden reports whether a type= pair replaced the field's type. The
	// field is then inline and non-nullable, so the builder detaches its $ref
	// link and clears its null bit.
	TypeOverridden bool
}

// Apply parses and applies a jsonschema struct tag. Value-scoped facts and
// annotations land on the authored canvas, while a type= pair restructures the
// type-derived payload (it replaces the reflected type assertion, not a canvas
// fact).
//
// Pairs apply strictly in order over a mutable shape, because a type= pair
// rewrites the payload and changes what every later pair means. The scalar keys
// (default, const, enum, examples) parse their values against the effective
// scalar type: the field's Go type until a type= pair overrides it, and
// afterward a stand-in Go type for the overridden JSON type (see
// [standInTypeFor]), so a scalar key before type= keeps Go-kind parsing while
// one after it parses as the overridden type. The non-scalar overrides (array,
// object, null) have no stand-in, so a scalar key following one is an error.
// TypeSchema is the type-derived schema the tag classifies the field against.
// It is the payload for an ordinary field, and differs only where the payload
// alone understates what the instance is: a nullable json:",string" field keeps
// its string on the node's null-branch base, so the caller passes a view saying
// so. A nil value means "use the payload".
func Apply(
	tag string, fieldType reflect.Type, canvas, payload, typeSchema *jsonschema.Schema,
) (Result, error) {
	directives, description, err := Parse(tag)
	if err != nil {
		return Result{}, err
	}

	if len(directives) == 0 {
		if description != "" {
			canvas.Description = description
		}

		return Result{}, nil
	}

	if typeSchema == nil {
		typeSchema = payload
	}

	state := &applyState{
		canvas:     canvas,
		payload:    payload,
		typeSchema: typeSchema,
		shapeType:  fieldType,
		scalarOK:   true,
		groupsSet:  map[string]bool{},
		seen:       map[string]bool{},
	}

	for _, d := range directives {
		err := state.apply(d)
		if err != nil {
			return Result{}, err
		}
	}

	return Result{TypeOverridden: state.overriddenType != ""}, nil
}

// applyState is the fold's mutable state: where facts land, the effective types
// a later pair reads, and the bookkeeping the type= conflict check needs.
type applyState struct {
	canvas  *jsonschema.Schema
	payload *jsonschema.Schema
	// The typeSchema field is what the field classifies against, which is the
	// payload except where the payload understates the instance (see [Apply]).
	// A type= override restates the payload outright, so it replaces this too.
	typeSchema *jsonschema.Schema
	// The shapeType field is what every key classifies against: the field's Go
	// type, or a stand-in for the overridden JSON type after a type= pair.
	shapeType      reflect.Type
	groupsSet      map[string]bool
	seen           map[string]bool
	overriddenType string
	// The scalarOK flag reports whether a scalar key still has a type to parse
	// against. It goes false after a type= override to array, object, or null,
	// which have no scalar stand-in, and is the gate that rejects a scalar key
	// following one.
	scalarOK bool
}

// apply folds one directive into the state.
func (s *applyState) apply(d Directive) error {
	key, value := d.Key, d.Value

	if !s.scalarOK && isScalarValueKey(key) {
		return fmt.Errorf("jsonschema tag: key %q cannot follow type=%s", key, s.overriddenType)
	}

	// A type= override drops the constraint groups the new type cannot use.
	// Dropping the keywords derived from the Go kind is intended, but a keyword
	// the tag set explicitly is the author's input, so report the conflict rather
	// than discarding it silently. This guard catches a keyword set before the
	// type= pair; the guard below catches one set after it, while the override is
	// already in force.
	if key == keyword.Type && typename.Valid(value) {
		if g := conflictingGroup(s.groupsSet, value); g != "" {
			return fmt.Errorf("jsonschema tag: %s constraint conflicts with type=%s", g, value)
		}
	}

	// Once an override is in force, a keyword from a family it dropped is a
	// conflict with the override. Reporting that here rather than letting the
	// shared model report that the overridden shape cannot carry the keyword
	// names the actual mistake: the author wrote both, and the override is why
	// the keyword cannot stay.
	if s.overriddenType != "" {
		if g := constraintGroup(key); g != "" && g != typeConstraintGroup(s.overriddenType) {
			return fmt.Errorf("jsonschema tag: %s constraint conflicts with type=%s", g, s.overriddenType)
		}
	}

	err := s.applyKey(key, value)
	if err != nil {
		return err
	}

	if g := constraintGroup(key); g != "" {
		s.groupsSet[g] = true
	}

	// An enum on a sequence field constrains the elements, so it lives inside
	// array structure that only the array type keeps; record it as an array
	// constraint so a non-array type= override reports a conflict instead of
	// silently dropping the author's enum with the items it rides on.
	if key == keyword.Enum && s.shape().Form == tagmodel.FormArray {
		s.groupsSet[groupArray] = true
	}

	if key == keyword.Type {
		s.scalarOK = standInTypeFor(value) != nil
		s.shapeType = overriddenShapeType(value)
		s.overriddenType = value
	}

	return nil
}

// applyKey routes one key: the annotations this dialect owns outright, the type=
// override that restructures the payload, and everything else through the shared
// model.
func (s *applyState) applyKey(key, value string) error {
	switch key {
	case keyword.Description, keyword.Title:
		// An empty string is the field's zero value, so the keyword would
		// silently not be emitted at all; reject the empty value like every
		// other key so a typo such as "title=" is a parse error rather than a
		// dropped constraint.
		if value == "" {
			return emptyValueError(key)
		}

		if key == keyword.Description {
			s.canvas.Description = value
		} else {
			s.canvas.Title = value
		}

		return nil

	case keyword.Type:
		if !typename.Valid(value) {
			return fmt.Errorf("jsonschema tag: key %q: %w: %q", key, ErrInvalidType, value)
		}

		applyTypeOverride(s.payload, value)

		// The override restates what the instance is, so it supersedes any
		// coerced view the caller supplied.
		s.typeSchema = s.payload

		return nil

	case keyword.Deprecated, keyword.ReadOnly, keyword.WriteOnly:
		return s.applyFlag(key, value)

	case keyword.Default:
		return s.applyDefault(key, value)

	case keyword.Examples:
		return s.applyExamples(key, value)
	}

	rule, known := tagKeys[key]
	if !known {
		return fmt.Errorf("jsonschema tag: unrecognized key %q", key)
	}

	return s.applyConstraint(rule, key, value)
}

// applyConstraint binds a constraint key's value against its declared arity and
// hands the rule to the shared model.
func (s *applyState) applyConstraint(rule tagmodel.KeyRule, key, value string) error {
	err := s.checkRepeat(rule, key)
	if err != nil {
		return err
	}

	shape := s.shape()

	bound, err := s.bind(rule, shape, key, value)
	if err != nil {
		return err
	}

	if rule.Param == tagmodel.ParamList {
		if slices.Contains(bound.Params.Values(), "") {
			return fmt.Errorf("jsonschema tag: key %q has an empty value segment", key)
		}
	}

	err = tagmodel.Apply(s.target(shape), bound, tagPolicy)
	if err != nil {
		return s.wrapApplyError(key, value, err)
	}

	return nil
}

// wrapApplyError gives a model error this dialect's phrasing. The
// exact-representability rejection keeps its own wording, which names the
// remedy, while still wrapping the shared sentinel so [errors.Is] identity to
// the public ErrBoundNotRepresentable holds.
func (s *applyState) wrapApplyError(key, value string, err error) error {
	if errors.Is(err, constraint.ErrNotRepresentable) {
		return fmt.Errorf(
			"jsonschema tag: key %q: integer bound %q exceeds exact float64 "+
				"precision (>2^53); use const for an exact extreme value: %w",
			key, value, constraint.ErrNotRepresentable,
		)
	}

	return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
}

// bind resolves a key's parameters, with the one exception this dialect's
// grammar owns: an empty const on a string field is the valid JSON Schema value
// "", not a missing parameter.
func (s *applyState) bind(
	rule tagmodel.KeyRule, shape tagmodel.Shape, key, value string,
) (tagmodel.Rule, error) {
	if value == "" && key == keyword.Const && s.isStringScalar() {
		return tagmodel.Rule{Op: rule.Op, Axis: rule.Axis, Params: tagmodel.ParamsOf("")}, nil
	}

	if value == "" {
		return tagmodel.Rule{}, emptyValueError(key)
	}

	bound, err := tagmodel.Bind(rule, shape, value, true)
	if err != nil {
		return tagmodel.Rule{}, fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	return bound, nil
}

// checkRepeat reports a key naming a string keyword twice in one tag.
//
// These are the only operations whose applier overwrites: this dialect replaces
// what the field's type declared, which is right across sources, where a
// documented precedence applies. Within one tag there is no precedence to appeal
// to, so two values for one keyword state two intentions and silently dropping
// either would be worse than reporting. Bounds are exempt, since two of those
// intersect -- a defined composition rather than a contradiction -- and a const
// or enum already conflicts on its own.
//
// The set comes from the model rather than being named here, so adding a
// content-keyword row to the table cannot silently escape the check.
func (s *applyState) checkRepeat(rule tagmodel.KeyRule, key string) error {
	if !rule.Op.IsStringKeyword() {
		return nil
	}

	if s.seen[key] {
		return fmt.Errorf("jsonschema tag: key %q is set twice in one tag", key)
	}

	s.seen[key] = true

	return nil
}

// applyFlag applies one of the boolean annotation keywords.
func (s *applyState) applyFlag(key, value string) error {
	b, err := parseFlag(key, value)
	if err != nil {
		return err
	}

	switch key {
	case keyword.Deprecated:
		s.canvas.Deprecated = b
	case keyword.ReadOnly:
		s.canvas.ReadOnly = b
	case keyword.WriteOnly:
		s.canvas.WriteOnly = b
	}

	return nil
}

// applyDefault records the default annotation. Its value parses through the same
// scalar constructor a const does, so a field whose schema is a string because
// it serializes itself as one gets the text it actually emits rather than the
// number's own spelling.
func (s *applyState) applyDefault(key, value string) error {
	// An empty value is rejected except on a string field, where "" is the valid
	// JSON Schema default the empty string.
	if value == "" && !s.isStringScalar() {
		return emptyValueError(key)
	}

	v, err := s.shape().ParseScalar(value, tagPolicy)
	if err != nil {
		return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	s.canvas.Default = raw

	return nil
}

// applyExamples records the examples annotation, parsing each member the way an
// enum member parses.
func (s *applyState) applyExamples(key, value string) error {
	if value == "" {
		return emptyValueError(key)
	}

	parts := splitEnumValues(value)
	if slices.Contains(parts, "") {
		return fmt.Errorf("jsonschema tag: key %q has an empty value segment", key)
	}

	vals, err := s.shape().ParseScalars(parts, tagPolicy)
	if err != nil {
		return fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	s.canvas.Examples = vals

	return nil
}

// shape classifies what the tag is currently constraining, from the effective
// Go type and the payload as it now stands, so a pair after a type= override
// sees the overridden shape.
func (s *applyState) shape() tagmodel.Shape {
	return tagmodel.ShapeOf(s.shapeType, s.typeSchema)
}

// target builds the write destination for the field, pairing the canvas's
// element slots with the payload's so an element rule reaches both. Supplying
// the elements here rather than reading only the canvas is what lets an element
// classify itself, including the coercion its own type implies.
func (s *applyState) target(shape tagmodel.Shape) tagmodel.Target {
	return newTarget(shape, s.canvas, s.typeSchema)
}

// newTarget builds a target for a field or element, recursing lazily into
// element slots.
func newTarget(shape tagmodel.Shape, canvas, payload *jsonschema.Schema) tagmodel.Target {
	var elems func() []tagmodel.Target

	if shape.Form == tagmodel.FormArray {
		elems = func() []tagmodel.Target {
			canvases := elementSchemas(canvas)
			payloads := elementSchemas(payload)
			elemType := shape.Elem.Elem()

			out := make([]tagmodel.Target, 0, len(canvases))

			for i, c := range canvases {
				var p *jsonschema.Schema

				if i < len(payloads) {
					p = payloads[i]
				}

				out = append(out, newTarget(tagmodel.ShapeOf(elemType, p), c, p))
			}

			return out
		}
	}

	return tagmodel.NewTarget(shape, canvas, payload, elems)
}

// elementSchemas returns the per-element sub-schemas of a sequence schema:
// prefixItems (Draft 2020-12) or the items-array form (Draft-07) for a fixed
// array, or the single Items schema for a slice. The canvas is generator-wired
// to mirror the payload's item structure, so one structural read reaches both.
func elementSchemas(s *jsonschema.Schema) []*jsonschema.Schema {
	if s == nil {
		return nil
	}

	switch {
	case len(s.PrefixItems) > 0:
		return s.PrefixItems
	case len(s.ItemsArray) > 0:
		return s.ItemsArray
	case s.Items != nil:
		return []*jsonschema.Schema{s.Items}
	default:
		return nil
	}
}

// emptyValueError is the shared phrasing for a key whose value is missing.
func emptyValueError(key string) error {
	return fmt.Errorf("jsonschema tag: key %q requires a non-empty value", key)
}

// parseFlag parses a boolean annotation value. It reads booleans the same way
// the constraint keys do, since those reach the shared literal parser through
// Bind and one tag should not spell true two ways.
func parseFlag(key, value string) (bool, error) {
	if value == "" {
		return false, emptyValueError(key)
	}

	b, err := tagmodel.ParseBoolLiteral(value)
	if err != nil {
		return false, fmt.Errorf("jsonschema tag: key %q: %w", key, err)
	}

	return b, nil
}

// isScalarValueKey reports whether a jsonschema tag key carries scalar values
// parsed against the effective scalar type (see [Apply]).
func isScalarValueKey(key string) bool {
	switch key {
	case keyword.Default, keyword.Const, keyword.Enum, keyword.Examples:
		return true
	default:
		return false
	}
}

// isStringScalar reports whether the effective type is a string kind. An empty
// const or default value is meaningful only there, where it expresses the valid
// JSON Schema value "".
func (s *applyState) isStringScalar() bool {
	return s.scalarOK && numkind.DerefType(s.shapeType).Kind() == reflect.String
}
