// Package schemashape holds small helpers that read the structure of a
// generated [jsonschema.Schema]. The reflection generator and the validate-tag
// interpreter live in separate packages but inspect the same generated shapes,
// so the lookups are centralized here to keep a single source of truth.
package schemashape

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

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

	if s.AnyOf[1].Type == typename.Null {
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

	// Per-position element schemas win over a bare items schema: under Draft
	// 2020-12 a tuple sets prefixItems for the elements and items only as the
	// additional-trailing-element constraint, so returning items there would
	// drop the real element schemas. Generator output sets exactly one of these
	// fields, so the order is a no-op today and a guard against that shape.
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
