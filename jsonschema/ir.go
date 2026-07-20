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
	payload *Schema   // bare payload; sub-schema fields hold child payloads (shared)
	def     *defEntry // non-nil iff kindRef
	items   *node     // slice element / map value
	// The snapshot is a shallow copy of payload taken before any field or element
	// hook runs, set only on a nullable field or element (the render split's only
	// reader). It captures the type-derived keywords, so render restores a value a
	// hook overwrote and tells an authored keyword (wrapper sibling) from a
	// type-derived one (value branch).
	snapshot *Schema

	// Base is the non-null base type of a nilable container (array/object/string
	// for a slice, map, or ",string" pointer). It is empty for every other node;
	// a non-empty base is exactly what makes render encode null as a
	// ["null", base] type list rather than an anyOf[base, null] wrapper.
	base   string
	props  []nodeProp  // struct properties, declaration order
	prefix []*node     // array elements (prefixItems / itemsArray)
	embeds []embedNode // struct allOf/anyOf composition branches

	kind nodeKind
	// Nullable is the single deferred null decision; base selects the encoding
	// render applies when it is set.
	nullable      bool
	isField       bool // gates the render-time nullable-field bound clearing
	boundAuthored bool // a jsonschema-tag numeric bound was authored on the field
}

// nilableContainer reports whether the node is a slice, map, or ",string"
// pointer, whose null render encodes as a ["null", base] type list. Every other
// node uses the anyOf[base, null] wrapper.
func (n *node) nilableContainer() bool {
	return n.base != ""
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
// node linking here, so the body is built once and each reference carries its
// own nullable bit, making $defs nullability order-independent.
type defEntry struct {
	typ      reflect.Type
	body     *node   // bare value node; nil while a cycle placeholder
	rendered *Schema // memoized render(body); the $defs value and null-dedup target
	baseName string  // namer output, pre-disambiguation; the provisional $ref token
	name     string  // final $defs key; set by assignDefNames before render
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

// snapshotNode records a shallow copy of n's bare payload, capturing its
// type-derived keywords before any field or element hook mutates it. Only a
// nullable node is ever split, so a non-nullable node needs no snapshot and
// skips the copy. Render reads the snapshot to restore an overwritten type value
// and to tell an authored keyword (wrapper sibling) from a type one.
func snapshotNode(n *node) {
	if !n.nullable || n.snapshot != nil {
		return
	}

	s := *n.payload
	n.snapshot = &s
}

// refNode builds a kindRef node linking to e and carrying nullable. Its payload
// holds the provisional $ref string (the pre-disambiguation name), so a
// build-time interpreter or extender reading .Ref sees a real reference; render
// re-emits the final name via renderRef and grafts any siblings.
func (g *generator) refNode(e *defEntry, nullable bool) *node {
	return &node{
		kind:     kindRef,
		def:      e,
		nullable: nullable,
		payload:  &Schema{Ref: g.draft.refPrefix() + e.baseName},
	}
}

// defineType fills t's def entry with body (if still a placeholder) and returns
// a reference node carrying nullable. The body is always the bare value node,
// and every reference keeps its own nullable bit.
func (g *generator) defineType(t reflect.Type, body *node, nullable bool) *node {
	e := g.newDefEntry(t)
	if e.body == nil {
		e.body = body
	}

	return g.refNode(e, nullable)
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
	prefix := g.draft.refPrefix()

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
// additionally follows the raw $ref strings a hook (an override, provider, or
// extender) may have authored into a payload: a payload subtree is not
// node-backed, so a $defs reference inside it is a reachability edge only a
// string scan sees. A ref node's own provisional token is not such an edge (its
// reference is the def link walkNodes follows, and render replaces the token
// with the final name), so the scan skips it rather than resolving it to
// whichever def happens to share the base name. Each def reached by a string
// hit has its body walked too, and
// onPayloadRef (when non-nil) observes every payload ref hit, seen or not.
// Payload subtrees are assumed acyclic, as everywhere else in the generator
// (hook schemas arrive JSON-decoded or JSON-round-trip cloned).
func (g *generator) walkReachable(
	root *node,
	seen map[*defEntry]bool,
	visit func(*node),
	onPayloadRef func(*defEntry),
) {
	targets := g.payloadRefTargets()
	prefix := g.draft.refPrefix()

	var scanPayload func(s *Schema, skipTopRef bool)

	visitAndScan := func(n *node) {
		visit(n)

		// A kindRef payload still carrying its own provisional token is the node's
		// reference edge itself: walkNodes already follows n.def, and render
		// replaces the token with the final name. Resolving it here through the
		// baseName fallback could hit an unrelated def sharing the base name, so
		// skip the top-level lookup and scan only the payload's children.
		skipTopRef := n.kind == kindRef && n.payload != nil &&
			n.payload.Ref == prefix+n.def.baseName
		scanPayload(n.payload, skipTopRef)
	}

	scanPayload = func(s *Schema, skipTopRef bool) {
		if s == nil {
			return
		}

		if e, ok := targets[s.Ref]; ok && !skipTopRef {
			if onPayloadRef != nil {
				onPayloadRef(e)
			}

			if !seen[e] {
				seen[e] = true
				walkNodes(e.body, seen, visitAndScan)
			}
		}

		for _, child := range schemafield.Children(s) {
			scanPayload(child, false)
		}
	}

	walkNodes(root, seen, visitAndScan)
}

// collectReferencedDefs walks the final root node graph and returns the def
// entries reachable from it, in build order. Reachability follows both node
// links and hook-authored payload $ref strings, so a def whose only remaining
// reference is a raw $ref inside an override or extender schema stays alive. A
// def orphaned by a type= override or by root inlining is never reached, so it
// is dropped from the output.
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
