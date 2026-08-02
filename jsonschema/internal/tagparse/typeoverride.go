package tagparse

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// Constraint group names: the JSON type family whose keywords a type= override
// keeps. A keyword outside the override's family is dropped, so an explicitly
// tagged keyword in a dropped family is a conflict.
const (
	groupNumeric = "numeric"
	groupString  = "string"
	groupArray   = "array"
	groupObject  = "object"
)

// constraintGroup returns the constraint group a jsonschema tag key belongs to,
// or "" for an annotation key such as description or default that survives any
// type. Only the tag-settable constraint keywords are classified; the
// kind-derived keywords a type= override also drops never originate from a tag.
func constraintGroup(key string) string {
	switch key {
	case keyword.Minimum, keyword.Maximum, keyword.ExclusiveMinimum, keyword.ExclusiveMaximum, keyword.MultipleOf:
		return groupNumeric
	case keyword.MinLength, keyword.MaxLength, keyword.Pattern, keyword.Format:
		return groupString
	case keyword.UniqueItems, keyword.MinItems, keyword.MaxItems:
		return groupArray
	case keyword.MinProperties, keyword.MaxProperties:
		return groupObject
	default:
		return ""
	}
}

// typeConstraintGroup returns the one constraint group whose keywords a type=
// value keeps: integer and number both keep the numeric group, the others keep
// their own, and boolean or null keep none.
func typeConstraintGroup(typeName string) string {
	switch typeName {
	case typename.Integer, typename.Number:
		return groupNumeric
	case typename.String:
		return groupString
	case typename.Array:
		return groupArray
	case typename.Object:
		return groupObject
	default:
		return ""
	}
}

// conflictingGroup returns the first constraint group in groupsSet that a type=
// override to typeName would drop, or "" when every set group survives. The
// fixed iteration order keeps the reported conflict deterministic.
func conflictingGroup(groupsSet map[string]bool, typeName string) string {
	kept := typeConstraintGroup(typeName)

	for _, g := range []string{groupNumeric, groupString, groupArray, groupObject} {
		if groupsSet[g] && g != kept {
			return g
		}
	}

	return ""
}

// standInTypeFor returns the Go type that scalar tag values parse against after
// a type= pair overrides the field's reflected type: the override replaces the
// schema's type, so subsequent scalar values must parse as the overridden JSON
// type rather than the field's Go kind. The stand-ins are never pointers, so
// "null" scalar values are rejected after an override. The non-scalar JSON types
// (array, object, null) have no scalar stand-in and return nil; scalar keys
// following such an override are an error.
func standInTypeFor(typeName string) reflect.Type {
	switch typeName {
	case typename.String:
		return reflect.TypeFor[string]()
	case typename.Integer:
		return reflect.TypeFor[int64]()
	case typename.Number:
		return reflect.TypeFor[float64]()
	case typename.Boolean:
		return reflect.TypeFor[bool]()
	default: // array, object, null
		return nil
	}
}

// overriddenShapeType returns the Go type an overridden field classifies
// against. It differs from [standInTypeFor] only for the non-scalar JSON types,
// which have no scalar stand-in but still have to answer whether the overridden
// shape carries a given keyword family: type=array,minItems=1 must work, and
// that question is separate from whether a scalar key may follow. The scalar
// gate stays [standInTypeFor]'s nil.
func overriddenShapeType(typeName string) reflect.Type {
	if t := standInTypeFor(typeName); t != nil {
		return t
	}

	switch typeName {
	case typename.Array:
		return reflect.TypeFor[[]any]()
	case typename.Object:
		return reflect.TypeFor[map[string]any]()
	default: // null
		return reflect.TypeFor[any]()
	}
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
