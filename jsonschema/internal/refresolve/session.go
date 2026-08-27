package refresolve

import (
	"maps"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
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
	// compile-time session exposes them via [Session.FallbackTargets] so
	// content checks extend to exactly the schemas the reference walk resolved.
	fallbackTargets []FallbackTarget

	// Structural vet applied to each schema the JSON-pointer fallback
	// materializes, before registration (see [Session.SetFallbackVet]). Nil
	// skips vetting: the compile-time session leaves it unset because
	// the compiler vets its [Session.FallbackTargets] in one shared pass after
	// resolution.
	fallbackVet func(sc *jsonschema.Schema, locator string) error

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

// SetFallbackVet installs the structural vet applied to each schema the
// JSON-pointer fallback materializes, before it is registered. A non-nil error
// rejects the target: the resolution reports the error instead of a target, so
// an ill-formed schema reached only through the fallback cannot silently
// mis-validate or inline. The validator's per-run sessions and the inliner's
// session install their vetting policy here; the compile-time session
// leaves it unset and the compiler vets its [Session.FallbackTargets] in one
// shared pass instead.
func (s *Session) SetFallbackVet(vet func(sc *jsonschema.Schema, locator string) error) {
	s.fallbackVet = vet
}

// ResolveJSONPointer resolves a JSON Pointer fragment against a schema. Typed
// traversal handles the common case; when it fails the pointer may still target
// a referenceable location with no typed field (a sub-schema carried as raw JSON
// in an unknown keyword, or the internals of a non-applicator keyword such as
// examples), so resolution falls back to walking the schema's JSON form. A
// non-nil error reports a fallback target the installed vet rejected (see
// [Session.SetFallbackVet]); an unlocatable pointer is a plain (nil, nil) miss.
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

// resolveJSONPointerViaJSON resolves a JSON Pointer by walking the schema's JSON
// encoding rather than its typed fields, reaching locations typed traversal
// cannot. A located schema is freshly unmarshaled and unknown to the compiled
// registries; it is vetted (when a vet is installed) and registered through the
// per-run fallback registries with the base URI in effect at its location.
// Results, including vet rejections, are cached per (root, pointer).
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
		vetErr = s.fallbackVet(target, locator)
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
// location it was resolved from, so a compile-time caller can run content
// checks over exactly the schemas the reference walk materialized.
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

// RegisterFallback walks a schema materialized by the JSON-pointer fallback (or
// a caller-supplied substitute) and records its subtree's base URIs, $ids, and
// anchors in the per-run fallback registries. A scratch registry collects the
// walk output so the shared registry stays untouched and concurrent runs cannot
// race on it.
func (s *Session) RegisterFallback(sc *jsonschema.Schema, base string) {
	scratch := NewRegistry(s.reg.deps, s.reg.draft, s.reg.inertIDs)
	// WalkFetched keeps the first entry for a duplicate $id/$anchor key within
	// the document, matching the precedence the validator's fetched-document
	// walk applies; the plain Walk would resolve such a duplicate last-wins
	// and the two engines would disagree on the same reference.
	scratch.WalkFetched(sc, base)

	if s.fallbackBaseURIs == nil {
		s.fallbackURI = map[string]*jsonschema.Schema{}
		s.fallbackAnchor = map[string]*jsonschema.Schema{}
		s.fallbackBaseURIs = map[*jsonschema.Schema]string{}
	}

	// $dynamicAnchor registrations are deliberately not carried into the
	// fallback scope: LookupDynamicAnchor resolves only against the shared
	// registry, so a dynamic anchor a fallback materialized cannot pollute an
	// unrelated $dynamicRef's dynamic scope.
	//
	// Merge first-write-wins so two distinct fallback schemas registering the
	// same absolute $id/$anchor key (only reachable from a malformed
	// duplicate-key schema) resolve deterministically to the earliest-materialized
	// one, matching register's onlyIfAbsent precedence rather than a
	// map-iteration race.
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
