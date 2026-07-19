// Package refresolve is the shared $ref/$dynamicRef/$anchor resolution core for
// the jsonschema validator and inliner. It owns registry construction, base-URI
// computation, anchor precedence, unified remote fetch orchestration, and
// structured ref-failure attribution, so the two engines resolve references
// identically by construction rather than by keeping two hand-rolled copies in
// sync.
//
// The package depends on the upstream [jsonschema.Schema] type and the internal
// uriref/jsonptr helpers, but not on the parent jsonschema package: parent-typed
// behavior it needs (sub-schema traversal, deep clone) is injected as closures
// through [Deps], mirroring the pattern internal/schemaclone uses.
//
// A [Registry] is the compiled, shareable resolution state built once at compile
// time; a [Session] is the per-run view derived from it, carrying the ref and
// JSON-pointer caches, the per-run fallback registrations, the negative cache,
// and the dynamic scope. A Session is not safe for concurrent use; a compiled
// Registry may be shared by reference across concurrent runs, each deriving its
// own Session, because the Session copies the Registry on write via [Session.EnsureOwned].
package refresolve

import "github.com/google/jsonschema-go/jsonschema"

// Draft selects the two draft-dependent branches of the resolution core: the
// Draft-7 sibling-$id exception during the registry walk and dynamic-scope
// seeding. It is a distinct enum from the parent package's Draft; the parent
// converts at the single construction site.
type Draft int

const (
	// Draft2020 targets JSON Schema Draft 2020-12.
	Draft2020 Draft = iota

	// Draft7 targets JSON Schema Draft-07, where a sibling $id does not affect
	// $ref resolution.
	Draft7
)

// Deps injects the parent-typed behavior the core needs but cannot name without
// importing the parent package. The parent builds one Deps and hands it to
// [NewRegistry].
type Deps struct {
	// Children returns the direct sub-schemas of a schema in a stable order,
	// the parent's schemaChildren. The walk consumes only the child schema, so
	// the []*jsonschema.Schema shape (dropping each child's Location) suffices.
	Children func(*jsonschema.Schema) []*jsonschema.Schema

	// Clone deep-copies a schema, the parent's cloneSchema. Injecting it keeps
	// the render-only PropertyOrder restore traversal on the parent side.
	Clone func(*jsonschema.Schema) (*jsonschema.Schema, error)
}
