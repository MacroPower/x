package constraint

import (
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

// ValueSet models the forbidden-value constraints on one schema (ne and
// required's non-zero check): the escalation from not.const to not.enum to allOf
// as rules accumulate, shared by the dialects. The allowed set (const/enum) is
// not modeled here: each writer composes it directly on its canvas, where the
// facade and the interpreters run their conflict checks against the canvas and
// the type-derived base.
type ValueSet struct {
	not       *jsonschema.Schema
	allOfNots []*jsonschema.Schema
}

// Forbid records that the value must not equal v, accumulating so several rules
// compose: the first forbidden value becomes not.const, a second distinct value
// promotes the pair to not.enum, and further values append. A not already
// carrying sibling keywords is a conjunction, so it moves under allOf beside a
// fresh not for v rather than merging into it.
func (vs *ValueSet) Forbid(v any) {
	switch {
	case vs.not == nil:
		vs.not = &jsonschema.Schema{Const: &v}

	case vs.not.Const != nil && constrainsConstOnly(vs.not):
		if valuesEqual(*vs.not.Const, v) {
			return
		}

		vs.not.Enum = []any{*vs.not.Const, v}
		vs.not.Const = nil

	case vs.not.Enum != nil && constrainsEnumOnly(vs.not):
		if slices.ContainsFunc(vs.not.Enum, func(e any) bool { return valuesEqual(e, v) }) {
			return
		}

		vs.not.Enum = append(vs.not.Enum, v)

	default:
		vs.allOfNots = append(vs.allOfNots,
			&jsonschema.Schema{Not: vs.not},
			&jsonschema.Schema{Not: &jsonschema.Schema{Const: &v}},
		)
		vs.not = nil
	}
}

// ForbidSchema forbids a whole subschema (a length range, from a collection ne),
// which cannot ride on the not.const/not.enum accumulation. It takes the single
// not slot when free, otherwise moves the existing not under allOf beside the
// new one so both apply conjunctively.
func (vs *ValueSet) ForbidSchema(forbidden *jsonschema.Schema) {
	if vs.not == nil {
		vs.not = forbidden

		return
	}

	vs.allOfNots = append(vs.allOfNots,
		&jsonschema.Schema{Not: vs.not},
		&jsonschema.Schema{Not: forbidden},
	)
	vs.not = nil
}

// SeedNot loads an existing not subschema so a subsequent [ValueSet.Forbid] or
// [ValueSet.ForbidSchema] composes with it. A caller that accumulates forbidden
// values onto a schema across separate calls seeds from the schema's current
// Not, forbids, then writes back with [ValueSet.WriteForbidden].
func (vs *ValueSet) SeedNot(not *jsonschema.Schema) { vs.not = not }

// WriteForbidden writes the accumulated forbidden state onto s: it sets Not to
// the single accumulated not (which an escalation to allOf may have cleared) and
// appends any conjoined nots, leaving other AllOf entries (an embedded struct's
// branch, say) in place. It is the write half of the SeedNot/Forbid round-trip
// that keeps the not.const -> not.enum -> allOf composition defined once here.
func (vs ValueSet) WriteForbidden(s *jsonschema.Schema) {
	s.Not = vs.not

	if len(vs.allOfNots) > 0 {
		s.AllOf = append(s.AllOf, vs.allOfNots...)
	}
}

// constrainsConstOnly reports whether s constrains nothing beyond its Const, so
// an accumulated not may promote it to an enum. Annotations ride along.
func constrainsConstOnly(s *jsonschema.Schema) bool {
	remainder := *s
	remainder.Const = nil

	return schemashape.IsEmpty(&remainder)
}

// constrainsEnumOnly is the enum analog of [constrainsConstOnly].
func constrainsEnumOnly(s *jsonschema.Schema) bool {
	remainder := *s
	remainder.Enum = nil

	return schemashape.IsEmpty(&remainder)
}

// ValuesEqual reports numeric-aware JSON-semantic equality between two
// schema-authored values, the equality the tag interpreters share for
// forbidden-value dedup and const conflict checks. It is [valuesEqual] exported.
func ValuesEqual(a, b any) bool {
	return valuesEqual(a, b)
}

// valuesEqual reports JSON-semantic equality with numeric awareness: two numbers
// compare through their exact shortest-decimal rationals, so the same value in
// different Go types (an untyped int 0 and a uint64 0) is equal, while values
// beyond float64's exact range stay distinct. Non-numeric values fall back to
// the guarded JSON equality.
func valuesEqual(a, b any) bool {
	if ar, ok := numrat.SchemaNumberRat(a); ok {
		if br, ok := numrat.SchemaNumberRat(b); ok {
			return ar.Cmp(br) == 0
		}
	}

	return jsonequal.EqualWithRat(a, nil, b)
}
