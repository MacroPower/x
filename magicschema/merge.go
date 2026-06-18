package magicschema

import (
	"cmp"
	"maps"
	"math"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// mergeSchemas merges two schemas using union semantics: the result accepts
// everything either input accepts. Properties from both schemas are included
// and conflicting types are widened. Validation constraints survive the merge
// only when both sides constrain -- a side without a constraint already
// permits everything, so keeping a one-sided constraint would fail closed.
// Bounds widen toward the permissive end, enums union (a const counts as a
// single-value enum), and exact-value constraints (pattern, format,
// multipleOf, patternProperties, and other keywords with no widening rule)
// are kept only when both sides agree. A type that is null or empty on one
// side and concrete on the other widens to a [type, null] union so the null
// input still validates. When the two types are incompatible the type
// constraint is dropped entirely, and every type-specific keyword
// (properties, items, bounds, pattern) drops with it -- a schema with no
// type but residual object or array constraints would still fail closed for
// those instances.
// Combinators and references ($ref, $dynamicRef, allOf/anyOf/oneOf, not) are
// dropped entirely, which is the most permissive behavior; the if/then/else
// conditional is kept as a unit when both sides agree exactly. Identity and
// informational keywords ($id, $comment, $anchor, $dynamicAnchor, $vocabulary)
// carry first-wins like title and description, since they annotate rather than
// constrain.
//
// The result aliases sub-structures of both inputs (one-sided properties,
// items, and kept-when-equal keywords are not cloned), so neither the
// inputs nor the merged result's shared contents may be mutated in place
// afterward; reassigning top-level fields on the result is safe.
func mergeSchemas(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil {
		return b
	}

	if b == nil {
		return a
	}

	result := &jsonschema.Schema{}

	// Merge types with widening.
	typesA, typesB := typeList(a), typeList(b)
	merged := widenTypeList(typesA, typesB)

	// An absent type on one side reads as a null or empty value, widening
	// the other side to a type-or-null union. That is right for value-derived
	// schemas, where a null records as the empty schema, but wrong for an
	// annotation-only constraint schema (pattern, enum, bounds with no type):
	// it already permits every type, so the union is typeless and injecting
	// null would claim a null is valid when neither input allowed one.
	switch {
	case len(typesA) == 0 && len(typesB) > 0 && constrainsValue(a):
		merged = nil
	case len(typesB) == 0 && len(typesA) > 0 && constrainsValue(b):
		merged = nil
	}

	switch len(merged) {
	case 0:
	case 1:
		result.Type = merged[0]
	default:
		result.Types = merged
	}

	// Merge metadata: prefer a, fall back to b.
	result.Title = firstNonEmpty(a.Title, b.Title)
	result.Description = firstNonEmpty(a.Description, b.Description)

	// Identity and informational keywords annotate rather than constrain, so
	// they carry first-wins like title and description. The annotator-merge
	// path (mergeSchemaFields) already keeps them; a later union merge must not
	// silently drop what survived single-input generation. References ($ref,
	// $dynamicRef) stay dropped (see the doc comment).
	result.ID = firstNonEmpty(a.ID, b.ID)
	result.Comment = firstNonEmpty(a.Comment, b.Comment)
	result.Anchor = firstNonEmpty(a.Anchor, b.Anchor)
	result.DynamicAnchor = firstNonEmpty(a.DynamicAnchor, b.DynamicAnchor)

	if a.Vocabulary != nil {
		result.Vocabulary = a.Vocabulary
	} else {
		result.Vocabulary = b.Vocabulary
	}

	if a.Default != nil {
		result.Default = a.Default
	} else {
		result.Default = b.Default
	}

	if a.Examples != nil {
		result.Examples = a.Examples
	} else {
		result.Examples = b.Examples
	}

	// Deprecated is informational and sticky; readOnly/writeOnly restrict
	// usage, so they hold only when both sides agree.
	result.Deprecated = a.Deprecated || b.Deprecated
	result.ReadOnly = a.ReadOnly && b.ReadOnly
	result.WriteOnly = a.WriteOnly && b.WriteOnly

	// Enum and const both constrain value sets (const is a single-value
	// enum), so they union with each other, independent of type. Equal
	// consts stay const; any other both-sides combination unions to enum.
	if a.Const != nil && b.Const != nil && reflect.DeepEqual(*a.Const, *b.Const) {
		result.Const = a.Const
	} else {
		result.Enum = unionEnums(enumValues(a), enumValues(b))
	}

	// Merge x-* custom annotations per key, a wins.
	result.Extra = mergeExtra(a.Extra, b.Extra)

	// When both sides constrain the type but the union is incompatible,
	// the type constraint is dropped entirely (fail open). Type-specific
	// validation keywords drop with it: keeping properties, items, or
	// bounds would still constrain instances of the now-unconstrained
	// union, failing closed.
	if len(merged) == 0 && len(typesA) > 0 && len(typesB) > 0 {
		return result
	}

	// Validation constraints: union, widen, or keep-when-equal.
	result.Pattern = keepEqual(a.Pattern, b.Pattern)
	result.Format = keepEqual(a.Format, b.Format)
	result.MultipleOf = keepEqual(a.MultipleOf, b.MultipleOf)
	result.Minimum = minPtr(a.Minimum, b.Minimum)
	result.Maximum = maxPtr(a.Maximum, b.Maximum)
	result.ExclusiveMinimum = minPtr(a.ExclusiveMinimum, b.ExclusiveMinimum)
	result.ExclusiveMaximum = maxPtr(a.ExclusiveMaximum, b.ExclusiveMaximum)
	result.MinLength = minPtr(a.MinLength, b.MinLength)
	result.MaxLength = maxPtr(a.MaxLength, b.MaxLength)
	result.MinItems = minPtr(a.MinItems, b.MinItems)
	result.MaxItems = maxPtr(a.MaxItems, b.MaxItems)
	result.MinProperties = minPtr(a.MinProperties, b.MinProperties)
	result.MaxProperties = maxPtr(a.MaxProperties, b.MaxProperties)
	result.UniqueItems = a.UniqueItems && b.UniqueItems

	// Merge object properties (union).
	if a.Properties != nil || b.Properties != nil {
		mergeProperties(result, a, b)
	}

	// Merge additionalProperties: fail-open (true wins over false).
	result.AdditionalProperties = mergeAdditionalProperties(a.AdditionalProperties, b.AdditionalProperties)

	// Merge required: intersection.
	result.Required = intersectStrings(a.Required, b.Required)

	// Merge items.
	switch {
	case a.Items != nil && b.Items != nil:
		result.Items = mergeSchemas(a.Items, b.Items)
	case a.Items != nil:
		result.Items = a.Items
	default:
		result.Items = b.Items
	}

	// Keywords with no widening rule are kept only when both sides agree
	// exactly, mirroring the pattern/format/const rule: identical
	// constraints union to themselves, anything else drops (fail open).
	result.PatternProperties = keepEqual(a.PatternProperties, b.PatternProperties)
	result.PropertyNames = keepEqual(a.PropertyNames, b.PropertyNames)
	result.DependentRequired = keepEqual(a.DependentRequired, b.DependentRequired)
	result.DependentSchemas = keepEqual(a.DependentSchemas, b.DependentSchemas)
	result.DependencySchemas = keepEqual(a.DependencySchemas, b.DependencySchemas)
	result.DependencyStrings = keepEqual(a.DependencyStrings, b.DependencyStrings)
	result.UnevaluatedProperties = keepEqual(a.UnevaluatedProperties, b.UnevaluatedProperties)
	result.UnevaluatedItems = keepEqual(a.UnevaluatedItems, b.UnevaluatedItems)
	result.PrefixItems = keepEqual(a.PrefixItems, b.PrefixItems)
	result.ItemsArray = keepEqual(a.ItemsArray, b.ItemsArray)
	result.AdditionalItems = keepEqual(a.AdditionalItems, b.AdditionalItems)
	result.Contains = keepEqual(a.Contains, b.Contains)
	result.MinContains = keepEqual(a.MinContains, b.MinContains)
	result.MaxContains = keepEqual(a.MaxContains, b.MaxContains)
	result.Definitions = keepEqual(a.Definitions, b.Definitions)
	result.Defs = keepEqual(a.Defs, b.Defs)
	result.ContentEncoding = keepEqual(a.ContentEncoding, b.ContentEncoding)
	result.ContentMediaType = keepEqual(a.ContentMediaType, b.ContentMediaType)
	result.ContentSchema = keepEqual(a.ContentSchema, b.ContentSchema)

	// The if/then/else conditional only has meaning as a unit, so it is
	// kept only when the whole trio agrees.
	if reflect.DeepEqual(a.If, b.If) &&
		reflect.DeepEqual(a.Then, b.Then) &&
		reflect.DeepEqual(a.Else, b.Else) {
		result.If, result.Then, result.Else = a.If, a.Then, a.Else
	}

	return result
}

// keepEqual returns the shared value when both sides agree exactly, or the
// zero value otherwise. A constraint present on only one side, or differing
// between sides, drops from the union (fail open).
func keepEqual[T any](a, b T) T {
	if reflect.DeepEqual(a, b) {
		return a
	}

	var zero T

	return zero
}

// enumValues returns a schema's value-set constraint: the enum, or the
// const as a single-value enum.
func enumValues(s *jsonschema.Schema) []any {
	if s.Enum != nil {
		return s.Enum
	}

	if s.Const != nil {
		return []any{*s.Const}
	}

	return nil
}

// unionEnums merges enum constraints. The value set is kept only when both
// sides constrain: an unconstrained side already allows everything, so the
// union has no enum at all.
func unionEnums(a, b []any) []any {
	if a == nil || b == nil {
		return nil
	}

	out := slices.Clone(a)

	for _, v := range b {
		if !slices.ContainsFunc(out, func(x any) bool { return reflect.DeepEqual(x, v) }) {
			out = append(out, v)
		}
	}

	return out
}

// valueSetFitsType reports whether every value in the set is valid for the
// schema's type constraint. A schema with no type accepts any value set, and a
// value whose decoded Go type maps to no JSON type is treated as a fit (fail
// open). It lets a merge skip grafting a lower-priority const or enum onto a
// higher-priority incompatible type, which would reject every value.
func valueSetFitsType(values []any, s *jsonschema.Schema) bool {
	types := typeList(s)
	if len(types) == 0 {
		return true
	}

	for _, v := range values {
		jts := jsonTypesOf(v)
		if len(jts) == 0 {
			continue
		}

		if !slices.ContainsFunc(jts, func(jt string) bool { return slices.Contains(types, jt) }) {
			return false
		}
	}

	return true
}

// jsonTypesOf returns the JSON Schema type tokens a decoded YAML/JSON value may
// satisfy. A whole-number float counts as both number and integer; an empty
// result means the Go type is unknown and the caller should treat it as a fit.
func jsonTypesOf(v any) []string {
	switch n := v.(type) {
	case nil:
		return []string{typeNull}
	case bool:
		return []string{typeBoolean}
	case string:
		return []string{typeString}
	case float32:
		return floatTypes(float64(n))
	case float64:
		return floatTypes(n)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return []string{typeNumber, typeInteger}
	case []any:
		return []string{typeArray}
	case map[string]any:
		return []string{typeObject}
	default:
		return nil
	}
}

// floatTypes classifies a float: a finite whole number satisfies both number
// and integer, anything else only number.
func floatTypes(f float64) []string {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) {
		return []string{typeNumber, typeInteger}
	}

	return []string{typeNumber}
}

// mergeExtra merges two x-* annotation maps per key, with a winning conflicts.
func mergeExtra(a, b map[string]any) map[string]any {
	if a == nil && b == nil {
		return nil
	}

	out := make(map[string]any, len(a)+len(b))
	maps.Copy(out, b)
	maps.Copy(out, a)

	return out
}

// mergeExtraInto folds src into dst in place, keeping dst's value on a key
// conflict (first-wins), and allocating dst when it is nil and src is not. It
// returns the resulting map. Unlike mergeExtra it mutates dst rather than
// allocating a fresh map, for callers filling a higher-priority schema's x-*
// annotations from a lower-priority one.
func mergeExtraInto(dst, src map[string]any) map[string]any {
	if src == nil {
		return dst
	}

	if dst == nil {
		dst = make(map[string]any, len(src))
	}

	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}

	return dst
}

// minPtr returns the smaller of two bounds, or nil if either side is
// unconstrained.
func minPtr[T cmp.Ordered](a, b *T) *T {
	if a == nil || b == nil {
		return nil
	}

	if *b < *a {
		return b
	}

	return a
}

// maxPtr returns the larger of two bounds, or nil if either side is
// unconstrained.
func maxPtr[T cmp.Ordered](a, b *T) *T {
	if a == nil || b == nil {
		return nil
	}

	if *b > *a {
		return b
	}

	return a
}

// mergeAdditionalProperties merges two additionalProperties values.
// Uses fail-open semantics: if either side allows additional properties,
// the result allows them. In JSON Schema, nil (unset) means no constraint,
// which is equivalent to allowing everything. A false schema yields to the
// other side (the union of "nothing extra" and a constraint is the
// constraint), and two constrained schemas merge recursively.
func mergeAdditionalProperties(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil && b == nil {
		return nil
	}

	// Nil means unset, which defaults to allowing everything in JSON Schema.
	// Per fail-open semantics: nil or true schema on either side means the
	// result allows additional properties.
	if a == nil || b == nil || isTrueSchema(a) || isTrueSchema(b) {
		return TrueSchema()
	}

	if isFalseSchema(a) {
		return b
	}

	if isFalseSchema(b) {
		return a
	}

	return mergeSchemas(a, b)
}

// isTrueSchema checks if a schema is the "true" schema (validates
// everything): the zero schema with no constraints, metadata, or extensions
// at all. A schema carrying only value constraints (pattern, enum, const,
// bounds) or x-* annotations is not "true" -- collapsing it would silently
// drop the constraint.
func isTrueSchema(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}

	return reflect.DeepEqual(*s, jsonschema.Schema{})
}

// isFalseSchema checks if a schema is the "false" schema (validates nothing),
// i.e. exactly {"not": {}} as produced by [FalseSchema], with no other
// constraints, metadata, or extensions.
func isFalseSchema(s *jsonschema.Schema) bool {
	if s == nil || !isTrueSchema(s.Not) {
		return false
	}

	c := *s
	c.Not = nil

	return reflect.DeepEqual(c, jsonschema.Schema{})
}

// constrainsValue reports whether a typeless schema carries a value
// constraint -- pattern, enum, bounds, sub-schemas, and the like -- rather
// than being an empty or metadata-only placeholder for a null or absent
// value. The merge uses it to tell an absent type that means the value was
// null (widen to a type-or-null union) from an absent type on an
// annotation-only constraint schema that already permits every type (so the
// union is typeless). Metadata such as title, description, default, and
// examples does not constrain the value set and is ignored.
//
// Every value-constraining keyword counts, including the object- and
// array-shaped ones (additionalProperties, contains, propertyNames,
// if/then/else, ...): a typeless schema constrained only by one of them still
// permits every other type, so widening it to a [type, null] union would
// reject values it currently accepts (fail closed).
func constrainsValue(s *jsonschema.Schema) bool {
	return s.Pattern != "" || s.Format != "" ||
		s.Enum != nil || s.Const != nil ||
		s.Minimum != nil || s.Maximum != nil ||
		s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil ||
		s.MultipleOf != nil ||
		s.MinLength != nil || s.MaxLength != nil ||
		s.MinItems != nil || s.MaxItems != nil || s.UniqueItems ||
		s.Items != nil || s.PrefixItems != nil || s.ItemsArray != nil ||
		s.AdditionalItems != nil || s.Contains != nil ||
		s.MinContains != nil || s.MaxContains != nil || s.UnevaluatedItems != nil ||
		s.MinProperties != nil || s.MaxProperties != nil ||
		s.Properties != nil || s.PatternProperties != nil ||
		s.AdditionalProperties != nil || s.PropertyNames != nil ||
		s.UnevaluatedProperties != nil ||
		len(s.Required) > 0 ||
		s.DependentRequired != nil || s.DependentSchemas != nil ||
		s.DependencySchemas != nil || s.DependencyStrings != nil ||
		s.AllOf != nil || s.AnyOf != nil || s.OneOf != nil || s.Not != nil ||
		s.If != nil || s.Then != nil || s.Else != nil
}

// intersectStrings returns the intersection of two string slices.
func intersectStrings(a, b []string) []string {
	if a == nil || b == nil {
		return nil
	}

	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}

	var result []string

	for _, s := range b {
		if set[s] {
			result = append(result, s)
		}
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// propertyKeys returns property keys in PropertyOrder, then any remaining
// keys sorted lexically so the result is deterministic.
func propertyKeys(s *jsonschema.Schema) []string {
	if s.Properties == nil {
		return nil
	}

	if len(s.PropertyOrder) > 0 {
		seen := make(map[string]bool, len(s.PropertyOrder))

		var keys []string

		for _, k := range s.PropertyOrder {
			if _, ok := s.Properties[k]; ok {
				keys = append(keys, k)
				seen[k] = true
			}
		}

		var rest []string

		for k := range s.Properties {
			if !seen[k] {
				rest = append(rest, k)
			}
		}

		slices.Sort(rest)

		return append(keys, rest...)
	}

	keys := make([]string, 0, len(s.Properties))

	for k := range s.Properties {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// mergeProperties merges properties from a and b into result using union semantics.
func mergeProperties(result, a, b *jsonschema.Schema) {
	result.Properties = make(map[string]*jsonschema.Schema)

	var order []string

	// Add all from a first.
	if a.Properties != nil {
		for _, k := range propertyKeys(a) {
			result.Properties[k] = a.Properties[k]
			order = append(order, k)
		}
	}

	// Merge from b.
	if b.Properties != nil {
		for _, k := range propertyKeys(b) {
			if existing, ok := result.Properties[k]; ok {
				result.Properties[k] = mergeSchemas(existing, b.Properties[k])
			} else {
				result.Properties[k] = b.Properties[k]
				order = append(order, k)
			}
		}
	}

	result.PropertyOrder = order
}
