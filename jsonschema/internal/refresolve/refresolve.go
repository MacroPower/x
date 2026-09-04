// Package refresolve is the shared $ref/$dynamicRef/$anchor resolution core for
// the jsonschema validator and inliner. It owns registry construction, base-URI
// computation, anchor precedence, unified remote fetch orchestration, and
// structured ref-failure attribution, so the two engines resolve references
// identically by construction.
//
// The package depends on the upstream [jsonschema.Schema] type and the internal
// schemavet, uriref, and jsonptr helpers, but not on the parent jsonschema
// package. Every document it holds arrives as a [schemavet.Doc], a vetted
// tree the parent froze at the boundary the document crossed, and the tables
// the registry merges are the ones the freeze computed, so the core walks no
// schema graph of its own. The one parent-typed behavior it needs, the
// materializer behind the JSON-pointer fallback, is injected through [Deps].
//
// A [Registry] is the compiled, shareable resolution state built once at compile
// time; a [Session] is the per-run view derived from it, carrying the ref and
// JSON-pointer caches, the per-run fallback registrations, the negative cache,
// and the dynamic scope. A Session is not safe for concurrent use; a compiled
// Registry may be shared by reference across concurrent runs, each deriving its
// own Session, because the Session copies the Registry on write via [Session.EnsureOwned].
package refresolve

import "github.com/google/jsonschema-go/jsonschema"

// Deps injects the parent-typed behavior the core needs but cannot name without
// importing the parent package. The parent builds one Deps and hands it to
// [NewRegistry].
type Deps struct {
	// Materialize converts a decoded JSON schema document (a map[string]any or
	// bool, with json.Number leaves) into a Schema, the parent's
	// ParseSchemaValue. The JSON-pointer fallback builds its targets through it
	// so a const or enum beyond float64 precision stays exact, matching every
	// other path a schema document takes into the engines.
	Materialize func(node any) (*jsonschema.Schema, error)
}
