// Package schemafield is the canonical field-metadata table for the upstream
// [jsonschema.Schema] type. Every behavior in the jsonschema package that must
// enumerate Schema's fields -- the true/empty/ref-sibling predicates, the
// sub-schema traversal, and the container-clone pass -- derives from the single
// [Fields] table here, so an upstream field addition is one table edit caught by
// one coverage test rather than six hand-maintained enumerations.
//
// The table is a package-level slice of concrete [Field] structs built once at
// load. Each field carries a closure-based [Field.IsZero] predicate (so the hot
// predicates loop over closures, not reflection), its structural [Shape] and
// [Class], and, where applicable, a sub-schema accessor or a container-clone
// closure. The package deliberately exposes raw field extractors rather than the
// jsonschema package's SubschemaEntry/Location types, leaving location assembly
// to the caller.
package schemafield

import (
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
)

// Schema is the upstream schema type this package tabulates.
type Schema = jsonschema.Schema

// Class groups a Schema field by the role it plays in validation, mirroring the
// JSON Schema keyword classification. Only the Constraint-versus-Applicator-
// versus-rest distinction is behaviorally load-bearing: IsEmpty reads the union
// of Constraint and Applicator, and nothing consults the specific non-constraint
// class. Annotation, Identifier, RenderOnly, and Extension are self-documenting
// labels for the fields IsTrue enumerates beyond IsEmpty, not behavioral gates.
type Class int

// Field classes. The zero value is intentionally invalid so a field that forgets
// to set a class fails the coverage test rather than defaulting silently.
const (
	// Constraint keywords assert something about the instance value directly
	// (type, enum, numeric and length bounds, pattern, format, content).
	Constraint Class = iota + 1

	// Applicator keywords apply sub-schemas to the instance ($ref, $dynamicRef,
	// and the 23 sub-schema-bearing fields).
	Applicator

	// Annotation keywords carry non-constraining metadata (title, description,
	// default, deprecated, readOnly, writeOnly, examples).
	Annotation

	// Identifier keywords name or scope the schema ($id, $schema, $comment,
	// $anchor, $dynamicAnchor, $vocabulary).
	Identifier

	// RenderOnly marks the PropertyOrder field, which affects only JSON
	// rendering order and never validation.
	RenderOnly

	// Extension marks the Extra catch-all map for unknown keywords. It fits none
	// of the classification's constraint roles and, like the non-constraint
	// classes, is ignored by IsEmpty.
	Extension
)

// Shape describes how a field holds sub-schemas. None means the field holds no
// sub-schema; the other three name the container the sub-schema accessor reads.
type Shape int

// Field shapes. None is the zero value, so a non-sub-schema field needs no shape.
const (
	// None marks a field that holds no sub-schema.
	None Shape = iota

	// Single marks a *Schema field, read via [Field.SingleOf].
	Single

	// Slice marks a []*Schema field, read via [Field.SliceOf].
	Slice

	// Map marks a map[string]*Schema field, read via [Field.MapOf].
	Map
)

// Field is one exported [Schema] field's canonical metadata. The pointer-bearing
// members lead and the integer enums trail so the garbage collector's scanned
// prefix stays minimal (govet fieldalignment).
type Field struct {
	// IsZero reports whether the field is unset on s, matching JSON's
	// nil-versus-empty distinction: a non-nil empty slice or map counts as set.
	IsZero func(s *Schema) bool

	// IsZeroInOutput reports whether the field leaves no trace in the schema's
	// marshaled output even though IsZero reports it set. It is non-nil only
	// for the containers upstream omits when empty and whose emptiness never
	// constrains validation: Examples and Extra (omitted by omitempty and the
	// Extra inlining), and the render-only PropertyOrder, whose empty value
	// cannot affect property ordering. HasSiblingsBesides prefers it over
	// IsZero so a bare $ref is not allOf-wrapped to preserve state no reader
	// of the output can see; IsTrue and IsEmpty keep the strict nil-based
	// semantics.
	IsZeroInOutput func(s *Schema) bool

	// SingleOf, SliceOf, and MapOf extract the field's sub-schemas. Exactly one
	// is non-nil, selected by Shape; all three are nil when Shape is None.
	SingleOf func(s *Schema) *Schema
	SliceOf  func(s *Schema) []*Schema
	MapOf    func(s *Schema) map[string]*Schema

	// CloneContainer reallocates the field's top-level container on s so writes
	// to it cannot reach the source, for the mutable header fields upstream
	// CloneSchemas leaves aliased. It is nil for every other field, including
	// the numeric pointer fields (reference-typed but pointing at immutable
	// scalars) and the sub-schema fields (cloned by upstream CloneSchemas).
	CloneContainer func(s *Schema)

	// Name is the Go field name (for example "Properties").
	Name string

	// Keyword is the JSON Pointer token addressing this field's sub-schemas
	// (for example "properties"). It is set only for the 23 sub-schema fields
	// (Shape != None); every other field leaves it empty, because those fields
	// are the only consumer and the identifier tokens have no keyword constant.
	Keyword string

	// Class is the field's validation role.
	Class Class

	// Shape is how the field holds sub-schemas.
	Shape Shape
}

// strField builds a string-typed field; IsZero is the empty string.
func strField(name string, class Class, get func(*Schema) string) Field {
	return Field{
		Name:   name,
		Class:  class,
		IsZero: func(s *Schema) bool { return get(s) == "" },
	}
}

// boolField builds a bool-typed field; IsZero is false.
func boolField(name string, class Class, get func(*Schema) bool) Field {
	return Field{
		Name:   name,
		Class:  class,
		IsZero: func(s *Schema) bool { return !get(s) },
	}
}

// floatField builds a *float64 field; IsZero is a nil pointer. Every numeric
// bound is a Constraint, and the pointee is an immutable scalar, so the field
// carries no CloneContainer.
func floatField(name string, get func(*Schema) *float64) Field {
	return Field{
		Name:   name,
		Class:  Constraint,
		IsZero: func(s *Schema) bool { return get(s) == nil },
	}
}

// intField builds a *int field; IsZero is a nil pointer. Like floatField, every
// such field is a Constraint and the pointee is immutable, so it carries no
// CloneContainer.
func intField(name string, get func(*Schema) *int) Field {
	return Field{
		Name:   name,
		Class:  Constraint,
		IsZero: func(s *Schema) bool { return get(s) == nil },
	}
}

// singleField builds a *Schema sub-schema field.
func singleField(name, kw string, get func(*Schema) *Schema) Field {
	return Field{
		Name:     name,
		Keyword:  kw,
		Class:    Applicator,
		Shape:    Single,
		IsZero:   func(s *Schema) bool { return get(s) == nil },
		SingleOf: get,
	}
}

// sliceField builds a []*Schema sub-schema field.
func sliceField(name, kw string, get func(*Schema) []*Schema) Field {
	return Field{
		Name:    name,
		Keyword: kw,
		Class:   Applicator,
		Shape:   Slice,
		IsZero:  func(s *Schema) bool { return get(s) == nil },
		SliceOf: get,
	}
}

// mapField builds a map[string]*Schema sub-schema field.
func mapField(name, kw string, get func(*Schema) map[string]*Schema) Field {
	return Field{
		Name:    name,
		Keyword: kw,
		Class:   Applicator,
		Shape:   Map,
		IsZero:  func(s *Schema) bool { return get(s) == nil },
		MapOf:   get,
	}
}

var (
	// Fields is the canonical metadata for every exported [Schema] field, in
	// upstream struct order. TestFieldTableMatchesUpstream guards that it lists
	// each exported field exactly once with a Shape matching the Go type and a
	// valid Class, so an upstream addition fails the test until it is added here.
	Fields = []Field{
		// Core.
		strField("ID", Identifier, func(s *Schema) string { return s.ID }),
		strField("Schema", Identifier, func(s *Schema) string { return s.Schema }),
		strField("Ref", Applicator, func(s *Schema) string { return s.Ref }),
		strField("Comment", Identifier, func(s *Schema) string { return s.Comment }),
		mapField("Defs", keyword.Defs, func(s *Schema) map[string]*Schema { return s.Defs }),
		mapField("Definitions", keyword.Definitions, func(s *Schema) map[string]*Schema { return s.Definitions }),
		mapField(
			"DependencySchemas",
			keyword.Dependencies,
			func(s *Schema) map[string]*Schema { return s.DependencySchemas },
		),
		{
			Name:           "DependencyStrings",
			Class:          Constraint,
			IsZero:         func(s *Schema) bool { return s.DependencyStrings == nil },
			CloneContainer: func(s *Schema) { s.DependencyStrings = maps.Clone(s.DependencyStrings) },
		},
		strField("Anchor", Identifier, func(s *Schema) string { return s.Anchor }),
		strField("DynamicAnchor", Identifier, func(s *Schema) string { return s.DynamicAnchor }),
		strField("DynamicRef", Applicator, func(s *Schema) string { return s.DynamicRef }),
		{
			Name:           "Vocabulary",
			Class:          Identifier,
			IsZero:         func(s *Schema) bool { return s.Vocabulary == nil },
			CloneContainer: func(s *Schema) { s.Vocabulary = maps.Clone(s.Vocabulary) },
		},

		// Metadata.
		strField("Title", Annotation, func(s *Schema) string { return s.Title }),
		strField("Description", Annotation, func(s *Schema) string { return s.Description }),
		{
			Name:           "Default",
			Class:          Annotation,
			IsZero:         func(s *Schema) bool { return s.Default == nil },
			CloneContainer: func(s *Schema) { s.Default = slices.Clone(s.Default) },
		},
		boolField("Deprecated", Annotation, func(s *Schema) bool { return s.Deprecated }),
		boolField("ReadOnly", Annotation, func(s *Schema) bool { return s.ReadOnly }),
		boolField("WriteOnly", Annotation, func(s *Schema) bool { return s.WriteOnly }),
		{
			Name:           "Examples",
			Class:          Annotation,
			IsZero:         func(s *Schema) bool { return s.Examples == nil },
			IsZeroInOutput: func(s *Schema) bool { return len(s.Examples) == 0 },
			CloneContainer: func(s *Schema) { s.Examples = slices.Clone(s.Examples) },
		},

		// Validation.
		strField("Type", Constraint, func(s *Schema) string { return s.Type }),
		{
			Name:           "Types",
			Class:          Constraint,
			IsZero:         func(s *Schema) bool { return s.Types == nil },
			CloneContainer: func(s *Schema) { s.Types = slices.Clone(s.Types) },
		},
		{
			Name:           "Enum",
			Class:          Constraint,
			IsZero:         func(s *Schema) bool { return s.Enum == nil },
			CloneContainer: func(s *Schema) { s.Enum = slices.Clone(s.Enum) },
		},
		{
			Name:   "Const",
			Class:  Constraint,
			IsZero: func(s *Schema) bool { return s.Const == nil },
			CloneContainer: func(s *Schema) {
				if s.Const != nil {
					c := *s.Const
					s.Const = &c
				}
			},
		},
		floatField("MultipleOf", func(s *Schema) *float64 { return s.MultipleOf }),
		floatField("Minimum", func(s *Schema) *float64 { return s.Minimum }),
		floatField("Maximum", func(s *Schema) *float64 { return s.Maximum }),
		floatField("ExclusiveMinimum", func(s *Schema) *float64 { return s.ExclusiveMinimum }),
		floatField("ExclusiveMaximum", func(s *Schema) *float64 { return s.ExclusiveMaximum }),
		intField("MinLength", func(s *Schema) *int { return s.MinLength }),
		intField("MaxLength", func(s *Schema) *int { return s.MaxLength }),
		strField("Pattern", Constraint, func(s *Schema) string { return s.Pattern }),

		// Arrays.
		sliceField("PrefixItems", keyword.PrefixItems, func(s *Schema) []*Schema { return s.PrefixItems }),
		singleField("Items", keyword.Items, func(s *Schema) *Schema { return s.Items }),
		sliceField("ItemsArray", keyword.Items, func(s *Schema) []*Schema { return s.ItemsArray }),
		intField("MinItems", func(s *Schema) *int { return s.MinItems }),
		intField("MaxItems", func(s *Schema) *int { return s.MaxItems }),
		singleField("AdditionalItems", keyword.AdditionalItems, func(s *Schema) *Schema { return s.AdditionalItems }),
		boolField("UniqueItems", Constraint, func(s *Schema) bool { return s.UniqueItems }),
		singleField("Contains", keyword.Contains, func(s *Schema) *Schema { return s.Contains }),
		intField("MinContains", func(s *Schema) *int { return s.MinContains }),
		intField("MaxContains", func(s *Schema) *int { return s.MaxContains }),
		singleField(
			"UnevaluatedItems",
			keyword.UnevaluatedItems,
			func(s *Schema) *Schema { return s.UnevaluatedItems },
		),

		// Objects.
		intField("MinProperties", func(s *Schema) *int { return s.MinProperties }),
		intField("MaxProperties", func(s *Schema) *int { return s.MaxProperties }),
		{
			Name:           "Required",
			Class:          Constraint,
			IsZero:         func(s *Schema) bool { return s.Required == nil },
			CloneContainer: func(s *Schema) { s.Required = slices.Clone(s.Required) },
		},
		{
			Name:           "DependentRequired",
			Class:          Constraint,
			IsZero:         func(s *Schema) bool { return s.DependentRequired == nil },
			CloneContainer: func(s *Schema) { s.DependentRequired = maps.Clone(s.DependentRequired) },
		},
		mapField("Properties", keyword.Properties, func(s *Schema) map[string]*Schema { return s.Properties }),
		mapField(
			"PatternProperties",
			keyword.PatternProperties,
			func(s *Schema) map[string]*Schema { return s.PatternProperties },
		),
		singleField(
			"AdditionalProperties",
			keyword.AdditionalProperties,
			func(s *Schema) *Schema { return s.AdditionalProperties },
		),
		singleField("PropertyNames", keyword.PropertyNames, func(s *Schema) *Schema { return s.PropertyNames }),
		singleField(
			"UnevaluatedProperties",
			keyword.UnevaluatedProperties,
			func(s *Schema) *Schema { return s.UnevaluatedProperties },
		),

		// Logic.
		sliceField("AllOf", keyword.AllOf, func(s *Schema) []*Schema { return s.AllOf }),
		sliceField("AnyOf", keyword.AnyOf, func(s *Schema) []*Schema { return s.AnyOf }),
		sliceField("OneOf", keyword.OneOf, func(s *Schema) []*Schema { return s.OneOf }),
		singleField("Not", keyword.Not, func(s *Schema) *Schema { return s.Not }),

		// Conditional.
		singleField("If", keyword.If, func(s *Schema) *Schema { return s.If }),
		singleField("Then", keyword.Then, func(s *Schema) *Schema { return s.Then }),
		singleField("Else", keyword.Else, func(s *Schema) *Schema { return s.Else }),
		mapField(
			"DependentSchemas",
			keyword.DependentSchemas,
			func(s *Schema) map[string]*Schema { return s.DependentSchemas },
		),

		// Content, format, and extensions.
		strField("ContentEncoding", Constraint, func(s *Schema) string { return s.ContentEncoding }),
		strField("ContentMediaType", Constraint, func(s *Schema) string { return s.ContentMediaType }),
		singleField("ContentSchema", keyword.ContentSchema, func(s *Schema) *Schema { return s.ContentSchema }),
		strField("Format", Constraint, func(s *Schema) string { return s.Format }),
		{
			Name:           "Extra",
			Class:          Extension,
			IsZero:         func(s *Schema) bool { return s.Extra == nil },
			IsZeroInOutput: func(s *Schema) bool { return len(s.Extra) == 0 },
			CloneContainer: func(s *Schema) { s.Extra = maps.Clone(s.Extra) },
		},
		{
			Name:           "PropertyOrder",
			Class:          RenderOnly,
			IsZero:         func(s *Schema) bool { return s.PropertyOrder == nil },
			IsZeroInOutput: func(s *Schema) bool { return len(s.PropertyOrder) == 0 },
			CloneContainer: func(s *Schema) { s.PropertyOrder = slices.Clone(s.PropertyOrder) },
		},
	}

	// Subschemas lists the 23 sub-schema-bearing fields in the emission order
	// the package's traversal contract pins: all map fields, then all slice
	// fields, then all single fields. That order is behaviorally load-bearing
	// (walk_test.go pins maps < slices < singles and specific within-group
	// orders), so it is encoded explicitly here rather than derived from struct
	// order, which would give the wrong map order. TestFieldTableMatchesUpstream
	// cross-checks that this list is exactly the set of Fields with Shape != None.
	Subschemas = func() []*Field {
		byName := make(map[string]*Field, len(Fields))
		for i := range Fields {
			byName[Fields[i].Name] = &Fields[i]
		}

		order := []string{
			// Maps.
			"Properties", "PatternProperties", "Defs", "Definitions",
			"DependentSchemas", "DependencySchemas",
			// Slices.
			"AllOf", "AnyOf", "OneOf", "PrefixItems", "ItemsArray",
			// Singles.
			"Items", "AdditionalProperties", "AdditionalItems", "Not", "If",
			"Then", "Else", "Contains", "PropertyNames", "UnevaluatedProperties",
			"UnevaluatedItems", "ContentSchema",
		}

		out := make([]*Field, len(order))
		for i, name := range order {
			out[i] = byName[name]
		}

		return out
	}()

	// The constraintFields subset is the Constraint-and-Applicator fields IsEmpty
	// reads, filtered once at load so the per-node check loops only the
	// constraining fields instead of re-testing every field's Class on every call.
	constraintFields = func() []*Field {
		var out []*Field

		for i := range Fields {
			if c := Fields[i].Class; c == Constraint || c == Applicator {
				out = append(out, &Fields[i])
			}
		}

		return out
	}()
)

// IsTrue reports whether s is the boolean true schema: non-nil with every field
// unset. It backs the jsonschema package's IsTrueSchema. The loop short-circuits
// on the first set field.
func IsTrue(s *Schema) bool {
	if s == nil {
		return false
	}

	for i := range Fields {
		if !Fields[i].IsZero(s) {
			return false
		}
	}

	return true
}

// IsEmpty reports whether s is non-nil with no constraining keyword set: every
// Constraint and Applicator field unset. Annotation, identifier, render-only,
// and extension fields are ignored. It backs schemashape.IsEmpty.
func IsEmpty(s *Schema) bool {
	if s == nil {
		return false
	}

	for _, f := range constraintFields {
		if !f.IsZero(s) {
			return false
		}
	}

	return true
}

// HasSiblingsBesides reports whether s has any field set other than the one named
// except. It backs schemashape.HasRefSiblings (called with "Ref"): a $ref
// carrying any sibling keyword must be wrapped in allOf so the sibling is not
// dropped. A field that is set but leaves no trace in marshaled output (see
// [Field.IsZeroInOutput]) is not a sibling: there is nothing the wrap could
// preserve. A nil s has no siblings.
func HasSiblingsBesides(s *Schema, except string) bool {
	if s == nil {
		return false
	}

	for i := range Fields {
		f := &Fields[i]
		if f.Name == except {
			continue
		}

		zero := f.IsZero
		if f.IsZeroInOutput != nil {
			zero = f.IsZeroInOutput
		}

		if !zero(s) {
			return true
		}
	}

	return false
}

// CloneContainers reallocates every mutable header container on s that upstream
// CloneSchemas leaves aliased, so in-place writes to one cannot reach the source.
// It backs schemashape.CloneOverrideExtras.
func CloneContainers(s *Schema) {
	for i := range Fields {
		if clone := Fields[i].CloneContainer; clone != nil {
			clone(s)
		}
	}
}
