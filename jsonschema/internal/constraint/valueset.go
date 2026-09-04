package constraint

import (
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

// ValueSet models the forbidden-value constraints on one schema, the ne rule and
// required's non-zero check, escalating from not.const to not.enum to allOf as
// rules accumulate. The dialects share it. The allowed set (const/enum) is not
// modeled here; each writer composes it directly on its canvas, where the facade
// and the interpreters run their conflict checks against the canvas and the
// type-derived base.
//
// A forbidden value never loses the single not slot, whatever else arrives and
// in whatever order. The two slots are not interchangeable. The keyword table
// scopes not to the null wrapper and allOf to the value branch, so a value
// forbid that lost the slot would stop applying to a null instance, which is how
// required on a nullable field asserts anything at all.
//
// A forbidden subschema takes the slot only when it arrives first and alone.
// That case is the one asymmetry left: a subschema naming no type validates null
// vacuously, so from the wrapper it rejects null, where the same subschema beside
// any other forbid moves to allOf and does not. Every caller in this module
// names a type on the schema it forbids, which makes the two placements agree.
type ValueSet struct {
	not       *jsonschema.Schema
	allOfNots []*jsonschema.Schema
}

// Forbid records that the value must not equal v, accumulating so several rules
// compose. The first forbidden value becomes not.const, a second distinct value
// promotes the pair to not.enum, and further values append. A not already
// carrying sibling keywords is a conjunction it cannot merge into, so that not
// moves under allOf and v takes the slot.
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
		vs.allOfNots = append(vs.allOfNots, &jsonschema.Schema{Not: vs.not})
		vs.not = &jsonschema.Schema{Const: &v}
	}
}

// ForbidSchema forbids a whole subschema (a length range, from a collection ne),
// which cannot ride on the not.const/not.enum accumulation. It takes the single
// not slot when free, and otherwise moves under allOf so both apply
// conjunctively. A not already holding forbidden values keeps the slot, since
// those apply to a null instance only from there.
func (vs *ValueSet) ForbidSchema(forbidden *jsonschema.Schema) {
	switch {
	case vs.not == nil:
		vs.not = forbidden

	case forbidsValuesOnly(vs.not):
		vs.allOfNots = append(vs.allOfNots, &jsonschema.Schema{Not: forbidden})

	default:
		vs.allOfNots = append(vs.allOfNots,
			&jsonschema.Schema{Not: vs.not},
			&jsonschema.Schema{Not: forbidden},
		)
		vs.not = nil
	}
}

// forbidsValuesOnly reports whether s forbids values and nothing else. Only
// such a not keeps the slot, since the null split reads the not slot alone.
func forbidsValuesOnly(s *jsonschema.Schema) bool {
	return (s.Const != nil && constrainsConstOnly(s)) || (s.Enum != nil && constrainsEnumOnly(s))
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

// ConjoinNot composes an authored not with a type-derived one so both hold: a
// bare const or enum forbid folds into the type's not through the shared
// not.const -> not.enum -> allOf escalation, and any other authored not moves
// under allOf beside the type's. The type not is copied before mutation, so
// the pristine payload it came from never changes. It returns the composed not
// (nil when the escalation moved everything under allOf) and the allOf
// conjuncts to append.
func ConjoinNot(typeNot, authored *jsonschema.Schema) (*jsonschema.Schema, []*jsonschema.Schema) {
	seed := *typeNot
	seed.Enum = slices.Clone(seed.Enum)

	vs := ValueSet{not: &seed}

	switch {
	case authored.Const != nil && constrainsConstOnly(authored):
		vs.Forbid(*authored.Const)

	case authored.Enum != nil && constrainsEnumOnly(authored):
		for _, v := range authored.Enum {
			vs.Forbid(v)
		}

	default:
		vs.ForbidSchema(authored)
	}

	return vs.not, vs.allOfNots
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

// valuesEqual reports JSON-semantic equality with numeric awareness: both
// values convert to the document view [jsonvalue.FromDocument] gives them,
// so two numbers compare through their canonical decimal forms and the same
// value in different Go types (an untyped int 0 and a uint64 0) is equal,
// while values beyond float64's exact range stay distinct. A value with no
// document form equals nothing.
func valuesEqual(a, b any) bool {
	av, ok := jsonvalue.FromDocument(a)
	if !ok {
		return false
	}

	bv, ok := jsonvalue.FromDocument(b)
	if !ok {
		return false
	}

	return av.Equal(bv)
}
