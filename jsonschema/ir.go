package jsonschema

import (
	"encoding/json/jsontext"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"go.jacobcolvin.com/x/jsonschema/internal/fieldset"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// nodeKind classifies an IR node by the JSON Schema shape its render produces.
type nodeKind uint8

const (
	// A kindValue node is a leaf: a scalar, an unrestricted schema, a []byte, or
	// an opaque override/provider payload. Its payload is rendered as-is.
	kindValue nodeKind = iota
	// A kindObject node is a struct: props hold its declared properties,
	// embeds its allOf/anyOf composition branches, and items its embedded
	// fallback's value node when the struct declares a map fallback.
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

// fallbackSlot names the extra-member keyword an object node's embedded
// fallback value renders into.
type fallbackSlot uint8

const (
	// A slotNone object carries no fallback value node.
	slotNone fallbackSlot = iota
	// A slotAdditional object renders its fallback value as
	// additionalProperties.
	slotAdditional
	// A slotUnevaluated object renders its fallback value as
	// unevaluatedProperties.
	slotUnevaluated
)

// node is one position in the generation IR. Build produces a node tree
// carrying intent; render walks it to a final [Schema]. Each node owns a bare
// payload (no null wrapper, no final $ref string) that nothing outside the
// node shares. A composite node's child positions live on the node (items,
// props, prefix), never in the payload's own sub-schema slots, which hold only
// what a hook authored there as a literal. A hook that needs the structure
// gets a [node.view], a private copy with each child slot filled from the
// child's own view.
type node struct {
	payload *Schema   // bare type-derived payload; child slots are node-backed, not stored here
	def     *defEntry // non-nil iff kindRef
	// Typ is the struct type an object node reflects, for the field hooks.
	typ   reflect.Type
	items *node // slice element / map value / object fallback value
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

	// The origin field names the field position this node occupies, for the
	// reports in [run.checkNullLiterals]. A field node carries it, and so
	// does every element node beneath that field. Nil for every other node.
	origin *fieldOrigin
	// The overrode field, on a node a jsonschema tag's type= pair rewrote in
	// place ([node.overrideType]), is a value copy of the node as reflected.
	// The null pass decides that copy too, and the tag's remaining directives
	// read its decision, since the pair replaced the occurrence they were
	// parsed against.
	overrode *node
	props    []nodeProp  // struct properties, declaration order
	prefix   []*node     // array elements (prefixItems / itemsArray)
	embeds   []embedNode // struct allOf/anyOf composition branches

	// Occ holds the facts of this occurrence that decide whether it admits
	// null, and stance the null-admission stance a type-level hook declared
	// for an inline node (an alias's own stance for a ref). Null is the
	// decision [run.resolveNullability] derives from them, and from the def
	// entry for a reference or a body, once the graph is complete; nothing
	// reads it before that pass, and no node reads another's.
	occ    occurrence
	stance Nullability
	null   nullDecision

	kind nodeKind
	// Fallback names the extra-member slot an object's fallback value node
	// (items) renders into.
	fallback fallbackSlot
	// IsBody marks a $defs body. A body is shared by every reference, so its
	// own decision ignores the occurrence's pointer-ness and a stance's grant
	// (a reference carries both) and keeps only a stance's veto over the
	// container null a format option adds.
	isBody bool
	// Composed marks an embed's allOf branch. An embed is composition rather
	// than an occurrence, so the branch never admits null.
	composed bool
	// Verbatim marks a kindValue leaf whose payload a type-level hook declared
	// through [TypeSchema.Verbatim]: it is emitted exactly as authored, so render
	// and reconcile skip the null encoding for it entirely.
	verbatim bool
	// Hooked marks a field node whose field-level hooks have run, so a node
	// reached twice (a def body's field seen through the body and through an
	// inlined copy) is hooked once.
	hooked  bool
	isField bool // marks a struct-field node, so reconcile applies the field const/enum bound subsumption
}

// containerKind names the nilable container an occurrence is, if any. A
// container's null renders as a ["null", base] type list rather than an
// anyOf[base, null] wrapper.
type containerKind uint8

const (
	containerNone containerKind = iota
	// A containerSlice occurrence is a slice with an array schema.
	containerSlice
	// A containerMap occurrence is a map with an object schema.
	containerMap
	// A containerBytes occurrence is a []byte with a base64 string schema.
	containerBytes
	// A containerQuoted occurrence is a json:",string" number with a string
	// schema.
	containerQuoted
)

// occurrence holds the facts about one position that decide its null
// admission.
type occurrence struct {
	// Pointer reports a *T at any depth, or an interface position, both of
	// which marshal null when nil.
	pointer bool
	// Container names the nilable container the position is, whose nil
	// marshals as null only under the matching WithJSONOptions format flag.
	container containerKind
}

// nullDecision is the resolved null admission of one node.
type nullDecision struct {
	// Admit reports that the occurrence admits a JSON null.
	admit bool
	// Wrap reports that render adds a null branch. It is false where the
	// declared base already names null, where a reference's body admits null
	// on its own, and on a verbatim node.
	wrap bool
}

// typeListEncoded reports whether the node is a slice, map, byte slice, or
// ",string" number, whose null render encodes as a ["null", base] type list.
// Every other node uses the anyOf[base, null] wrapper. A reference is never
// one: its body's container kind feeds its null admission, and the body's
// own render carries the type list.
func (n *node) typeListEncoded() bool {
	return n.kind != kindRef && n.occ.container != containerNone
}

// containerType returns the JSON type name a nilable container's schema
// carries when it admits no null.
func (n *node) containerType() string {
	switch n.occ.container {
	case containerSlice:
		return typename.Array
	case containerMap:
		return typename.Object
	case containerBytes, containerQuoted:
		return typename.String
	case containerNone:
	}

	return ""
}

// prop returns the node-backed property of an object node with the given
// JSON name, or nil when none or when the property lives only in the payload
// as a literal a hook declared.
func (n *node) prop(name string) *node {
	for i := range n.props {
		if n.props[i].name == name {
			return n.props[i].schema
		}
	}

	return nil
}

// fieldOrigin names the field position a node occupies: the struct declaring
// the field, the field's JSON name, and whether the node is an element beneath
// that field rather than the field itself. A field-level writer can commit to a
// null the occurrence's final decision later withdraws, so the generator
// carries this record to a pass that runs once every decision is final. The
// element flag holds no index, so a tuple element's report names the field
// rather than the position within it.
type fieldOrigin struct {
	parent reflect.Type // the struct whose schema carries the field
	// Typ is the Go type of the occurrence, pointer levels removed, which the
	// report names.
	typ     reflect.Type
	field   string // the field's JSON name
	element bool   // an element beneath the field, not the field itself
}

// nodeProp is a struct property: its value node, its JSON name, and the field
// the field-level hooks read.
type nodeProp struct {
	schema *node
	name   string
	fi     fieldset.Field
	// Quoted records that the json:",string" option coerced the field to a
	// string schema.
	quoted bool
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
	body     *node  // bare value node; nil while a cycle placeholder
	baseName string // namer output, pre-disambiguation; collisions are grouped on it
	name     string // final $defs key; set by assignDefNames before render
	// Token is the provisional $ref string every reference to the entry
	// carries until assignDefNames settles the final key. It is unique per
	// run, and its "@" separator is a character no final key contains, so a
	// token never spells another entry's key and a hook that copies one into
	// a literal of its own is rewritten to the final key by finalizeRefs.
	token string
	// Container is the body's nilable container kind, recorded when the body
	// is defined so every reference reads the fact off the entry rather than
	// the body's decision. It is containerNone for a hook-declared body.
	container containerKind
	// Nullability is the type's declared null-admission stance, recorded once at
	// definition time and combined with each reference's pointer-ness in
	// nullableDecision. The stance is a per-type property, so recording it on the
	// entry (rather than on the shared body) keeps it applied consistently at
	// every reference and leaves the def body bare. It is NullFromReflection for a
	// type with no stance.
	nullability Nullability
}

// newDefEntry registers a placeholder $defs entry for t with no body yet. A
// re-entry for t returns the existing entry, so a self- or mutually-recursive
// type resolves to one shared body and its cycle is broken.
func (g *run) newDefEntry(t reflect.Type) *defEntry {
	if e, ok := g.typeToDef[t]; ok {
		return e
	}

	baseName := g.schemaName(t)
	e := &defEntry{
		typ:      t,
		baseName: baseName,
		token:    g.profile.refPrefix() + baseName + "@" + strconv.Itoa(len(g.defs)),
	}
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
// sequence or map element beneath it, so [run.checkNullLiterals] can name
// the field a late-refused null literal sits in. It descends only into
// elements (items and prefix), wherever a node carries them. A struct property
// is a separate field node, which takes its own origin when the generator
// builds its struct. Each depth whose Go type has an element type takes its
// own element origin naming that type with pointer levels removed, so a
// [][]*T inner element reports T where the outer one reports []*T. A deeper
// node whose type has no element type shares the origin above it.
func assignFieldOrigins(n *node, origin *fieldOrigin) {
	if n == nil {
		return
	}

	n.origin = origin

	if n.items == nil && len(n.prefix) == 0 {
		return
	}

	elem := origin
	if et := elementType(origin.typ); et != nil || !origin.element {
		elem = &fieldOrigin{parent: origin.parent, field: origin.field, element: true}
		if et != nil {
			elem.typ = numkind.DerefType(et)
		}
	}

	assignFieldOrigins(n.items, elem)

	for _, c := range n.prefix {
		assignFieldOrigins(c, elem)
	}
}

// refNode builds a kindRef node linking to e, recording the occurrence's
// pointer-ness. [run.resolveNullability] later combines it with the def
// entry's recorded stance: a pointer occurrence of a NullForbidden type still
// admits no null, and a non-pointer occurrence of a NullAllowed type does. Its
// payload holds the entry's provisional token, so a type-level hook reading
// .Ref sees a reference it can test, clear, or copy; [run.finalizeRefs]
// rewrites the token to the final key once assignDefNames settles it, and
// renderRef emits that key and grafts any siblings.
func (g *run) refNode(e *defEntry, pointer bool) *node {
	return &node{
		kind:    kindRef,
		def:     e,
		occ:     occurrence{pointer: pointer},
		payload: &Schema{Ref: e.token},
	}
}

// defineType fills t's def entry with body (if still a placeholder), records the
// type's nullability stance on the entry, and returns a reference node. The
// entry is the one a cyclic re-entry registered while t was being built, or a
// fresh one for a type extracted to $defs on its first visit. The body
// is always the bare value node; the stance lives on the entry and is combined
// with each reference's pointer-ness in the null pass, so $defs nullability
// stays order-independent.
func (g *run) defineType(t reflect.Type, body *node, stance Nullability, pointer bool) *node {
	e := g.newDefEntry(t)
	e.nullability = stance

	if e.body == nil {
		e.body = body
		e.container = body.occ.container
		body.isBody = true
	}

	return g.refNode(e, pointer)
}

// resolveNullability decides the null admission of every node in the graph
// from the facts reflection recorded, in one walk. Each node's facts are
// read by [run.factsOf] and decided by [admitNull] and [wrapNull], two pure
// functions over [nullFacts]; a reference reads its body's container kind
// and stance off the def entry, both recorded when the body was defined, and
// never the body's own decision, so the walk order does not matter. The
// occurrence a type= pair replaced is decided too, since the directives
// before the pair read it.
func (g *run) resolveNullability(root *node) {
	entries := make(map[*node]*defEntry, len(g.defs))
	for _, e := range g.defs {
		if e.body != nil {
			entries[e.body] = e
		}
	}

	seen := map[*defEntry]bool{}
	visit := func(n *node) {
		n.null = g.decideNull(n, entries[n])

		if n.overrode != nil {
			n.overrode.null = g.decideNull(n.overrode, nil)
		}
	}

	walkNodes(root, seen, visit)

	for _, e := range g.defs {
		if !seen[e] {
			seen[e] = true
			walkNodes(e.body, seen, visit)
		}
	}
}

// decideNull reads a node's facts and decides both halves of its null
// decision. The entry is the one a body node belongs to, nil otherwise.
func (g *run) decideNull(n *node, bodyOf *defEntry) nullDecision {
	f := g.factsOf(n, bodyOf)

	admit := admitNull(f)

	return nullDecision{admit: admit, wrap: wrapNull(f, admit)}
}

// nullRole is the part a node plays in the null decision.
type nullRole uint8

const (
	// A roleOccurrence node is a position in the graph: a field, an element,
	// a root, or a reference standing in such a position.
	roleOccurrence nullRole = iota
	// A roleBody node is a $defs body, shared by every reference to it.
	roleBody
	// A roleComposed node is an embed's allOf branch, composition rather
	// than an occurrence.
	roleComposed
)

// nullFacts is everything the null decision reads about one node, all of
// it recorded by reflection: no fact is another node's decision, so the
// decision is order-free. [run.factsOf] fills it; [admitNull] and
// [wrapNull] read it. The cross-product test in ir_internal_test.go
// enumerates every field, so a fact added here is decided in the open.
type nullFacts struct {
	// Container is the nilable container kind of the occurrence, or for a
	// reference the kind of the body it resolves to, whose nil the marshal
	// writes as null only under the matching format flag.
	container containerKind
	// Stance is the null-admission stance a type-level hook declared for an
	// inline node, or an alias's own stance for a reference.
	stance Nullability
	// DefStance is the stance recorded on the def entry a reference or a
	// body resolves against, NullFromReflection for an inline node.
	defStance Nullability
	// Role is the part the node plays.
	role nullRole
	// Pointer reports a *T at any depth or an interface position.
	pointer bool
	// Ref reports a reference node, whose container and defStance are the
	// body's and whose targetNull is the body's own null.
	ref bool
	// Verbatim marks a leaf a hook declared verbatim, which carries no null
	// encoding at all.
	verbatim bool
	// DeclaresNull reports that the node's own payload names the null type.
	declaresNull bool
	// TargetNull reports, for a reference, that the body's payload names the
	// null type or is an unrestricted leaf, so the target admits null
	// without a wrapper.
	targetNull bool
	// NilSliceNull and nilMapNull are the run's format flags: whether the
	// marshal writes null for a nil slice (or byte slice) and a nil map.
	nilSliceNull bool
	nilMapNull   bool
}

// containerNull reports whether the marshal writes null for a nil container
// of the facts' kind under the run's format flags.
func (f nullFacts) containerNull() bool {
	switch f.container {
	case containerSlice, containerBytes:
		return f.nilSliceNull
	case containerMap:
		return f.nilMapNull
	case containerQuoted, containerNone:
		return false
	}

	return false
}

// factsOf reads a node's null facts. A reference takes its container kind
// and stance from the def entry, recorded when the body was defined, and its
// target null from the body's payload; bodyOf is the entry a body node
// belongs to, so the body reads the stance the entry holds for it.
func (g *run) factsOf(n *node, bodyOf *defEntry) nullFacts {
	f := nullFacts{
		pointer:      n.occ.pointer,
		container:    n.occ.container,
		stance:       n.stance,
		verbatim:     n.verbatim,
		nilSliceNull: g.nilSliceNull,
		nilMapNull:   g.nilMapNull,
	}

	switch {
	case n.composed:
		f.role = roleComposed
	case n.isBody:
		f.role = roleBody
	default:
		f.role = roleOccurrence
	}

	if bodyOf != nil {
		f.defStance = bodyOf.nullability
	}

	if n.kind == kindRef {
		f.ref = true
		f.container = n.def.container
		f.defStance = n.def.nullability

		if body := n.def.body; body != nil {
			f.targetNull = schemashape.DeclaresType(body.payload, typename.Null) ||
				(body.kind == kindValue && !body.verbatim && schemashape.IsEmpty(body.payload))
		}

		return f
	}

	f.declaresNull = schemashape.DeclaresType(n.payload, typename.Null)

	return f
}

// admitNull decides whether a node admits a JSON null.
//
// A composed embed branch admits none, and neither does a verbatim leaf,
// which carries no null encoding at all. A body ignores the pointer-ness of
// the occurrence that built it and a stance's grant, since each reference
// carries those, and keeps only the entry's veto over the container null a
// format option adds; a body whose payload names null admits it outright. An
// occurrence admits null when its payload names it, or when it is a
// reference to a body that admits null on its own (a payload naming null or
// an unrestricted leaf), since the rendered $ref admits whatever its target
// does; otherwise it admits null when the def entry's stance, then its own
// stance, then the position itself say so: a stance of NullAllowed grants
// and NullForbidden vetoes, and NullFromReflection defers to a pointer
// position or a container whose nil the marshal writes as null.
func admitNull(f nullFacts) bool {
	switch {
	case f.role == roleComposed || f.verbatim:
		return false
	case f.role == roleBody:
		return (f.defStance != NullForbidden && f.containerNull()) || f.declaresNull
	case f.declaresNull:
		return true
	case f.ref && f.targetNull:
		return true
	default:
		return f.defStance.apply(f.stance.apply(f.pointer || f.containerNull()))
	}
}

// wrapNull decides whether render adds a null branch to a node that admits
// null. It does not where the node's own payload names null, and for a
// reference where the body already admits it on its own: a body naming null
// or an unrestricted leaf, or a container body whose type list carries the
// null its entry's stance does not veto.
func wrapNull(f nullFacts, admit bool) bool {
	switch {
	case !admit || f.verbatim || f.declaresNull:
		return false
	case f.ref && f.targetNull:
		return false
	case f.ref && f.defStance != NullForbidden && f.containerNull():
		return false
	default:
		return true
	}
}

// apply resolves a [Nullability] stance against an occurrence's own answer:
// NullAllowed always admits null, NullForbidden never does, and
// NullFromReflection defers to the occurrence.
func (s Nullability) apply(occurrence bool) bool {
	switch s {
	case NullAllowed:
		return true
	case NullForbidden:
		return false
	case NullFromReflection:
	}

	return occurrence
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

	for i := range root.props {
		walkNodes(root.props[i].schema, seen, visit)
	}

	for _, c := range root.prefix {
		walkNodes(c, seen, visit)
	}

	for _, e := range root.embeds {
		walkNodes(e.branch, seen, visit)
	}
}

// view returns a private copy of the node's bare base for a hook: a deep clone
// of the payload, with each node-backed child slot holding the child's own
// view and a ref child as its $ref (the provisional token before
// [run.finalizeRefs] runs, the final key after). The tuple form follows the
// draft. A hook may mutate the copy freely; the generator reads a declaration
// back from it only where it chooses to ([node.absorbView]).
func (n *node) view(draft Draft) *Schema {
	v := schemaclone.Clone(n.payload)

	switch n.kind {
	case kindObject:
		if len(n.props) > 0 && v.Properties == nil {
			v.Properties = make(map[string]*Schema, len(n.props))
		}

		for i := range n.props {
			v.Properties[n.props[i].name] = n.props[i].schema.view(draft)
		}

		if n.items != nil {
			switch n.fallback {
			case slotAdditional:
				v.AdditionalProperties = n.items.view(draft)
			case slotUnevaluated:
				v.UnevaluatedProperties = n.items.view(draft)
			case slotNone:
			}
		}

	case kindList:
		if n.items != nil {
			v.Items = n.items.view(draft)
		}

	case kindMap:
		if n.items != nil {
			v.AdditionalProperties = n.items.view(draft)
		}

	case kindTuple:
		elems := make([]*Schema, len(n.prefix))
		for i, c := range n.prefix {
			elems[i] = c.view(draft)
		}

		if draft == Draft7 {
			v.ItemsArray = elems
		} else {
			v.PrefixItems = elems
		}

	case kindValue, kindRef:
	}

	return v
}

// overrideType rewrites the node in place for a jsonschema tag's type= pair:
// the pair names a JSON type, so it is a transform over the reflected node,
// never a second reflection. The first pair on a node keeps the node as
// reflected in overrode, a value copy sharing the child pointers, whose null
// decision the directives before the pair read.
//
// The payload takes the override on a private clone, so the copy still
// carries the reflected one. The type-derived facts reset: the def link (an
// explicit type replaces a $ref outright), the occurrence, a hook's stance,
// and the verbatim mark, so an overridden field admits no null and takes the
// null encoding like any other. Every other field keeps its value: the
// authored canvas, the field origin, the hooked mark, and the field, body,
// and composed roles are facts of the position, not of the type.
//
// Children stay only where the new type keeps the container kind: an array
// keeps a list's element node or a tuple's prefix nodes, an object keeps a
// map's value node or a struct's properties, embeds, and fallback value node.
// Each kept node still carries its own null decision, canvas, and hooks. Any
// other pairing is a leaf.
func (n *node) overrideType(typeName string) {
	if n.overrode == nil {
		replaced := *n
		n.overrode = &replaced
		n.payload = schemaclone.Clone(n.payload)
	}

	tagparse.ApplyTypeOverride(n.payload, typeName)

	n.def = nil
	n.occ = occurrence{}
	n.stance = NullFromReflection
	n.null = nullDecision{}
	n.verbatim = false

	switch {
	case n.kind == kindList && typeName == typename.Array,
		n.kind == kindTuple && typeName == typename.Array,
		n.kind == kindMap && typeName == typename.Object,
		n.kind == kindObject && typeName == typename.Object:
		return
	}

	n.kind = kindValue
	n.typ = nil
	n.items = nil
	n.prefix = nil
	n.props = nil
	n.embeds = nil
	n.fallback = slotNone
}

// absorbView takes a hook's edited view as the node's new base and classifies
// each child slot against the pristine view it was handed. A slot whose own
// fields, the ones outside its node-backed sub-slots, match the pristine one
// stays node-backed. It leaves the base so render fills it from the child,
// the fields it gained land on the child's base, and the child classifies its
// own sub-slots the same way against its pristine view, so an edit at any
// depth reaches the node it belongs to. An absent slot drops the child.
// Anything else, a cyclic slot included (upstream MarshalJSON does not
// terminate on a cycle, so no comparison runs over one), stays in the base as
// the literal the hook authored and the child is dropped.
func (n *node) absorbView(edited, pristine *Schema, draft Draft) {
	n.payload = edited

	switch n.kind {
	case kindObject:
		n.absorbProps(edited, pristine, draft)
		n.absorbFallback(edited, pristine, draft)

	case kindList:
		if n.items != nil && !absorbSlot(n.items, &edited.Items, pristine.Items, draft) {
			n.items = nil
		}

	case kindMap:
		if n.items != nil && !absorbSlot(n.items, &edited.AdditionalProperties, pristine.AdditionalProperties, draft) {
			n.items = nil
		}

	case kindTuple:
		n.absorbPrefix(edited, pristine, draft)

	case kindValue, kindRef:
	}
}

// absorbProps classifies an object's property slots. A dropped property keeps
// whatever the hook left in the base's map, and a node-backed one is deleted
// from it so render fills it from the child.
func (n *node) absorbProps(edited, pristine *Schema, draft Draft) {
	kept := n.props[:0]

	for i := range n.props {
		p := &n.props[i]

		slot, ok := edited.Properties[p.name]
		if !ok {
			continue
		}

		if absorbSlot(p.schema, &slot, pristine.Properties[p.name], draft) {
			delete(edited.Properties, p.name)

			kept = append(kept, *p)
		}
	}

	clear(n.props[len(kept):])

	n.props = kept
}

// absorbFallback classifies an object's fallback value slot.
func (n *node) absorbFallback(edited, pristine *Schema, draft Draft) {
	if n.items == nil {
		return
	}

	var ok bool

	switch n.fallback {
	case slotAdditional:
		ok = absorbSlot(n.items, &edited.AdditionalProperties, pristine.AdditionalProperties, draft)
	case slotUnevaluated:
		ok = absorbSlot(n.items, &edited.UnevaluatedProperties, pristine.UnevaluatedProperties, draft)
	case slotNone:
	}

	if !ok {
		n.items = nil
		n.fallback = slotNone
	}
}

// absorbPrefix classifies a tuple's element slots. A hook that changed the
// element count authored the whole list, which then stays literal.
func (n *node) absorbPrefix(edited, pristine *Schema, draft Draft) {
	elems, pristineElems := &edited.PrefixItems, pristine.PrefixItems
	if draft == Draft7 {
		elems, pristineElems = &edited.ItemsArray, pristine.ItemsArray
	}

	if len(*elems) != len(n.prefix) || len(pristineElems) != len(n.prefix) {
		n.prefix = nil

		return
	}

	for i, c := range n.prefix {
		if !absorbSlot(c, &(*elems)[i], pristineElems[i], draft) {
			n.prefix = nil

			return
		}
	}

	*elems = nil
}

// absorbSlot classifies one child slot and reports whether it stays
// node-backed. A node-backed slot is cleared and its edited view becomes the
// child's base through [node.absorbView], which classifies the child's own
// slots in turn.
func absorbSlot(child *node, slot **Schema, pristine *Schema, draft Draft) bool {
	edited := *slot
	if edited == nil || pristine == nil || schemaclone.FindCycle(edited) != nil {
		return false
	}

	if !keepsOwnFields(edited, pristine, child.slotFields(draft)) {
		return false
	}

	child.absorbView(edited, pristine, draft)

	*slot = nil

	return true
}

// slotFields names the payload fields the node's kind fills from its
// children, the fields a comparison of the node's own shape sets aside. The
// tuple form follows the draft, as [node.view] does.
func (n *node) slotFields(draft Draft) []string {
	switch n.kind {
	case kindObject:
		fields := []string{"Properties"}

		if n.items != nil {
			switch n.fallback {
			case slotAdditional:
				fields = append(fields, "AdditionalProperties")
			case slotUnevaluated:
				fields = append(fields, "UnevaluatedProperties")
			case slotNone:
			}
		}

		return fields

	case kindList:
		if n.items != nil {
			return []string{"Items"}
		}

	case kindMap:
		if n.items != nil {
			return []string{"AdditionalProperties"}
		}

	case kindTuple:
		if draft == Draft7 {
			return []string{"ItemsArray"}
		}

		return []string{"PrefixItems"}

	case kindValue, kindRef:
	}

	return nil
}

// keepsOwnFields reports whether every field set on pristine, apart from the
// named slot fields, is equal on edited. A field zero on pristine is one
// edited may have gained, which changes nothing about the shape it kept.
func keepsOwnFields(edited, pristine *Schema, slots []string) bool {
	ev, pv := reflect.ValueOf(edited).Elem(), reflect.ValueOf(pristine).Elem()

	for i := range ev.NumField() {
		f := ev.Type().Field(i)
		if !f.IsExported() || slices.Contains(slots, f.Name) {
			continue
		}

		if pf := pv.Field(i); !pf.IsZero() && !reflect.DeepEqual(ev.Field(i).Interface(), pf.Interface()) {
			return false
		}
	}

	return true
}

// checkNullLiterals scans the authored canvas of every field and element
// node for a null literal a field-level writer committed against an
// occurrence whose final decision admits none, and reports the first. The
// jsonschema tag refuses such a literal at parse time, since it runs after
// the null pass; a tag interpreter writes onto the canvas without consulting
// the decision, through [FieldContext.Canvas] or a [Constraints] setter, so
// this pass is where its literal meets the decision.
//
// The walk is [run.walkReachable] rather than [walkNodes], so the check
// covers exactly the defs render emits. A def whose only surviving reference is
// a raw $ref string an extender authored into a payload is reachable by that
// scan alone. The visitor returns nothing, so the pass keeps the first report
// and lets the walk finish. Walk order is deterministic, which is what makes
// the first report name one occurrence rather than an arbitrary one.
func (g *run) checkNullLiterals(root *node) error {
	var reported error

	g.walkReachable(root, map[*defEntry]bool{}, func(n *node) {
		if reported == nil {
			reported = nullLiteralReport(n)
		}
	}, nil)

	return reported
}

// nullLiteralReport returns the rejection a node's authored canvas earns
// against its final null decision, or nil when the node admits null, carries
// no origin (it is not a field or element), or holds no literal. The report
// names the field the literal sits in, marks an element position, and names
// the occurrence's Go type.
func nullLiteralReport(n *node) error {
	if n.origin == nil || n.null.admit {
		return nil
	}

	keyword := canvasNullLiteral(n.authored)
	if keyword == "" {
		return nil
	}

	typ := n.origin.typ
	if n.kind == kindRef {
		typ = n.def.typ
	}

	if n.origin.element {
		return fmt.Errorf(
			"%s field %q: element: authored canvas: keyword %q: %w %s",
			n.origin.parent, n.origin.field, keyword,
			tagmodel.ErrNullNotAdmitted, typ,
		)
	}

	return fmt.Errorf("%s field %q: authored canvas: keyword %q: %w %s",
		n.origin.parent, n.origin.field, keyword,
		tagmodel.ErrNullNotAdmitted, typ)
}

// isRawNull reports whether raw holds the JSON null literal. Two call sites
// share it, so the two cannot drift apart: canvasNullLiteral, scanning an
// authored default, and [run.seedDefaults], testing a marshaled field
// value. Both hand it a marshaled value, so the kind of its first token
// answers, and leading whitespace does not change that kind.
func isRawNull(raw jsontext.Value) bool {
	return raw.Kind() == jsontext.KindNull
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

// isJSONNull reports whether v renders as a JSON null in the emitted schema
// document. It reads v through [jsonvalue.FromDocument], the same document
// view the validator compares const and enum against, so every spelling
// answers alike: a nil pointer, a typed nil map or slice, a [jsontext.Value]
// holding the literal, and a marshaler returning one.
//
// The canvas values this guards (const, enum, examples) are rendered by the
// upstream Schema.MarshalJSON, which marshals them with [encoding/json] v1
// semantics, where a typed nil map or slice writes null, so the probe asks
// v1, which FromDocument does (v1 still honors the v2 marshaler interfaces,
// so a MarshalJSONTo returning null answers true). A value the marshal
// refuses (a func or a channel) or whose own marshaler panics is not a null.
// Leaving it on the canvas lets the caller's own marshal report the fault,
// so generation reports through errors alone.
func isJSONNull(v any) bool {
	dv, ok := jsonvalue.FromDocument(v)

	return ok && dv.Kind() == jsonvalue.Null
}

// payloadRefTargets maps the final $ref string of every def entry to the
// entry. It reads the final keys, so it is only meaningful once
// assignDefNames and [run.finalizeRefs] have run; from then on a payload
// carries a def reference only in this form, whether a ref node's own, one
// a hook copied out of a view, or one a hook spelled by hand.
func (g *run) payloadRefTargets() map[string]*defEntry {
	prefix := g.profile.refPrefix()

	targets := make(map[string]*defEntry, len(g.defs))
	for _, e := range g.defs {
		targets[prefix+e.name] = e
	}

	return targets
}

// walkReachable visits every node reachable from root like walkNodes, and
// additionally follows the raw $ref strings inside every payload. A payload
// holds no node-backed child, so what the scan reaches is what a hook
// declared: a Verbatim payload ([TypeSchema.Verbatim]), a provider's Value, a
// slot a build-time extender replaced or a branch it grafted. A $defs
// reference inside any of those is a reachability edge only a string scan
// sees. A kindRef node's own Ref is scanned too; it is node-backed (walkNodes
// follows it via n.def) and holds the same final key the string scan
// resolves, so the hit is a repeat. Each def reached by a string hit has its
// body walked too, and onPayloadRef (when non-nil) observes every payload ref
// hit, seen or not. Payload subtrees are assumed acyclic, as everywhere else
// in the generator (hook schemas arrive JSON-decoded or JSON-round-trip
// cloned); the scanned set is a dedup, keeping shared payload subtrees
// scanned once.
func (g *run) walkReachable(
	root *node,
	seen map[*defEntry]bool,
	visit func(*node),
	onPayloadRef func(*defEntry),
) {
	targets := g.payloadRefTargets()
	scanned := map[*Schema]bool{}

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
// links and the raw $ref strings inside every payload, so a def whose only
// remaining reference is a raw $ref a hook authored stays alive. A def
// orphaned by a type= override or by root inlining is never reached, so it is
// dropped from the output.
func (g *run) collectReferencedDefs(root *node) []*defEntry {
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
