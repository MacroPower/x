package jsonschema

import (
	"encoding/json/jsontext"
	"fmt"
	"reflect"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/fieldset"
	"go.jacobcolvin.com/x/jsonschema/internal/jsonvalue"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
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
	// The overrode field, on a node a jsonschema tag's type= pair rebuilt,
	// is the node it replaced. The tag's remaining directives read that node's
	// null decision, since the pair replaced the occurrence they were parsed
	// against.
	overrode *node
	props    []nodeProp  // struct properties, declaration order
	prefix   []*node     // array elements (prefixItems / itemsArray)
	embeds   []embedNode // struct allOf/anyOf composition branches

	// Occ holds the facts of this occurrence that decide whether it admits
	// null, and stance the null-admission stance a type-level hook declared
	// for an inline node (an alias's own stance for a ref). Null is the
	// decision [run.resolveNullability] derives from them once the
	// graph is complete; nothing reads it before that pass.
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

// nilableContainer reports whether the node is a slice, map, byte slice, or
// ",string" number, whose null render encodes as a ["null", base] type list.
// Every other node uses the anyOf[base, null] wrapper.
func (n *node) nilableContainer() bool {
	return n.occ.container != containerNone
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

// declaresNull reports whether a schema's own type keyword names null: a
// jsonschema:"type=null" override, or a hook's Types list carrying it.
func declaresNull(s *Schema) bool {
	return s.Type == typename.Null || slices.Contains(s.Types, typename.Null)
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
	baseName string // namer output, pre-disambiguation; the provisional $ref token
	name     string // final $defs key; set by assignDefNames before render
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
// sequence or map element beneath it, so [run.checkNullLiterals] can name
// the field a late-refused null literal sits in. It descends only into
// elements (items and prefix), wherever a node carries them. A struct property
// is a separate field node, which takes its own origin when the generator
// builds its struct. The walk allocates one element origin per field that has
// elements and hands the same pointer to every depth, so a [][]*T inner element
// reads the same origin as the outer one.
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

	if et := elementType(origin.typ); et != nil {
		elem = &fieldOrigin{parent: origin.parent, field: origin.field, element: true, typ: numkind.DerefType(et)}
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
// payload holds the provisional $ref string (the pre-disambiguation name), so
// a hook reading .Ref sees a real reference; render re-emits the final name
// via renderRef and grafts any siblings.
func (g *run) refNode(e *defEntry, pointer bool) *node {
	return &node{
		kind:    kindRef,
		def:     e,
		occ:     occurrence{pointer: pointer},
		payload: &Schema{Ref: g.profile.refPrefix() + e.baseName},
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
		body.isBody = true
	}

	return g.refNode(e, pointer)
}

// resolveNullability decides the null admission of every node in the graph
// once it is complete, so a hook that reads a field's decision reads the
// final one. Every def body resolves first, since a reference's own answer
// reads its body's: a body admits null on its own when it is a nilable
// container under a format flag its stance does not veto, when its declared
// type names null, or when it is an unrestricted leaf, and a reference to
// such a body adds no null branch of its own.
//
// An inline occurrence admits null when its stance grants it, or when its
// stance defers and the position is a pointer, an interface, or a container
// whose nil the marshal writes as null. A body ignores the pointer-ness of the
// occurrence that built it and a stance's grant, since each reference carries
// those, and keeps only the veto. A composed embed branch admits none, and
// neither does a verbatim leaf, which carries no null encoding at all. A
// declared null type admits null whatever the occurrence, since the schema
// names it outright. Render adds a null branch (wrap) to an admitting node
// unless its declared type already names null or its body already admits.
func (g *run) resolveNullability(root *node) {
	for _, e := range g.defs {
		if e.body != nil {
			g.resolveAdmit(e.body, e)
		}
	}

	seen := map[*defEntry]bool{}
	visit := func(n *node) {
		g.resolveNode(n)

		if n.overrode != nil {
			g.resolveNode(n.overrode)
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

// resolveNode fills a node's decision. A body was already given its admit;
// every other node derives it here.
func (g *run) resolveNode(n *node) {
	switch {
	case n.isBody:
	case n.kind == kindRef:
		g.resolveAdmit(n, n.def)
	default:
		g.resolveAdmit(n, nil)
	}

	n.null.wrap = n.null.admit && !n.verbatim && !g.targetAdmitsNull(n)
}

// resolveAdmit derives a node's admit from its facts; e is the def entry a
// body or a reference resolves against, nil for an inline node.
func (g *run) resolveAdmit(n *node, e *defEntry) {
	switch {
	case n.composed || n.verbatim:
		n.null.admit = false
	case n.isBody:
		n.null.admit = e.nullability != NullForbidden && g.containerNull(n.occ.container)
	case n.kind == kindRef:
		n.null.admit = e.nullability.apply(n.stance.apply(n.occ.pointer))
	default:
		n.null.admit = n.stance.apply(n.occ.pointer || g.containerNull(n.occ.container))
	}

	if n.kind != kindRef && declaresNull(n.payload) {
		n.null.admit = true
	}
}

// containerNull reports whether the marshal writes null for a nil container
// of the given kind under the run's WithJSONOptions value.
func (g *run) containerNull(c containerKind) bool {
	switch c {
	case containerSlice, containerBytes:
		return g.nilSliceNull
	case containerMap:
		return g.nilMapNull
	case containerQuoted, containerNone:
		return false
	}

	return false
}

// targetAdmitsNull reports whether the schema a node renders already admits
// null before any wrapper: for a reference its body, otherwise its declared
// base. A nilable container that admits null carries it in its own type list,
// which is what a reference to an extracted container reads.
func (g *run) targetAdmitsNull(n *node) bool {
	if n.kind == kindRef {
		body := n.def.body
		if body == nil {
			return false
		}

		return body.null.admit && body.nilableContainer() || declaresNull(body.payload) ||
			(body.kind == kindValue && !body.verbatim && schemashape.IsEmpty(body.payload))
	}

	return declaresNull(n.payload)
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
// view and a ref child as its provisional $ref. The tuple form follows the
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

// payloadRefTargets maps every $defs ref string a hook may have authored to its
// def entry: the final assigned name of each def, plus its provisional baseName
// where no final name claims it (a ref node's own payload carries the
// provisional form until render). It must be built after assignDefNames.
func (g *run) payloadRefTargets() map[string]*defEntry {
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
// additionally follows the raw $ref strings inside every payload. A payload
// holds no node-backed child, so what the scan reaches is what a hook
// declared: a Verbatim payload ([TypeSchema.Verbatim]), a provider's Value, a
// slot a build-time extender replaced or a branch it grafted. A $defs
// reference inside any of those is a reachability edge only a string scan
// sees. The one payload Ref not scanned is a kindRef node's own: that edge is
// node-backed (walkNodes follows it via n.def) and its string is the
// provisional token, which a base-name collision would resolve to the wrong
// def; the siblings a hook grafted onto that payload are still scanned. Each
// def reached by a string hit has its body walked too, and onPayloadRef (when
// non-nil) observes every payload ref hit, seen or not. Payload subtrees are
// assumed acyclic, as everywhere else in the generator (hook schemas arrive
// JSON-decoded or JSON-round-trip cloned); the scanned set is a dedup,
// keeping shared payload subtrees scanned once.
func (g *run) walkReachable(
	root *node,
	seen map[*defEntry]bool,
	visit func(*node),
	onPayloadRef func(*defEntry),
) {
	targets := g.payloadRefTargets()
	scanned := map[*Schema]bool{}

	var scanPayload func(s *Schema)

	scanChildren := func(s *Schema) {
		for _, child := range schemafield.Children(s) {
			scanPayload(child)
		}
	}

	visitAndScan := func(n *node) {
		visit(n)

		if n.kind == kindRef {
			scanChildren(n.payload)

			return
		}

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

		scanChildren(s)
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
