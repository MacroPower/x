package tagparse

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// Constraint group names: the JSON type family whose keywords a type= override
// keeps. A keyword outside the override's family is dropped, so an explicitly
// tagged keyword in a dropped family is a conflict. Each group is one of the
// shared model's bound axes, under the name this dialect's messages give it.
const (
	groupNumeric = "numeric"
	groupString  = "string"
	groupArray   = "array"
	groupObject  = "object"
)

var (
	// The groups table pairs each constraint group with the model axis it names,
	// in the order a conflict is reported, which is what keeps the reported
	// conflict deterministic when a tag set several.
	groups = [...]struct {
		name string
		axis tagmodel.Axis
	}{
		{groupNumeric, tagmodel.AxisNumeric},
		{groupString, tagmodel.AxisLength},
		{groupArray, tagmodel.AxisItems},
		{groupObject, tagmodel.AxisProperties},
	}

	// The opGroups table classifies the keys whose family their [tagmodel.KeyRule]
	// row cannot state, because an axis names a bound's endpoint family and these
	// keywords are not bounds. It is keyed by the operation rather than by the
	// keyword, so the classification holds however a key is spelled.
	opGroups = map[tagmodel.Op]string{
		tagmodel.OpMultipleOf: groupNumeric,
		tagmodel.OpUnique:     groupArray,
		tagmodel.OpPattern:    groupString,
		tagmodel.OpFormat:     groupString,
	}

	// The groupless set is the operations that deliberately belong to no family:
	// a pinned or enumerated value is a value, not a keyword an override drops,
	// and the scalar gate is what decides whether it may follow one.
	groupless = map[tagmodel.Op]bool{
		tagmodel.OpEqual: true,
		tagmodel.OpOneOf: true,
	}

	// The constraintGroups table classifies every constraint key this tag spells,
	// derived from the tagKeys table rather than restated beside it.
	constraintGroups = buildConstraintGroups()
)

// buildConstraintGroups derives the key-to-group table from the key table: a
// bound row is classified by the axis it already declares, and the handful of
// non-bound keywords by their operation. A row that classifies as neither is a
// panic on first import, so adding a key to tagKeys either classifies from what
// the row already says or reports, the same totality the shared model's matrix
// has.
func buildConstraintGroups() map[string]string {
	out := make(map[string]string, len(tagKeys))

	for key, rule := range tagKeys {
		switch {
		case groupForAxis(rule.Axis) != "":
			out[key] = groupForAxis(rule.Axis)
		case opGroups[rule.Op] != "":
			out[key] = opGroups[rule.Op]
		case groupless[rule.Op]:
		default:
			panic(fmt.Sprintf("tagparse: key %q names no constraint group", key))
		}
	}

	return out
}

// groupForAxis names the constraint group a model axis belongs to, or "" for
// [tagmodel.AxisAuto], which pins no family.
func groupForAxis(axis tagmodel.Axis) string {
	for _, g := range groups {
		if g.axis == axis {
			return g.name
		}
	}

	return ""
}

// constraintGroup returns the constraint group a jsonschema tag key belongs to,
// or "" for an annotation key such as description or default that survives any
// type. Only the tag-settable constraint keywords are classified; the
// kind-derived keywords a type= override also drops never originate from a tag.
func constraintGroup(key string) string {
	return constraintGroups[key]
}

// keepsGroup reports whether a type= override to typeName keeps one constraint
// group's keywords. The answer is the shared model's: the overridden instance
// takes a known form, and whether that form has the group's keyword family is
// the same fact the model checks before it lands a bound.
func keepsGroup(typeName, group string) bool {
	form := tagmodel.FormForTypeName(typeName)

	for _, g := range groups {
		if g.name == group {
			return tagmodel.FormCarriesAxis(form, g.axis)
		}
	}

	return false
}

// conflictingGroup returns the first constraint group in groupsSet that a type=
// override to typeName would drop, or "" when every set group survives.
func conflictingGroup(groupsSet map[string]bool, typeName string) string {
	form := tagmodel.FormForTypeName(typeName)

	for _, g := range groups {
		if groupsSet[g.name] && !tagmodel.FormCarriesAxis(form, g.axis) {
			return g.name
		}
	}

	return ""
}

// applyTypeOverride applies a type= tag value, replacing the reflected type
// assertion: it sets Type, clears a Types array, drops a bare $ref to a
// definition, removes the nullable anyOf wrapper a pointer field generates,
// and drops the keyword groups the new type cannot use. The numeric bounds,
// array keywords, object keywords, and string constraints each derive from the
// original Go kind (an int64-reflected field such as [time.Duration] carries
// range bounds, a slice carries items, a struct carries properties, and a
// string-reflected field such as [time.Time] or [big.Rat] carries a
// format/pattern, and a []byte field carries the string-only content
// keywords); left on a schema of a different type they are vacuous but
// emit as confusing dead structure. Tag pairs apply in order, so keys after
// type= still take effect.
func applyTypeOverride(s *jsonschema.Schema, typeName string) {
	// A field whose type was extracted to $defs reflects to a bare {$ref}; the
	// explicit type replaces that assertion, so drop the ref. Leaving it would
	// emit {$ref, type}, which under 2020-12 requires both to hold and is
	// unsatisfiable when the referenced definition is a different type.
	s.Ref = ""

	s.Type = typeName
	s.Types = nil

	if typeName != typename.Integer && typeName != typename.Number {
		s.Minimum = nil
		s.Maximum = nil
		s.ExclusiveMinimum = nil
		s.ExclusiveMaximum = nil
		s.MultipleOf = nil
	}

	if typeName != typename.String {
		s.Format = ""
		s.Pattern = ""
		s.MinLength = nil
		s.MaxLength = nil
		s.ContentEncoding = ""
		s.ContentMediaType = ""
		s.ContentSchema = nil
	}

	if typeName != typename.Array {
		s.Items = nil
		s.PrefixItems = nil
		s.ItemsArray = nil
		s.AdditionalItems = nil
		s.UnevaluatedItems = nil
		s.MinItems = nil
		s.MaxItems = nil
		s.UniqueItems = false
		s.Contains = nil
		s.MinContains = nil
		s.MaxContains = nil
	}

	if typeName != typename.Object {
		s.Properties = nil
		s.PatternProperties = nil
		s.AdditionalProperties = nil
		s.PropertyNames = nil
		s.UnevaluatedProperties = nil
		s.Required = nil
		s.MinProperties = nil
		s.MaxProperties = nil
		s.DependentRequired = nil
		s.DependentSchemas = nil
	}
}
