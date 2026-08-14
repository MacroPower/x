package constraint

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/numrat"
)

// SizeField selects which length/count axis a size bound applies to.
type SizeField uint8

const (
	// Length is minLength/maxLength (string length).
	Length SizeField = iota
	// Items is minItems/maxItems (array length).
	Items
	// Props is minProperties/maxProperties (object entry count).
	Props
)

// Set is the per-node aggregate of the typed bound constraints: the numeric
// interval, the length/count intervals, and multipleOf. The bound sources
// contribute into one Set instead of writing the schema fields directly, so
// [Set.ResolveBounds] applies precedence once and the result is written in one
// place ([Resolved.RenderBounds]).
type Set struct {
	multipleOf *Endpoint
	numeric    axis
	length     axis
	items      axis
	props      axis
}

// New returns an empty Set ready to absorb contributions.
func New() *Set {
	return &Set{}
}

// AddNumeric contributes one numeric bound.
func (set *Set) AddNumeric(b Bound) { set.numeric.add(b) }

// AddSize contributes the bounds of one length/count rule to the named axis.
func (set *Set) AddSize(field SizeField, bounds []Bound) {
	ax := set.axisFor(field)
	for _, b := range bounds {
		ax.add(b)
	}
}

// SetMultipleOf records a multipleOf value. A later value overwrites an earlier
// one, matching the tag's last-pair-wins rule; the caller validates it is
// positive.
func (set *Set) SetMultipleOf(val float64) {
	e := numericEndpoint(val, numrat.Float64ToRat(val))
	set.multipleOf = &e
}

// axisFor returns the axis backing a size field.
func (set *Set) axisFor(field SizeField) *axis {
	switch field {
	case Items:
		return &set.items
	case Props:
		return &set.props
	default:
		return &set.length
	}
}

// ResolveMode is the const/enum precedence choice for the numeric axis, supplied
// by the caller so it can drive the subsumption its own way: a field enum drops
// the kind-derived numeric bounds, while an element enum keeps them unless the
// element was pinned. The length/count axes always fold every tier, since no
// const or enum subsumes a size bound.
type ResolveMode uint8

const (
	// ResolveKeepKind folds every numeric tier, applying no const/enum subsumption.
	ResolveKeepKind ResolveMode = iota
	// ResolveDropKind drops the kind-derived numeric bounds and keeps the authored
	// ones (a field enum, or an element enum with an authored narrowing bound).
	ResolveDropKind
	// ResolveDropAll drops every numeric bound (a const on the value, or a pinned
	// element, subsumes them all).
	ResolveDropAll
)

// Resolved is the outcome of a resolve: the merged intervals, with the
// const/enum precedence already applied.
type Resolved struct {
	MultipleOf *Endpoint
	Numeric    Interval
	Length     Interval
	Items      Interval
	Props      Interval
}

// ResolveBounds resolves the size axes (folding every tier) and the numeric axis
// under mode, returning the merged intervals and multipleOf. It applies no
// const/enum precedence of its own: the caller supplies it through mode, since
// only the caller knows whether a node is a field or an element and what its
// effective const/enum is (the value set lives on the caller's schema, not
// here). An unsatisfiable interval is preserved in the result rather than
// loosened.
func (set *Set) ResolveBounds(mode ResolveMode) Resolved {
	r := Resolved{
		Length:     set.length.resolve(resolveFull),
		Items:      set.items.resolve(resolveFull),
		Props:      set.props.resolve(resolveFull),
		MultipleOf: set.multipleOf,
	}

	switch mode {
	case ResolveDropAll:
		// A const, or a pinned element, subsumes every numeric bound,
		// multipleOf included: against a single pinned value a divisor is
		// either redundant or contradictory, never narrowing. An enum keeps
		// it (ResolveDropKind below), since a divisor does narrow a set.
		r.Numeric = Interval{}
		r.MultipleOf = nil

	case ResolveDropKind:
		r.Numeric = set.numeric.resolve(resolveDropKind) // an enum keeps only authored bounds
	default:
		r.Numeric = set.numeric.resolve(resolveFull)
	}

	return r
}

// RenderBounds writes the resolved numeric, length, items, and properties
// intervals and multipleOf onto s, clearing each keyword field first so a stale
// bound cannot survive. It leaves the value set (const/enum/not) untouched, so
// the caller that composed it directly on the schema keeps it. It is the writer
// the reconcile path uses after resolving a node's bounds under an explicit
// [ResolveMode], where the value set already lives on the schema.
func (r *Resolved) RenderBounds(s *jsonschema.Schema) {
	renderNumeric(s, r.Numeric)
	renderSize(&s.MinLength, &s.MaxLength, r.Length)
	renderSize(&s.MinItems, &s.MaxItems, r.Items)
	renderSize(&s.MinProperties, &s.MaxProperties, r.Props)

	s.MultipleOf = nil
	if r.MultipleOf != nil {
		v := r.MultipleOf.Val
		s.MultipleOf = &v
	}
}

// AbsorbAxes folds a schema's numeric and length/count bounds into the set at the
// given tier, leaving the value set (multipleOf, const, enum, not) untouched. It
// is how the reconcile path absorbs a field's kind-derived payload (as the
// Baseline tier) and its authored canvas (as the Replace tier) into one model.
func (set *Set) AbsorbAxes(s *jsonschema.Schema, mode Mode, prov Provenance) {
	absorbBound(&set.numeric, s.Minimum, true, true, mode, prov)
	absorbBound(&set.numeric, s.ExclusiveMinimum, true, false, mode, prov)
	absorbBound(&set.numeric, s.Maximum, false, true, mode, prov)
	absorbBound(&set.numeric, s.ExclusiveMaximum, false, false, mode, prov)

	absorbSizeField(&set.length, s.MinLength, s.MaxLength, mode, prov)
	absorbSizeField(&set.items, s.MinItems, s.MaxItems, mode, prov)
	absorbSizeField(&set.props, s.MinProperties, s.MaxProperties, mode, prov)
}

// absorbBound adds a numeric bound endpoint present on a schema.
func absorbBound(ax *axis, val *float64, lower, inclusive bool, mode Mode, prov Provenance) {
	if val == nil {
		return
	}

	e := Endpoint{Rat: numrat.Float64ToRat(*val), Val: *val, Inclusive: inclusive}
	ax.add(Bound{End: e, Lower: lower, Mode: mode, Provenance: prov})
}

// absorbSizeField adds the inclusive integer floor/ceiling present on a schema.
func absorbSizeField(ax *axis, minPtr, maxPtr *int, mode Mode, prov Provenance) {
	if minPtr != nil {
		ax.add(Bound{End: intEndpoint(*minPtr), Lower: true, Mode: mode, Provenance: prov})
	}

	if maxPtr != nil {
		ax.add(Bound{End: intEndpoint(*maxPtr), Lower: false, Mode: mode, Provenance: prov})
	}
}

// CanonicalizeNumeric collapses a schema's numeric bound keywords to the single
// tighter keyword per side, so a minimum beside an exclusiveMinimum (which a min
// and a gt rule on one field produce) renders as the single keyword that implies
// the other. A schema carrying at most one bound per side is left with the same
// effective bounds. It is the shared render-time collapse the field/element
// reconcile path applies, so a redundant sibling never reaches the output on
// that path.
func CanonicalizeNumeric(s *jsonschema.Schema) {
	if s.Minimum == nil && s.ExclusiveMinimum == nil &&
		s.Maximum == nil && s.ExclusiveMaximum == nil {
		return
	}

	var ax axis

	absorbBound(&ax, s.Minimum, true, true, Intersect, Authored)
	absorbBound(&ax, s.ExclusiveMinimum, true, false, Intersect, Authored)
	absorbBound(&ax, s.Maximum, false, true, Intersect, Authored)
	absorbBound(&ax, s.ExclusiveMaximum, false, false, Intersect, Authored)

	renderNumeric(s, ax.resolve(resolveFull))
}

// renderNumeric writes a numeric interval, clearing all four keyword fields
// first so only the resolved (canonical) keyword per side survives.
func renderNumeric(s *jsonschema.Schema, iv Interval) {
	s.Minimum, s.ExclusiveMinimum = nil, nil
	s.Maximum, s.ExclusiveMaximum = nil, nil

	if iv.Lo.set() {
		v := iv.Lo.Val
		if iv.Lo.Inclusive {
			s.Minimum = &v
		} else {
			s.ExclusiveMinimum = &v
		}
	}

	if iv.Hi.set() {
		v := iv.Hi.Val
		if iv.Hi.Inclusive {
			s.Maximum = &v
		} else {
			s.ExclusiveMaximum = &v
		}
	}
}

// renderSize writes an integer interval onto a floor/ceiling *int pair, clearing
// both first. The endpoints are exact integers, so the value comes from the
// rational directly with no float64 round-trip.
func renderSize(minPtr, maxPtr **int, iv Interval) {
	*minPtr, *maxPtr = nil, nil

	if iv.Lo.set() {
		n := int(iv.Lo.Rat.Num().Int64())
		*minPtr = &n
	}

	if iv.Hi.set() {
		n := int(iv.Hi.Rat.Num().Int64())
		*maxPtr = &n
	}
}
