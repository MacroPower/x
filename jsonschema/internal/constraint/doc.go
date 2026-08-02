// Package constraint is the shared typed bound algebra for schema generation.
// The three bound sources -- Go-kind reflection, the jsonschema struct tag, and
// tag interpreters such as the validate tag -- merge their numeric and
// length/count bounds through this one model, so intersection, the
// const/enum-subsumes-bounds precedence, and the preservation of an
// unsatisfiable range are each defined once and hold regardless of the order
// the sources ran in.
//
// The scope is the bound algebra and the forbidden-value escalation, and stops
// there. Which axis a struct-tag rule targets, whether a scalar takes its native
// or its serialized form, and which operations a given shape can carry at all
// live one layer above in internal/tagmodel, whose vocabulary is struct tags --
// a vocabulary reconcile, this package's other caller, runs at a phase where
// struct tags no longer exist. Its two callers are internal/tagmodel and the
// main package's reconcile.go.
//
// The production surface is small:
//
//   - [ParseNumericBound] and [ParseSizeBound] are the single parse policies:
//     the 2^53 exact-representability check ([ErrNotRepresentable]) for numeric
//     bounds, and the exclusive-to-inclusive fold, non-negative clamp, and
//     unsatisfiable-range construction for length/count bounds. Both tag
//     dialects parse through them.
//   - [Set] aggregates one node's bound contributions -- each a [Bound] carrying
//     its [Mode] tier (Baseline for the kind-derived payload, Intersect for
//     everything authored) and [Provenance] -- via [Set.AbsorbAxes],
//     [Set.AddNumeric]/[Set.AddSize], and [Set.SetMultipleOf].
//     [Set.ResolveBounds] folds each axis into an exact-decimal [Interval]
//     ([math/big.Rat] endpoints) under a caller-chosen [ResolveMode], and
//     [Resolved.RenderBounds] writes the collapsed result back. The reconcile
//     path is the caller: it alone knows whether a node is a field or an
//     element and what its effective const/enum is, so it selects the mode (a
//     const drops every numeric bound, an enum drops only the KindDerived ones)
//     rather than this package inferring it.
//   - [ValueSet] holds the forbidden-value escalation (not.const -> not.enum ->
//     allOf) and the numeric-aware equality [ValuesEqual] the dialects share.
//     The allowed set (const/enum) is not modeled here: each writer composes it
//     directly on its canvas, where the facade and the interpreters check
//     conflicts against the canvas and the type-derived base.
//   - [CanonicalizeNumeric] is the render-time collapse that leaves one keyword
//     per numeric side (a minimum beside a weaker exclusiveMinimum drops the
//     redundant sibling).
//
// Order-independence comes from two properties working together: the facade's
// bound writes are eager intersections against the effective bound (so repeated
// interpreter rules AND in any order), and the reconcile-time fold here is
// itself commutative (Baseline contributions are overwritten per slot,
// Intersect contributions keep the tighter endpoint). An unsatisfiable interval
// (a floor above its ceiling) is rendered as its impossible bounds rather than
// loosened.
//
// The package renders onto the upstream
// github.com/google/jsonschema-go/jsonschema.Schema type (which the main package
// aliases), not the main package's alias, so both the main-package reflection
// pipeline and the sibling tag-interpreter packages can import it without a
// cycle.
package constraint
