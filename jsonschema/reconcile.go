package jsonschema

import (
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
	"go.jacobcolvin.com/x/jsonschema/internal/keywordmeta"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// reconcileField composes the final schema for a field or element node from two
// sources whose separation is the field-level facts' provenance: the
// type-derived payload (reflection, plus any type-level hook) and the authored
// canvas (the jsonschema tag, the comment provider, tag interpreters). It merges
// the canvas onto a copy of the rendered payload, then applies the deferred null
// encoding, partitioning the authored keywords across the value branch and the
// null wrapper. The pristine payload supplies each keyword's type-derived value,
// which the split restores to the value branch; the merged copy carries the
// authored facts overlaid on top.
func (g *generator) reconcileField(n *node) *Schema {
	if n.kind == kindRef {
		return g.reconcileRefField(n)
	}

	base := g.renderBase(n)

	merged := *base
	overlayAuthored(&merged, n.authored, base)

	// A verbatim-typed field still overlays its field-level facts (description,
	// tags) onto the authored copy, but the payload is emitted with no null
	// encoding, so skip the null split and return the merged copy directly. Its
	// bounds resolve only when the canvas authored one, so the tighten-only
	// guarantee holds for a tag bound on a verbatim-typed field while a bound-free
	// canvas leaves the verbatim payload's own keywords exactly as authored,
	// never collapsed to canonical form.
	if n.verbatim {
		if canvasAuthorsBounds(n.authored) {
			g.resolveBounds(n, &merged, base)
		}

		return &merged
	}

	// Resolve the numeric/length/count bounds through the shared algebra before
	// the null split: the kind-derived payload (Baseline) is intersected with the
	// authored canvas (Replace) under the const/enum subsumption the node's role
	// chooses, then collapsed to one keyword per side. The split then partitions
	// the resolved keywords.
	g.resolveBounds(n, &merged, base)

	if !n.null.admit {
		if n.nilableContainer() {
			bareContainerType(&merged, n.containerType())
		}

		return &merged
	}

	// A nilable container with no const/enum encodes as a ["null", base] type
	// list carrying its authored keywords inline; its bare payload has no type
	// yet, so the empty-schema dedup (which treats a typeless payload as
	// null-admitting) must not run first.
	hasConstEnum := merged.Const != nil || merged.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		nullTypeList(&merged, n.containerType())

		return &merged
	}

	// A declared null type needs no branch, and an empty merged schema (an
	// interface field whose canvas constrains nothing) already admits null, so
	// either returns with its authored keywords inline.
	if !n.null.wrap || (!n.nilableContainer() && schemashape.IsEmpty(&merged)) {
		return &merged
	}

	// Split the authored wrapper-scoped keywords onto a fresh anyOf wrapper,
	// restoring the value branch's type-derived values from the pristine payload;
	// const/enum and structural children stay on the value branch. The bound
	// subsumption already happened in resolveBounds, so the split only partitions
	// the resolved keywords.
	wrapper := splitFieldKeywords(&merged, base)

	if n.nilableContainer() {
		bareContainerType(&merged, n.containerType())
	}

	wrapper.AnyOf = []*Schema{&merged, {Type: typename.Null}}

	return wrapper
}

// reconcileRefField composes a $ref field or element. It merges the authored
// canvas onto the provisional-ref payload, then emits the final $ref: annotations
// move to the wrapper while const/enum ride the $ref on the value branch, which
// takes its own Draft-07 sibling wrap in [generator.renderRef]. When the
// referenced definition already admits null (it is empty, or a nilable container
// whose def body carries the null), the decision says so and the ref carries
// every keyword with no null branch added.
func (g *generator) reconcileRefField(n *node) *Schema {
	g.renderDef(n.def)

	base := n.payload

	merged := *base
	overlayAuthored(&merged, n.authored, base)

	// Resolve bounds before renderRef so a facade- or tag-contributed bound lands
	// on merged first; the Draft-07 sibling wrap in renderRef then moves it into
	// the allOf beside the $ref rather than leaving it as an ignored sibling.
	g.resolveBounds(n, &merged, base)

	if !n.null.wrap {
		return g.renderRef(&merged, n.def)
	}

	wrapper := splitFieldKeywords(&merged, base)

	value := g.renderRef(&merged, n.def)
	wrapper.AnyOf = []*Schema{value, {Type: typename.Null}}

	return wrapper
}

// resolveBounds resolves the node's numeric, length, and count bounds through
// the shared constraint algebra and writes the collapsed result onto merged. It
// absorbs the kind-derived payload (base) as the Baseline tier and the authored
// canvas (n.authored) as the Intersect tier -- never merged, whose
// payload-copied kind bounds would be mislabeled authored -- then resolves the
// numeric axis under the const/enum subsumption the node's role selects. The
// Intersect tier makes every authored bound tighten-only: a canvas bound weaker
// than the type's own (a tag maximum the field's Go kind can never reach) never
// weakens it, for every writer at once (the jsonschema tag, validate,
// third-party interpreters). The size axes always fold every tier. It reads the
// effective const/enum from merged (post-overlay, so a type-override const/enum
// on base is honored). The axis resolution already collapses each side to one
// keyword; CanonicalizeNumeric applies the shared canonical-form pass afterward
// so the field/element numeric output stays canonical regardless of how the
// bounds were written.
func (g *generator) resolveBounds(n *node, merged, base *Schema) {
	set := constraint.New()
	set.AbsorbAxes(base, constraint.Baseline, constraint.KindDerived)
	set.AbsorbAxes(n.authored, constraint.Intersect, constraint.Authored)

	if base.MultipleOf != nil {
		set.SetMultipleOf(*base.MultipleOf)
	}

	if n.authored.MultipleOf != nil {
		set.SetMultipleOf(*n.authored.MultipleOf)
	}

	resolved := set.ResolveBounds(numericResolveMode(merged, n))
	resolved.RenderBounds(merged)
	constraint.CanonicalizeNumeric(merged)
}

// numericResolveMode selects the const/enum subsumption for the numeric axis
// from the node's role, with one precedence shared by fields and elements: a
// const drops every numeric bound; an enum drops the kind-derived bounds and
// keeps the authored ones that narrow it. A field reads the merged value
// (post-overlay, so a type-override const/enum on the payload is honored); an
// element reads its own authored canvas, so a const/enum the element's type
// supplies is not conflated with an authored pin. Which dialect authored the
// element's const/enum does not enter into it: the precedence is a property of
// the keyword, not of its provenance. A verbatim payload is emitted as authored:
// its const/enum never subsumes its own bounds, so the algebra only intersects.
func numericResolveMode(merged *Schema, n *node) constraint.ResolveMode {
	if n.verbatim {
		return constraint.ResolveKeepKind
	}

	if n.isField {
		switch {
		case merged.Const != nil:
			return constraint.ResolveDropAll
		case merged.Enum != nil:
			return constraint.ResolveDropKind
		default:
			return constraint.ResolveKeepKind
		}
	}

	// An element (or any non-field node reaching here) pins when its own canvas
	// authored a const or enum, mirroring the field precedence above.
	switch {
	case n.authored.Const != nil:
		return constraint.ResolveDropAll
	case n.authored.Enum != nil:
		return constraint.ResolveDropKind
	default:
		return constraint.ResolveKeepKind
	}
}

// canvasAuthorsBounds reports whether the authored canvas carries any numeric or
// length/count bound keyword, gating the verbatim-path bound resolution: only an
// authored bound warrants folding a verbatim payload through the algebra. The
// endpoints come from [keywordmeta.Bounds], so a keyword joins the algebra by
// declaring [keywordmeta.Keyword.Bound], not by being listed here.
func canvasAuthorsBounds(canvas *Schema) bool {
	for _, kw := range keywordmeta.Bounds {
		if kw.Set(canvas) {
			return true
		}
	}

	return false
}

// overlayAuthored merges the authored canvas onto merged: every keyword
// [keywordmeta.Authored] declares (the wrapper-scoped annotations and bounds
// alongside the value-scoped const, enum, and string-content keywords), plus the
// forbid-value allOf conjuncts. It sets a keyword only where the canvas authored
// it, so a payload keyword the canvas did not touch survives. Structural
// sub-schema fields (items, properties, additionalProperties) declare no Assign,
// which keeps them out of the set: they hold the child element canvases for the
// tag path to navigate, and each child reconciles its own canvas onto the
// rendered payload.
//
// A bare Schema canvas cannot distinguish a bool keyword authored to false
// (deprecated, readOnly, writeOnly, uniqueItems) from an unset one, so a field
// tag cannot override a type-level bool annotation the field's own type set to
// true back to false. Authoring one of these to false is meaningless in every
// other case, so the ambiguity is confined to that one legacy-seam combination.
//
// The bound keywords the overlay blind-assigns here are provisional: resolveBounds
// then re-derives them from the pristine payload and the canvas through the
// shared algebra, so an authored bound can only tighten the type's own, never
// weaken it.
func overlayAuthored(merged, canvas, base *Schema) {
	for _, kw := range keywordmeta.Authored {
		if kw.Set(canvas) {
			kw.Assign(canvas, merged)
		}
	}

	// A forbidValue accumulation lands in the canvas's allOf; append it after the
	// payload's own allOf (a composite's embed branches) so both apply
	// conjunctively. The two never coexist -- a field carrying an embed allOf is a
	// struct, which no forbid-value tag targets -- but the clone keeps the
	// payload's slice header intact regardless.
	if len(canvas.AllOf) > 0 {
		merged.AllOf = append(slices.Clone(base.AllOf), canvas.AllOf...)
	}

	// A canvas not composes with a type-derived not rather than replacing it:
	// the generic overlay above blind-assigned the canvas value over the type's
	// own forbid, which would loosen the type constraint (a field-level forbid
	// letting a type-forbidden value validate, while the nullable split keeps
	// both, so pointer-ness flipped the semantics). The shared escalation folds
	// a bare const/enum forbid into the type's not and moves anything else under
	// allOf, so both always hold, for every writer (the facade, an interpreter
	// authoring the canvas not directly).
	if canvas.Not != nil && base.Not != nil && canvas.Not != base.Not {
		not, conjuncts := constraint.ConjoinNot(base.Not, canvas.Not)
		merged.Not = not

		if len(conjuncts) > 0 {
			merged.AllOf = append(slices.Clone(merged.AllOf), conjuncts...)
		}
	}
}

// splitFieldKeywords moves each authored wrapper-scoped keyword off value onto a
// fresh wrapper, restoring value's type-derived value from base. A keyword is
// authored when it differs from base: the canvas overlaid it and it is not the
// type's own value (a tag re-setting a keyword to the type value leaves no
// wrapper sibling). It returns the wrapper; const/enum and structural children
// are left on value untouched. Base is the pristine payload, carrying the
// type-derived keyword values.
func splitFieldKeywords(value, base *Schema) *Schema {
	wrapper := &Schema{}

	for _, kw := range keywordmeta.Movable {
		if !kw.Differs(base, value) {
			continue
		}

		// A keyword the value cleared relative to base is a deliberate removal,
		// not an authored sibling: leave it cleared rather than moving it out and
		// restoring the type value. This guard is load-bearing since the bound
		// algebra resolves each numeric side to a single keyword: when an authored
		// endpoint changes which keyword represents a side (a kind Minimum=0 plus an
		// authored gt=5 renders ExclusiveMinimum and clears Minimum), the now-empty
		// Minimum is left cleared on the value branch rather than restored to the
		// kind value. That is safe because the facade is intersect-only, so the
		// authored endpoint on the same side always subsumes the kind one.
		if !kw.Set(value) {
			continue
		}

		kw.Assign(value, wrapper) // wrapper gets the authored value
		kw.Assign(base, value)    // value branch restores the type value
	}

	return wrapper
}
