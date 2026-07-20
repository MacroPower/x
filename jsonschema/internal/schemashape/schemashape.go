// Package schemashape holds small helpers that read and reshape the structure
// of a generated [jsonschema.Schema]. The reflection generator and the
// validate-tag interpreter live in separate packages but inspect and adjust the
// same generated shapes, so the logic is centralized here to keep a single
// source of truth.
package schemashape

import (
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// AdmitsNull reports whether s already accepts a JSON null, so wrapping it in
// anyOf[s, {"type":"null"}] would add a redundant second null branch. It covers
// a true (empty) schema, a type or type list naming null, and an anyOf or oneOf
// already carrying a {"type":"null"} branch -- the last being a shape a provider
// or override may supply that the reflection generator never emits itself. A nil
// schema (an unfilled cycle placeholder) is not yet known and returns false.
func AdmitsNull(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}

	if IsEmpty(s) || s.Type == typename.Null || slices.Contains(s.Types, typename.Null) {
		return true
	}

	for _, branch := range s.AnyOf {
		if branch != nil && branch.Type == typename.Null {
			return true
		}
	}

	for _, branch := range s.OneOf {
		if branch != nil && branch.Type == typename.Null {
			return true
		}
	}

	return false
}

// NullableInnerSchema returns the value (non-null) branch of a schema produced
// for a nullable field: an anyOf of a value schema and {"type":"null"} in
// either order. It returns nil when s does not have that shape. The generator
// always emits the null branch second, but a provider or override schema may
// supply it first, so both orderings are recognized.
func NullableInnerSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}

	if len(s.AnyOf) != 2 || s.AnyOf[0] == nil || s.AnyOf[1] == nil {
		return nil
	}

	switch {
	case s.AnyOf[1].Type == typename.Null:
		return s.AnyOf[0]
	case s.AnyOf[0].Type == typename.Null:
		return s.AnyOf[1]
	default:
		return nil
	}
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

// NullableTypeListBase reports whether s is a two-element type list pairing
// "null" with one other, non-null type, returning the non-null type. A
// degenerate ["null", "null"] list is not a nullable base, so it returns false
// rather than fabricating a null value branch.
func NullableTypeListBase(s *jsonschema.Schema) (string, bool) {
	if len(s.Types) != 2 {
		return "", false
	}

	switch {
	case s.Types[0] == typename.Null && s.Types[1] != typename.Null:
		return s.Types[1], true
	case s.Types[1] == typename.Null && s.Types[0] != typename.Null:
		return s.Types[0], true
	default:
		return "", false
	}
}

// MoveConstEnum transfers any Const and Enum set on src onto dst, clearing them
// on src. Each keyword moves only when set, so a nil keyword never clobbers a
// dst keyword.
func MoveConstEnum(src, dst *jsonschema.Schema) {
	if src.Const != nil {
		dst.Const, src.Const = src.Const, nil
	}

	if src.Enum != nil {
		dst.Enum, src.Enum = src.Enum, nil
	}
}

// RelocateConstEnumToValueBranch moves any Const and Enum set beside a nullable
// wrapper onto its value (non-null) branch and returns the schema that holds
// them afterward. Const and enum test the instance value regardless of its
// type, so left on the wrapper they reject the permitted null; relocating them
// onto the value branch keeps null valid. Type-gated keywords such as minimum
// and pattern do not apply to null and stay on the wrapper.
//
// The generator builds nullable positions from bare payloads and lands
// const/enum on the value branch by construction, so this reshaping serves the
// hook-authored wrappers only: an override or provider may supply the
// anyOf[value, {"type":"null"}] wrapper or the {"type":["null", base]} type
// list itself, with the const/enum stamped beside it afterward by a tag or
// interpreter. The type-list shape cannot gate const/enum to the non-null
// type, so it is rewritten into the anyOf form when it carries either.
//
// When s is not a nullable shape, or carries neither Const nor Enum, s is
// returned unchanged.
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

// IsEmpty reports whether s has no constraining keyword set (no type, no
// applicator, no validation keyword). It is the constraint-only complement to
// the jsonschema package's exported IsTrueSchema/IsFalseSchema predicates, which
// additionally cover the annotation, identifier, render-only, and extension
// fields. The classification lives in the canonical
// [go.jacobcolvin.com/x/jsonschema/internal/schemafield] table (the union of its
// Constraint and Applicator classes); a nil schema is not empty.
func IsEmpty(s *jsonschema.Schema) bool {
	return schemafield.IsEmpty(s)
}

// HasRefSiblings reports whether a schema has any keyword set beyond just $ref.
// Any such keyword is a sibling Draft-07 validators ignore alongside $ref, so a
// constraint added by field-level processing (jsonschema struct tag or tag
// interpreter) would be silently dropped unless the $ref is wrapped in allOf.
//
// It delegates to [schemafield.HasSiblingsBesides], which reports any field
// other than Ref set on s -- constraint, applicator, annotation, identifier,
// render-only, and the Extra escape hatch alike, so every keyword that must
// survive the allOf wrap is caught, including future upstream additions. A
// non-nil empty Examples, Extra, or PropertyOrder is not a sibling: it leaves
// no trace in marshaled output, so a wrap would preserve nothing (the table's
// IsZeroInOutput semantics).
func HasRefSiblings(s *jsonschema.Schema) bool {
	return schemafield.HasSiblingsBesides(s, "Ref")
}
