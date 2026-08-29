package refresolve

import (
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// Registry is the compiled, shareable resolution state: the maps built once at
// compile time by [Registry.Build] and immutable afterward except through
// [Registry.Clone]'s copy-on-write. Only URI is exported, and only for reading.
// The shared closure walk iterates it to reach each registered document. Every
// write, and every other map, goes through this package's own methods.
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

	// Records every schema the walk has registered, so a node reached twice
	// registers once. It is a dedup guard rather than a cycle policy. It
	// bounds the walk over an aliased or cyclic graph, and this package
	// judges neither shape, since the parent owns the node index and the
	// graph rules. The parent rejects instead, at the clone boundary every
	// resolution entry point crosses and in its tree check over a compiled or
	// inlined root.
	walked map[*jsonschema.Schema]bool

	root *jsonschema.Schema
	deps Deps

	draft    Draft
	inertIDs bool
}

// NewRegistry allocates an empty registry. The draft argument selects the
// draft-dependent walk branches; inertIDs treats $id as an inert annotation (no
// URI/anchor registration and no base-URI change), for WithRetrievalBase.
func NewRegistry(deps Deps, draft Draft, inertIDs bool) *Registry {
	return &Registry{
		URI:           map[string]*jsonschema.Schema{},
		anchor:        map[string]*jsonschema.Schema{},
		dynamicAnchor: map[string]*jsonschema.Schema{},
		baseURIs:      map[*jsonschema.Schema]string{},
		walked:        map[*jsonschema.Schema]bool{},
		draft:         draft,
		inertIDs:      inertIDs,
		deps:          deps,
	}
}

// Build walks the entire schema tree rooted at root to fill the URI, anchor, and
// base-URI registries for $id and $anchor resolution, seeded with base, which
// must already be normalized (the caller applies [uriref.NormalizeBaseURI]); a
// non-empty base is registered for the root document when its own $id did not
// already claim one, so a ref that absolutizes back to the root document
// resolves to this copy instead of being fetched.
func (r *Registry) Build(root *jsonschema.Schema, base string) {
	r.root = root
	r.Walk(root, base)

	if base != "" {
		if _, ok := r.URI[base]; !ok {
			r.URI[base] = root
		}
	}
}

// Walk registers a schema tree's $id/$anchor entries and base URIs, overwriting
// any key it repeats, which is the right behavior for the single authoritative
// document [Registry.Build] walks.
func (r *Registry) Walk(s *jsonschema.Schema, parentBase string) {
	r.walkInto(s, parentBase, false)
}

// WalkFetched registers a document fetched from a resolver, keeping an existing
// entry instead of overwriting it. Every caller walks into a scratch registry,
// so the only entry the walk yields to is one this same document already
// claimed. A duplicate $id or anchor within one document therefore resolves to
// the first the walk reaches, and a key another document holds is a collision
// the caller reports once the walk is done.
func (r *Registry) WalkFetched(s *jsonschema.Schema, parentBase string) {
	r.walkInto(s, parentBase, true)
}

// walkInto is the shared walk core. When onlyIfAbsent is true, a string-keyed
// registration ($id URI, $anchor, $dynamicAnchor) yields to an existing entry;
// the pointer-keyed base URI is always recorded so every node resolves its own
// relative refs.
func (r *Registry) walkInto(schema *jsonschema.Schema, parentBase string, onlyIfAbsent bool) {
	if schema == nil {
		return
	}

	// Registering each pointer once and returning on a repeat keeps the walk
	// bounded over a graph that aliases or cycles. This walk rejects neither
	// shape; see the walked field for where the policy lives.
	if r.walked[schema] {
		return
	}

	r.walked[schema] = true

	currentBase := parentBase

	if schema.ID != "" && !r.inertIDs {
		if uriref.IsFragmentOnly(schema.ID) {
			// Draft-07: fragment-only $id acts as an anchor. Draft 2020-12
			// forbids a fragment in $id (core section 8.2.1), so there the
			// form registers nothing and a ref naming it stays unresolvable.
			if r.draft == Draft7 {
				anchor := schema.ID[1:] // strip leading '#'
				register(r.anchor, uriref.AnchorKey(currentBase, anchor), schema, onlyIfAbsent)
			}
		} else {
			resolved := uriref.IDBase(currentBase, schema.ID)
			register(r.URI, resolved, schema, onlyIfAbsent)

			currentBase = resolved
		}
	}

	// $anchor and $dynamicAnchor are Draft 2020-12 keywords; under Draft-07
	// they are unknown annotations, register nothing, and a plain-name
	// fragment naming one stays unresolvable.
	if r.draft != Draft7 {
		// 2020-12: $anchor keyword.
		if schema.Anchor != "" {
			register(r.anchor, uriref.AnchorKey(currentBase, schema.Anchor), schema, onlyIfAbsent)
		}

		// 2020-12: $dynamicAnchor keyword. Also registered as a regular anchor
		// (accessible via $ref).
		if schema.DynamicAnchor != "" {
			key := uriref.AnchorKey(currentBase, schema.DynamicAnchor)
			register(r.anchor, key, schema, onlyIfAbsent)
			register(r.dynamicAnchor, key, schema, onlyIfAbsent)
		}
	}

	// Store base URI for this schema (used during $ref resolution). Draft-07
	// exception: a sibling $id doesn't affect $ref resolution.
	if r.draft == Draft7 && schema.Ref != "" && schema.ID != "" && !uriref.IsFragmentOnly(schema.ID) {
		r.baseURIs[schema] = parentBase
	} else {
		r.baseURIs[schema] = currentBase
	}

	// Recurse into all sub-schema fields. Every child inherits currentBase.
	for _, child := range r.deps.Children(schema) {
		r.walkInto(child, currentBase, onlyIfAbsent)
	}
}

// KnownSchema reports whether sc was registered by this registry's walks: it
// belongs to the root document or to a document walked in while the registry
// was built (for the compiled registry, the documents present at compile
// time). A schema materialized by the JSON-pointer fallback or fetched into a
// per-run clone is not known to the compiled registry.
func (r *Registry) KnownSchema(sc *jsonschema.Schema) bool {
	return r.walked[sc]
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
	c.walked = maps.Clone(r.walked)

	return &c
}

// NewSession derives a per-run [Session] sharing r's maps by reference until the
// first write clones them via [Session.EnsureOwned]. Requiring vet at
// construction forces every session to state its vetting policy: vet is the
// [FallbackVet] applied to each JSON-pointer fallback target the session
// materializes, and nil is reserved for the compile-time session, whose
// targets the compiler vets in one shared pass after resolution.
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
// the claimed key itself, printing it would repeat one URI and leave the holder
// unnamed, so holderName uses another URI the holder is registered under
// instead, found by the other key mapping to the same schema.
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

// absorb merges a scratch walk's registrations into r. The caller rejects a
// collision first, so every key the two share holds the same schema and a plain
// copy and an only-if-absent merge agree. The walked set merges too, and
// deliberately. It decides whether the parent reports or skips an unresolvable
// fragment ref inside the document, so a fetched document's nodes must reach
// it.
func (r *Registry) absorb(scratch *Registry) {
	maps.Copy(r.URI, scratch.URI)
	maps.Copy(r.anchor, scratch.anchor)
	maps.Copy(r.dynamicAnchor, scratch.dynamicAnchor)
	maps.Copy(r.baseURIs, scratch.baseURIs)
	maps.Copy(r.walked, scratch.walked)
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

// register stores s under key in reg. When onlyIfAbsent is true an existing
// entry is preserved, so a duplicate $id or anchor key within one document
// resolves to the first the walk reaches.
func register(reg map[string]*jsonschema.Schema, key string, s *jsonschema.Schema, onlyIfAbsent bool) {
	if onlyIfAbsent {
		if _, ok := reg[key]; ok {
			return
		}
	}

	reg[key] = s
}
