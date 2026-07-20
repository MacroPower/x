package refresolve

import (
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// Fetch is the caller's remote-document registration strategy, the seam
// unifying the validator's remote fetch (both the compile-time gate and each
// per-run session) and the inliner's fetchDoc. [Session.ResolveRef] calls it for
// a non-fragment ref whose base URI is not already registered. It returns the
// registered document on success, (nil, nil) for a plain miss, or (nil, err) for
// a failure whose error becomes [Result.Err]. The closure owns resolver
// invocation, deep copy, registration target, and negative caching; the core
// owns the resolution decision tree around it.
//
// The upstream Schema.Resolve loader is a distinct concern: it must never fail
// resolution on a miss, so it does not go through Fetch, sharing only the
// resolver-invocation helper.
type Fetch func(baseURI string) (*jsonschema.Schema, error)

// Result is the structured outcome of a reference resolution that both engines
// map to their own error and attribution shapes.
type Result struct {
	// Target is the resolved schema, nil on failure.
	Target *jsonschema.Schema

	// Err wraps [ErrRefResolve] when the fetch closure reported a failure; nil
	// otherwise (including an ordinary unresolved fragment).
	Err error

	// DocumentURI is the located document's base URI for a non-fragment ref, ""
	// for a same-document fragment ref.
	DocumentURI string

	// Fragment is the fragment portion for a pointer target within a located
	// document, "" otherwise.
	Fragment string
}

// ResolveRef resolves a $ref string to a target, caching a successful result
// per (schema, ref) for the run. Resolution is deterministic within a run
// because the registries are fixed at compile time (or cloned once per run when
// a resolver is configured). Only successes are cached: a failure re-resolves,
// so the fetch closure's negative cache governs whether the resolver is
// re-called.
func (s *Session) ResolveRef(schema *jsonschema.Schema, ref string, fetch Fetch) Result {
	key := refCacheKey{schema: schema, ref: ref}
	if cached, ok := s.refCache[key]; ok {
		return cached
	}

	res := s.resolveRefUncached(schema, ref, fetch)
	if res.Target != nil {
		if s.refCache == nil {
			s.refCache = map[refCacheKey]Result{}
		}

		s.refCache[key] = res
	}

	return res
}

// resolveRefUncached performs the resolution decision tree behind ResolveRef's
// per-run cache.
func (s *Session) resolveRefUncached(schema *jsonschema.Schema, ref string, fetch Fetch) Result {
	parsed, err := url.Parse(ref)
	if err != nil {
		return Result{}
	}

	// Fragment-only refs (e.g. "#", "#/$defs/foo", "#anchor").
	//nolint:nestif // Resolution walks distinct fragment forms (pointer, anchor, root).
	if uriref.IsFragmentOnly(ref) {
		fragment := parsed.Fragment

		// Find the root of the current resource.
		resourceRoot := s.reg.root

		base := s.SchemaBase(schema)
		if base != "" {
			if target, ok := s.LookupURI(base); ok {
				resourceRoot = target
			}
		}

		if fragment == "" {
			return Result{Target: resourceRoot}
		}

		// JSON Pointer. Pass the still-encoded fragment so a member name escaped
		// as %2F is not mistaken for a pointer separator.
		if strings.HasPrefix(fragment, "/") {
			raw, encoded := uriref.RawFragment(parsed)

			return Result{Target: s.ResolveJSONPointer(resourceRoot, raw, encoded)}
		}

		// Anchor reference.
		if target, ok := s.LookupAnchor(uriref.AnchorKey(base, fragment)); ok {
			return Result{Target: target}
		}

		return Result{}
	}

	// Non-fragment ref: resolve against the current schema's base URI.
	base := s.SchemaBase(schema)
	absRef := uriref.ResolveURI(base, ref)

	parsedAbs, err := url.Parse(absRef)
	if err != nil {
		return Result{}
	}

	fragment := parsedAbs.Fragment
	rawFrag, fragEncoded := uriref.RawFragment(parsedAbs)
	parsedAbs.Fragment = ""
	parsedAbs.RawFragment = ""
	baseURI := parsedAbs.String()

	target, ok := s.LookupURI(baseURI)
	if !ok {
		// Try remote resolution as fallback.
		cp, fetchErr := fetch(baseURI)
		if cp == nil {
			return Result{Err: fetchErr}
		}

		target = cp
	}

	if fragment == "" {
		return Result{Target: target, DocumentURI: baseURI}
	}

	// JSON Pointer within resolved schema.
	if strings.HasPrefix(fragment, "/") {
		if t := s.ResolveJSONPointer(target, rawFrag, fragEncoded); t != nil {
			return Result{Target: t, DocumentURI: baseURI, Fragment: fragment}
		}

		return Result{}
	}

	// Anchor within resolved schema, resolved via the shared cross-document
	// precedence (retrieval base first, then the document's canonical $id base).
	if anchorTarget, ok := s.LookupAnchorWithFallback(baseURI, target, fragment); ok {
		return Result{Target: anchorTarget, DocumentURI: baseURI}
	}

	return Result{}
}

// ResolveDynamicRef resolves a $dynamicRef string. It resolves statically first
// (as a $ref), then engages dynamic resolution only when static resolution
// landed on the schema bearing a $dynamicAnchor of the fragment name
// (bookending), walking the dynamic scope outermost to innermost for the first
// match. The inliner never calls it.
func (s *Session) ResolveDynamicRef(schema *jsonschema.Schema, ref string, fetch Fetch) Result {
	parsed, err := url.Parse(ref)
	if err != nil {
		return Result{}
	}

	fragment := parsed.Fragment

	// Phase 1: static resolution (same as $ref).
	static := s.ResolveRef(schema, ref, fetch)
	if static.Target == nil {
		return static
	}

	// JSON Pointer fragments (and whole-document refs) bypass dynamic resolution.
	if strings.HasPrefix(fragment, "/") || fragment == "" {
		return static
	}

	// Phase 2: bookending check. Dynamic resolution engages only when static
	// resolution actually landed on the schema that bears a $dynamicAnchor of
	// the fragment name, not merely when the static target's resource defines
	// one somewhere.
	staticBase := s.SchemaBase(static.Target)
	if anchored, ok := s.LookupDynamicAnchor(uriref.AnchorKey(staticBase, fragment)); !ok || anchored != static.Target {
		return static // no bookend -> behave like $ref
	}

	// Phase 3: walk dynamic scope outermost->innermost for the first matching
	// $dynamicAnchor.
	for _, scopeBase := range s.dynamicScope {
		if target, ok := s.LookupDynamicAnchor(uriref.AnchorKey(scopeBase, fragment)); ok {
			return Result{Target: target}
		}
	}

	return static
}
