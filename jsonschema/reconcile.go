package jsonschema

import (
	"slices"

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

	// A value-scoped const/enum stamped beside a legacy hook-supplied nullable
	// wrapper (a not-yet-migrated provider/override/extender at the field's type)
	// moves onto the wrapper's value branch, so the wrapper's permitted null is
	// not rejected. A generator-built payload is bare, so this is a no-op there.
	g.relocateAuthoredConstEnum(n, &merged)

	if !n.nullable {
		g.clearFieldBounds(n, &merged)

		if n.nilableContainer() {
			merged.Type = n.base
		}

		return &merged
	}

	// A nilable container with no const/enum encodes as a ["null", base] type
	// list carrying its authored keywords inline; its bare payload has no type
	// yet, so the AdmitsNull dedup (which treats a typeless payload as
	// null-admitting) must not run first.
	hasConstEnum := merged.Const != nil || merged.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		merged.Types = []string{typename.Null, n.base}

		return &merged
	}

	// A payload that already admits null (an empty schema, or a legacy wrapper)
	// needs no second null branch: return it with its authored keywords inline.
	if !n.nilableContainer() && schemashape.AdmitsNull(&merged) {
		return &merged
	}

	// Split the authored wrapper-scoped keywords onto a fresh anyOf wrapper,
	// restoring the value branch's type-derived values from the pristine payload;
	// const/enum and structural children stay on the value branch.
	wrapper := splitFieldKeywords(&merged, base)

	// A const (or unauthored enum, or a pinned element) subsumes the type-derived
	// bounds: drop them from the value branch and from any authored bound the
	// split moved to the wrapper, which would otherwise reject a value outside it.
	if g.pinsValue(n, &merged) {
		schemashape.ClearNumericBounds(&merged)
		schemashape.ClearNumericBounds(wrapper)
	}

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
// referenced definition already admits null, the ref carries every keyword and no
// null branch is added.
func (g *generator) reconcileRefField(n *node) *Schema {
	g.renderDef(n.def)

	base := n.payload

	merged := *base
	overlayAuthored(&merged, n.authored, base)

	if !n.nullable {
		s := g.renderRef(&merged, n.def)
		g.clearFieldBounds(n, s)

		return s
	}

	if schemashape.AdmitsNull(n.def.rendered) {
		s := g.renderRef(&merged, n.def)
		g.clearFieldBounds(n, s)

		return s
	}

	wrapper := splitFieldKeywords(&merged, base)

	if g.pinsValue(n, &merged) {
		schemashape.ClearNumericBounds(&merged)
		schemashape.ClearNumericBounds(wrapper)
	}

	value := g.renderRef(&merged, n.def)
	wrapper.AnyOf = []*Schema{value, {Type: typename.Null}}

	return wrapper
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
		// restoring the type value. The canvas never clears a payload keyword, so
		// this guard is defensive.
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
		valueKeyword(
			func(s *Schema) string { return s.Pattern },
			func(s *Schema, v string) { s.Pattern = v }),
		valueKeyword(
			func(s *Schema) string { return s.Format },
			func(s *Schema, v string) { s.Format = v }),
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
			func(s *Schema) *float64 { return s.MultipleOf },
			func(s *Schema, v *float64) { s.MultipleOf = v }),
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
