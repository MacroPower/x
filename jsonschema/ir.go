package jsonschema

import (
	"reflect"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
)

// nodeKind classifies an IR node by the JSON Schema shape its render produces.
type nodeKind uint8

const (
	// A kindValue node is a leaf: a scalar, an unrestricted schema, a []byte, or
	// an opaque override/provider payload. Its payload is rendered as-is.
	kindValue nodeKind = iota
	// A kindObject node is a struct: props hold its declared properties and
	// embeds its allOf/anyOf composition branches.
	kindObject
	// A kindList node is a slice: items holds the element node.
	kindList
	// A kindTuple node is a fixed-length array: prefix holds one node per element.
	kindTuple
	// A kindMap node is a map: items holds the value node.
	kindMap
	// A kindRef node is a reference to a $defs entry named by def.
	kindRef
)

// node is one position in the generation IR. Build produces a node tree
// carrying intent; render walks it to a final [Schema]. Each node owns a bare
// payload (no null wrapper, no final $ref string). For a composite node the
// payload's sub-schema fields hold the child nodes' payloads, the same
// pointers, so field and element hooks navigate and mutate the shared bare
// leaves in place during build.
type node struct {
	payload *Schema   // bare type-derived payload; sub-schema fields hold child payloads (shared)
	def     *defEntry // non-nil iff kindRef
	items   *node     // slice element / map value
	// The authored canvas carries the field-level facts that field and element
	// hooks (the jsonschema tag, the comment provider, tag interpreters) declare:
	// annotations, value-scoped const/enum, and numeric/string/array bounds. It is
	// allocated for every field and element node and stays separate from payload,
	// so which schema a keyword lives in is its provenance: reconcileField composes
	// the final schema from payload (type-derived) plus authored (field-level). For
	// a composite field or element its sub-schema fields hold the child nodes'
	// authored canvases, the same pointers, so the tag path navigates element
	// canvases the way it navigates payloads.
	authored *Schema

	// Base is the non-null base type of a nilable container (array/object/string
	// for a slice, map, or ",string" pointer). It is empty for every other node;
	// a non-empty base is exactly what makes render encode null as a
	// ["null", base] type list rather than an anyOf[base, null] wrapper.
	base   string
	props  []nodeProp  // struct properties, declaration order
	prefix []*node     // array elements (prefixItems / itemsArray)
	embeds []embedNode // struct allOf/anyOf composition branches

	kind nodeKind
	// Nullable is the single deferred null decision for a non-kindRef node; base
	// selects the encoding render applies when it is set. A kindRef node instead
	// carries ptrNullable and resolves its decision lazily in nullableDecision,
	// so a stance recorded on the def entry after a self-referential placeholder
	// ref is built still reaches that reference.
	nullable bool
	// PtrNullable is a kindRef node's occurrence pointer-ness, combined with the
	// def entry's recorded stance in nullableDecision.
	ptrNullable bool
	// Verbatim marks a kindValue leaf whose payload a type-level hook declared
	// through [TypeSchema.Verbatim]: it is emitted exactly as authored, so render
	// and reconcile skip the null encoding for it entirely.
	verbatim bool
	isField  bool // marks a struct-field node, so reconcile applies the field const/enum bound subsumption
	// The keepElementBounds flag records the jsonschema-tag enum-on-elements
	// carve-out: a jsonschema `enum` written onto a sequence or map element keeps
	// that element's type-derived numeric bounds even though it authored an enum,
	// because the tag path writes the enum onto the bare element canvas and never
	// intends to pin the value the way an interpreter's dive does. Build stamps it
	// after the jsonschema tag runs, on any element whose canvas authored an enum;
	// reconcile then treats an authored element enum as pinning (dropping the
	// bounds) unless this flag keeps them. It is the element counterpart of the
	// field-enum rule, which reconcile derives from the merged value instead.
	keepElementBounds bool
}

// nilableContainer reports whether the node is a slice, map, or ",string"
// pointer, whose null render encodes as a ["null", base] type list. Every other
// node uses the anyOf[base, null] wrapper.
func (n *node) nilableContainer() bool {
	return n.base != ""
}

// nullableDecision resolves whether the node's occurrence admits null. A kindRef
// combines its occurrence pointer-ness with the def entry's recorded stance,
// read lazily so a stance set after a self-referential placeholder ref is built
// (the extender runs after the recursive fields it reaches) still applies. Every
// other node carries its decision directly, fixed when the node is built.
func (n *node) nullableDecision() bool {
	if n.kind == kindRef {
		return combineNullable(n.def.nullability, n.ptrNullable)
	}

	return n.nullable
}

// nodeProp is a struct property: its value node and JSON name.
type nodeProp struct {
	schema *node
	name   string
}

// embedNode is one struct composition branch. The optional flag wraps the
// branch in anyOf[branch, {}] at render (a pointer embed contributes nothing
// when nil).
type embedNode struct {
	branch   *node
	optional bool
}

// defEntry is a shared $defs entry. Every reference to the type is a kindRef
// node linking here, so the body is built once and each reference resolves its
// own null decision from its pointer-ness and the entry's stance, making $defs
// nullability order-independent.
type defEntry struct {
	typ      reflect.Type
	body     *node   // bare value node; nil while a cycle placeholder
	rendered *Schema // memoized render(body); the $defs value and null-dedup target
	baseName string  // namer output, pre-disambiguation; the provisional $ref token
	name     string  // final $defs key; set by assignDefNames before render
	// Nullability is the type's declared null-admission stance, recorded once at
	// definition time and combined with each reference's pointer-ness in
	// nullableDecision. The stance is a per-type property, so recording it on the
	// entry (rather than on the shared body) keeps it applied consistently at
	// every reference and leaves the def body bare. It is NullFromReflection for a
	// type with no stance.
	nullability Nullability
	// Rendering guards re-entrancy while body is mid-render: a self- or mutually
	// recursive body reaching its own ref sees rendered still nil and keeps its
	// null wrapper rather than deduping against an unfinished body.
	rendering bool
}

// newDefEntry registers a placeholder $defs entry for t with no body yet. A
// re-entry for t returns the existing entry, so a self- or mutually-recursive
// type resolves to one shared body and its cycle is broken.
func (g *generator) newDefEntry(t reflect.Type) *defEntry {
	if e, ok := g.typeToDef[t]; ok {
		return e
	}

	e := &defEntry{typ: t, baseName: g.schemaName(t)}
	g.typeToDef[t] = e
	g.defs = append(g.defs, e)

	return e
}

// allocCanvasTree allocates the authored canvas for a field node and every
// sequence or map element beneath it, mirroring payload's sub-schema structure
// so a composite field's canvas wires each element's canvas into its Items,
// prefixItems (or the Draft-07 items-array), or additionalProperties slot. The
// tag path then navigates element canvases the same way it navigates payloads,
// while reconcileField reads each node's own canvas for its field-level facts.
// It recurses only into elements (items and prefix), not struct properties or
// embeds, which are separate field nodes that receive their own canvases when
// their struct is built. A node whose canvas is already allocated is left alone.
func allocCanvasTree(n *node, draft Draft) {
	if n == nil || n.authored != nil {
		return
	}

	a := &Schema{}

	switch n.kind {
	case kindList:
		if n.items != nil {
			allocCanvasTree(n.items, draft)

			a.Items = n.items.authored
		}

	case kindMap:
		if n.items != nil {
			allocCanvasTree(n.items, draft)

			a.AdditionalProperties = n.items.authored
		}

	case kindTuple:
		elems := make([]*Schema, len(n.prefix))
		for i, c := range n.prefix {
			allocCanvasTree(c, draft)

			elems[i] = c.authored
		}

		if draft == Draft7 {
			a.ItemsArray = elems
		} else {
			a.PrefixItems = elems
		}

	case kindValue, kindObject, kindRef:
		// A leaf value, a struct (its properties are separate field nodes), or a
		// $ref carries no element canvas of its own.
	}

	n.authored = a
}

// markKeptElementEnums stamps keepElementBounds on every sequence or map element
// node beneath n whose canvas authored an enum, recording the jsonschema-tag
// enum-on-elements carve-out. It runs after the jsonschema tag has applied, the
// only field-level step that can author an element enum without pinning the
// value, so it marks exactly the elements whose type-derived numeric bounds must
// survive their authored enum. It mirrors allocCanvasTree's recursion, descending
// only into elements (items and prefix), never struct properties or embeds.
func markKeptElementEnums(n *node) {
	if n == nil {
		return
	}

	visit := func(elem *node) {
		if elem == nil {
			return
		}

		if elem.authored != nil && elem.authored.Enum != nil {
			elem.keepElementBounds = true
		}

		markKeptElementEnums(elem)
	}

	switch n.kind {
	case kindList, kindMap:
		visit(n.items)
	case kindTuple:
		for _, c := range n.prefix {
			visit(c)
		}

	case kindValue, kindObject, kindRef:
		// A leaf value, a struct (its properties are separate field nodes), or a
		// $ref carries no element node of its own.
	}
}

// refNode builds a kindRef node linking to e, carrying the occurrence's
// pointer-ness. The nullableDecision method later combines it with the def
// entry's recorded stance: a pointer occurrence of a NullForbidden type still
// admits no null, and a non-pointer occurrence of a NullAllowed type does. The
// combine is deferred rather than baked in here, so a stance the def entry
// records after a self-referential placeholder ref is built still reaches that
// reference. Its payload holds the
// provisional $ref string (the pre-disambiguation name), so a build-time
// interpreter or extender reading .Ref sees a real reference; render re-emits the
// final name via renderRef and grafts any siblings.
func (g *generator) refNode(e *defEntry, ptrNullable bool) *node {
	return &node{
		kind:        kindRef,
		def:         e,
		ptrNullable: ptrNullable,
		payload:     &Schema{Ref: g.profile.refPrefix() + e.baseName},
	}
}

// defineType fills t's def entry with body (if still a placeholder), records the
// type's nullability stance on the entry, and returns a reference node. The body
// is always the bare value node; the stance lives on the entry and is combined
// with each reference's pointer-ness in refNode, so $defs nullability stays
// order-independent.
func (g *generator) defineType(t reflect.Type, body *node, stance Nullability, ptrNullable bool) *node {
	e := g.newDefEntry(t)
	e.nullability = stance

	if e.body == nil {
		e.body = body
	}

	return g.refNode(e, ptrNullable)
}

// walkNodes visits every node reachable from root, following items, props,
// prefix, embeds, and each reference's def body, calling visit on each node.
// Seen guards def bodies so a self- or mutually recursive graph terminates and
// each body is descended once; on return it holds every def reached. The
// visitor may inspect n.def.
func walkNodes(root *node, seen map[*defEntry]bool, visit func(*node)) {
	if root == nil {
		return
	}

	visit(root)

	if root.def != nil && !seen[root.def] {
		seen[root.def] = true
		walkNodes(root.def.body, seen, visit)
	}

	walkNodes(root.items, seen, visit)

	for _, p := range root.props {
		walkNodes(p.schema, seen, visit)
	}

	for _, c := range root.prefix {
		walkNodes(c, seen, visit)
	}

	for _, e := range root.embeds {
		walkNodes(e.branch, seen, visit)
	}
}

// payloadRefTargets maps every $defs ref string a hook may have authored to its
// def entry: the final assigned name of each def, plus its provisional baseName
// where no final name claims it (a ref node's own payload carries the
// provisional form until render). It must be built after assignDefNames.
func (g *generator) payloadRefTargets() map[string]*defEntry {
	prefix := g.profile.refPrefix()

	targets := make(map[string]*defEntry, len(g.defs))
	for _, e := range g.defs {
		targets[prefix+e.name] = e
	}

	for _, e := range g.defs {
		if _, claimed := targets[prefix+e.baseName]; !claimed {
			targets[prefix+e.baseName] = e
		}
	}

	return targets
}

// walkReachable visits every node reachable from root like walkNodes, and
// additionally follows the raw $ref strings inside a Verbatim payload
// ([TypeSchema.Verbatim]): a verbatim payload is opaque and not node-backed, so
// a $defs reference inside it is a reachability edge only a string scan sees.
// Every other reference is a node-backed kindRef edge walkNodes already follows,
// so only verbatim payloads are scanned. Each def reached by a string hit has
// its body walked too, and onPayloadRef (when non-nil) observes every payload
// ref hit, seen or not. Verbatim payload subtrees are assumed acyclic, as
// everywhere else in the generator (hook schemas arrive JSON-decoded or
// JSON-round-trip cloned).
func (g *generator) walkReachable(
	root *node,
	seen map[*defEntry]bool,
	visit func(*node),
	onPayloadRef func(*defEntry),
) {
	targets := g.payloadRefTargets()

	var scanPayload func(s *Schema)

	visitAndScan := func(n *node) {
		visit(n)

		// Only a verbatim payload carries raw $ref strings that are reachability
		// edges; every other reference is a node-backed kindRef edge.
		if n.verbatim {
			scanPayload(n.payload)
		}
	}

	scanPayload = func(s *Schema) {
		if s == nil {
			return
		}

		if e, ok := targets[s.Ref]; ok {
			if onPayloadRef != nil {
				onPayloadRef(e)
			}

			if !seen[e] {
				seen[e] = true
				walkNodes(e.body, seen, visitAndScan)
			}
		}

		for _, child := range schemafield.Children(s) {
			scanPayload(child)
		}
	}

	walkNodes(root, seen, visitAndScan)
}

// collectReferencedDefs walks the final root node graph and returns the def
// entries reachable from it, in build order. Reachability follows both node
// links and the raw $ref strings inside a Verbatim payload, so a def whose only
// remaining reference is a raw $ref inside a verbatim schema stays alive. A def
// orphaned by a type= override or by root inlining is never reached, so it is
// dropped from the output.
func (g *generator) collectReferencedDefs(root *node) []*defEntry {
	seen := map[*defEntry]bool{}
	g.walkReachable(root, seen, func(*node) {}, nil)

	reached := make([]*defEntry, 0, len(seen))
	for _, e := range g.defs {
		if seen[e] {
			reached = append(reached, e)
		}
	}

	return reached
}
