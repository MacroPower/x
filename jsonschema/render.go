package jsonschema

import (
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// render produces the final schema for a node: its base shape with the null
// encoding applied. It is the only place null wrapping, $ref strings, dedup,
// and the Draft-7 sibling wrap are decided, all from the complete graph. A
// field or element node (the only nodes carrying an authored canvas) routes to
// [generator.reconcileField], which composes the type-derived payload with the
// field-level facts and owns its wrapper/value keyword placement; every other
// node applies the null encoding to its bare base directly.
func (g *generator) render(n *node) *Schema {
	if n.authored != nil {
		return g.reconcileField(n)
	}

	// A verbatim payload (a TypeSchema.Verbatim escape hatch) is emitted exactly
	// as authored, so the null encoding is skipped even for a pointer occurrence's
	// nullable bit.
	if n.verbatim {
		return n.payload
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

		if !g.profile.prefixItemsTuple {
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
	s.Ref = g.profile.refPrefix() + def.name

	if !g.profile.honorRefSiblings && schemashape.HasRefSiblings(&s) {
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
// every node that has no authored canvas: the root, definition bodies, embed
// branches, and type-level provider/override payloads. It chooses the null
// encoding and performs the null-admission dedup for the inline and $ref paths.
// A field or element node never reaches here; render routes it to
// [generator.reconcileField].
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

	// The only null-admitting bare shape reaching here is the empty schema (an
	// interface, or a $ref to an empty def): every hook-supplied null wrapper is
	// gone, and byte slices take the nilable-container path above. An empty
	// schema already permits null, so no second branch is added.
	target := base
	if n.kind == kindRef {
		target = n.def.rendered
	}

	if schemashape.IsEmpty(target) {
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

// pinsValue reports whether a pinned value subsumes the numeric bounds, so they
// are dropped. A struct field pins when its value branch carries a const, or an
// enum the jsonschema tag did not narrow with its own bound (boundAuthored). An
// element pins when a tag interpreter marked it via [FieldContext.PinElementValue]
// (the dropBounds signal), which the jsonschema enum-on-elements path leaves
// unset so the element keeps its type bounds. Every other node (type-level,
// root, def body) is neither a field nor an element and never pins here.
func (g *generator) pinsValue(n *node, value *Schema) bool {
	if n.isField {
		return value.Const != nil || (value.Enum != nil && !n.boundAuthored)
	}

	return n.dropBounds
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

// referencedElsewhere reports whether any node other than exclude links to def,
// or any hook-authored payload $ref string targets it. The walk starts from
// def's own body with def pre-seen, so exclude (the root $ref that targets def)
// is skipped while everything the body reaches, including a self- or
// mutual-recursion ref back to def and a raw $ref an extender authored into a
// payload, is inspected.
func (g *generator) referencedElsewhere(exclude *node, def *defEntry) bool {
	found := false

	g.walkReachable(def.body, map[*defEntry]bool{def: true}, func(n *node) {
		if n.def == def && n != exclude {
			found = true
		}
	}, func(e *defEntry) {
		if e == def {
			found = true
		}
	})

	return found
}
