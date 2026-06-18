// Package schemashape holds small helpers that read the structure of a
// generated [jsonschema.Schema]. The reflection generator and the validate-tag
// interpreter live in separate packages but inspect the same generated shapes,
// so the lookups are centralized here to keep a single source of truth.
package schemashape

import "github.com/google/jsonschema-go/jsonschema"

// typeNameNull is the JSON Schema type name for the null branch of a nullable
// schema.
const typeNameNull = "null"

// NullableInnerSchema returns the value (non-null) branch of a schema produced
// for a nullable field: an anyOf of a value schema and {"type":"null"}. It
// returns nil when s does not have that exact shape.
func NullableInnerSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}

	if len(s.AnyOf) != 2 || s.AnyOf[0] == nil || s.AnyOf[1] == nil {
		return nil
	}

	if s.AnyOf[1].Type == typeNameNull {
		return s.AnyOf[0]
	}

	return nil
}

// ItemSchemas returns the per-element schemas of a generated slice or fixed
// array field schema: Items for slices, prefixItems (Draft 2020-12) or the
// items-as-array form (Draft-07) for fixed arrays. A nullable pointer field
// wraps the value schema in anyOf[value, null]; the lookup follows that wrapper
// first. A []byte field (a base64 string) has no element schema and yields nil.
func ItemSchemas(s *jsonschema.Schema) []*jsonschema.Schema {
	if inner := NullableInnerSchema(s); inner != nil {
		s = inner
	}

	switch {
	case s.Items != nil:
		return []*jsonschema.Schema{s.Items}
	case len(s.PrefixItems) > 0:
		return s.PrefixItems
	case len(s.ItemsArray) > 0:
		return s.ItemsArray
	default:
		return nil
	}
}
