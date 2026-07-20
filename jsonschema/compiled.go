package jsonschema

import "go.jacobcolvin.com/x/jsonschema/internal/schemafield"

// compiledDoc is the frozen node-identity index over a schema graph: every
// distinct *Schema pointer reachable through sub-schema keywords is assigned a
// dense id in [0, len(schemas)), once, at construction. The single
// *Schema->id mapping lives in one place (ids), and the validator's per-node
// precompute caches are slices indexed by that id.
//
// The index references the caller's live *Schema pointers; it never copies
// them. It is built once and not mutated afterward, except that [compiledDoc.extend]
// appends the nodes of a document fetched while compiling (a remote $ref target),
// which is still single-threaded construction, never a validation-time mutation.
//
// Aliased (a schema reachable through several paths) and pointer-cyclic graphs
// terminate because each distinct pointer is indexed once, matching [Walk]'s
// visit-once contract; no cycle is rejected here. The reachability walk reuses
// [schemafield.Children], the same traversal precompute and the refresolve
// registry use, so the frozen id-set is exactly the set precompute caches.
type compiledDoc struct {
	// The ids map resolves a *Schema to its node id; a miss means the schema is
	// outside the frozen graph (a fallback target materialized only at
	// validation time).
	ids map[*Schema]int
	// The schemas slice maps a node id to the caller's *Schema, referenced not
	// copied.
	schemas []*Schema
}

// freeze builds the frozen node-identity index over the schema graph rooted at
// root, assigning each distinct reachable *Schema a dense id.
func freeze(root *Schema) *compiledDoc {
	d := &compiledDoc{ids: map[*Schema]int{}}
	d.extend(root)

	return d
}

// extend indexes every not-yet-seen *Schema reachable from root and returns from,
// the id count before the walk, so a caller can precompute the new range
// [from, len()). It shares freeze's pointer dedup: a schema already indexed keeps
// its id, so a subtree wholly aliasing already-indexed nodes (the common case for
// a fetched remote, whose root is re-registered under its base URI and whose
// nodes are reached through several URIs) adds nothing and returns from == len().
// Without the shared dedup an aliased node would get a second id while ids maps to
// only one, corrupting the index.
func (d *compiledDoc) extend(root *Schema) int {
	from := len(d.schemas)
	d.walk(root)

	return from
}

// walk assigns s an id and descends into its sub-schemas, skipping any pointer
// already indexed so aliased and cyclic graphs terminate.
func (d *compiledDoc) walk(s *Schema) {
	if s == nil {
		return
	}

	if _, seen := d.ids[s]; seen {
		return
	}

	d.ids[s] = len(d.schemas)
	d.schemas = append(d.schemas, s)

	for _, child := range schemafield.Children(s) {
		d.walk(child)
	}
}

// nodeID returns s's frozen node id and whether s is in the graph.
func (d *compiledDoc) nodeID(s *Schema) (int, bool) {
	id, ok := d.ids[s]

	return id, ok
}

// len reports the number of indexed nodes, the size the per-node cache slices
// must reach.
func (d *compiledDoc) len() int {
	return len(d.schemas)
}
