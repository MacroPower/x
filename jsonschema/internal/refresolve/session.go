package refresolve

import (
	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// Session is the per-run resolution view derived from a [Registry]: the ref and
// JSON-pointer caches, the per-run fallback registrations, the negative cache,
// and the dynamic scope. It is not safe for concurrent use; derive one per
// validation or inline run with [Registry.NewSession].
type Session struct {
	// The active registry: the shared compiled one until the first owned write,
	// then a private clone (see [Session.EnsureOwned]). Its deps and inertIDs
	// are the single source the fallback walk reads, so the session carries
	// no copies of them.
	reg *Registry

	// Per-run fallback registrations for schemas materialized by the
	// JSON-pointer fallback ([Session.ResolveJSONPointer]) and for substitutes.
	// Lookups consult the shared registry first and these second, so
	// concurrent runs never write the shared maps.
	fallbackURI      map[string]*jsonschema.Schema
	fallbackAnchor   map[string]*jsonschema.Schema
	fallbackBaseURIs map[*jsonschema.Schema]string

	// Maps every node a fallback registration holds to the frozen tree it is
	// a node of, so a JSON-pointer resolution rooted inside one finds its
	// tree.
	fallbackNodes map[*jsonschema.Schema]*schemavet.Frozen

	// Maps each materialized target's root to the [schemavet.Node] the vet
	// minted for it, so a caller that records targets reads the proof
	// rather than vetting the same target twice. [Session.FallbackNode]
	// reaches it from any node of the tree through fallbackNodes.
	fallbackMinted map[*jsonschema.Schema]schemavet.Node

	// The schemas the JSON-pointer fallback materialized this session, in
	// materialization order, each with the location that produced it. The
	// session exposes them via [Session.FallbackTargets] so the reference
	// closure walk can ref-walk each target and reach the documents behind it.
	fallbackTargets []FallbackTarget

	// Structural vet applied to each schema the JSON-pointer fallback
	// materializes, before registration (the [FallbackVet] passed to
	// [Registry.NewSession]). Every production session passes one. Only a test
	// whose walk materializes no target passes nil.
	fallbackVet FallbackVet

	refCache         map[refCacheKey]Result
	jsonPointerCache map[jsonPointerKey]fallbackResult

	// Negative cache of baseURIs the resolver could not serve this run; a nil
	// value is a plain miss, a non-nil value the error to replay.
	remoteMiss map[string]error

	dynamicScope []string

	owned bool
}

// refCacheKey identifies a plain $ref resolution within a run. The containing
// schema fixes the base URI the ref resolves against, so the pair suffices.
type refCacheKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	schema *jsonschema.Schema
	//nolint:unused // Read via struct equality when used as a map key.
	ref string
}

// jsonPointerKey identifies a JSON-pointer fallback lookup within a run.
type jsonPointerKey struct {
	//nolint:unused // Read via struct equality when used as a map key.
	root *jsonschema.Schema
	//nolint:unused // Read via struct equality when used as a map key.
	pointer string
}

// fallbackResult is a cached JSON-pointer fallback outcome: the materialized
// target, or the vet error that rejected it (both nil for a plain miss).
type fallbackResult struct {
	target *jsonschema.Schema
	err    error
}

// Registry returns the session's current registry: the shared compiled one, or
// the private clone once [Session.EnsureOwned] has run. A fetch closure writes
// through it after registering a remote document.
func (s *Session) Registry() *Registry { return s.reg }

// EnsureOwned gives this run its own copy of the registry maps before it writes
// any of them, so a registration cannot race a concurrent run sharing the
// compiled registry. It is idempotent: the first call clones, later calls reuse
// the owned copy.
func (s *Session) EnsureOwned() {
	if s.owned {
		return
	}

	s.reg = s.reg.Clone()
	s.owned = true
}

// RemoteMiss reports whether baseURI was recorded as unresolvable this run, and
// the error to replay (nil for a plain miss).
func (s *Session) RemoteMiss(baseURI string) (error, bool) {
	err, ok := s.remoteMiss[baseURI]

	return err, ok
}

// RecordRemoteMiss notes that the resolver could not serve baseURI this run so
// the fetch closure skips re-calling it. A nil err records a plain miss; a
// non-nil err is replayed on each later evaluation of the same ref.
func (s *Session) RecordRemoteMiss(baseURI string, err error) {
	if s.remoteMiss == nil {
		s.remoteMiss = map[string]error{}
	}

	s.remoteMiss[baseURI] = err
}

// SeedDynamicScope seeds the dynamic scope with rootBase, the root document's
// base URI. The caller invokes it once per run under Draft 2020-12; under
// Draft 7 the scope stays nil.
func (s *Session) SeedDynamicScope(rootBase string) {
	s.dynamicScope = []string{rootBase}
}

// EnterScope pushes base onto the dynamic scope when it differs from the current
// top, returning a function that pops it. When the scope is empty or base
// already tops it, no push happens and it returns nil, so the caller registers a
// defer only on the rarer resource-boundary crossing rather than on every node.
func (s *Session) EnterScope(base string) func() {
	if len(s.dynamicScope) == 0 || base == s.dynamicScope[len(s.dynamicScope)-1] {
		return nil
	}

	s.dynamicScope = append(s.dynamicScope, base)

	return func() { s.dynamicScope = s.dynamicScope[:len(s.dynamicScope)-1] }
}

// SchemaBase returns the base URI registered for sc, consulting the shared
// registry first and the per-run fallback registrations second.
func (s *Session) SchemaBase(sc *jsonschema.Schema) string {
	if base, ok := s.reg.baseURIs[sc]; ok {
		return base
	}

	return s.fallbackBaseURIs[sc]
}

// DocOf returns the registered document sc is a node of, and whether the
// registry holds one. A fallback target or substitute is not a registered
// document.
func (s *Session) DocOf(sc *jsonschema.Schema) (schemavet.Doc, bool) {
	return s.reg.DocOf(sc)
}

// FallbackNode returns the [schemavet.Node] the session minted for the
// materialized JSON-pointer target sc is a node of, narrowed to sc, and
// whether sc is one. The target's tree registers the $ids and anchors its
// nodes declare, so a reference resolves to a node below the root as readily
// as to the root; the proof covers the whole tree either way. A substitute's
// node is not one, since the session holds no minted proof for a substitute.
func (s *Session) FallbackNode(sc *jsonschema.Schema) (schemavet.Node, bool) {
	f, ok := s.fallbackNodes[sc]
	if !ok {
		return schemavet.Node{}, false
	}

	node, ok := s.fallbackMinted[f.Root()]
	if !ok {
		return schemavet.Node{}, false
	}

	return node.Narrow(sc)
}

// LookupURI resolves an absolute URI to its schema, consulting the shared
// registry first and the per-run fallback registrations second.
func (s *Session) LookupURI(uri string) (*jsonschema.Schema, bool) {
	if sc, ok := s.reg.URI[uri]; ok {
		return sc, true
	}

	sc, ok := s.fallbackURI[uri]

	return sc, ok
}

// LookupAnchor resolves a baseURI#anchor key, consulting the shared registry
// first and the per-run fallback registrations second.
func (s *Session) LookupAnchor(key string) (*jsonschema.Schema, bool) {
	if sc, ok := s.reg.anchor[key]; ok {
		return sc, true
	}

	sc, ok := s.fallbackAnchor[key]

	return sc, ok
}

// LookupAnchorWithFallback resolves a named anchor fragment within an
// already-located document, applying the cross-document precedence: the
// retrieval/current base first, then the document's own canonical base ($id)
// when docRoot declares one distinct from baseURI. Both the validator's
// resolution and the inliner's call it, so an anchor resolves identically on
// both paths.
func (s *Session) LookupAnchorWithFallback(
	baseURI string,
	docRoot *jsonschema.Schema,
	fragment string,
) (*jsonschema.Schema, bool) {
	if target, ok := s.LookupAnchor(uriref.AnchorKey(baseURI, fragment)); ok {
		return target, true
	}

	if canonBase := s.SchemaBase(docRoot); canonBase != "" && canonBase != baseURI {
		if target, ok := s.LookupAnchor(uriref.AnchorKey(canonBase, fragment)); ok {
			return target, true
		}
	}

	return nil, false
}

// LookupDynamicAnchor resolves a baseURI#name key against $dynamicAnchor
// registrations in the shared compile-time registry only. Unlike URI and anchor
// lookups it does not consult the per-run JSON-pointer fallback: a $dynamicAnchor
// a fallback materialized for an unrelated ref is outside any $dynamicRef's
// dynamic scope and must not be selectable as its target.
func (s *Session) LookupDynamicAnchor(key string) (*jsonschema.Schema, bool) {
	sc, ok := s.reg.dynamicAnchor[key]

	return sc, ok
}

// FallbackVet freezes and vets each schema the JSON-pointer fallback
// materializes, before the session registers it. It receives the base URI
// in effect at the target's position, which the target's own identifiers
// resolve against, and the locator a violation names. On success it returns
// the minted [schemavet.Node], whose tree the session registers and hands
// back as the resolution's target. A non-nil error rejects the target, so
// the resolution reports the error and [Result.TargetRejected] instead of a
// target, and an ill-formed schema reached only through the fallback cannot
// silently mis-validate or inline. Every session names its policy at
// construction ([Registry.NewSession]), and every production session passes
// a vet. The compile-time session, the validator's per-run sessions, and the
// inliner's session all vet each target where they materialize it.
type FallbackVet func(sc *jsonschema.Schema, base, locator string) (schemavet.Node, error)

// ResolveJSONPointer resolves a JSON Pointer fragment against a resource
// root: the root document, an embedded $id resource, or a fallback target.
// The frozen tree the root belongs to answers a pointer that stays on
// sub-schema keyword edges directly. A pointer that leaves the typed tree
// may still target a referenceable location with no typed field (a
// sub-schema carried as raw JSON in an unknown keyword, or the internals of
// a non-applicator keyword such as examples), so resolution continues from
// the deepest typed node through the schema's JSON form. A non-nil error
// reports a fallback target the session's [FallbackVet] rejected; an
// unlocatable pointer is a plain (nil, nil) miss.
func (s *Session) ResolveJSONPointer(
	root *jsonschema.Schema, fragment string, encoded bool,
) (*jsonschema.Schema, error) {
	segments, ok := jsonptr.FragmentSegments(fragment, encoded)
	if !ok {
		return nil, nil //nolint:nilnil // An unlocatable pointer is a plain miss, not an error.
	}

	// ID tracking during the JSON-form walk follows the same inertIDs policy
	// as the frozen tables: under a retrieval-base run a crossed $id must not
	// rebase the located schema, or its refs would absolutize against the $id
	// instead of the document's retrieval base.
	trackIDs := !s.reg.inertIDs

	node, rest, base := s.typedPrefix(root, segments)
	if len(rest) == 0 {
		return node, nil
	}

	return s.resolveJSONPointerViaJSON(root, segments, node, rest, base, trackIDs)
}

// typedPrefix follows segments from root through the frozen tree root belongs
// to and returns the deepest node it reaches, the segments left unconsumed
// there, and the base URI a child of that node inherits, which is where a
// JSON-form walk below it starts rebasing. A root outside every frozen tree
// consumes nothing.
func (s *Session) typedPrefix(
	root *jsonschema.Schema, segments []string,
) (*jsonschema.Schema, []string, string) {
	f, rootID, ok := s.locate(root)
	if !ok {
		return root, segments, s.SchemaBase(root)
	}

	prefix := f.Path(rootID)

	for i := len(segments); i > 0; i-- {
		if node, found := f.At(prefix + jsonptr.JoinTokens(segments[:i])); found {
			id, _ := f.ID(node)

			return node, segments[i:], f.ScopeBase(id)
		}
	}

	return root, segments, f.ScopeBase(rootID)
}

// locate returns the frozen tree sc is a node of and its id there, consulting
// the shared registry first and the per-run fallback registrations second.
func (s *Session) locate(sc *jsonschema.Schema) (*schemavet.Frozen, int, bool) {
	if doc, ok := s.reg.nodes[sc]; ok {
		f := doc.Frozen()
		id, _ := f.ID(sc)

		return f, id, true
	}

	if f, ok := s.fallbackNodes[sc]; ok {
		id, _ := f.ID(sc)

		return f, id, true
	}

	return nil, 0, false
}

// resolveJSONPointerViaJSON resolves a JSON Pointer through the schema's JSON
// encoding where its typed fields end, reaching locations typed traversal
// cannot. A located schema is freshly unmarshaled and unknown to the compiled
// registries. The session freezes and vets it through its [FallbackVet] and
// registers the frozen tree through the per-run fallback registries with the
// base URI in effect at its location. A rejected target registers nothing and
// joins no frontier, so the walk never reads past it. Results, including vet
// rejections, are cached per (root, pointer).
//
// The node, rest, and prefixBase parameters carry what the typed prefix
// already reached for these segments, so the JSON-form walk resumes there
// instead of retaking the typed steps.
func (s *Session) resolveJSONPointerViaJSON(
	root *jsonschema.Schema, segments []string,
	node *jsonschema.Schema, rest []string, prefixBase string, trackIDs bool,
) (*jsonschema.Schema, error) {
	if s.jsonPointerCache == nil {
		s.jsonPointerCache = map[jsonPointerKey]fallbackResult{}
	}

	key := jsonPointerKey{root: root, pointer: jsonptr.SegmentsKey(segments)}
	if cached, ok := s.jsonPointerCache[key]; ok {
		return cached.target, cached.err
	}

	target, base := jsonptr.SchemaAtJSONForm(
		node, rest, prefixBase, trackIDs, s.reg.deps.Materialize,
	)

	locator := s.SchemaBase(root) + "#" + displayPointer(segments)

	var vetErr error

	if target != nil {
		var minted schemavet.Node

		minted, vetErr = s.vetFallback(target, base, locator)
		target = minted.Root()

		if target != nil {
			s.RegisterFallback(minted)

			s.fallbackTargets = append(s.fallbackTargets, FallbackTarget{
				Schema:  target,
				Locator: locator,
			})
		}
	}

	s.jsonPointerCache[key] = fallbackResult{target: target, err: vetErr}

	return target, vetErr
}

// vetFallback runs the session's [FallbackVet] over a materialized target, or
// freezes the target under an empty profile where a test installed none.
func (s *Session) vetFallback(target *jsonschema.Schema, base, locator string) (schemavet.Node, error) {
	if s.fallbackVet != nil {
		return s.fallbackVet(target, base, locator)
	}

	return schemavet.FreezeNode(target, locator, base, schemavet.Profile{InertIDs: s.reg.inertIDs})
}

// FallbackTarget pairs a schema the JSON-pointer fallback materialized with the
// location it was resolved from, so the reference closure walk can ref-walk
// exactly the schemas the session materialized and reach the documents their
// own references name.
type FallbackTarget struct {
	// Schema is the root of the frozen target.
	Schema *jsonschema.Schema
	// Locator is the target's location for error paths: the base URI of the
	// document it was resolved within (possibly empty), "#", and the RFC 6901
	// pointer that located it.
	Locator string
}

// FallbackTargets returns the schemas the JSON-pointer fallback materialized in
// this session, in materialization order. The order is deterministic within a
// run because resolution follows the deterministic sub-schema traversal.
func (s *Session) FallbackTargets() []FallbackTarget {
	return s.fallbackTargets
}

// displayPointer renders decoded JSON Pointer segments back into an RFC 6901
// pointer for error locations, escaping each reference token.
func displayPointer(segments []string) string {
	return jsonptr.JoinTokens(segments)
}

// RegisterFetched registers a document fetched from a resolver under its
// retrieval URI, the base it was frozen against, and merges its $ids,
// anchors, base URIs, and nodes into the session's registry, the one path
// both engines take for a fetched document. It reports [ErrIDCollision] when
// the document claims a URI or anchor the registry already holds for a
// different schema, and merges nothing in that case, so a refused document
// leaves no half-written registry behind.
//
// Copy-on-write ownership stays the caller's. The compile-time session writes
// the shared registry on purpose, so a document fetched while compiling
// persists into the registry every run shares, and the validator's per-run
// session calls [Session.EnsureOwned] first so its registrations cannot race a
// concurrent run. A session over a registry built for one run, as the inliner
// builds, has nothing to protect and owns its maps already.
//
// A document whose own $id equals the URI it was fetched from names that key
// with that schema twice, so it registers one schema under one key rather
// than colliding with itself.
//
// In practice every collision surfaces in the URI space. Two documents share an
// anchor key only by sharing the base it hangs off, and sharing a base means
// claiming one URI, which the URI space reports first. The check covers the
// anchor spaces anyway, so the rule holds by construction rather than by that
// argument.
func (s *Session) RegisterFetched(doc schemavet.Doc) error {
	f := doc.Frozen()

	err := s.CheckFetched(f)
	if err != nil {
		return err
	}

	s.reg.absorb(doc)

	s.reg.URI[f.Base()] = f.Root()

	return nil
}

// CheckFetched reports the [ErrIDCollision] that [Session.RegisterFetched]
// would report for the same document, registering nothing. It serves the
// fetch closures, which check for a collision between the freeze and the
// structural vet. A caller that vetted first would report the structural
// cause instead, and one document carrying both faults would fail with two
// different sentinels depending on which engine read it, so the collision
// settles ahead of the vet at every fetch.
func (s *Session) CheckFetched(f *schemavet.Frozen) error {
	claimant := documentName(f.Base())

	for _, space := range []struct {
		claimed, held map[string]*jsonschema.Schema
		kind          string
	}{
		{claims(f, f.Base()), s.reg.URI, "URI"},
		{f.Anchors(), s.reg.anchor, "anchor"},
		{f.DynamicAnchors(), s.reg.dynamicAnchor, "dynamic anchor"},
	} {
		err := s.reg.collision(space.claimed, space.held, space.kind, claimant)
		if err != nil {
			return err
		}
	}

	return nil
}

// CheckFallbackDocument reports the [ErrIDCollision] that
// [Session.RegisterFallbackDocument] would report for a substitute frozen as
// f, registering nothing, so the inliner settles the collision ahead of the
// structural vet as the fetch closures do.
func (s *Session) CheckFallbackDocument(f *schemavet.Frozen, claimant string) error {
	return s.reg.collision(f.URIs(), s.reg.URI, "URI", claimant)
}

// RegisterFallbackDocument registers a caller-supplied substitute, which enters
// resolution space as a document of its own, into the per-run fallback
// registries. It reports [ErrIDCollision] when the substitute's $id names a URI
// the shared registry already holds for a different schema, the one claim a
// substitute makes that a real document could answer instead. The claimant
// argument names the substitute in that message, so the caller can identify the
// reference whose fallback supplied it.
//
// Anchors and the per-run fallback registrations are outside the check, and both
// exclusions are load-bearing. A substitute is frozen against the base in
// effect at the failing reference rather than a base of its own, so its
// anchors land in the containing document's anchor space. No reference
// reaches them, because the substitute path builds no anchor registry a
// lookup consults before the shared one. The check skips the fallback
// registrations because the parent consults a fallback once per referencing
// node and freezes the substitute afresh each time, so one substitute
// answering several references would otherwise collide with its own earlier
// copies.
func (s *Session) RegisterFallbackDocument(doc schemavet.Doc, claimant string) error {
	err := s.CheckFallbackDocument(doc.Frozen(), claimant)
	if err != nil {
		return err
	}

	s.mergeFallback(doc.Frozen())

	return nil
}

// RegisterFallback records a schema the JSON-pointer fallback materialized:
// its subtree's base URIs, $ids, and anchors join the per-run fallback
// registries, and its root is remembered with the proof the vet minted. Such
// a target is a fragment of a document the run already registered rather
// than a document of its own, so it makes no identifier claim against
// another document and no collision check applies; two targets claiming one
// key resolve first-write-wins, in materialization order.
func (s *Session) RegisterFallback(node schemavet.Node) {
	if s.fallbackMinted == nil {
		s.fallbackMinted = map[*jsonschema.Schema]schemavet.Node{}
	}

	s.fallbackMinted[node.Root()] = node
	s.mergeFallback(node.Frozen())
}

// mergeFallback folds a frozen tree's tables into the per-run fallback
// registries.
//
// It deliberately drops $dynamicAnchor registrations from the fallback scope.
// LookupDynamicAnchor resolves only against the shared registry, so a dynamic
// anchor a fallback materialized cannot pollute an unrelated $dynamicRef's
// dynamic scope.
//
// The merge is first-write-wins so two schemas registering the same absolute
// $id/$anchor key resolve deterministically to the earliest-materialized one,
// matching the frozen tables' own precedence rather than a map-iteration race.
func (s *Session) mergeFallback(f *schemavet.Frozen) {
	if s.fallbackBaseURIs == nil {
		s.fallbackURI = map[string]*jsonschema.Schema{}
		s.fallbackAnchor = map[string]*jsonschema.Schema{}
		s.fallbackBaseURIs = map[*jsonschema.Schema]string{}
		s.fallbackNodes = map[*jsonschema.Schema]*schemavet.Frozen{}
	}

	for k, v := range f.URIs() {
		if _, ok := s.fallbackURI[k]; !ok {
			s.fallbackURI[k] = v
		}
	}

	for k, v := range f.Anchors() {
		if _, ok := s.fallbackAnchor[k]; !ok {
			s.fallbackAnchor[k] = v
		}
	}

	// Base URIs and tree membership key on the schema pointer, which is
	// unique per node, so no cross-schema collision is possible and a plain
	// write is correct.
	for id, node := range f.Nodes() {
		s.fallbackBaseURIs[node] = f.NodeBase(id)
		s.fallbackNodes[node] = f
	}
}
