package jsonschema

import (
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

// schemaIndex is a node-identity index (a pointer interner) over a schema
// graph: every distinct *Schema pointer reachable through sub-schema keywords
// is assigned a dense id in [0, len(schemas)), once, when first interned. The
// single *Schema->id mapping lives in one place (ids), and callers keep their
// per-node state in slices indexed by the assigned id.
//
// The index references the caller's live *Schema pointers; it never copies
// them, and it asserts nothing about the schemas themselves, which other code
// may keep mutating in place. It only grows: [schemaIndex.extend] and
// [schemaIndex.intern] add nodes as callers discover them (the validator folds
// in each remote document fetched while compiling; the inliner interns fallback
// substitutes and materialized targets mid-run), and an assigned id is never
// reused or invalidated.
//
// Aliased (a schema reachable through several paths) and pointer-cyclic graphs
// terminate because each distinct pointer is indexed once, matching [Walk]'s
// visit-once contract; no cycle is rejected here. The reachability walk reuses
// [schemafield.Children], the same traversal the precompute and the refresolve
// registry use, so the indexed id-set is exactly the set precompute caches.
type schemaIndex struct {
	// The ids map resolves a *Schema to its node id; a miss means the schema is
	// outside the indexed graph (a fallback target materialized only at
	// validation time).
	ids map[*Schema]int
	// The schemas slice maps a node id to the caller's *Schema, referenced not
	// copied.
	schemas []*Schema
}

// newSchemaIndex returns an empty index, ready for [schemaIndex.intern] or
// [schemaIndex.extend] to populate it.
func newSchemaIndex() *schemaIndex {
	return &schemaIndex{ids: map[*Schema]int{}}
}

// intern returns s's node id, assigning a fresh dense id when s is not yet
// indexed. The bool reports whether s was already present, so a caller walking a
// graph can record per-node metadata exactly once (first-write-wins) and stop
// descending a subtree it has already indexed. A nil s must not be interned;
// callers guard it.
func (d *schemaIndex) intern(s *Schema) (int, bool) {
	if id, ok := d.ids[s]; ok {
		return id, true
	}

	id := len(d.schemas)
	d.ids[s] = id
	d.schemas = append(d.schemas, s)

	return id, false
}

// extend indexes every not-yet-seen *Schema reachable from the vetted document
// and returns from, the id count before the walk, so a caller can precompute
// the new range [from, len()). Demanding the [schemavet.Doc] currency rather
// than a raw *Schema makes skipping the vet a compile error; the indexed
// id-set is exactly the set the precompute caches trust, so every document
// folded in must have passed the structural vet first. It shares intern's
// pointer dedup: a schema already indexed keeps its id, so a subtree wholly
// aliasing already-indexed nodes (the common case for a fetched remote, whose
// root is re-registered under its base URI and whose nodes are reached through
// several URIs) adds nothing and returns from == len(). Without the shared
// dedup an aliased node would get a second id while ids maps to only one,
// corrupting the index.
func (d *schemaIndex) extend(doc schemavet.Doc) int {
	from := len(d.schemas)
	d.walk(doc.Root())

	return from
}

// walk assigns s an id and descends into its sub-schemas, skipping any pointer
// already indexed so aliased and cyclic graphs terminate.
func (d *schemaIndex) walk(s *Schema) {
	if s == nil {
		return
	}

	if _, seen := d.intern(s); seen {
		return
	}

	for _, child := range schemafield.Children(s) {
		d.walk(child)
	}
}

// nodeID returns s's node id and whether s is in the index.
func (d *schemaIndex) nodeID(s *Schema) (int, bool) {
	id, ok := d.ids[s]

	return id, ok
}

// len reports the number of indexed nodes, the size the per-node cache slices
// must reach.
func (d *schemaIndex) len() int {
	return len(d.schemas)
}
