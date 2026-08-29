package jsonschema

import (
	"errors"
	"fmt"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
)

// refClosure is the reference-closure walk both engines drive. It statically
// resolves every $ref (and, under a dialect with $dynamicRef, every
// $dynamicRef) reachable from the root, from each registry-known document, and
// from each JSON-pointer fallback target, through the shared resolution core.
// Resolving is also what fetches remote documents. A non-fragment ref whose
// document is absent triggers the session's fetch, which consults the
// [RefResolver] at most once per URI (the session negative-caches misses and
// failures) and registers the document in the registry the walk then drains.
// Compile runs it to build its validator, and Inline runs it before expanding
// anything, so the two engines hold the same set of documents and vet the same
// graph.
//
// The caller supplies the two hooks that differ. The document hook runs once
// per document the walk reaches through the registry, which is where Compile
// vets the document and folds it into its node index. The target hook runs
// once per JSON-pointer fallback target, which is where Compile vets the
// targets its own session materialized without a [refresolve.FallbackVet]; a
// session carrying one vets each target as the walk materializes it, so its
// caller passes nil here.
//
// Strictness splits by provenance and by keyword. The root and registry-known
// documents obey strictRef and strictDyn. A reference that resolves to nothing
// while its document is present can never resolve later, so it fails the walk.
// The error wraps the resolver's reported error when one exists and
// [ErrNotResolved] otherwise. The walk tolerates a document miss regardless of
// any carried resolver error, which upholds the Remote References contract. A
// resolver that serves a document only later leaves the report to the engine
// that reaches the reference.
//
// The walk always treats fallback targets tolerantly, since that pass exists
// to materialize deeper targets and fetch their documents. A fallback-borne
// node sits outside the compiled registry, so under Compile the validation
// walk never silently skips a miss there.
type refClosure struct {
	// The session every reference resolves through, owning the registry the
	// walk drains.
	session *refresolve.Session

	// The engine's remote-document fetch closure, handed to each resolution so
	// the resolution retrieves and registers an absent document.
	fetch refresolve.Fetch

	// The document the walk starts from, which the walk reports with an empty
	// locator. The
	// walk visits it before the registry frontier, so a reference in it
	// resolves and caches its answer before any fetch can change what a later
	// document registers.
	root *Schema

	// Runs for each registry document the walk reaches, in key-sorted order.
	// Nil skips the pass.
	onDoc func(s *Schema, uri string) error

	// Runs for each JSON-pointer fallback target the walk materializes, in
	// materialization order. Each target's locator names the pointer that
	// materialized it, so a target's violation names that pointer. Nil skips
	// the pass.
	onTarget func(ft refresolve.FallbackTarget) error

	// Whether the run's dialect resolves $dynamicRef at all.
	dynamicRef bool

	// Whether a $ref that resolves to nothing inside a present document fails
	// the walk. Compile demands it, because the validation walk silently skips
	// a fragment miss on a registry-known node on the strength of this pass
	// (see validateRef), so a run that relaxed it there would let a broken
	// fragment ref through both passes.
	strictRef bool

	// The strictRef counterpart for $dynamicRef.
	strictDyn bool
}

// run walks the closure until it is complete, returning the first error a hook
// or a strict resolution reports.
//
// The fixpoint loop drains three monotone frontiers until none advances: the
// not-yet-processed registry documents in key-sorted order, the
// fallback-target hook cursor, and the fallback-target ref cursor. Fetches and
// pointer fallbacks only append to those frontiers, and the session caches make
// re-resolution idempotent, so the loop terminates.
func (c refClosure) run() error {
	// Cross-pass dedup on top of Walk's per-call dedup, so a node reachable
	// from several documents resolves its references once. Skipping an
	// already-walked node's children is sound because the pass that first
	// reached the node walked its whole subtree. Fallback targets are fresh
	// ParseSchemaValue objects, never pointer-aliased into registry
	// documents, so a tolerant fallback visit can never suppress a later
	// strict check through this set.
	walked := map[*Schema]bool{}

	walkRefs := func(doc *Schema, locator string, strict bool) error {
		//nolint:wrapcheck // Walk relays the callback's already-constructed error.
		return Walk(doc, func(loc Location, s *Schema) error {
			if walked[s] {
				return SkipChildren
			}

			walked[s] = true

			if s.Ref != "" {
				res := c.session.ResolveRef(s, s.Ref, c.fetch)

				err := refWalkError(res, KeywordRef, s.Ref, locator, loc, strict && c.strictRef)
				if err != nil {
					return err
				}
			}

			if c.dynamicRef && s.DynamicRef != "" {
				res := c.session.ResolveDynamicRef(s, s.DynamicRef, c.fetch)

				err := refWalkError(
					res, KeywordDynamicRef, s.DynamicRef, locator, loc, strict && c.strictDyn)
				if err != nil {
					return err
				}
			}

			return nil
		})
	}

	// The root document first, so its references cache their answers before a
	// fetch can register a document that would resolve one of them elsewhere.
	err := walkRefs(c.root, "", true)
	if err != nil {
		return err
	}

	processed := map[string]bool{}
	hookCursor, refCursor := 0, 0

	for {
		progressed := false

		// Registry-known documents, key-sorted for stable attribution. The
		// first round covers the URIs registry construction seeded before any
		// fetch (the root under its normalized base and nested absolute-$id
		// subschemas); later rounds cover documents the walks fetched. Each is
		// handed to onDoc and then strictly ref-walked, since an in-document
		// reference that cannot resolve now never can.
		var pending []string

		// One snapshot per round. It holds because no fetch this walk drives
		// clones the registry. Only a validation run's copy-on-write fetch calls
		// EnsureOwned, and that run never drives a closure. A fetch that
		// registers mid-round still lands, since the next round re-reads.
		reg := c.session.Registry()

		for uri := range reg.URI {
			if !processed[uri] {
				pending = append(pending, uri)
			}
		}

		slices.Sort(pending)

		for _, uri := range pending {
			processed[uri] = true
			progressed = true

			s := reg.URI[uri]

			if c.onDoc != nil {
				err := c.onDoc(s, uri)
				if err != nil {
					return err
				}
			}

			err := walkRefs(s, uri+"#", true)
			if err != nil {
				return err
			}
		}

		// The fallback targets materialized since the last round: schemas
		// carved out of unknown keywords or non-applicator keyword internals,
		// which no typed pass reaches and which never join the registry. The
		// list is append-only, so a cursor covers late arrivals.
		for ; hookCursor < len(c.session.FallbackTargets()); hookCursor++ {
			progressed = true

			if c.onTarget == nil {
				continue
			}

			err := c.onTarget(c.session.FallbackTargets()[hookCursor])
			if err != nil {
				return err
			}
		}

		// Ref-walk the same targets tolerantly. The pass materializes targets
		// one reference deeper and fetches their documents, without failing on
		// a miss.
		for ; refCursor < len(c.session.FallbackTargets()); refCursor++ {
			ft := c.session.FallbackTargets()[refCursor]
			progressed = true

			err := walkRefs(ft.Schema, ft.Locator, false)
			if err != nil {
				return err
			}
		}

		if !progressed {
			return nil
		}
	}
}

// refWalkError maps one resolution outcome to the closure walk's error policy.
// It answers nil when the target resolved, when the walk is tolerant, or when
// the document is missing (with or without a carried resolver error; the engine
// that reaches the reference reports it, per the Remote References contract). A
// strict miss inside a present document wraps the resolver's reported error
// when one exists and [ErrNotResolved] otherwise, naming the bearing node
// through the document locator and the node's pointer. It reports an
// [ErrIDCollision] whether the walk is strict or tolerant, since no later fetch
// can resolve it.
func refWalkError(res refresolve.Result, keyword, ref, locator string, loc Location, strict bool) error {
	// This check reports an identifier collision ahead of the strictness gate,
	// for the reason the gate exists. A tolerant pass defers an answer a later
	// fetch may change, and no later fetch changes a refused registration.
	// Without this the tolerant pass over the fallback targets would swallow a
	// collision reachable only through a target's own ref.
	collided := res.Target == nil && errors.Is(res.Err, ErrIDCollision)

	if !collided && (!strict || res.Target != nil || res.DocumentMiss) {
		return nil
	}

	cause := res.Err
	if cause == nil {
		cause = ErrNotResolved
	}

	return fmt.Errorf("%s%s: cannot resolve %s %q: %w", locator, loc.Pointer, keyword, ref, cause)
}
