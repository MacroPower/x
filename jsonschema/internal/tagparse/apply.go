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
	// spellable here, since this tag has a null literal. And because every key
	// names a JSON Schema keyword outright, a cell the model would drop as a
	// rule-shaped no-op (uniqueItems on a map) is an error here instead: the
	// author asked for that keyword, and this dialect emits no keyword nothing
	// enforces.
	tagPolicy = tagmodel.Policy{
		BoundKind:       reflect.Invalid,
		Sizes:           tagmodel.SizeStrict,
		AllowNullScalar: true,
		NamedKeywords:   true,
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

// Input is one field occurrence for [Apply] to read: the field's Go type, the
// schemas a fact lands on and classifies against, the tag text itself, and the
// two facts none of those expresses.
type Input struct {
	// FieldType is the field's own Go type, which every key classifies against
	// until a type= pair replaces the classification outright.
	FieldType reflect.Type
	// Canvas is where the value-scoped facts and annotations land.
	Canvas *jsonschema.Schema
	// Payload is the type-derived schema a type= pair restructures, since such
	// a pair replaces the reflected type assertion rather than declaring a
	// canvas fact.
	Payload *jsonschema.Schema
	// TypeSchema is the type-derived schema the tag classifies the field
	// against. It is the payload for an ordinary field, and differs only where
	// the payload alone understates what the instance is. A nullable
	// json:",string" field keeps its string on the node's null-branch base, so
	// the caller passes a view saying so. A nil value means "use the payload".
	TypeSchema *jsonschema.Schema
	// Tag is the jsonschema struct tag's value.
	Tag string
	// Quoted is the field's json:",string" flag when it applies to a string Go
	// kind, the one coercion no schema view can express (see
	// [tagmodel.ShapeOfQuoted]).
	Quoted bool
	// Nullable reports whether the occurrence admits null, which the caller
	// reads off the field's node. It is the generator's decision rather than
	// the Go type's. A slice, map, byte slice, or interface is nilable without
	// being a pointer, and WithNullable(false) drops the null branch a pointer
	// would otherwise carry. A scalar key spells that admission as the literal
	// null, so [Apply] accepts default=null wherever that decision applies,
	// whether or not the rendered schema keeps a null branch.
	Nullable bool
}

// Apply parses and applies a jsonschema struct tag. Value-scoped facts and
// annotations land on the authored canvas, while a type= pair restructures the
// type-derived payload (it replaces the reflected type assertion, not a canvas
// fact).
//
// Pairs apply strictly in order over a mutable shape, because a type= pair
// rewrites the payload and changes what every later pair means. The scalar keys
// (default, const, enum, examples) parse their values against the effective
// shape: the field's own classification until a type= pair overrides it, and
// afterward the shape the named JSON type takes (see
// [tagmodel.ShapeForTypeName]), so a scalar key before type= keeps Go-kind
// parsing while one after it parses as the overridden type. The non-scalar
// overrides (array, object, null) spell no scalar, so a scalar key following
// one is an error.
func Apply(in Input) (Result, error) {
	directives, description, err := Parse(in.Tag)
	if err != nil {
		return Result{}, err
	}

	if len(directives) == 0 {
		if description != "" {
			in.Canvas.Description = description
		}

		return Result{}, nil
	}

	typeSchema := in.TypeSchema
	if typeSchema == nil {
		typeSchema = in.Payload
	}

	state := &applyState{
		canvas:     in.Canvas,
		payload:    in.Payload,
		typeSchema: typeSchema,
		fieldType:  in.FieldType,
		quoted:     in.Quoted,
		nullable:   in.Nullable,
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
	// The fieldType field is the field's own Go type, which every key classifies
	// against until a type= pair replaces the classification outright.
	fieldType reflect.Type
	groupsSet map[string]bool
	seen      map[string]bool
	// The overriddenType field names the JSON type a type= pair installed, and is
	// empty until one does.
	overriddenType string
	// The overridden field is the shape that type takes, which displaces the
	// field's own classification for every later pair. It is meaningful only
	// while overriddenType names the type it came from.
	overridden tagmodel.Shape
	// The quoted field carries the json:",string" flag for a string Go kind,
	// which no schema view can express (see [Input.Quoted]).
	quoted bool
	// The nullable field is the occurrence's null admission as the generator
	// decided it (see [Input.Nullable]).
	nullable bool
}

// apply folds one directive into the state.
func (s *applyState) apply(d Directive) error {
	key, value := d.Key, d.Value

	if !s.scalarOK() && isScalarValueKey(key) {
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
		if g := constraintGroup(key); g != "" && !keepsGroup(s.overriddenType, g) {
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
		s.overridden = tagmodel.ShapeForTypeName(value)
		s.overriddenType = value
	}

	return nil
}

// scalarOK reports whether a scalar key still has something to parse its value
// against. A type= override to array, object, or null leaves the shape with no
// scalar kind, and that is the gate rejecting a scalar key that follows one.
func (s *applyState) scalarOK() bool {
	return s.overriddenType == "" || s.overridden.Kind != reflect.Invalid
}

// applyKey routes one key: the annotations this dialect owns outright, the type=
// override that restructures the payload, and everything else through the shared
// model.
func (s *applyState) applyKey(key, value string) error {
	switch key {
	case keyword.Description, keyword.Title, keyword.Default, keyword.Examples,
		keyword.Deprecated, keyword.ReadOnly, keyword.WriteOnly:
		// The annotation appliers all overwrite, so a repeat in one tag is
		// two stated intentions with no precedence to pick between.
		err := s.checkRepeat(key)
		if err != nil {
			return err
		}
	}

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
	if rule.Op.Overwrites() {
		err := s.checkRepeat(key)
		if err != nil {
			return err
		}
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
	if value == "" && key == keyword.Const && allowsEmptyScalar(shape) {
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

// checkRepeat reports a key set twice in one tag when its applier overwrites.
//
// An overwriting key replaces what the field's type declared, which is right
// across sources, where a documented precedence applies. Within one tag there
// is no precedence to appeal to, so two values for one keyword state two
// intentions and silently dropping either would be worse than reporting.
// Bounds are exempt, since two of those intersect -- a defined composition
// rather than a contradiction -- and a const or enum already conflicts on its
// own.
//
// For the constraint keys the overwriting set comes from the model
// ([tagmodel.Op.Overwrites]), so adding an overwriting row to the table cannot
// silently escape the check. The annotation keys this dialect owns outright
// (title, description, default, examples, and the boolean flags) all
// overwrite, and [applyState.applyKey] routes them here directly.
func (s *applyState) checkRepeat(key string) error {
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
	shape := s.shape()

	// An empty value is rejected except on a string field, where "" is the valid
	// JSON Schema default the empty string.
	if value == "" && !allowsEmptyScalar(shape) {
		return emptyValueError(key)
	}

	v, err := shape.ParseScalar(value, tagPolicy)
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

// shape classifies what the tag is currently constraining: the shape a type=
// pair installed, which restates the instance outright, or the field's own Go
// type against the payload as it now stands.
//
// The classification reads the Go type, so it approximates null admission as
// pointer-ness. The generator's own decision replaces that answer. An override
// keeps the never-null shape [tagmodel.ShapeForTypeName] builds, because the
// named type displaces the occurrence along with the Go type.
func (s *applyState) shape() tagmodel.Shape {
	if s.overriddenType != "" {
		return s.overridden
	}

	shape := tagmodel.ShapeOfQuoted(s.fieldType, s.typeSchema, s.quoted)
	shape.Nullable = s.nullable

	return shape
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

	// A type=array override names a sequence with no Go element type behind it,
	// so it supplies no element targets and an element rule reports rather than
	// descending into a shape the tag invented.
	if shape.Form == tagmodel.FormArray && shape.Elem != nil {
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

// allowsEmptyScalar reports whether an empty tag value names a real value on
// this shape: the empty string, which is a value only where the instance is a
// plain string.
//
// Reading the classified form rather than the Go kind keeps the exception in
// step with the form the value is then parsed under. A string-kinded field
// whose payload declares a number is a number here, and "" is not one of those.
func allowsEmptyScalar(shape tagmodel.Shape) bool {
	return shape.Form == tagmodel.FormString
}
