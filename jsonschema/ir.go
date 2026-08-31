package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
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
	base string
	// TagView is the corrected type view handed to field-level hooks (the
	// jsonschema tag and tag interpreters) when the payload alone understates
	// the type: a ",string" pointer's payload is empty (the coercion lives on
	// base for the null-branch split), so hooks dispatch on this
	// {"type":["null","string"]} view instead. Nil for every other node.
	tagView *Schema
	// The origin field names the field position this node occupies, for the
	// reports in [generator.checkNullLiterals]. A field node carries it, and so
	// does every element node beneath that field. Nil for every other node.
	origin *fieldOrigin
	// The nullKeys field records the keys the field's jsonschema tag took a
	// null literal for, in tag order. Empty for every node whose tag spelled
	// none.
	nullKeys []string
	props    []nodeProp  // struct properties, declaration order
	prefix   []*node     // array elements (prefixItems / itemsArray)
	embeds   []embedNode // struct allOf/anyOf composition branches

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
	// NullWrapped records that applyNull emitted the anyOf[base, null] wrapper
	// for this node's render, so the rendered schema's two-element AnyOf is the
	// generator's null encoding. A nullable node whose base already admits null
	// (a hook schema naming "null" in its type list) skips the wrapper and
	// keeps the flag unset, so a hook-authored anyOf is never mistaken for the
	// wrapper; [generator.rootDefaultsTarget] resolves through the wrapper only
	// on this flag.
	nullWrapped bool
	isField     bool // marks a struct-field node, so reconcile applies the field const/enum bound subsumption
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

// fieldOrigin names the field position a node occupies: the struct declaring
// the field, the field's JSON name, and whether the node is an element beneath
// that field rather than the field itself. A field-level writer can commit to a
// null the occurrence's final decision later withdraws, so the generator
// carries this record to a pass that runs once every decision is final. The
// element flag holds no index, so a tuple element's report names the field
// rather than the position within it.
type fieldOrigin struct {
	parent  reflect.Type // the struct whose schema carries the field
	field   string       // the field's JSON name
	element bool         // an element beneath the field, not the field itself
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

// assignFieldOrigins records the field position on a field node and on every
// sequence or map element beneath it, so [generator.checkNullLiterals] can name
// the field a late-refused null literal sits in. It descends only into
// elements (items and prefix), wherever a node carries them. A struct property
// or an embed is a separate field node, which takes its own origin when the
// generator builds its struct. The walk allocates one element origin per field
// that has elements and hands the same pointer to every depth, so a [][]*T
// inner element reads the same origin as the outer one.
func assignFieldOrigins(n *node, origin *fieldOrigin) {
	if n == nil {
		return
	}

	n.origin = origin

	if n.items == nil && len(n.prefix) == 0 {
		return
	}

	elem := origin
	if !elem.element {
		elem = &fieldOrigin{parent: origin.parent, field: origin.field, element: true}
	}

	assignFieldOrigins(n.items, elem)

	for _, c := range n.prefix {
		assignFieldOrigins(c, elem)
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

// checkNullLiterals re-checks the null literals a field's two writers
// committed. It compares each against the occurrence's final null decision and
// reports the first one that decision refuses. A self- or mutually recursive
// field resolves against a $defs entry still being built, so a [Nullability]
// stance a type-level hook records for that type lands after both field-level
// writers have read the decision. [node.nullableDecision] reads the stance
// lazily, and this pass is where those early answers and the late stance meet.
//
// Two writers reach this pass. The jsonschema tag records on the node the keys
// it took a literal for, and a tag interpreter writes its literal onto the
// authored canvas, which canvasNullLiteral scans. The pass reports a field
// carrying both faults on its tag key.
//
// The walk is [generator.walkReachable] rather than [walkNodes], so the check
// covers exactly the defs render emits. A def whose only surviving reference is
// a raw $ref string an extender authored into a payload is reachable by that
// scan alone. The visitor returns nothing, so the pass keeps the first report
// and lets the walk finish. Walk order is deterministic, which is what makes
// the first report name one occurrence rather than an arbitrary one.
func (g *generator) checkNullLiterals(root *node) error {
	var reported error

	g.walkReachable(root, map[*defEntry]bool{}, func(n *node) {
		if reported == nil {
			reported = nullLiteralReport(n)
		}
	}, nil)

	return reported
}

// nullLiteralReport returns the rejection a node's recorded null literals earn
// against its final null decision, or nil when the node has none to answer for.
//
// The check covers a reference and nothing else. Every other node has its
// decision fixed in schemaForType before buildFieldSchema reads it, so tagparse
// refuses the tag at parse time and the node records nothing. The generator
// runs extendTypeSchema inside buildStructSchema ahead of defineType, so the
// cycle placeholder is the one window a stance lands in afterwards. Holding
// the canvas scan to that same scope is a deliberate narrowing. An interpreter
// that writes a null onto a node whose decision was already final has a bug of
// its own, and this pass does not look for it. A type= override rebuilds the
// field as a non-reference node carrying no origin, while the element nodes it
// keeps carry theirs, so the pass still reports an element that stayed a
// reference.
func nullLiteralReport(n *node) error {
	if n.kind != kindRef || n.origin == nil || n.nullableDecision() {
		return nil
	}

	if len(n.nullKeys) > 0 {
		return fmt.Errorf("%s field %q: jsonschema tag: key %q: %w %s",
			n.origin.parent, n.origin.field, n.nullKeys[0],
			tagmodel.ErrNullNotAdmitted, n.def.typ)
	}

	keyword := canvasNullLiteral(n.authored)
	if keyword == "" {
		return nil
	}

	if n.origin.element {
		return fmt.Errorf(
			"%s field %q: element: authored canvas: keyword %q: %w %s",
			n.origin.parent, n.origin.field, keyword,
			tagmodel.ErrNullNotAdmitted, n.def.typ)
	}

	return fmt.Errorf("%s field %q: authored canvas: keyword %q: %w %s",
		n.origin.parent, n.origin.field, keyword,
		tagmodel.ErrNullNotAdmitted, n.def.typ)
}

// isRawNull reports whether raw holds the JSON null literal. Both null-literal
// writers recognize a literal through it: the authored-canvas default
// canvasNullLiteral scans, and the marshaled field value
// [generator.applyInstanceDefaults] seeds. Whitespace around a JSON value is
// insignificant, so the comparison trims it first.
func isRawNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// canvasNullLiteral returns the name of the first authored-canvas keyword
// holding a JSON null, or "" when no keyword holds one. It reads the four
// keywords a field-level writer spells a literal value with. It never descends
// into not or allOf. A forbidden null lands there, and forbidding null on a
// reference that admits none is redundant rather than wrong.
//
// Default is already raw JSON, so the scan compares it as text. The other
// three hold Go values, which isJSONNull judges by what encoding/json writes
// for them.
func canvasNullLiteral(canvas *Schema) string {
	if canvas == nil {
		return ""
	}

	if isRawNull(canvas.Default) {
		return "default"
	}

	if canvas.Const != nil && isJSONNull(*canvas.Const) {
		return "const"
	}

	if slices.ContainsFunc(canvas.Enum, isJSONNull) {
		return "enum"
	}

	if slices.ContainsFunc(canvas.Examples, isJSONNull) {
		return "examples"
	}

	return ""
}

// isJSONNull reports whether v marshals to a JSON null. Apart from an untyped
// nil it asks encoding/json rather than testing the Go value, so every spelling
// answers alike: a nil pointer, slice, or map, a [json.RawMessage] holding the
// literal, and a [json.Marshaler] returning one. A value that encoding/json
// refuses to marshal (a func or a channel) is not a null. Leaving it on the
// canvas lets the caller's own marshal report the fault.
//
// Maps, pointers, and slices are the only kinds encoding/json writes null for
// on their own, so every other kind answers false without a marshal unless it
// carries its own [json.Marshaler]. That keeps the scan off most values, and
// marshalsToNull recovers a third-party MarshalJSON panic, so generation
// reports through errors alone.
func isJSONNull(v any) bool {
	if v == nil {
		return true
	}

	if _, marshaler := v.(json.Marshaler); !marshaler {
		switch reflect.ValueOf(v).Kind() {
		case reflect.Map, reflect.Pointer, reflect.Slice:
		default:
			return false
		}
	}

	return marshalsToNull(v)
}

// marshalsToNull reports whether encoding/json writes null for v. It answers
// false for a value encoding/json refuses and for one whose own MarshalJSON
// panics, so neither the error nor the panic reaches
// [generator.checkNullLiterals].
func marshalsToNull(v any) bool {
	null := false

	func() {
		defer func() {
			if recover() != nil {
				null = false
			}
		}()

		encoded, err := json.Marshal(v)
		null = err == nil && bytes.Equal(encoded, []byte("null"))
	}()

	return null
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
// additionally follows the raw $ref strings inside every payload: a Verbatim
// payload ([TypeSchema.Verbatim]) is opaque and not node-backed, and a
// build-time extender can author a raw $ref into a reflected payload (a
// property, allOf branch, or additionalProperties renderBase preserves), so a
// $defs reference inside either is a reachability edge only a string scan
// sees. The one payload Ref not scanned is a kindRef node's own: that edge is
// node-backed (walkNodes follows it via n.def) and its string is the
// provisional token, which a base-name collision would resolve to the wrong
// def. Each def reached by a string hit has its body walked too, and
// onPayloadRef (when non-nil) observes every payload ref hit, seen or not.
// Payload subtrees are assumed acyclic, as everywhere else in the generator
// (hook schemas arrive JSON-decoded or JSON-round-trip cloned); the scanned
// set is a dedup, keeping shared payload subtrees scanned once.
func (g *generator) walkReachable(
	root *node,
	seen map[*defEntry]bool,
	visit func(*node),
	onPayloadRef func(*defEntry),
) {
	targets := g.payloadRefTargets()
	scanned := map[*Schema]bool{}

	// A kindRef node's payload is aliased into its parent composite's payload
	// (a field ref's payload sits in the parent's Properties), so the plain
	// scan can reach one through the parent before walkNodes visits the ref
	// node itself. The skip must therefore recognize a ref payload by
	// identity, wherever the scan reaches it: collect every kindRef payload
	// up front, across the root graph and every def body, since a def is
	// often discovered only mid-walk through a string hit.
	refPayloads := map[*Schema]bool{}
	collectSeen := map[*defEntry]bool{}
	markRefPayloads := func(n *node) {
		if n.kind == kindRef {
			refPayloads[n.payload] = true
		}
	}

	walkNodes(root, collectSeen, markRefPayloads)

	for _, e := range g.defs {
		if !collectSeen[e] {
			collectSeen[e] = true
			walkNodes(e.body, collectSeen, markRefPayloads)
		}
	}

	var scanPayload func(s *Schema)

	visitAndScan := func(n *node) {
		visit(n)
		scanPayload(n.payload)
	}

	scanPayload = func(s *Schema) {
		if s == nil || scanned[s] {
			return
		}

		scanned[s] = true

		scanChildren := func() {
			for _, child := range schemafield.Children(s) {
				scanPayload(child)
			}
		}

		// A kindRef payload's own Ref is the node-backed edge walkNodes
		// already follows via n.def, and it carries the provisional
		// (pre-disambiguation) token: string-resolving it under a base-name
		// collision would map to the wrong def. Skip it, but still scan any
		// hook-grafted siblings on the payload.
		if refPayloads[s] {
			scanChildren()

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

		scanChildren()
	}

	walkNodes(root, seen, visitAndScan)
}

// collectReferencedDefs walks the final root node graph and returns the def
// entries reachable from it, in build order. Reachability follows both node
// links and the raw $ref strings inside every payload, so a def whose only
// remaining reference is a raw $ref a hook authored stays alive. A def
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
