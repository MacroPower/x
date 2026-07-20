package jsonschema

import (
	"go.jacobcolvin.com/x/jsonschema/internal/keyword"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// render produces the final schema for a node: its base shape with the null
// encoding applied. It is the only place null wrapping, $ref strings, dedup,
// and the Draft-7 sibling wrap are decided, all from the complete graph. A
// nullable position that field or element processing may have mutated (it
// carries a snapshot) takes the split path, which owns its wrapper/value
// keyword placement (and the value branch's own Draft-7 ref wrap).
func (g *generator) render(n *node) *Schema {
	if n.nullable && n.snapshot != nil {
		return g.renderNullableSplit(n)
	}

	return g.applyNull(n, g.renderBase(n))
}

// renderBase renders a node's shape without the null encoding. For a composite
// it merges the rendered child nodes into the node's own payload rather than
// rebuilding it, so any property, allOf branch, or additionalProperties a
// build-time extender added (not node-backed) survives.
func (g *generator) renderBase(n *node) *Schema {
	switch n.kind {
	case kindValue:
		return n.payload

	case kindObject:
		for _, p := range n.props {
			n.payload.Properties[p.name] = g.render(p.schema)
		}

		for _, e := range n.embeds {
			branch := g.render(e.branch)
			if e.optional {
				branch = &Schema{AnyOf: []*Schema{branch, {}}}
			}

			n.payload.AllOf = append(n.payload.AllOf, branch)
		}

		return n.payload

	case kindList:
		n.payload.Items = g.render(n.items)

		return n.payload

	case kindTuple:
		elems := make([]*Schema, len(n.prefix))
		for i, c := range n.prefix {
			elems[i] = g.render(c)
		}

		if g.draft == Draft7 {
			n.payload.ItemsArray = elems
		} else {
			n.payload.PrefixItems = elems
		}

		return n.payload

	case kindMap:
		n.payload.AdditionalProperties = g.render(n.items)

		return n.payload

	case kindRef:
		g.renderDef(n.def)

		return g.renderRef(n.payload, n.def)

	default:
		return n.payload
	}
}

// renderRef emits a $ref schema from payload, replacing the provisional name
// with the def's final name. Under Draft-07 a $ref beside any sibling keyword
// moves into allOf, since Draft-07 readers ignore keywords next to $ref; under
// 2020-12 the siblings stay alongside.
func (g *generator) renderRef(payload *Schema, def *defEntry) *Schema {
	s := *payload
	s.Ref = g.draft.refPrefix() + def.name

	if g.draft == Draft7 && schemashape.HasRefSiblings(&s) {
		inner := &Schema{Ref: s.Ref}
		s.Ref = ""
		s.AllOf = append(s.AllOf, inner)
	}

	return &s
}

// renderDef renders a def body once and memoizes it as both the $defs value and
// the null-dedup target. While a body is mid-render (a self- or mutually
// recursive body reaching its own ref), rendered is still nil, so the in-body
// ref keeps its null wrapper rather than deduping against an unfinished body.
func (g *generator) renderDef(e *defEntry) {
	if e == nil || e.rendered != nil || e.rendering {
		return
	}

	e.rendering = true
	e.rendered = g.render(e.body)
	e.rendering = false
}

// applyNull applies a node's deferred null decision to its rendered base for
// every node that did not take the split path (all non-nullable nodes, plus
// nullable nodes without a snapshot). It is the single site that chooses the
// null encoding and performs the null-admission dedup for the inline and $ref
// paths. A nullable node carrying a snapshot never reaches here; render routes
// it to renderNullableSplit.
func (g *generator) applyNull(n *node, base *Schema) *Schema {
	if !n.nullable {
		g.clearFieldBounds(n, base)

		if n.nilableContainer() {
			base.Type = n.base
		}

		return base
	}

	hasConstEnum := base.Const != nil || base.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		base.Types = []string{typename.Null, n.base}

		return base
	}

	target := base
	if n.kind == kindRef {
		target = n.def.rendered
	}

	if schemashape.AdmitsNull(target) {
		return base
	}

	if n.nilableContainer() {
		// A const/enum cannot ride on a ["null", base] list, so flip to the
		// anyOf form with the base type on the value branch.
		base.Type = n.base
	}

	return &Schema{AnyOf: []*Schema{base, {Type: typename.Null}}}
}

// clearFieldBounds drops the type-derived numeric bounds a const or unauthored
// enum on a field subsumes, at the value schema. It runs only for struct
// fields: an element's bounds are already cleared (or deliberately kept) by the
// interpreter that stamped its const/enum, so render must not re-decide.
func (g *generator) clearFieldBounds(n *node, value *Schema) {
	if g.pinsValue(n, value) {
		schemashape.ClearNumericBounds(value)
	}
}

// renderNullableSplit encodes a nullable position that carries a snapshot (a
// struct field, or a slice/map/array element), splitting its keywords into the
// anyOf value branch (type-derived keywords plus const/enum) and the wrapper
// siblings (each authored non-const/enum keyword). A plain ["null", base]
// container with no const/enum needs no split: its authored keywords merge onto
// the type list.
func (g *generator) renderNullableSplit(n *node) *Schema {
	// A reference field splits before the Draft-7 ref wrap: annotations move to
	// the wrapper, const/enum stay with the $ref on the value branch.
	if n.kind == kindRef {
		return g.renderNullableRefField(n)
	}

	base := g.renderBase(n)

	// A nilable container is always encoded as a type list (or flipped to anyOf
	// by a const/enum below); its bare payload has no type yet, so the AdmitsNull
	// dedup, which treats a typeless payload as null-admitting, must not run
	// first.
	hasConstEnum := base.Const != nil || base.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		base.Types = []string{typename.Null, n.base}

		return base
	}

	if !n.nilableContainer() && schemashape.AdmitsNull(base) {
		return base
	}

	wrapper := g.splitFieldKeywords(base, n.snapshot)

	// A const (or unauthored enum) pins the value: drop the type-derived bounds
	// it subsumes from the value branch, and any authored bound the split moved
	// to the wrapper, which would otherwise reject a const outside it.
	if g.pinsValue(n, base) {
		schemashape.ClearNumericBounds(base)
		schemashape.ClearNumericBounds(wrapper)
	}

	if n.nilableContainer() {
		base.Type = n.base
	}

	wrapper.AnyOf = []*Schema{base, {Type: typename.Null}}

	return wrapper
}

// pinsValue reports whether a field's const or unauthored enum on the value
// branch subsumes the numeric bounds, so they are dropped. Element positions
// never pin here: their interpreter already cleared or kept the bounds.
func (g *generator) pinsValue(n *node, value *Schema) bool {
	return n.isField && (value.Const != nil || (value.Enum != nil && !n.boundAuthored))
}

// renderNullableRefField encodes a nullable $ref field. When the referenced def
// admits null the ref carries every keyword and no null branch is added;
// otherwise annotations move to the wrapper while const/enum ride the $ref on
// the value branch, which takes its own Draft-7 sibling wrap.
func (g *generator) renderNullableRefField(n *node) *Schema {
	g.renderDef(n.def)

	if schemashape.AdmitsNull(n.def.rendered) {
		return g.renderRef(n.payload, n.def)
	}

	valuePayload := *n.payload
	wrapper := g.splitFieldKeywords(&valuePayload, n.snapshot)

	g.clearFieldBounds(n, &valuePayload)

	value := g.renderRef(&valuePayload, n.def)
	wrapper.AnyOf = []*Schema{value, {Type: typename.Null}}

	return wrapper
}

// splitFieldKeywords moves each authored non-const/enum keyword off value onto a
// fresh wrapper at its mutated value, restoring value's type-derived value from
// the snapshot. It returns the wrapper; const/enum and structural children are
// left on value untouched.
func (g *generator) splitFieldKeywords(value, snapshot *Schema) *Schema {
	wrapper := &Schema{}

	for _, kw := range movableKeywords {
		if !keywordDiffers(kw, snapshot, value) {
			continue
		}

		// A keyword the value cleared relative to the snapshot (an element
		// interpreter dropping a bound its const/enum pinned) is a deliberate
		// removal, not an authored sibling: leave it cleared rather than moving
		// it out and restoring the type value.
		if !keywordSet(kw, value) {
			continue
		}

		assignKeyword(kw, value, wrapper)  // wrapper gets the mutated value
		assignKeyword(kw, snapshot, value) // value branch restores the type value
	}

	return wrapper
}

// keywordSet reports whether the named keyword is set (non-zero) on s.
func keywordSet(kw string, s *Schema) bool {
	return keywordDiffers(kw, emptySchema, s)
}

// maybeInlineRoot inlines a root $ref whose def is reached from nowhere else,
// dropping the entry. A def referenced elsewhere (self-reference or mutual
// recursion) keeps the root a $ref so those references never dangle.
func (g *generator) maybeInlineRoot(root *node) *node {
	if root.kind != kindRef {
		return root
	}

	// A nullable root renders as anyOf[{$ref}, null], not a bare $ref, so its
	// def stays referenced through the wrapper and is never inlined; only a
	// bare-$ref root (a non-pointer struct, or a pointer root under
	// WithNullable(false)) is a candidate.
	if root.nullable {
		return root
	}

	if g.referencedElsewhere(root, root.def) {
		return root
	}

	// The root is a bare-$ref (non-nullable) ref here, so the inlined body keeps
	// its own encoding: a container body already folds g.nullable into a type
	// list, and a scalar/array/struct body is bare and non-nullable like the root.
	inlined := *root.def.body

	return &inlined
}

// referencedElsewhere reports whether any node other than exclude links to def.
// The walk starts from def's own body with def pre-seen, so exclude (the root
// $ref that targets def) is skipped while everything the body reaches, including
// a self- or mutual-recursion ref back to def, is inspected.
func (g *generator) referencedElsewhere(exclude *node, def *defEntry) bool {
	found := false

	walkNodes(def.body, map[*defEntry]bool{def: true}, func(n *node) {
		if n.def == def && n != exclude {
			found = true
		}
	})

	return found
}

// movableKeywords are the field-authorable keywords the nullable split moves
// onto the anyOf wrapper. Const and enum are handled separately (they stay on
// the value branch), as are the structural sub-schema fields, which are
// re-rendered from the value node. AllOf is deliberately absent: a composite's
// allOf holds its embed branches (structural, appended by renderBase), so it
// belongs on the value branch, not the wrapper; a field never authors an allOf
// that must move outward.
var (
	movableKeywords = []string{
		keyword.Description, keyword.Title, keyword.Default,
		keyword.Deprecated, keyword.ReadOnly, keyword.WriteOnly,
		keyword.Examples, keyword.Comment,
		keyword.Pattern, keyword.Format,
		keyword.Minimum, keyword.Maximum,
		keyword.ExclusiveMinimum, keyword.ExclusiveMaximum,
		keyword.MultipleOf,
		keyword.MinLength, keyword.MaxLength,
		keyword.MinItems, keyword.MaxItems, keyword.UniqueItems,
		keyword.MinProperties, keyword.MaxProperties,
		keyword.Not,
	}

	// EmptySchema is the zero-value schema keywordSet compares against; it is
	// never mutated.
	emptySchema = &Schema{}
)

// keywordDiffers reports whether the named keyword differs in value between the
// snapshot and the mutated payload, marking it as authored by field or element
// processing. This value comparison is deliberate: a hook that re-sets a keyword
// to the type's own value is reported as unchanged, dropping a redundant wrapper
// sibling that carries no meaning either way. A nil snapshot (a node with no
// split) reports no difference.
func keywordDiffers(kw string, snap, cur *Schema) bool {
	if snap == nil {
		return false
	}

	switch kw {
	case keyword.Description:
		return snap.Description != cur.Description
	case keyword.Title:
		return snap.Title != cur.Title
	case keyword.Default:
		return string(snap.Default) != string(cur.Default)
	case keyword.Deprecated:
		return snap.Deprecated != cur.Deprecated
	case keyword.ReadOnly:
		return snap.ReadOnly != cur.ReadOnly
	case keyword.WriteOnly:
		return snap.WriteOnly != cur.WriteOnly
	case keyword.Examples:
		// A field rarely re-authors a type's examples; a length change is a
		// sufficient authored signal (an equal-length re-set is inert either way).
		return len(snap.Examples) != len(cur.Examples)
	case keyword.Comment:
		return snap.Comment != cur.Comment
	case keyword.Pattern:
		return snap.Pattern != cur.Pattern
	case keyword.Format:
		return snap.Format != cur.Format
	case keyword.Minimum:
		return !ptrEqual(snap.Minimum, cur.Minimum)
	case keyword.Maximum:
		return !ptrEqual(snap.Maximum, cur.Maximum)
	case keyword.ExclusiveMinimum:
		return !ptrEqual(snap.ExclusiveMinimum, cur.ExclusiveMinimum)
	case keyword.ExclusiveMaximum:
		return !ptrEqual(snap.ExclusiveMaximum, cur.ExclusiveMaximum)
	case keyword.MultipleOf:
		return !ptrEqual(snap.MultipleOf, cur.MultipleOf)
	case keyword.MinLength:
		return !ptrEqual(snap.MinLength, cur.MinLength)
	case keyword.MaxLength:
		return !ptrEqual(snap.MaxLength, cur.MaxLength)
	case keyword.MinItems:
		return !ptrEqual(snap.MinItems, cur.MinItems)
	case keyword.MaxItems:
		return !ptrEqual(snap.MaxItems, cur.MaxItems)
	case keyword.UniqueItems:
		return snap.UniqueItems != cur.UniqueItems
	case keyword.MinProperties:
		return !ptrEqual(snap.MinProperties, cur.MinProperties)
	case keyword.MaxProperties:
		return !ptrEqual(snap.MaxProperties, cur.MaxProperties)
	case keyword.Not:
		return snap.Not != cur.Not
	default:
		return false
	}
}

// assignKeyword copies the named keyword from src onto dst, overwriting dst's
// value (which clears it when src's is the zero value).
func assignKeyword(kw string, src, dst *Schema) {
	switch kw {
	case keyword.Description:
		dst.Description = src.Description
	case keyword.Title:
		dst.Title = src.Title
	case keyword.Default:
		dst.Default = src.Default
	case keyword.Deprecated:
		dst.Deprecated = src.Deprecated
	case keyword.ReadOnly:
		dst.ReadOnly = src.ReadOnly
	case keyword.WriteOnly:
		dst.WriteOnly = src.WriteOnly
	case keyword.Examples:
		dst.Examples = src.Examples
	case keyword.Comment:
		dst.Comment = src.Comment
	case keyword.Pattern:
		dst.Pattern = src.Pattern
	case keyword.Format:
		dst.Format = src.Format
	case keyword.Minimum:
		dst.Minimum = src.Minimum
	case keyword.Maximum:
		dst.Maximum = src.Maximum
	case keyword.ExclusiveMinimum:
		dst.ExclusiveMinimum = src.ExclusiveMinimum
	case keyword.ExclusiveMaximum:
		dst.ExclusiveMaximum = src.ExclusiveMaximum
	case keyword.MultipleOf:
		dst.MultipleOf = src.MultipleOf
	case keyword.MinLength:
		dst.MinLength = src.MinLength
	case keyword.MaxLength:
		dst.MaxLength = src.MaxLength
	case keyword.MinItems:
		dst.MinItems = src.MinItems
	case keyword.MaxItems:
		dst.MaxItems = src.MaxItems
	case keyword.UniqueItems:
		dst.UniqueItems = src.UniqueItems
	case keyword.MinProperties:
		dst.MinProperties = src.MinProperties
	case keyword.MaxProperties:
		dst.MaxProperties = src.MaxProperties
	case keyword.Not:
		dst.Not = src.Not
	}
}

// ptrEqual reports whether two pointers hold equal values, treating two nil
// pointers as equal and a nil and a non-nil as unequal.
func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
