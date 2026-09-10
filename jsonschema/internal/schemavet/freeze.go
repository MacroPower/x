package schemavet

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// Frozen is a private tree copy of one schema document, with the tables the
// resolution machinery reads off it: a dense id and JSON Pointer per node,
// the base URI in effect at each node, and the $id, $anchor, and
// $dynamicAnchor registrations the document makes against its base. Nothing
// in a Frozen is shared with the value it was frozen from, and no node has
// two positions, so every table is a function of the pointer alone.
//
// A Frozen has passed no vetting check. [Frozen.Vet] runs them and mints the
// [Doc] currency; [Frozen.VetNode] runs the structural checks alone and mints
// [Node].
type Frozen struct {
	root    *Schema
	ids     map[*Schema]int
	byPath  map[string]int
	uri     map[uriref.DocKey]*Schema
	anchor  map[uriref.AnchorKey]*Schema
	dynamic map[uriref.AnchorKey]*Schema
	profile Profile
	base    uriref.DocKey
	nodes   []*Schema
	paths   []string
	bases   []uriref.DocKey
	scopes  []uriref.DocKey
}

// Freeze copies s into a tree and builds its tables. A node the source
// reaches through two paths is copied once per path, so the copy holds no
// aliasing; a node copied twice that carries an identifier the run reads is
// refused with [ErrIDCollision], since its two copies would claim one key. A
// loop that crosses a schema is refused with [ErrSchemaCycle], in a message
// naming subject (the document as the caller names it in errors), the pointer
// where the loop closes, and the pointer it returns to.
//
// The base argument is the document's own base URI, already normalized: the
// root's configured base, a fetched document's retrieval URI, or the base in
// effect at the position a fragment or substitute stands in. Each node's $id
// resolves against the base of its parent, and the profile decides what
// registers. Under [Profile.Draft7] a $id carrying a plain-name fragment
// registers an anchor, whether fragment-only or on a URI (a URI part
// registers and rebases as it does without the fragment), $anchor and
// $dynamicAnchor register nothing, and a $id beside a $ref
// registers nothing and rebases nothing, since the draft ignores every
// sibling of $ref; under [Profile.InertIDs] no $id registers or rebases at
// all; and under [Profile.Fragment] every $id registers but rebases nothing.
// A key two nodes claim within the document resolves to the first the walk
// reaches.
func Freeze(s *Schema, subject string, base uriref.DocKey, profile Profile) (*Frozen, error) {
	tree, cyc := schemaclone.CloneTree(s)
	if cyc != nil {
		return nil, fmt.Errorf("%w: %s holds a loop where %q crosses a schema and returns to %q",
			ErrSchemaCycle, subject, cyc.Path, cyc.Target)
	}

	f := &Frozen{
		root:    tree.Root,
		ids:     map[*Schema]int{},
		byPath:  map[string]int{},
		uri:     map[uriref.DocKey]*Schema{},
		anchor:  map[uriref.AnchorKey]*Schema{},
		dynamic: map[uriref.AnchorKey]*Schema{},
		base:    base,
		profile: profile,
	}

	f.walk(tree.Root, "", base)

	err := f.refuseAliasedIdentifiers(subject, tree)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// refuseAliasedIdentifiers reports the duplicated source node that carries
// an identifier the profile registers, choosing by the walk order of its
// first copy where the tree holds several. Its copies would claim one key,
// and a document may not hold a key twice through one node; the registration
// rule for a fetched document already refuses the same claim across
// documents. The walk has run, so every copy's pointer is on record, and the
// walk-order choice keeps the message stable across runs, since the aliased
// set is a map.
func (f *Frozen) refuseAliasedIdentifiers(subject string, tree schemaclone.Tree) error {
	type collision struct {
		key string
		ids []int
	}

	var found []collision

	for src, copies := range tree.Aliased {
		key, ok := f.registeredKey(src)
		if !ok {
			continue
		}

		var ids []int

		for _, cp := range copies {
			if id, ok := f.ids[cp]; ok {
				ids = append(ids, id)
			}
		}

		if len(ids) == 0 {
			continue
		}

		slices.Sort(ids)

		found = append(found, collision{key: key, ids: ids})
	}

	if len(found) == 0 {
		return nil
	}

	first := slices.MinFunc(found, func(a, b collision) int { return cmp.Compare(a.ids[0], b.ids[0]) })

	paths := make([]string, 0, len(first.ids))
	for _, id := range first.ids {
		paths = append(paths, fmt.Sprintf("%q", f.paths[id]))
	}

	return fmt.Errorf("%w: %s reaches one schema carrying %s at %s",
		ErrIDCollision, subject, first.key, strings.Join(paths, " and "))
}

// registeredKey names the identifier keyword and value s carries that the
// profile registers, and reports false where s carries none.
func (f *Frozen) registeredKey(s *Schema) (string, bool) {
	d := identifiers(s, uriref.DocKey{}, f.profile)

	switch {
	case d.hasURI:
		return fmt.Sprintf("$id %q", s.ID), true
	case s.Anchor != "" && !f.profile.Draft7:
		return fmt.Sprintf("$anchor %q", s.Anchor), true
	case s.DynamicAnchor != "" && !f.profile.Draft7:
		return fmt.Sprintf("$dynamicAnchor %q", s.DynamicAnchor), true
	case len(d.anchors) > 0:
		// A Draft-07 fragment-only $id registers as an anchor.
		return fmt.Sprintf("$id %q", s.ID), true
	default:
		return "", false
	}
}

// walk assigns s its id, records its pointer and base, registers its
// identifiers, and descends its sub-schemas, threading the base each child
// inherits exactly as the registry walk did. Every registration and the
// child scope come from [identifiers], the one reading of a node's
// identifier keywords the freeze, the identifier checks, and the JSON-form
// pointer walk share.
func (f *Frozen) walk(s *Schema, path string, parentBase uriref.DocKey) {
	id := len(f.nodes)
	f.nodes = append(f.nodes, s)
	f.ids[s] = id
	f.byPath[path] = id
	f.paths = append(f.paths, path)

	d := identifiers(s, parentBase, f.profile)

	if d.hasURI {
		registerFirst(f.uri, d.uri, s)
	}

	for _, key := range d.anchors {
		registerFirst(f.anchor, key, s)
	}

	for _, key := range d.dynamic {
		registerFirst(f.dynamic, key, s)
	}

	f.bases = append(f.bases, d.scope)
	f.scopes = append(f.scopes, d.scope)

	for _, entry := range Entries(s) {
		f.walk(entry.Schema, path+string(entry.Pointer), d.scope)
	}
}

// declared is what one node's identifier keywords register and the base URI
// its children inherit, read from the node alone against the base in effect
// at it. It is the single reading the freeze walk, the identifier checks, and
// the JSON-form pointer walk consume, so the three cannot disagree on which
// keyword registers what under which draft.
type declared struct {
	// The uri is the URI a live, absolutizable $id registers, valid only when
	// hasURI is set.
	uri uriref.DocKey
	// The scope is the base URI a child of the node inherits: the node's own
	// $id applied to the parent base, or the parent base unchanged.
	scope uriref.DocKey
	// The anchors are the anchor keys the node registers: its $anchor and
	// $dynamicAnchor under Draft 2020-12, or its fragment-only $id under
	// Draft-07.
	anchors []uriref.AnchorKey
	// The dynamic keys are the $dynamicAnchor keys, a subset of anchors kept
	// apart for the dynamic-anchor table.
	dynamic []uriref.AnchorKey
	hasURI  bool
}

// identifiers reads the identifier keywords of s against the base in effect at
// it and reports what registers and the base its children inherit. Under
// [Profile.Draft7] a $id beside a $ref registers nothing and rebases nothing
// (the draft ignores every sibling of $ref), a plain-name fragment on a $id is
// the anchor spelling, and $anchor and $dynamicAnchor are unknown keywords that
// register nothing; under [Profile.InertIDs] no $id registers or rebases; and a $id that
// does not parse against the base registers no URI and leaves the scope
// unchanged, so a reference targeting it misses and the identifier check
// reports the parse fault separately.
func identifiers(s *Schema, parent uriref.DocKey, profile Profile) declared {
	d := declared{scope: parent}

	applyID(&d, s, parent, profile)

	if !profile.Draft7 {
		if s.Anchor != "" {
			d.anchors = append(d.anchors, d.scope.Anchor(s.Anchor))
		}

		if s.DynamicAnchor != "" {
			key := d.scope.Anchor(s.DynamicAnchor)
			d.anchors = append(d.anchors, key)
			d.dynamic = append(d.dynamic, key)
		}
	}

	return d
}

// applyID folds the $id registration and the child scope into d. A $id the run
// reads as inert (under [Profile.InertIDs], or beside a $ref under
// [Profile.Draft7]) registers nothing; a plain-name fragment is a Draft-07
// anchor and nothing under 2020-12, declared within the enclosing document
// for a fragment-only $id and within the document the URI part names
// otherwise, where the URI part registers and rebases as it would without
// the fragment; an $id that does not resolve to a key registers no URI
// and leaves the scope on the parent base; and under [Profile.Fragment] the
// URI part registers but leaves the scope on the parent base, since a
// fragment carries no document base for a $id to replace. The anchor takes
// the decoded fragment text, the same form a reference's fragment resolves
// by, so an escaped spelling registers under the name a reference reaches
// it by.
func applyID(d *declared, s *Schema, parent uriref.DocKey, profile Profile) {
	ignoreID := profile.Draft7 && s.Ref != ""
	if s.ID == "" || profile.InertIDs || ignoreID {
		return
	}

	key, fragment, err := uriref.Resolve(parent, s.ID)
	if err != nil {
		return
	}

	if profile.Draft7 && !fragment.IsEmpty() && !fragment.IsPointer() {
		d.anchors = append(d.anchors, key.Anchor(fragment.Name()))
	}

	if uriref.IsFragmentOnly(s.ID) {
		return
	}

	d.uri, d.hasURI = key, true

	if !profile.Fragment {
		d.scope = key
	}
}

// ScopeOfJSON reads the base a child of a JSON-form schema object inherits: the
// object's own live $id applied to the parent base, or the parent base
// unchanged. The JSON-form pointer walk calls it where its typed traversal ends
// and it descends raw maps, so a crossed $id rebases the located target on the
// same terms [identifiers] rebases a typed node.
func ScopeOfJSON(obj map[string]any, parent uriref.DocKey, profile Profile) uriref.DocKey {
	id, ok := obj["$id"].(string)
	if !ok || id == "" || uriref.IsFragmentOnly(id) || profile.InertIDs {
		return parent
	}

	key, _, err := uriref.Resolve(parent, id)
	if err != nil {
		return parent
	}

	return key
}

// registerFirst stores s under key unless the key is already held, so a key
// claimed twice within one document resolves to the first the walk reaches.
func registerFirst[K comparable](reg map[K]*Schema, key K, s *Schema) {
	if _, ok := reg[key]; !ok {
		reg[key] = s
	}
}

// Root returns the frozen document's root, or nil for a nil receiver.
func (f *Frozen) Root() *Schema {
	if f == nil {
		return nil
	}

	return f.root
}

// Base returns the base URI the document was frozen against.
func (f *Frozen) Base() uriref.DocKey {
	if f == nil {
		return uriref.DocKey{}
	}

	return f.base
}

// Nodes returns every node of the tree in walk order, indexed by id. The
// slice is the Frozen's own and must not be mutated.
func (f *Frozen) Nodes() []*Schema {
	if f == nil {
		return nil
	}

	return f.nodes
}

// ID returns the id of a node of the tree and whether s is one.
func (f *Frozen) ID(s *Schema) (int, bool) {
	if f == nil {
		return 0, false
	}

	id, ok := f.ids[s]

	return id, ok
}

// Path returns the JSON Pointer of the node with the given id within the
// document; the root's pointer is "".
func (f *Frozen) Path(id int) string {
	return f.paths[id]
}

// NodeBase returns the base URI in effect at the node with the given id, the
// one its own references resolve against.
func (f *Frozen) NodeBase(id int) uriref.DocKey {
	return f.bases[id]
}

// ScopeBase returns the base URI a child of the node with the given id
// inherits: the node's own $id applied to its parent's base, whether or not
// a Draft-07 $ref beside it ignores that $id for the node's own reference.
func (f *Frozen) ScopeBase(id int) uriref.DocKey {
	return f.scopes[id]
}

// At returns the node the JSON Pointer addresses through sub-schema keyword
// edges alone, and whether one stands there, by one map lookup. A pointer
// into a value keyword or an unknown keyword names no node here; the
// caller's JSON-form fallback answers those.
func (f *Frozen) At(pointer string) (*Schema, bool) {
	if f == nil {
		return nil, false
	}

	id, ok := f.byPath[pointer]
	if !ok {
		return nil, false
	}

	return f.nodes[id], true
}

// URIs returns the document's $id registrations, keyed by absolute URI. The
// map is the Frozen's own and must not be mutated.
func (f *Frozen) URIs() map[uriref.DocKey]*Schema { return f.uri }

// Anchors returns the document's $anchor registrations, including each
// $dynamicAnchor, keyed by baseURI#name. The map is the Frozen's own and must
// not be mutated.
func (f *Frozen) Anchors() map[uriref.AnchorKey]*Schema { return f.anchor }

// DynamicAnchors returns the document's $dynamicAnchor registrations, keyed
// by baseURI#name. The map is the Frozen's own and must not be mutated.
func (f *Frozen) DynamicAnchors() map[uriref.AnchorKey]*Schema { return f.dynamic }

// Vet runs the vetting policy over the whole document: the structural checks
// [Frozen.VetNode] runs plus the identifier checks ($id domain and
// $vocabulary placement) against the document's base. Each violation names
// its path under pathPrefix, so a fetched document's violation names the
// document. It returns the minted [Doc] on success, or the zero Doc and the
// first violation.
func (f *Frozen) Vet(pathPrefix string) (Doc, error) {
	_, err := f.VetNode(pathPrefix)
	if err != nil {
		return Doc{}, err
	}

	err = checkIdentifiers(f.root, pathPrefix, f.base, f.profile, map[*Schema]bool{})
	if err != nil {
		return Doc{}, err
	}

	return Doc{root: f.root, f: f}, nil
}

// VetNode runs the structural checks alone: field structure, type names,
// bound domains, under [Profile.RejectItemsArray] the array form of items,
// and under [Profile.KnownFormat] the format names, in that order. It serves
// a fragment of a document, such as a JSON-pointer target
// materialized from an unknown keyword, which has no document base of its
// own. It returns the minted [Node] on success, or the zero Node and the
// first violation.
func (f *Frozen) VetNode(pathPrefix string) (Node, error) {
	err := checkSchemaStructure(f.root, pathPrefix, map[*Schema]bool{})
	if err != nil {
		return Node{}, err
	}

	err = checkTypeNames(f.root, pathPrefix, map[*Schema]bool{})
	if err != nil {
		return Node{}, err
	}

	err = checkBoundDomains(f.root, pathPrefix, map[*Schema]bool{})
	if err != nil {
		return Node{}, err
	}

	err = checkPatterns(f.root, pathPrefix, map[*Schema]bool{})
	if err != nil {
		return Node{}, err
	}

	if f.profile.RejectItemsArray {
		err = checkItemsArrayDraft2020(f.root, pathPrefix, map[*Schema]bool{})
		if err != nil {
			return Node{}, err
		}
	}

	if f.profile.KnownFormat != nil {
		err = checkFormatNames(f.root, pathPrefix, f.profile.KnownFormat, map[*Schema]bool{})
		if err != nil {
			return Node{}, err
		}
	}

	return Node{root: f.root, f: f}, nil
}

// FreezeNode freezes a materialized fragment and runs the structural checks
// over it, minting the [Node] currency. The locator names the fragment in a
// violation, and base is the base URI in effect at its position, which its
// own identifiers resolve against.
func FreezeNode(s *Schema, locator string, base uriref.DocKey, profile Profile) (Node, error) {
	f, err := Freeze(s, locator, base, profile)
	if err != nil {
		return Node{}, err
	}

	return f.VetNode(locator)
}
