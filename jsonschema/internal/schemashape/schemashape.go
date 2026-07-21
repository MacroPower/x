// Package schemashape holds small structural helpers over a generated
// [jsonschema.Schema]: the empty-schema and $ref-sibling predicates that derive
// from the canonical [go.jacobcolvin.com/x/jsonschema/internal/schemafield]
// table, and the numeric-bound clear a pinned const/enum triggers. The
// reflection generator and the validate-tag interpreter live in separate
// packages but inspect the same generated shapes, so the logic is centralized
// here to keep a single source of truth.
package schemashape

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

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
