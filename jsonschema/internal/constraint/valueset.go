package constraint

import (
	"errors"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

// ErrConflict marks two constraints that pin mutually exclusive discrete values
// -- two different consts, a second enum, or a required bool against a const of
// false. It aborts generation rather than shipping a schema that silently drops
// one constraint. Callers wrap it with their own dialect-specific phrasing so
// the message and any sentinel identity they expose stay under their control.
var ErrConflict = errors.New("conflicting value constraints")

// ValueSet models the discrete-value constraints on one schema: the allowed set
// (a const pins one value, an enum restricts to a set) and the forbidden set (ne
// and required's non-zero check). It holds the const/enum conflict detection and
// the forbidden-value escalation (not.const -> not.enum -> allOf) shared by the
// dialects.
type ValueSet struct {
	constVal  any
	not       *jsonschema.Schema
	enum      []any
	allOfNots []*jsonschema.Schema
	constSet  bool
	enumSet   bool
}

// SetConst pins the const, reporting [ErrConflict] rather than overwriting a
// const a previous rule already pinned to a different value. Equality is
// numeric-aware, so the same number arriving as different Go types does not read
// as a conflict.
func (vs *ValueSet) SetConst(v any) error {
	if vs.constSet && !valuesEqual(vs.constVal, v) {
		return fmt.Errorf("%w: %v pins a different const", ErrConflict, v)
	}

	vs.constSet = true
	vs.constVal = v

	return nil
}

// SetEnum sets the enum, reporting [ErrConflict] when an enum is already set, so
// a oneof rule and an earlier enum cannot silently overwrite one another.
func (vs *ValueSet) SetEnum(v []any) error {
	if vs.enumSet {
		return fmt.Errorf("%w: enum already set", ErrConflict)
	}

	vs.enumSet = true
	vs.enum = v

	return nil
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

// Conflict reports a discrete unsatisfiability that the set-time checks did not
// already surface. The const/enum clashes abort at [ValueSet.SetConst] and
// [ValueSet.SetEnum]; a const forbidden by an accumulated not is left as an
// impossible schema rather than an error, so this adds no further check.
func (vs ValueSet) Conflict() error { return nil }

// Point returns the value a const pins, so the numeric-bound axis can drop the
// bounds a single value subsumes.
func (vs ValueSet) Point() (any, bool) {
	if vs.constSet {
		return vs.constVal, true
	}

	return nil, false
}

// EnumValues returns the enum members and whether an enum is set, so a public
// facade's Enum getter can expose the pinned set for an interpreter's build-time
// conflict pre-check before it sets its own enum.
func (vs ValueSet) EnumValues() ([]any, bool) { return vs.enum, vs.enumSet }

// pinsConst reports whether a const is set (subsumes every numeric bound).
func (vs ValueSet) pinsConst() bool { return vs.constSet }

// pinsEnum reports whether an enum is set (subsumes only kind-derived bounds).
func (vs ValueSet) pinsEnum() bool { return vs.enumSet }

// Render writes the allowed and forbidden constraints onto the schema.
func (vs ValueSet) Render(s *jsonschema.Schema) {
	if vs.constSet {
		v := vs.constVal
		s.Const = &v
	}

	if vs.enumSet {
		s.Enum = vs.enum
	}

	if vs.not != nil {
		s.Not = vs.not
	}

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
