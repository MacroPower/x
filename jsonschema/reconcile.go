package jsonschema

import (
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/constraint"
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

	if !n.nullable {
		if n.nilableContainer() {
			merged.Type = n.base
		}

		return &merged
	}

	// A nilable container with no const/enum encodes as a ["null", base] type
	// list carrying its authored keywords inline; its bare payload has no type
	// yet, so the empty-schema dedup (which treats a typeless payload as
	// null-admitting) must not run first.
	hasConstEnum := merged.Const != nil || merged.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		merged.Types = []string{typename.Null, n.base}

		return &merged
	}

	// An empty payload (an interface field) already admits null, so it needs no
	// second null branch: return it with its authored keywords inline.
	if !n.nilableContainer() && schemashape.IsEmpty(&merged) {
		return &merged
	}

	// Split the authored wrapper-scoped keywords onto a fresh anyOf wrapper,
	// restoring the value branch's type-derived values from the pristine payload;
	// const/enum and structural children stay on the value branch. The bound
	// subsumption already happened in resolveBounds, so the split only partitions
	// the resolved keywords.
	wrapper := splitFieldKeywords(&merged, base)

	if n.nilableContainer() {
		merged.Type = n.base
	}

	wrapper.AnyOf = []*Schema{&merged, {Type: typename.Null}}

	return wrapper
}

// reconcileRefField composes a $ref field or element. It merges the authored
// canvas onto the provisional-ref payload, then emits the final $ref: annotations
// move to the wrapper while const/enum ride the $ref on the value branch, which
// takes its own Draft-7 sibling wrap in [generator.renderRef]. When the
// referenced definition already admits null (it is empty, or a nilable container
// whose def body carries the null), the ref carries every keyword and no null
// branch is added.
func (g *generator) reconcileRefField(n *node) *Schema {
	g.renderDef(n.def)

	base := n.payload

	merged := *base
	overlayAuthored(&merged, n.authored, base)

	// Resolve bounds before renderRef so a facade- or tag-contributed bound lands
	// on merged first; the Draft-07 sibling wrap in renderRef then moves it into
	// the allOf beside the $ref rather than leaving it as an ignored sibling.
	g.resolveBounds(n, &merged, base)

	if !n.nullableDecision() {
		return g.renderRef(&merged, n.def)
	}

	if refTargetAdmitsNull(n.def.rendered) {
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
// supplies is not conflated with an authored pin, and the jsonschema-tag
// enum-on-elements carve-out (keepElementBounds) keeps the type bounds. A
// verbatim payload is emitted as authored: its const/enum never subsumes its
// own bounds, so the algebra only intersects.
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
	// authored a const or enum, mirroring the field precedence above -- unless
	// the enum is the jsonschema-tag enum-on-elements carve-out, which
	// deliberately keeps the type bounds.
	switch {
	case n.authored.Const != nil:
		return constraint.ResolveDropAll
	case n.authored.Enum != nil && !n.keepElementBounds:
		return constraint.ResolveDropKind
	default:
		return constraint.ResolveKeepKind
	}
}

// canvasAuthorsBounds reports whether the authored canvas carries any numeric or
// length/count bound keyword, gating the verbatim-path bound resolution: only an
// authored bound warrants folding a verbatim payload through the algebra.
func canvasAuthorsBounds(canvas *Schema) bool {
	return canvas.Minimum != nil || canvas.ExclusiveMinimum != nil ||
		canvas.Maximum != nil || canvas.ExclusiveMaximum != nil ||
		canvas.MultipleOf != nil ||
		canvas.MinLength != nil || canvas.MaxLength != nil ||
		canvas.MinItems != nil || canvas.MaxItems != nil ||
		canvas.MinProperties != nil || canvas.MaxProperties != nil
}

// overlayAuthored merges the authored canvas onto merged: each wrapper-scoped
// keyword the canvas set (the movableKeywords partition), plus the value-scoped
// const, enum, string-content keywords, and forbid-value allOf conjuncts. It sets
// a keyword only where the canvas authored it, so a payload keyword the canvas did
// not touch survives. Structural sub-schema fields (items, properties,
// additionalProperties) are not merged: they carry the child element canvases for
// the tag path to navigate, and each child reconciles its own canvas onto the
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
	for _, kw := range movableKeywords {
		if kw.differs(emptySchema, canvas) {
			kw.assign(canvas, merged)
		}
	}

	if canvas.Const != nil {
		merged.Const = canvas.Const
	}

	if canvas.Enum != nil {
		merged.Enum = canvas.Enum
	}

	// Pattern and format resolve with replace semantics (the authored value
	// overwrites the type-derived one) and gate only a string instance, never
	// null, so like multipleOf they are value-scoped: overlay them here and keep
	// them out of movableKeywords, so the null split leaves the resolved value on
	// the value branch instead of conjoining the authored value on the wrapper
	// with the type value restored beneath it.
	if canvas.Pattern != "" {
		merged.Pattern = canvas.Pattern
	}

	if canvas.Format != "" {
		merged.Format = canvas.Format
	}

	// The string-content keywords are value-scoped like const/enum (they gate a
	// string instance, not null), so they ride on the value branch rather than
	// the null wrapper: overlay them here rather than partitioning them in
	// splitFieldKeywords.
	if canvas.ContentEncoding != "" {
		merged.ContentEncoding = canvas.ContentEncoding
	}

	if canvas.ContentMediaType != "" {
		merged.ContentMediaType = canvas.ContentMediaType
	}

	if canvas.ContentSchema != nil {
		merged.ContentSchema = canvas.ContentSchema
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

	for _, kw := range movableKeywords {
		if !kw.differs(base, value) {
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
		if !kw.differs(emptySchema, value) {
			continue
		}

		kw.assign(value, wrapper) // wrapper gets the authored value
		kw.assign(base, value)    // value branch restores the type value
	}

	return wrapper
}

// movableKeyword is one field-authorable keyword the nullable split can move
// onto the anyOf wrapper: differs reports whether it holds different values in
// two schemas (against base it marks the keyword authored; against emptySchema it
// marks it set), and assign copies it from src onto dst, overwriting dst's value
// (which clears it when src's is the zero value). Each keyword defines both
// closures in one table entry, so a keyword cannot be movable without also being
// comparable and assignable.
type movableKeyword struct {
	differs func(a, b *Schema) bool
	assign  func(src, dst *Schema)
}

// valueKeyword builds a movableKeyword over a directly comparable field
// (strings, bools, and the pointer-identity Not). The differs comparison is by
// value deliberately: a hook that re-sets a keyword to the type's own value is
// reported as unchanged, dropping a redundant wrapper sibling that carries no
// meaning either way.
func valueKeyword[T comparable](get func(*Schema) T, set func(*Schema, T)) movableKeyword {
	return movableKeyword{
		differs: func(a, b *Schema) bool { return get(a) != get(b) },
		assign:  func(src, dst *Schema) { set(dst, get(src)) },
	}
}

// pointerKeyword builds a movableKeyword over a *T scalar field, comparing the
// pointed-at values (two nils equal, nil versus non-nil unequal) and assigning
// the pointer.
func pointerKeyword[T comparable](get func(*Schema) *T, set func(*Schema, *T)) movableKeyword {
	return movableKeyword{
		differs: func(a, b *Schema) bool { return !ptrEqual(get(a), get(b)) },
		assign:  func(src, dst *Schema) { set(dst, get(src)) },
	}
}

// movableKeywords are the field-authorable keywords the nullable split moves onto
// the anyOf wrapper. Const and enum are handled separately (they stay on the
// value branch), as are the structural sub-schema fields, which are re-rendered
// from the value node. AllOf is deliberately absent: a composite's allOf holds
// its embed branches (structural, appended by renderBase) and a forbid-value
// allOf conjunct, both of which belong on the value branch, not the wrapper.
// MultipleOf, Pattern, and Format are deliberately absent too: each resolves
// with replace semantics (the authored value overwrites the type-derived one),
// so the split's move-and-restore -- correct for the intersected bounds, where
// the restored type value is subsumed by the moved authored one -- would put
// the authored value on the wrapper AND the type value back on the value
// branch, silently conjoining the two (multipleOf 6 over a type's 4 accepting
// only multiples of 12, or disjoint patterns rejecting every string, where the
// non-nullable field accepts the authored value). The resolved value stays on
// the value branch, where null is never subject to it.
var (
	movableKeywords = []movableKeyword{
		valueKeyword(
			func(s *Schema) string { return s.Description },
			func(s *Schema, v string) { s.Description = v }),
		valueKeyword(
			func(s *Schema) string { return s.Title },
			func(s *Schema, v string) { s.Title = v }),
		{
			differs: func(a, b *Schema) bool { return string(a.Default) != string(b.Default) },
			assign:  func(src, dst *Schema) { dst.Default = src.Default },
		},
		valueKeyword(
			func(s *Schema) bool { return s.Deprecated },
			func(s *Schema, v bool) { s.Deprecated = v }),
		valueKeyword(
			func(s *Schema) bool { return s.ReadOnly },
			func(s *Schema, v bool) { s.ReadOnly = v }),
		valueKeyword(
			func(s *Schema) bool { return s.WriteOnly },
			func(s *Schema, v bool) { s.WriteOnly = v }),
		{
			// A field rarely re-authors a type's examples; a length change is a
			// sufficient authored signal (an equal-length re-set is inert either
			// way).
			differs: func(a, b *Schema) bool { return len(a.Examples) != len(b.Examples) },
			assign:  func(src, dst *Schema) { dst.Examples = src.Examples },
		},
		valueKeyword(
			func(s *Schema) string { return s.Comment },
			func(s *Schema, v string) { s.Comment = v }),
		pointerKeyword(
			func(s *Schema) *float64 { return s.Minimum },
			func(s *Schema, v *float64) { s.Minimum = v }),
		pointerKeyword(
			func(s *Schema) *float64 { return s.Maximum },
			func(s *Schema, v *float64) { s.Maximum = v }),
		pointerKeyword(
			func(s *Schema) *float64 { return s.ExclusiveMinimum },
			func(s *Schema, v *float64) { s.ExclusiveMinimum = v }),
		pointerKeyword(
			func(s *Schema) *float64 { return s.ExclusiveMaximum },
			func(s *Schema, v *float64) { s.ExclusiveMaximum = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MinLength },
			func(s *Schema, v *int) { s.MinLength = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MaxLength },
			func(s *Schema, v *int) { s.MaxLength = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MinItems },
			func(s *Schema, v *int) { s.MinItems = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MaxItems },
			func(s *Schema, v *int) { s.MaxItems = v }),
		valueKeyword(
			func(s *Schema) bool { return s.UniqueItems },
			func(s *Schema, v bool) { s.UniqueItems = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MinProperties },
			func(s *Schema, v *int) { s.MinProperties = v }),
		pointerKeyword(
			func(s *Schema) *int { return s.MaxProperties },
			func(s *Schema, v *int) { s.MaxProperties = v }),
		// Not compares by pointer identity: the split only needs to know a hook
		// swapped the sub-schema in, not whether its contents are equivalent.
		valueKeyword(
			func(s *Schema) *Schema { return s.Not },
			func(s, v *Schema) { s.Not = v }),
	}

	// EmptySchema is the zero-value schema overlayAuthored and splitFieldKeywords
	// compare against to test whether a keyword is set; it is never mutated.
	emptySchema = &Schema{}
)

// ptrEqual reports whether two pointers hold equal values, treating two nil
// pointers as equal and a nil and a non-nil as unequal.
func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
