package magicschema

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// mergeSchemas merges two schemas using union semantics.
// Properties from both schemas are included. Conflicting types are widened.
func mergeSchemas(a, b *jsonschema.Schema) *jsonschema.Schema {
	if a == nil {
		return b
	}

	if b == nil {
		return a
	}

	result := &jsonschema.Schema{}

	// Merge types.
	typeA := schemaType(a)
	typeB := schemaType(b)
	merged := widenType(typeA, typeB)

	if merged != "" {
		result.Type = merged
	}

	// Merge metadata: prefer a, fall back to b.
	result.Title = firstNonEmpty(a.Title, b.Title)
	result.Description = firstNonEmpty(a.Description, b.Description)

	if a.Default != nil {
		result.Default = a.Default
	} else {
		result.Default = b.Default
	}

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

	return result
}

// schemaType returns the effective type string from a schema.
func schemaType(s *jsonschema.Schema) string {
	if s.Type != "" {
		return s.Type
	}

	if len(s.Types) == 1 {
		return s.Types[0]
	}

	return ""
}

// mergeAdditionalProperties merges two additionalProperties values.
// Uses fail-open semantics: if either side allows additional properties,
// the result allows them. In JSON Schema, nil (unset) means no constraint,
// which is equivalent to allowing everything.
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

	// Both are non-nil and non-true (e.g., both are false schemas).
	return a
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
