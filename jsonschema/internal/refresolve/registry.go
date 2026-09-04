package refresolve

import (
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

// Registry is the compiled, shareable resolution state: the maps built once at
// compile time from the frozen documents [Registry.Build] and
// [Session.RegisterFetched] merge, immutable afterward except through
// [Registry.Clone]'s copy-on-write. Only URI is exported, and only for
// reading. The shared closure walk iterates it to reach each registered
// document. Every write, and every other map, goes through this package's own
// methods.
//
// Every schema the registry holds is a node of a [schemavet.Doc], a vetted
// tree copy the parent froze at the boundary the document crossed, so no
// pointer here is shared with a caller or a resolver and no node has two
// positions.
type Registry struct {
	// URI maps an absolute URI to the schema registered under it ($id or the
	// document base).
	URI map[string]*jsonschema.Schema

	// Maps a baseURI#anchor key to its schema ($anchor, and $dynamicAnchor which
	// is also reachable via $ref).
	anchor map[string]*jsonschema.Schema

	// Maps a baseURI#name key to its $dynamicAnchor schema.
	dynamicAnchor map[string]*jsonschema.Schema

	// Maps a schema to its base URI, consulted during $ref resolution.
	baseURIs map[*jsonschema.Schema]string

	// Maps every node the registry holds to the document it is a node of, so
	// a JSON-pointer resolution finds the tree to answer from and a walk can
	// ask whether a schema belongs to a registered document.
	nodes map[*jsonschema.Schema]schemavet.Doc

	root *jsonschema.Schema
	deps Deps

	inertIDs bool
}

// NewRegistry allocates an empty registry. The inertIDs flag records that the
// run treats $id as an inert annotation (WithRetrievalBase), which the
// JSON-form pointer walk reads so a crossed $id rebases nothing.
func NewRegistry(deps Deps, inertIDs bool) *Registry {
	return &Registry{
		URI:           map[string]*jsonschema.Schema{},
		anchor:        map[string]*jsonschema.Schema{},
		dynamicAnchor: map[string]*jsonschema.Schema{},
		baseURIs:      map[*jsonschema.Schema]string{},
		nodes:         map[*jsonschema.Schema]schemavet.Doc{},
		inertIDs:      inertIDs,
		deps:          deps,
	}
}

// Build seeds the registry with the root document: its $id, anchor, and
// base-URI tables, frozen against the base the caller froze it with. A
// non-empty base is registered for the root when its own $id did not already
// claim one, so a ref that absolutizes back to the root document resolves to
// this copy instead of being fetched.
func (r *Registry) Build(root schemavet.Doc) {
	r.root = root.Root()
	r.absorb(root)

	if base := root.Frozen().Base(); base != "" {
		if _, ok := r.URI[base]; !ok {
			r.URI[base] = r.root
		}
	}
}

// KnownSchema reports whether sc is a node of a document the registry holds:
// the root document or a document merged in while the registry was built
// (for the compiled registry, the documents present at compile time). A
// schema materialized by the JSON-pointer fallback or fetched into a per-run
// clone is not known to the compiled registry.
func (r *Registry) KnownSchema(sc *jsonschema.Schema) bool {
	_, ok := r.nodes[sc]

	return ok
}

// DocOf returns the document sc is a node of, and whether the registry holds
// one.
func (r *Registry) DocOf(sc *jsonschema.Schema) (schemavet.Doc, bool) {
	doc, ok := r.nodes[sc]

	return doc, ok
}

// Clone returns a copy-on-write duplicate of r: the five maps are cloned so a
// run that registers a remote fetch cannot race a concurrent run sharing the
// compiled registry, while the immutable configuration is copied by value.
func (r *Registry) Clone() *Registry {
	c := *r
	c.URI = maps.Clone(r.URI)
	c.anchor = maps.Clone(r.anchor)
	c.dynamicAnchor = maps.Clone(r.dynamicAnchor)
	c.baseURIs = maps.Clone(r.baseURIs)
	c.nodes = maps.Clone(r.nodes)

	return &c
}

// NewSession derives a per-run [Session] sharing r's maps by reference until the
// first write clones them via [Session.EnsureOwned]. Requiring vet at
// construction forces every session to state its vetting policy. The vet is
// the [FallbackVet] the session applies to each JSON-pointer fallback target
// it materializes, and every production session passes one. Only a test whose
// walk materializes no target passes nil, which freezes each target under an
// empty profile instead.
func (r *Registry) NewSession(vet FallbackVet) *Session {
	return &Session{reg: r, fallbackVet: vet}
}

// collision reports the first identifier that claimed and held bind to
// different schemas, naming the key, the claimant the caller supplies, and the
// document holding it. The kind argument names the identifier space for the
// message. Sorting the colliding keys keeps the report stable, so a document
// colliding on several keys names the same one every run rather than following
// Go's map order.
func (r *Registry) collision(claimed, held map[string]*jsonschema.Schema, kind, claimant string) error {
	var keys []string

	for key, sc := range claimed {
		if other, ok := held[key]; ok && other != sc {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return nil
	}

	slices.Sort(keys)

	key := keys[0]

	return fmt.Errorf("%w: %s claims %s %q, already held by %s",
		ErrIDCollision, claimant, kind, key, r.holderName(held[key], key))
}

// holderName names the holder of key by its own base URI. When that base is
// the claimed key itself, printing it repeats the key the claimant is claiming
// and names nothing new, so holderName prefers another URI the holder is
// registered under, found by the other key mapping to the same schema. A holder
// registered under that one URI alone offers no other name, so the message
// repeats it and lets the surrounding wording carry which document is which.
func (r *Registry) holderName(sc *jsonschema.Schema, key string) string {
	base := r.baseURIs[sc]
	if base != key {
		return documentName(base)
	}

	var others []string

	for uri, held := range r.URI {
		if held == sc && uri != key {
			others = append(others, uri)
		}
	}

	if len(others) == 0 {
		return documentName(base)
	}

	slices.Sort(others)

	return fmt.Sprintf("the document retrieved from %q", others[0])
}

// absorb merges a document's tables into r. The caller rejects a collision
// first, so every key the two share holds the same schema and a plain copy
// and an only-if-absent merge agree. Every node joins the node map, and
// deliberately. It decides whether the parent reports or skips an
// unresolvable fragment ref inside the document, so a fetched document's
// nodes must reach it.
func (r *Registry) absorb(doc schemavet.Doc) {
	f := doc.Frozen()

	maps.Copy(r.URI, f.URIs())
	maps.Copy(r.anchor, f.Anchors())
	maps.Copy(r.dynamicAnchor, f.DynamicAnchors())

	for id, node := range f.Nodes() {
		r.baseURIs[node] = f.NodeBase(id)
		r.nodes[node] = doc
	}
}

// claims returns the URI table a frozen document claims when it registers
// under baseURI: its own $id registrations plus the retrieval URI for its
// root. A document whose own $id equals the URI it was fetched from names
// that key with that schema twice, so it registers one schema under one key
// rather than colliding with itself.
func claims(f *schemavet.Frozen, baseURI string) map[string]*jsonschema.Schema {
	uris := maps.Clone(f.URIs())
	uris[baseURI] = f.Root()

	return uris
}

// documentName renders a document's base URI for a collision message. An empty
// base names the root document, the one document that holds registrations
// without a URI of its own.
func documentName(base string) string {
	if base == "" {
		return "the root document"
	}

	return fmt.Sprintf("document %q", base)
}
