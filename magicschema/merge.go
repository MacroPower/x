package magicschema

import (
	"maps"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// mergeSchemas merges two schemas using union semantics: the result accepts
// everything either input accepts. Properties from both schemas are included
// and conflicting types are widened. Validation constraints survive the merge
// only when both sides constrain -- a side without a constraint already
// permits everything, so keeping a one-sided constraint would fail closed.
// Bounds widen toward the permissive end, enums union, and exact-value
// constraints (pattern, format, const, multipleOf) are kept only when both
// sides agree. Combinators and references ($ref, allOf/anyOf/oneOf, not,
// if/then/else) are dropped entirely, which is the most permissive behavior.
func mergeSchemas(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil {
		return b
	}

	if b == nil {
		return a
	}

	result := &jsonschema.Schema{}

	// Merge types with widening.
	switch merged := widenTypeList(typeList(a), typeList(b)); len(merged) {
	case 0:
	case 1:
		result.Type = merged[0]
	default:
		result.Types = merged
	}

	// Merge metadata: prefer a, fall back to b.
	result.Title = firstNonEmpty(a.Title, b.Title)
	result.Description = firstNonEmpty(a.Description, b.Description)

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

	// Validation constraints: union, widen, or keep-when-equal.
	result.Enum = unionEnums(a.Enum, b.Enum)

	if a.Const != nil && b.Const != nil && reflect.DeepEqual(*a.Const, *b.Const) {
		result.Const = a.Const
	}

	if a.Pattern == b.Pattern {
		result.Pattern = a.Pattern
	}

	if a.Format == b.Format {
		result.Format = a.Format
	}

	result.MultipleOf = equalFloat64Ptr(a.MultipleOf, b.MultipleOf)
	result.Minimum = minFloat64Ptr(a.Minimum, b.Minimum)
	result.Maximum = maxFloat64Ptr(a.Maximum, b.Maximum)
	result.ExclusiveMinimum = minFloat64Ptr(a.ExclusiveMinimum, b.ExclusiveMinimum)
	result.ExclusiveMaximum = maxFloat64Ptr(a.ExclusiveMaximum, b.ExclusiveMaximum)
	result.MinLength = minIntPtr(a.MinLength, b.MinLength)
	result.MaxLength = maxIntPtr(a.MaxLength, b.MaxLength)
	result.MinItems = minIntPtr(a.MinItems, b.MinItems)
	result.MaxItems = maxIntPtr(a.MaxItems, b.MaxItems)
	result.MinProperties = minIntPtr(a.MinProperties, b.MinProperties)
	result.MaxProperties = maxIntPtr(a.MaxProperties, b.MaxProperties)
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

	// Merge x-* custom annotations per key, a wins.
	result.Extra = mergeExtra(a.Extra, b.Extra)

	return result
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

// minFloat64Ptr returns the smaller of two bounds, or nil if either side is
// unconstrained.
func minFloat64Ptr(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}

	if *b < *a {
		return b
	}

	return a
}

// maxFloat64Ptr returns the larger of two bounds, or nil if either side is
// unconstrained.
func maxFloat64Ptr(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}

	if *b > *a {
		return b
	}

	return a
}

// equalFloat64Ptr returns the shared value when both sides agree, nil
// otherwise.
func equalFloat64Ptr(a, b *float64) *float64 {
	if a == nil || b == nil || *a != *b {
		return nil
	}

	return a
}

// minIntPtr returns the smaller of two bounds, or nil if either side is
// unconstrained.
func minIntPtr(a, b *int) *int {
	if a == nil || b == nil {
		return nil
	}

	if *b < *a {
		return b
	}

	return a
}

// maxIntPtr returns the larger of two bounds, or nil if either side is
// unconstrained.
func maxIntPtr(a, b *int) *int {
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

// isTrueSchema checks if a schema is the "true" schema (validates everything).
func isTrueSchema(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}

	return s.Not == nil &&
		s.Type == "" &&
		len(s.Types) == 0 &&
		s.Properties == nil &&
		s.Items == nil &&
		len(s.AllOf) == 0 &&
		len(s.AnyOf) == 0 &&
		len(s.OneOf) == 0
}

// isFalseSchema checks if a schema is the "false" schema (validates nothing),
// i.e. exactly {"not": {}} as produced by [FalseSchema].
func isFalseSchema(s *jsonschema.Schema) bool {
	if s == nil || s.Not == nil {
		return false
	}

	return isTrueSchema(s.Not) &&
		s.Type == "" &&
		len(s.Types) == 0 &&
		s.Properties == nil &&
		s.Items == nil &&
		len(s.AllOf) == 0 &&
		len(s.AnyOf) == 0 &&
		len(s.OneOf) == 0
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
// keys in an undefined order.
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

		for k := range s.Properties {
			if !seen[k] {
				keys = append(keys, k)
			}
		}

		return keys
	}

	keys := make([]string, 0, len(s.Properties))

	for k := range s.Properties {
		keys = append(keys, k)
	}

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
