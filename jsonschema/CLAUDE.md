# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This file covers the `jsonschema` module only. It is part of the repo's go.work workspace.

## Architecture

The package has two independent halves sharing the `Schema` type:

- **Generation** (`generate.go`, `reflect.go`, `tags.go`, `names.go`,
  `comments.go`): Go types -> JSON Schema via reflection. `generator` in
  `reflect.go` is the core; `generate.go` holds the functional options and
  the `GenerateFor`/`Generate` entry points.
- **Validation** (`validate.go`, `errors.go`): JSON instances -> structured
  `*ValidationError` trees. `Compile` builds a `validator` once (registry
  construction from `$id`/`$anchor`, precomputed numeric bounds and compiled
  regexes, draft and vocabulary detection); `validator.forInstance` derives
  cheap per-run state. `Compile` also freezes a node-identity index over the root
  document (`compiled.go`: `compiledDoc`, built by `freeze`): every schema
  reachable through sub-schema keywords is assigned a dense id once, and the
  per-node precompute caches (numeric bounds, compiled patterns, sorted key sets,
  const/enum rationals, item plans) are slices indexed by that id. The index
  references the caller's live pointers, so
  `Validator.Schema()` still returns the caller's value; a schema reached only at
  validation time (a remote or JSON-pointer fallback target) is outside the index
  and its accessors recompute on the fly. `extend` folds a remote document fetched
  while compiling into the same index, sharing `freeze`'s pointer dedup. This is
  the validation-side analog of the generation IR (`ir.go`): identity assigned
  once, no in-place mutation after construction. The inliner (`inline.go`) uses
  the same node-identity index type: its `record` walk interns every pristine
  schema (the root document, each fetched document, each `WithRefFallback`
  substitute, and each target materialized from an unknown keyword) into its own
  `compiledDoc` via `intern`, and its expansion bookkeeping (the in-flight cycle
  guard, the memoized self-contained copies, and each node's path and
  containing-document URI) lives in slices indexed by the assigned id. The
  inliner clones through `internal/schemaclone` for its pristine and working
  copies. Three once-sketched follow-ups were assessed and intentionally not
  pursued, because each is worse-or-neutral against the "the index references
  live pointers, never clones" decision: keying `refresolve`'s `baseURIs`/`walked`
  by node id (both already key on stable, never-mutated pointers, and `refresolve`
  is a standalone package with no parent import, so id-keying only adds a lookup
  or breaks the boundary); replacing the defensive fetched-document `cloneSchema`
  with an index entry (the clone provides isolation from the resolver-owned and
  upstream-`Resolve`-mutated schema, which an index cannot); and shrinking
  `internal/schemaclone` or retiring `schemaFormsTree` (the clone's cycle check is
  intrinsic to the JSON round-trip, and `schemaFormsTree` _rejects_ non-tree
  graphs, the opposite of the identity index, which _tolerates_ them).
  `$ref`/`$dynamicRef`/`$anchor` resolution lives in the
  shared `internal/refresolve` core, which both the validator and the inliner
  (`inline.go`) consume so the two engines cannot disagree; the inliner resolves
  through a `refresolve.Session`. Self-contained
  helpers live under `internal/`:
  `internal/format` (built-in string-format validators), `internal/vocab`
  (vocabulary modelling and resolution), `internal/jsonptr` (RFC 6901
  escaping, plus `SafeToken` for $ref/$defs token sanitization),
  `internal/numrat` (exact-decimal arithmetic core for JSON
  numbers: canonical decomposition, bounded `big.Rat` expansion, modular
  `multipleOf`), `internal/numkind` (Go reflect kind classification
  shared by both halves: integer parse-bit-width mapping, the
  integer/unsigned/float kind predicates, plus `DerefType` for pointer-chain
  dereference with cycle detection), `internal/typename` (the seven
  canonical JSON Schema type-name constants and their predicate, shared by
  both halves and schemashape), `internal/uriref` (RFC 3986 URI-reference
  resolution and fragment handling for the `$ref` absolutization layer,
  including the opaque/URN merge that corrects `net/url.ResolveReference`),
  `internal/normalize` (Go value -> JSON-shaped value normalization: integer
  widths to `json.Number`, float32 widening, recursive container coercion with
  copy-on-change and a cycle guard), `internal/schemafield` (the canonical
  field-metadata table for the upstream `Schema` type: one row per exported
  field carrying its class, sub-schema shape, zero predicate, and container-clone
  closure, from which the true/empty/ref-sibling predicates, the sub-schema
  traversal, and the container-clone pass all derive), `internal/schemashape`
  (structural shape classification of a `Schema`), `internal/schemaclone`
  (deep copy of
  a `Schema` via JSON round-trip with render-only `PropertyOrder` restored
  through a caller-supplied sub-schema traversal), `internal/jsonequal` (DoS-guarded,
  JSON-semantic value equality for `const`/`enum` and the matching content
  hash for `uniqueItems`, layered on `internal/numrat` for exact decimal
  comparison), `internal/goast` (doc-comment and type/field-shape
  extraction from a parsed Go package, for the generation half's comment
  provider), `internal/regexcache` (process-wide compile-once cache for
  validation-time regular-expression patterns, memoizing the compiled
  expression or the compile error so a pattern compiles at most once and fails
  closed identically across runs), and `internal/annotations` (the 2020-12
  annotation collection -- evaluated-property set, matched-item index set,
  items watermark, saturation flags -- with the nil-safe `Set` type whose
  `Merge` is the union the `unevaluatedProperties`/`unevaluatedItems` walk
  consults; the merge _policy_ of when a subschema rolls up stays in the
  validator), and `internal/content` (the `contentEncoding`/`contentMediaType`
  decode-and-classify core: base64 decoding, the `application/json` media-type
  fold, and the `base64` value the generator emits and the validator asserts;
  the validator keeps the vocabulary gating and error construction), and
  `internal/refresolve` (the shared `$ref`/`$dynamicRef`/`$anchor` resolution
  core: registry construction, base-URI computation, anchor precedence, the
  unified remote fetch/caching seam, and structured ref-failure attribution via
  its `Result`. A compiled `Registry` is shared by reference; each run derives a
  `Session` that copies the registry on write. The `ErrRefResolve` and
  `ErrNotResolved` sentinels live here and are re-exported from `errors.go` so
  `errors.Is` identity holds across the boundary. Sub-schema traversal, which it
  cannot name without importing the parent, is injected as a `Deps` closure
  (deep cloning stays parent-side in the fetch closures), and the two
  draft-dependent branches use its own two-value `Draft` enum).

### Relationship to google/jsonschema-go

`Schema` is a type alias to the upstream type (`schema.go`). The upstream is
used for exactly two things: structural well-formedness via `Schema.Resolve`
(called once per `Compile`, result discarded) and JSON-semantic value equality
(`const`/`enum`/`uniqueItems`). Everything else — the reflection pipeline, all
`$ref`/`$dynamicRef`/`$anchor` resolution, the validation walk, path tracking,
format checking — is implemented here, because the upstream's resolved
reference graph is unexported and its validator stops at the first error.

The frozen compiled form (`compiled.go`) does not change this relationship: it
is an identity index _over_ the upstream `Schema` graph, mapping each reachable
`*Schema` to a dense node id, not a third use that re-models `Schema`'s fields
(those stay tabulated in `internal/schemafield`). It references the caller's live
pointers rather than cloning, so `Validator.Schema()` still returns the exact
value passed to `Compile`.

The single update site on an upstream bump is the canonical field-metadata
table in `internal/schemafield`: `IsTrueSchema`, `internal/schemashape`'s
`IsEmpty` and `HasRefSiblings`, `SubschemaEntries`, and the container-clone pass
all derive from it and do not carry their own field enumerations. Add the new
field to the table (one row) and the derived predicates and traversals pick it
up. Reflection-based maintenance guards fail on an unclassified upstream
addition; the main-package guards below run through the public API (that
package has no in-package test files by policy), while `TestFieldTableMatchesUpstream`
is an in-package test in `internal/schemafield`, where that policy does not apply:

- `TestFieldTableMatchesUpstream` (internal/schemafield): the primary staleness
  alarm. It reflects over the upstream `Schema` and asserts every exported field
  appears in the table exactly once with a `Shape` matching its Go type, a valid
  `Class`, the right sub-schema accessor, and the right container-clone presence.
  A new upstream field fails this until it is added to the table.
- `TestIsTrueSchemaRejectsEverySetField` (`schema_test.go`): every exported
  field set alone must defeat `IsTrueSchema`. It is the only guard that each
  field's zero predicate reads the correct field (a presence-only table check
  cannot catch a wrong-field accessor); when it fires, fix the field's `IsZero`
  accessor in the `internal/schemafield` table.
- `TestSchemaSerializableFieldCoverage` (schema_test.go): every field must
  carry a json tag or be allowlisted, guarding the JSON round-trip that
  `Inline`'s deep copy and `ParseSchemaValue` rely on.
- `TestTypeSchemaOverrideContainersUnaliased` (generate_test.go):
  container fields of a `WithTypeSchema` override must not stay aliased in
  generated schemas.

### Type resolution priority (generation)

For each Go type, the first matching step wins: `WithTypeSchema` override ->
`JSONSchemaProvider` -> built-in overrides (exact `reflect.Type` match:
`time.Time`, `json.RawMessage`, `big.Int`, ...) -> marshaler methods promoted
from an embedded field -> direct `encoding.TextMarshaler` -> kind-based
reflection. A direct `json.Marshaler` is deliberately not consulted (its
output shape is unknowable). `JSONSchemaExtender` runs only when reflection
produced the schema. The full behavioral spec lives in `doc.go`.

### Behavior is spec'd in doc.go and README.md

`doc.go` and `README.md` document edge-case behavior exhaustively (embedded
field composition, draft differences, numeric precision caps, vocabulary
gating, error tree shape). Any behavior change must update both; they are the
contract the tests enforce.

### Tests

- `suite_test.go` runs the official JSON Schema Test Suite vendored under
  `testdata/suite/` (draft7, draft2020-12, remotes). Known deviations (e.g.
  ECMA-262 regex semantics — this package uses Go RE2) are skipped via
  `buildSuiteSkips`, each with a documented reason. New skips need a reason
  constant.
- `conformance_test.go` validates generated schemas against the official
  metaschemas vendored in `testdata/metaschemas/`, using this package's own
  validator and a `RefResolver` for the metaschema's vocabulary sub-schemas.
