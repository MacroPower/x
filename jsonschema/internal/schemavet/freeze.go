package schemavet

import (
	"fmt"
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
		uri:     map[string]*Schema{},
		anchor:  map[string]*Schema{},
		dynamic: map[string]*Schema{},
		base:    base,
		profile: profile,
	}

	err := f.refuseAliasedIdentifiers(subject, tree)
	if err != nil {
		return nil, err
	}

	f.walk(tree.Root, "", base)

	return f, nil
}

// refuseAliasedIdentifiers reports the first duplicated source node that
// carries an identifier the profile registers. Its copies would claim one
// key, and a document may not hold a key twice through one node; the
// registration rule for a fetched document already refuses the same claim
// across documents.
func (f *Frozen) refuseAliasedIdentifiers(subject string, tree schemaclone.Tree) error {
	for src, copies := range tree.Aliased {
		var key string

		switch {
		case src.ID != "" && !f.profile.InertIDs:
			key = "$id " + fmt.Sprintf("%q", src.ID)
		case src.Anchor != "" && !f.profile.Draft7:
			key = "$anchor " + fmt.Sprintf("%q", src.Anchor)
		case src.DynamicAnchor != "" && !f.profile.Draft7:
			key = "$dynamicAnchor " + fmt.Sprintf("%q", src.DynamicAnchor)
		default:
			continue
		}

		// The walk has not run yet, so the copies have no recorded paths;
		// locate them by a scan, which only a refusal pays for.
		var paths []string

		for _, cp := range copies {
			paths = append(paths, fmt.Sprintf("%q", pointerOf(tree.Root, cp)))
		}

		return fmt.Errorf("%w: %s reaches one schema carrying %s at %s",
			ErrIDCollision, subject, key, strings.Join(paths, " and "))
	}

	return nil
}

// pointerOf returns the JSON Pointer of target within the tree rooted at
// root, or "" when target is the root or absent.
func pointerOf(root, target *Schema) string {
	var find func(s *Schema, path string) (string, bool)

	find = func(s *Schema, path string) (string, bool) {
		if s == target {
			return path, true
		}

		for _, entry := range Entries(s) {
			if found, ok := find(entry.Schema, path+string(entry.Pointer)); ok {
				return found, true
			}
		}

		return "", false
	}

	path, _ := find(root, "")

	return path
}

// walk assigns s its id, records its pointer and base, registers its
// identifiers, and descends its sub-schemas, threading the base each child
// inherits exactly as the registry walk did.
func (f *Frozen) walk(s *Schema, path, parentBase string) {
	id := len(f.nodes)
	f.nodes = append(f.nodes, s)
	f.ids[s] = id
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
// edges alone, and whether one stands there. A pointer into a value keyword
// or an unknown keyword names no node here; the caller's JSON-form fallback
// answers those.
func (f *Frozen) At(pointer string) (*Schema, bool) {
	if f == nil {
		return nil, false
	}

	for id, path := range f.paths {
		if path == pointer {
			return f.nodes[id], true
		}
	}

	return nil, false
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
