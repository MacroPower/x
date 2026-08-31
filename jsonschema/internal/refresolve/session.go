package refresolve

import (
	"maps"
	"strings"

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
	// then a private clone (see [Session.EnsureOwned]). Its deps, draft, and
	// inertIDs are the single source RegisterFallback reads, so the session
	// carries no copies of them.
	reg *Registry

	// Per-run fallback registrations for schemas materialized by the
	// JSON-pointer fallback ([Session.ResolveJSONPointer]). Lookups consult the
	// shared registry first and these second, so concurrent runs never write
	// the shared maps.
	fallbackURI      map[string]*jsonschema.Schema
	fallbackAnchor   map[string]*jsonschema.Schema
	fallbackBaseURIs map[*jsonschema.Schema]string

	// The schemas the JSON-pointer fallback materialized this session, in
	// materialization order, each with the location that produced it. The
	// session exposes them via [Session.FallbackTargets] so the reference
	// closure walk can ref-walk each target and reach the documents behind it.
	fallbackTargets []FallbackTarget

	// Structural vet applied to each schema the JSON-pointer fallback
	// materializes, before registration (the [FallbackVet] passed to
	// [Registry.NewSession]). Every production session passes one. Only a test
	// whose walk materializes no target passes nil, which skips vetting.
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

// FallbackVet is the structural vet a session applies to each schema the
// JSON-pointer fallback materializes, before it is registered. On success it
// returns the minted [schemavet.Node], proof the target passed the structural
// checks. A non-nil error rejects the target, so the resolution reports the
// error and [Result.TargetRejected] instead of a target, and an ill-formed
// schema reached only through the fallback cannot silently mis-validate or
// inline. Every session names its policy at construction
// ([Registry.NewSession]), and every production session passes a vet. The
// compile-time session, the validator's per-run sessions, and the inliner's
// session all vet each target where they materialize it. Only a test whose
// walk materializes no target passes nil, which skips vetting.
type FallbackVet func(sc *jsonschema.Schema, locator string) (schemavet.Node, error)

// ResolveJSONPointer resolves a JSON Pointer fragment against a schema. Typed
// traversal handles the common case; when it fails the pointer may still target
// a referenceable location with no typed field (a sub-schema carried as raw JSON
// in an unknown keyword, or the internals of a non-applicator keyword such as
// examples), so resolution falls back to walking the schema's JSON form. A
// non-nil error reports a fallback target the session's [FallbackVet]
// rejected; an unlocatable pointer is a plain (nil, nil) miss.
func (s *Session) ResolveJSONPointer(
	root *jsonschema.Schema, fragment string, encoded bool,
) (*jsonschema.Schema, error) {
	segments, ok := jsonptr.FragmentSegments(fragment, encoded)
	if !ok {
		return nil, nil //nolint:nilnil // An unlocatable pointer is a plain miss, not an error.
	}

	if target := jsonptr.TraverseSchema(root, segments); target != nil {
		return target, nil
	}

	return s.resolveJSONPointerViaJSON(root, segments)
}

// resolveJSONPointerViaJSON resolves a JSON Pointer through the schema's JSON
// encoding where its typed fields end, reaching locations typed traversal
// cannot. A located schema is freshly unmarshaled and unknown to the compiled
// registries. The session vets it (when a vet is installed) and registers it
// through the per-run fallback registries with the base URI in effect at its
// location. A rejected target registers nothing and joins no frontier, so the
// walk never reads past it. Results, including vet rejections, are cached per
// (root, pointer).
func (s *Session) resolveJSONPointerViaJSON(
	root *jsonschema.Schema, segments []string,
) (*jsonschema.Schema, error) {
	if s.jsonPointerCache == nil {
		s.jsonPointerCache = map[jsonPointerKey]fallbackResult{}
	}

	key := jsonPointerKey{root: root, pointer: jsonptr.SegmentsKey(segments)}
	if cached, ok := s.jsonPointerCache[key]; ok {
		return cached.target, cached.err
	}

	// ID tracking during pointer navigation follows the same inertIDs policy
	// as the registry walk: under a retrieval-base run a crossed $id must not
	// rebase the located schema, or its refs would absolutize against the $id
	// instead of the document's retrieval base.
	target, base := jsonptr.SchemaAtJSONPointer(
		root, segments, s.SchemaBase(root), !s.reg.inertIDs, s.reg.deps.Materialize,
	)

	locator := s.SchemaBase(root) + "#" + displayPointer(segments)

	var vetErr error

	if target != nil && s.fallbackVet != nil {
		_, vetErr = s.fallbackVet(target, locator)
		if vetErr != nil {
			target = nil
		}
	}

	if target != nil {
		s.RegisterFallback(target, base)

		s.fallbackTargets = append(s.fallbackTargets, FallbackTarget{
			Schema:  target,
			Locator: locator,
		})
	}

	s.jsonPointerCache[key] = fallbackResult{target: target, err: vetErr}

	return target, vetErr
}

// FallbackTarget pairs a schema the JSON-pointer fallback materialized with the
// location it was resolved from, so the reference closure walk can ref-walk
// exactly the schemas the session materialized and reach the documents their
// own references name.
type FallbackTarget struct {
	// Schema is the freshly-unmarshaled schema at the pointer target.
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
	var b strings.Builder

	for _, seg := range segments {
		b.WriteByte('/')
		b.WriteString(jsonptr.Escape(seg))
	}

	return b.String()
}

// walkScratch walks sc into a fresh registry, so the caller can inspect what the
// document claims before any of it reaches a live registry. The scratch starts
// empty, so [Registry.WalkFetched]'s only-if-absent gate settles a repeat within
// one document here, leaving the collision check to speak only for keys another
// document holds.
func (s *Session) walkScratch(sc *jsonschema.Schema, base string) *Registry {
	scratch := NewRegistry(s.reg.deps, s.reg.draft, s.reg.inertIDs)
	scratch.WalkFetched(sc, base)

	return scratch
}

// RegisterFetched registers a document fetched from a resolver under its
// retrieval URI and merges its subtree's $ids, anchors, and base URIs into the
// session's registry, the one path both engines take for a fetched document. It
// reports [ErrIDCollision] when the document claims a URI or anchor the registry
// already holds for a different schema, and merges nothing in that case, so a
// refused document leaves no half-written registry behind.
//
// Copy-on-write ownership stays the caller's. The compile-time session writes
// the shared registry on purpose, so a document fetched while compiling
// persists into the registry every run shares, and the validator's per-run
// session calls [Session.EnsureOwned] first so its registrations cannot race a
// concurrent run. A session over a registry built for one run, as the inliner
// builds, has nothing to protect and owns its maps already.
//
// The scratch walk runs first, and checkFetched then writes the retrieval URI
// over its result. A document whose own $id equals the URI it was fetched from
// therefore registers one schema under one key rather than colliding with
// itself, because both writes name that key with that schema. The order
// carries no weight either way, since the collision check reads the finished
// scratch rather than watching it fill.
//
// In practice every collision surfaces in the URI space. Two documents share an
// anchor key only by sharing the base it hangs off, and sharing a base means
// claiming one URI, which the URI space reports first. The check covers the
// anchor spaces anyway, so the rule holds by construction rather than by that
// argument.
func (s *Session) RegisterFetched(doc *jsonschema.Schema, baseURI string) error {
	scratch, err := s.checkFetched(doc, baseURI)
	if err != nil {
		return err
	}

	s.reg.absorb(scratch)

	return nil
}

// CheckFetched reports the [ErrIDCollision] that [Session.RegisterFetched]
// would report for the same document, registering nothing. A caller that goes
// on to register walks the document into a scratch registry twice, once here
// and once there; a fetch is rare enough that sharing the walk is not worth the
// state it would take.
//
// It exists for the two callers that run a structural vet between the fetch and
// the registration. Every other caller registers at the fetch, so the collision
// settles there; a caller that vetted first would report the structural cause
// instead, and one document carrying both faults would fail with two different
// sentinels depending on which engine read it. Checking without merging keeps
// the order without registering a document the vet goes on to reject.
func (s *Session) CheckFetched(doc *jsonschema.Schema, baseURI string) error {
	_, err := s.checkFetched(doc, baseURI)

	return err
}

// checkFetched walks doc into a scratch registry and reports the first
// identifier it claims that the session's registry already holds for a
// different schema. It returns the scratch so a caller that means to register
// can merge the same walk rather than repeating it.
func (s *Session) checkFetched(doc *jsonschema.Schema, baseURI string) (*Registry, error) {
	scratch := s.walkScratch(doc, baseURI)
	scratch.URI[baseURI] = doc

	claimant := documentName(baseURI)

	for _, space := range []struct {
		claimed, held map[string]*jsonschema.Schema
		kind          string
	}{
		{scratch.URI, s.reg.URI, "URI"},
		{scratch.anchor, s.reg.anchor, "anchor"},
		{scratch.dynamicAnchor, s.reg.dynamicAnchor, "dynamic anchor"},
	} {
		err := s.reg.collision(space.claimed, space.held, space.kind, claimant)
		if err != nil {
			return nil, err
		}
	}

	return scratch, nil
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
// exclusions are load-bearing. A substitute registers under the base in effect at
// the failing reference rather than a base of its own, so its anchors land in
// the containing document's anchor space. No reference reaches them, because the
// substitute path builds no anchor registry a lookup consults before the shared
// one. The check skips the fallback registrations because the parent consults a
// fallback once per referencing node and clones the substitute afresh each time,
// so one substitute answering several references would otherwise collide with
// its own earlier copies.
func (s *Session) RegisterFallbackDocument(sc *jsonschema.Schema, base, claimant string) error {
	scratch := s.walkScratch(sc, base)

	err := s.reg.collision(scratch.URI, s.reg.URI, "URI", claimant)
	if err != nil {
		return err
	}

	s.mergeFallback(scratch)

	return nil
}

// RegisterFallback walks a schema the JSON-pointer fallback materialized and
// records its subtree's base URIs, $ids, and anchors in the per-run fallback
// registries. Such a target is a fragment of a document the run already
// registered rather than a document of its own, so it makes no identifier claim
// against another document and no collision check applies; two targets claiming
// one key resolve first-write-wins, in materialization order.
func (s *Session) RegisterFallback(sc *jsonschema.Schema, base string) {
	s.mergeFallback(s.walkScratch(sc, base))
}

// mergeFallback folds a scratch walk into the per-run fallback registries.
//
// It deliberately drops $dynamicAnchor registrations from the fallback scope.
// LookupDynamicAnchor resolves only against the shared registry, so a dynamic
// anchor a fallback materialized cannot pollute an unrelated $dynamicRef's
// dynamic scope.
//
// The merge is first-write-wins so two schemas registering the same absolute
// $id/$anchor key resolve deterministically to the earliest-materialized one,
// matching register's onlyIfAbsent precedence rather than a map-iteration race.
func (s *Session) mergeFallback(scratch *Registry) {
	if s.fallbackBaseURIs == nil {
		s.fallbackURI = map[string]*jsonschema.Schema{}
		s.fallbackAnchor = map[string]*jsonschema.Schema{}
		s.fallbackBaseURIs = map[*jsonschema.Schema]string{}
	}

	for k, v := range scratch.URI {
		if _, ok := s.fallbackURI[k]; !ok {
			s.fallbackURI[k] = v
		}
	}

	for k, v := range scratch.anchor {
		if _, ok := s.fallbackAnchor[k]; !ok {
			s.fallbackAnchor[k] = v
		}
	}

	// Base URIs key on the schema pointer, which is unique per node, so no
	// cross-schema collision is possible and a plain copy is correct.
	maps.Copy(s.fallbackBaseURIs, scratch.baseURIs)
}
