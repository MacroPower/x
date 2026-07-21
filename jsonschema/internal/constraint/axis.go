package constraint

// Mode is how a bound contribution combines with those from other sources. It
// encodes precedence as data: resolution folds contributions by Mode, not by the
// order the sources ran in.
type Mode uint8

const (
	// Baseline is the weakest tier: the Go-kind-derived bound. Every Intersect
	// tightens the result.
	Baseline Mode = iota
	// Intersect tightens whatever the slot resolved to, so an authored bound (the
	// jsonschema tag, a validate-tag rule, an interpreter facade contribution) is
	// ANDed and never weakens a stronger bound.
	Intersect
)

// Provenance records where a bound came from, so the const/enum precedence can
// drop the redundant ones: a const subsumes every bound, while an enum subsumes
// only the kind-derived ones and keeps the authored bounds that narrow it.
type Provenance uint8

const (
	// KindDerived is a Go-kind reflection bound, dropped by both const and enum.
	KindDerived Provenance = iota
	// Authored is a tag- or interpreter-supplied bound, dropped only by const.
	Authored
)

// Bound is one endpoint contribution from a single source: the endpoint itself,
// which side it bounds, how it combines, and where it came from.
type Bound struct {
	End        Endpoint
	Lower      bool
	Mode       Mode
	Provenance Provenance
}

// resolveMode selects which contribution tiers an [axis] resolve folds in. It
// makes the const/enum subsumption express its bound filtering as data rather
// than as separate code paths.
type resolveMode uint8

const (
	// The resolveFull mode folds every contribution: the baseline, tightened by
	// every intersect. It is the ordinary resolve.
	resolveFull resolveMode = iota
	// The resolveDropKind mode drops the KindDerived contributions and keeps only
	// the Authored ones, which is how an enum keeps its narrowing bound.
	resolveDropKind
)

// axis accumulates the raw bound contributions for one interval, kept
// unresolved so the const/enum subsumption can re-resolve under a provenance
// filter.
type axis struct {
	bounds []Bound
}

// add appends a contribution.
func (a *axis) add(b Bound) { a.bounds = append(a.bounds, b) }

// resolve folds the contributions into a single [Interval] under mode.
func (a axis) resolve(mode resolveMode) Interval {
	return Interval{
		Lo: a.resolveSide(true, mode),
		Hi: a.resolveSide(false, mode),
	}
}

// resolveSide resolves one side by taking the tighter of its inclusive and
// exclusive slots, so a minimum and an exclusiveMinimum collapse to the single
// stronger keyword the schema renders.
func (a axis) resolveSide(lower bool, mode resolveMode) Endpoint {
	incl := a.resolveSlot(lower, true, mode)
	excl := a.resolveSlot(lower, false, mode)

	return tighter(lower, incl, excl)
}

// resolveSlot resolves the contributions to one (side, inclusivity) slot in
// precedence order regardless of insertion order: the baseline (a later baseline
// on the same slot overwrites an earlier one), tightened by every intersect. The
// mode filters by [Provenance]: a dropKind resolve skips every KindDerived
// contribution, so only the authored bounds reach the result.
func (a axis) resolveSlot(lower, inclusive bool, mode resolveMode) Endpoint {
	var cur Endpoint

	matches := func(b Bound, tier Mode) bool {
		if mode == resolveDropKind && b.Provenance == KindDerived {
			return false
		}

		return b.Lower == lower && b.End.Inclusive == inclusive && b.Mode == tier
	}

	for _, b := range a.bounds {
		if matches(b, Baseline) {
			cur = b.End
		}
	}

	for _, b := range a.bounds {
		if matches(b, Intersect) {
			if !cur.set() {
				cur = b.End
			} else {
				cur = tighter(lower, cur, b.End)
			}
		}
	}

	return cur
}
