package jsonschema

import (
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// render produces the final schema for a node: its base shape with the null
// encoding applied. It is the only place null wrapping, $ref strings, dedup,
// and the Draft-07 sibling wrap are decided, all from the complete graph. A
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
// it fills the node's own payload slots from the rendered child nodes. A slot
// a build-time extender authored as a literal (a property replaced with the
// extender's own schema, say) is no longer node-backed, so it survives in the
// payload as written.
func (g *generator) renderBase(n *node) *Schema {
	switch n.kind {
	case kindValue:
		return n.payload

	case kindObject:
		if len(n.props) > 0 && n.payload.Properties == nil {
			n.payload.Properties = make(map[string]*Schema, len(n.props))
		}

		for i := range n.props {
			n.payload.Properties[n.props[i].name] = g.render(n.props[i].schema)
		}

		for _, e := range n.embeds {
			branch := g.render(e.branch)
			if e.optional {
				branch = &Schema{AnyOf: []*Schema{branch, {}}}
			}

			n.payload.AllOf = append(n.payload.AllOf, branch)
		}

		// An embedded fallback's value node fills whichever extra-member slot
		// the build chose.
		if n.items != nil {
			switch n.fallback {
			case slotAdditional:
				n.payload.AdditionalProperties = g.render(n.items)
			case slotUnevaluated:
				n.payload.UnevaluatedProperties = g.render(n.items)
			case slotNone:
			}
		}

		return n.payload

	case kindList:
		if n.items != nil {
			n.payload.Items = g.render(n.items)
		}

		return n.payload

	case kindTuple:
		if len(n.prefix) == 0 {
			return n.payload
		}

		elems := make([]*Schema, len(n.prefix))
		for i, c := range n.prefix {
			elems[i] = g.render(c)
		}

		if g.profile.prefixItemsTuple {
			n.payload.PrefixItems = elems
		} else {
			n.payload.ItemsArray = elems
		}

		return n.payload

	case kindMap:
		if n.items != nil {
			n.payload.AdditionalProperties = g.render(n.items)
		}

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

// applyNull applies a node's null decision to its rendered base for every
// node that has no authored canvas: the root, definition bodies, embed
// branches, and type-level provider/override payloads. It chooses the null
// encoding from the container kind and the presence of const/enum, and adds
// no branch where the decision says the target already admits null or where
// an inline leaf is unrestricted (an interface, whose {} admits null). A field
// or element node never reaches here; render routes it to
// [generator.reconcileField].
func (g *generator) applyNull(n *node, base *Schema) *Schema {
	if !n.null.admit {
		if n.nilableContainer() {
			bareContainerType(base, n.containerType())
		}

		return base
	}

	hasConstEnum := base.Const != nil || base.Enum != nil
	if n.nilableContainer() && !hasConstEnum {
		nullTypeList(base, n.containerType())

		return base
	}

	if !n.null.wrap || (!n.nilableContainer() && schemashape.IsEmpty(base)) {
		return base
	}

	if n.nilableContainer() {
		// A const/enum cannot ride on a ["null", base] list, so flip to the
		// anyOf form with the base type on the value branch.
		bareContainerType(base, n.containerType())
	}

	// Record the wrap on the node, so the defaults target resolution can tell
	// this generator-emitted anyOf from one the base itself authored.
	n.nullWrapped = true

	return &Schema{AnyOf: []*Schema{base, {Type: typename.Null}}}
}

// nullTypeList applies the ["null", base] type-list encoding to a nilable
// container's schema, honoring a hook-authored type slot: an authored Types
// list gains "null" when absent, and an authored Type replaces the
// kind-derived base as the list's value type. Exactly one of Type/Types is
// set afterward, so the schema always marshals.
func nullTypeList(s *Schema, base string) {
	if s.Types != nil {
		if !slices.Contains(s.Types, typename.Null) {
			s.Types = append([]string{typename.Null}, s.Types...)
		}

		return
	}

	if s.Type != "" {
		base = s.Type
	}

	s.Type = ""
	s.Types = []string{typename.Null, base}
}

// bareContainerType restores a nilable container's bare type, honoring a
// hook-authored type slot: an authored Types list already carries the type
// constraint and an authored Type wins over the kind-derived base, so the
// base fills the slot only when both are empty.
func bareContainerType(s *Schema, base string) {
	if s.Types == nil && s.Type == "" {
		s.Type = base
	}
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
	// bare-$ref root (a non-pointer struct, or a pointer root whose type
	// declares NullForbidden) is a candidate.
	if root.null.admit {
		return root
	}

	if g.referencedElsewhere(root, root.def) {
		return root
	}

	// The root is a bare-$ref (non-nullable) ref here, so the inlined body keeps
	// its own encoding: a container body already folds its null into a type
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
