// Package schemashape holds small helpers that read and reshape the structure
// of a generated [jsonschema.Schema]. The reflection generator and the
// validate-tag interpreter live in separate packages but inspect and adjust the
// same generated shapes, so the logic is centralized here to keep a single
// source of truth.
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

// RelocateConstEnumToValueBranch moves any Const and Enum keywords set on a
// nullable pointer field onto its value (non-null) branch and returns the schema
// that holds them afterward. Const and enum test the instance value regardless
// of its type, so left on the nullable wrapper they reject the permitted null;
// relocating them onto the value branch keeps null valid. Type-gated keywords
// such as minimum and pattern do not apply to null and stay on the wrapper.
//
// Two nullable shapes occur: the anyOf[value, {"type":"null"}] wrapper (nullable
// $ref and value pointers), and the {"type":["null", base]} type list that the
// generator emits for a nullable pointer to a container or a ",string"
// stringable. The type-list shape cannot gate const/enum to the non-null type,
// so it is rewritten into the anyOf form when it carries either.
//
// When s is not a nullable schema, or carries neither Const nor Enum, s is
// returned unchanged. Each keyword moves only when set, so a nil keyword never
// clobbers a value-branch keyword.
func RelocateConstEnumToValueBranch(s *jsonschema.Schema) *jsonschema.Schema {
	if s.Const == nil && s.Enum == nil {
		return s
	}

	if inner := NullableInnerSchema(s); inner != nil {
		MoveConstEnum(s, inner)

		return inner
	}

	if base, ok := NullableTypeListBase(s); ok {
		inner := &jsonschema.Schema{Type: base}
		MoveConstEnum(s, inner)

		s.Types = nil
		s.AnyOf = []*jsonschema.Schema{inner, {Type: typename.Null}}

		return inner
	}

	return s
}

// ClearNumericBounds drops the four numeric range keywords from s. Used once a
// const/enum pins the value, where the type-derived bounds are redundant and
// could reject a value set to the type's own boundary.
func ClearNumericBounds(s *jsonschema.Schema) {
	s.Minimum = nil
	s.Maximum = nil
	s.ExclusiveMinimum = nil
	s.ExclusiveMaximum = nil
}

// DropTypeBoundsForConstEnum relocates a nullable pointer's const/enum onto the
// value branch, then drops the redundant numeric bounds. The bounds may sit on
// the relocated value branch or, for a nullable pointer, on the anyOf/type-list
// wrapper, so both are cleared.
//
// A const fully pins the value, so every bound it carries is subsumed and
// dropped, even one the author set explicitly. An enum only restricts the value
// to a set, so an author-set bound narrows it further (enum ∩ bound) and is kept
// (boundAuthored); only the kind-derived bounds an enum makes redundant are
// dropped. The tag-interpreter path passes boundAuthored false: it has no
// per-keyword provenance, so it keeps the prior drop-all behavior.
func DropTypeBoundsForConstEnum(fieldSchema *jsonschema.Schema, boundAuthored bool) {
	target := RelocateConstEnumToValueBranch(fieldSchema)

	switch {
	case target.Const != nil:
		ClearNumericBounds(target)
		ClearNumericBounds(fieldSchema)

	case target.Enum != nil && !boundAuthored:
		ClearNumericBounds(target)
		ClearNumericBounds(fieldSchema)
	}
}

// MoveConstEnum transfers any Const and Enum set on src onto dst, clearing them
// on src.
func MoveConstEnum(src, dst *jsonschema.Schema) {
	if src.Const != nil {
		dst.Const, src.Const = src.Const, nil
	}

	if src.Enum != nil {
		dst.Enum, src.Enum = src.Enum, nil
	}
}

// NullableTypeListBase reports whether s is a two-element type list pairing
// "null" with one other type (the shape a nullable pointer container emits),
// returning the non-null type.
func NullableTypeListBase(s *jsonschema.Schema) (string, bool) {
	if len(s.Types) != 2 {
		return "", false
	}

	switch {
	case s.Types[0] == typename.Null:
		return s.Types[1], true
	case s.Types[1] == typename.Null:
		return s.Types[0], true
	default:
		return "", false
	}
}

// IsEmpty reports whether s has no constraining keyword set (no type, no
// applicator, no validation keyword). It is the constraint-only complement to
// the jsonschema package's exported IsTrueSchema/IsFalseSchema predicates, which
// additionally enumerate the annotation and identifier fields. Those three field
// enumerations are co-maintained: when the upstream Schema gains a field,
// revisit all of them. The jsonschema package's
// TestIsTrueSchemaRejectsEverySetField guards the exported pair. A nil schema is
// not empty.
func IsEmpty(s *jsonschema.Schema) bool {
	return s != nil &&
		s.Type == "" && s.Types == nil &&
		s.Ref == "" && s.DynamicRef == "" &&
		s.Properties == nil && s.Required == nil &&
		s.Items == nil && s.PrefixItems == nil && s.ItemsArray == nil &&
		s.AllOf == nil && s.AnyOf == nil && s.OneOf == nil && s.Not == nil &&
		s.If == nil && s.Then == nil && s.Else == nil &&
		s.Enum == nil && s.Const == nil &&
		s.Minimum == nil && s.Maximum == nil &&
		s.ExclusiveMinimum == nil && s.ExclusiveMaximum == nil &&
		s.MinLength == nil && s.MaxLength == nil &&
		s.Pattern == "" && s.Format == "" &&
		s.MinItems == nil && s.MaxItems == nil &&
		!s.UniqueItems &&
		s.MinProperties == nil && s.MaxProperties == nil &&
		s.AdditionalProperties == nil && s.AdditionalItems == nil &&
		s.PatternProperties == nil && s.PropertyNames == nil &&
		s.Contains == nil &&
		s.MultipleOf == nil &&
		s.UnevaluatedProperties == nil && s.UnevaluatedItems == nil &&
		s.DependentRequired == nil && s.DependentSchemas == nil &&
		s.DependencySchemas == nil && s.DependencyStrings == nil &&
		s.MinContains == nil && s.MaxContains == nil &&
		s.Defs == nil && s.Definitions == nil &&
		s.ContentEncoding == "" && s.ContentMediaType == "" &&
		s.ContentSchema == nil
}
