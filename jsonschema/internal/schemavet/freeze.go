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
	uri     map[string]*Schema
	anchor  map[string]*Schema
	dynamic map[string]*Schema
	base    string
	nodes   []*Schema
	paths   []string
	bases   []string
	scopes  []string
	profile Profile
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
// registers. Under [Profile.Draft7] a fragment-only $id registers an anchor,
// $anchor and $dynamicAnchor register nothing, and a node bearing both $ref
// and $id keeps its parent's base for its own reference; under
// [Profile.InertIDs] no $id registers or rebases at all. A key two nodes
// claim within the document resolves to the first the walk reaches.
func Freeze(s *Schema, subject, base string, profile Profile) (*Frozen, error) {
	tree, cyc := schemaclone.CloneTree(s)
	if cyc != nil {
		return nil, fmt.Errorf("%w: %s holds a loop where %q crosses a schema and returns to %q",
			ErrSchemaCycle, subject, cyc.Path, cyc.Target)
	}

	f := &Frozen{
		root:    tree.Root,
		ids:     map[*Schema]int{},
		byPath:  map[string]int{},
		uri:     map[string]*Schema{},
		anchor:  map[string]*Schema{},
		dynamic: map[string]*Schema{},
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
	switch {
	case s.ID != "" && !f.profile.InertIDs:
		return fmt.Sprintf("$id %q", s.ID), true
	case s.Anchor != "" && !f.profile.Draft7:
		return fmt.Sprintf("$anchor %q", s.Anchor), true
	case s.DynamicAnchor != "" && !f.profile.Draft7:
		return fmt.Sprintf("$dynamicAnchor %q", s.DynamicAnchor), true
	default:
		return "", false
	}
}

// walk assigns s its id, records its pointer and base, registers its
// identifiers, and descends its sub-schemas, threading the base each child
// inherits exactly as the registry walk did.
func (f *Frozen) walk(s *Schema, path, parentBase string) {
	id := len(f.nodes)
	f.nodes = append(f.nodes, s)
	f.ids[s] = id
	f.byPath[path] = id
	f.paths = append(f.paths, path)

	currentBase := parentBase

	if s.ID != "" && !f.profile.InertIDs {
		if uriref.IsFragmentOnly(s.ID) {
			// Draft-07: a fragment-only $id is the anchor spelling. Draft
			// 2020-12 forbids a fragment in $id, so there the form registers
			// nothing and a ref naming it stays unresolvable.
			if f.profile.Draft7 {
				registerFirst(f.anchor, uriref.AnchorKey(currentBase, s.ID[1:]), s)
			}
		} else {
			resolved := uriref.IDBase(currentBase, s.ID)
			registerFirst(f.uri, resolved, s)

			currentBase = resolved
		}
	}

	// $anchor and $dynamicAnchor are Draft 2020-12 keywords; under Draft-07
	// they are unknown annotations and register nothing.
	if !f.profile.Draft7 {
		if s.Anchor != "" {
			registerFirst(f.anchor, uriref.AnchorKey(currentBase, s.Anchor), s)
		}

		if s.DynamicAnchor != "" {
			key := uriref.AnchorKey(currentBase, s.DynamicAnchor)
			registerFirst(f.anchor, key, s)
			registerFirst(f.dynamic, key, s)
		}
	}

	// Draft-07 ignores the siblings of a $ref, so a sibling $id does not
	// change the base the node's own reference resolves against.
	base := currentBase
	if f.profile.Draft7 && s.Ref != "" && s.ID != "" && !uriref.IsFragmentOnly(s.ID) {
		base = parentBase
	}

	f.bases = append(f.bases, base)
	f.scopes = append(f.scopes, currentBase)

	for _, entry := range Entries(s) {
		f.walk(entry.Schema, path+string(entry.Pointer), currentBase)
	}
}

// registerFirst stores s under key unless the key is already held, so a key
// claimed twice within one document resolves to the first the walk reaches.
func registerFirst(reg map[string]*Schema, key string, s *Schema) {
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
func (f *Frozen) Base() string {
	if f == nil {
		return ""
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
func (f *Frozen) NodeBase(id int) string {
	return f.bases[id]
}

// ScopeBase returns the base URI a child of the node with the given id
// inherits: the node's own $id applied to its parent's base, whether or not
// a Draft-07 $ref beside it ignores that $id for the node's own reference.
func (f *Frozen) ScopeBase(id int) string {
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
func (f *Frozen) URIs() map[string]*Schema { return f.uri }

// Anchors returns the document's $anchor registrations, including each
// $dynamicAnchor, keyed by baseURI#name. The map is the Frozen's own and must
// not be mutated.
func (f *Frozen) Anchors() map[string]*Schema { return f.anchor }

// DynamicAnchors returns the document's $dynamicAnchor registrations, keyed
// by baseURI#name. The map is the Frozen's own and must not be mutated.
func (f *Frozen) DynamicAnchors() map[string]*Schema { return f.dynamic }

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
// bound domains, and under [Profile.RejectItemsArray] the array form of
// items. It serves a fragment of a document, such as a JSON-pointer target
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

	if f.profile.RejectItemsArray {
		err = checkItemsArrayDraft2020(f.root, pathPrefix, map[*Schema]bool{})
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
func FreezeNode(s *Schema, locator, base string, profile Profile) (Node, error) {
	f, err := Freeze(s, locator, base, profile)
	if err != nil {
		return Node{}, err
	}

	return f.VetNode(locator)
}
