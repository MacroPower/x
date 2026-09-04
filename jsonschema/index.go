package jsonschema

import (
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

// schemaIndex is a node-identity index over the frozen documents a run
// holds: every node of every document folded in is assigned a dense id in
// [0, len(schemas)), once, when first seen. The single *Schema->id mapping
// lives in one place (ids), and callers keep their per-node state in slices
// indexed by the assigned id.
//
// The index references the frozen documents' nodes; every one is a private
// tree copy, so no pointer here is shared with a caller and no node has two
// positions. It only grows: [schemaIndex.extend] and [schemaIndex.intern]
// add nodes as callers discover them (the validator folds in each remote
// document fetched while compiling; the inliner interns fallback substitutes
// and materialized targets mid-run), and an assigned id is never reused or
// invalidated.
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

// extend indexes every not-yet-seen node of the vetted document, in the
// document's own node order, and returns from, the id count before the fold,
// so a caller can precompute the new range [from, len()). Demanding the
// [schemavet.Doc] currency rather than a raw *Schema makes skipping the vet a
// compile error; the indexed id-set is exactly the set the precompute caches
// trust, so every document folded in must have passed the structural vet
// first. A document already folded in keeps its ids, so folding it again
// adds nothing and returns from == len().
func (d *schemaIndex) extend(doc schemavet.Doc) int {
	from := len(d.schemas)

	for _, node := range doc.Frozen().Nodes() {
		d.intern(node)
	}

	return from
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
