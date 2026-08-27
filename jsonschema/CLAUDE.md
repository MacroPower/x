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
  cheap per-run state. `Compile` also builds a node-identity index over the root
  document (`index.go`: `schemaIndex`, a pointer interner): every schema
  reachable through sub-schema keywords is assigned a dense id once, and the
  per-node precompute caches (numeric bounds, compiled patterns, sorted key sets,
  const/enum rationals, item plans) are slices indexed by that id. The index
  references the caller's live pointers, so
  `Validator.Schema()` still returns the caller's value; a schema reached only at
  validation time (a remote or JSON-pointer fallback target) is outside the index
  and its accessors recompute on the fly. `extend` folds a remote document fetched
  while compiling into the same index, sharing `intern`'s pointer dedup. The inliner (`inline.go`) uses
  the same node-identity index type: its `record` walk interns every pristine
  schema (the root document, each fetched document, each `WithRefFallback`
  substitute, and each target materialized from an unknown keyword) into its own
  `schemaIndex` via `intern`, and its expansion bookkeeping (the in-flight cycle
  guard, the memoized self-contained copies, and each node's path and
  containing-document URI) lives in slices indexed by the assigned id. The
  inliner clones through `internal/schemaclone` for its pristine and working
  copies. Follow-up work here is not done and remains open: a single-point
  cycle policy and subsuming the per-clone `checkAcyclic` in
  `internal/schemaclone`. Two constraints any such design
  must handle: `refresolve` is a standalone package with no parent import, so
  keying its `baseURIs`/`walked` by node id means either an extra lookup at the
  boundary or breaking that boundary; and the defensive fetched-document
  `cloneSchema` keeps every cache independent of the resolver-owned schema (a
  resolver may hand out one shared object to many callers, and cached
  documents are walked and registered long after the resolver returned), an
  isolation a pointer index alone cannot provide.
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
  dereference with cycle detection), `internal/constraint` (the shared typed
  bound algebra for generation: an `Interval` over exact `big.Rat` endpoints, a
  `ValueSet` for forbidden values with the `not.const -> not.enum -> allOf`
  escalation and the shared numeric-aware `ValuesEqual`, and a per-node `Set`
  whose axes carry the numeric/length/count contributions tiered by `Mode`
  (Baseline/Intersect) and `Provenance` (KindDerived/Authored). It renders onto
  the upstream `Schema` type so the reflection pipeline and the sibling
  tag-interpreter packages both import it without a cycle; the single 2^53
  policy (`ParseNumericBound`, `ErrNotRepresentable`), the size-bound fold
  (`ParseSizeBound`), the const/enum subsumption (`ResolveBounds` under a
  caller-chosen `ResolveMode`), and the redundant-sibling collapse
  (`CanonicalizeNumeric`) all live here. Its scope stops at the bound algebra
  and the forbidden-value escalation: `internal/tagmodel` and `reconcile.go`
  are its two callers, and the allowed set (const/enum) is composed on each
  writer's canvas, not modeled in the package),
  `internal/tagmodel` (the one interpretation of a struct-tag constraint,
  layered above `internal/constraint` and shared by both tag dialects. A `Form`
  is the JSON shape an instance actually takes -- classified by `ShapeOf` from
  the Go type _and_ the type-derived base, which wins when the two disagree --
  and it is the dispatch column, so string coercion is a column of the table
  rather than a gate each operation re-derives. An `Op` is the shared operation
  vocabulary, a `Target` the one write destination a field and an element share
  (which makes element retargeting one implementation), `Shape.ParseScalar` the
  one scalar constructor including the convert-and-marshal round-trip a
  text-marshaling type needs, and a total `[opCount][formCount]` matrix the
  dispatch. Dialect divergence is expressed as a named `Policy` parameter (the
  numeric-bound literal domain, the negative-size question) or a field on the
  dialect's own `KeyRule` row (arity, list spelling, the implied value of a bare
  key), never as duplicated code. It renders onto the upstream `Schema` for the
  same no-cycle reason `constraint` does and must not import the main package),
  `internal/typename` (the seven
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
  traversal, and the container-clone pass all derive), `internal/keywordmeta`
  (schemafield's keyword-side sibling: one declared row per keyword name in
  `internal/keyword`, stating how an authored value merges with the type-derived
  one (`Merge`), which branch of the nullable `anyOf` split it lands on
  (`Scope`), and the drafts and vocabulary gating it. `reconcile.go`'s movable,
  authorable, and bound sets all derive from it, so a replace-semantics keyword
  cannot land on the null wrapper. That is the failure mode behind two past bugs
  (`multipleOf`, then `pattern` and `format`), each a product of a
  hand-maintained movable list. Each row also names the `internal/schemafield`
  rows it claims, and each dispatch row's draft range derives from its member
  keywords' declared ranges, while the row's vocabulary is cross-checked against
  them at load), `internal/schemashape` (structural shape classification of a
  `Schema`), `internal/schemaclone`
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
used for exactly one behavior beyond the type alias: as a recovering fallback,
value equality for hand-built operand shapes the JSON-semantic walk in
`internal/jsonequal` does not model (`const`/`enum`/`uniqueItems` comparisons
otherwise run entirely in that package, and a panic in the upstream fallback
degrades to unequal). Everything else — the reflection pipeline, the
compile-time structure/identifier/reference checks, all
`$ref`/`$dynamicRef`/`$anchor` resolution, the validation walk, path tracking,
format checking — is implemented here, because the upstream's resolved
reference graph is unexported and its validator stops at the first error.

The node-identity index (`index.go`) does not change this relationship: it
is an identity index _over_ the upstream `Schema` graph, mapping each reachable
`*Schema` to a dense node id, not a third use that re-models `Schema`'s fields
(those stay tabulated in `internal/schemafield`). It references the caller's live
pointers rather than cloning, so `Validator.Schema()` still returns the exact
value passed to `Compile`.

An upstream bump has two update sites, both guarded. The first is the canonical
field-metadata table in `internal/schemafield`: `IsTrueSchema`,
`internal/schemashape`'s `IsEmpty` and `HasRefSiblings`, `SubschemaEntries`, and
the container-clone pass all derive from it and do not carry their own field
enumerations. Add the new field to the table (one row) and the derived
predicates and traversals pick it up. The second is the per-keyword semantics
table in `internal/keywordmeta`: every new field's keyword needs a row there,
naming the schemafield rows it claims. The generation half's movable,
authorable, and bound sets and the dispatch table's draft ranges all derive from
it. Reflection-based maintenance guards fail on an unclassified upstream
addition; the main-package guards below run through the public API (that
package has no in-package test files by policy), while `TestFieldTableMatchesUpstream`
and the `TestKeywordMeta*` guards are in-package tests in `internal/schemafield`
and `internal/keywordmeta`, where that policy does not apply:

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
  container fields of a `WithTypeSchema` override (in both its `TypeSchema.Value`
  and `TypeSchema.Verbatim` forms) must not stay aliased in generated schemas.
- `TestKeywordMetaCoversSchemaFields` (internal/keywordmeta): the keyword-side
  staleness alarm, chaining through `TestFieldTableMatchesUpstream` to upstream.
  Every schemafield row is claimed by exactly one keyword row or allowlisted with
  a reason (seven are allowlisted: the five identifiers with no keyword
  constant, plus `PropertyOrder` and `Extra`). A new upstream field essentially
  always arrives with a new keyword, so this is the forcing function for
  classifying it.
- `TestKeywordMetaColumnsConsistent` (internal/keywordmeta): the per-row
  invariants, the load-bearing one being that a `ScopeWrapper` row can never be
  `MergeReplace`. That is the check that would have caught both prior bugs.
- `TestKeywordMetaCoversConstants` and `TestKeywordMetaDerivedSets`
  (internal/keywordmeta): the row-per-constant bijection, and the membership (by
  name, not count) of the `Movable`, `Authored`, and `Bounds` sets the field
  reconciliation derives.
- `TestReconcileSplitConsistency` (reconcile_split_test.go): the behavioral
  guard on the partition the table declares. A `T` field and a `*T` field
  carrying the identical authored value over the identical type-level schema
  must accept and reject the identical non-null instances.
- `TestReconcileSplitCoversAuthored` (reconcile_split_test.go): that guard's
  coverage is an exact partition of `keywordmeta.Authored` (covered cases xor
  skips, each skip carrying a reason), so a new authorable keyword cannot land
  uncovered.
- `TestAssertionKeywordsCoverage` (dispatch_test.go): the cross-check between the
  two tables, asserting the declared-asserted set equals `AssertionKeywords()`.
  The dispatch table's own init additionally panics at load when a row's derived
  draft range is one no member keyword declares outright (which is what rules out
  a hull that widened over a gap none of them covers), or when a row's vocabulary
  disagrees with a member's declared one. A member whose eval step re-checks a
  finer vocabulary gate inline declares `VocabRefined` to opt out of the latter,
  so the exception lives on the keyword rather than in a list beside the check.
- `TestPublicKeywordConstantsMirrorInternal` (dispatch_test.go): the public
  `Keyword*` re-exports must mirror the internal keyword set minus `$comment`,
  so a keyword added internally cannot silently lack a public constant.
- `TestDispatchDraftGating` (dispatch_test.go): at least one probe per dispatch
  row, pinning through the public API whether that row's keyword fires under
  each draft. It is the behavioral baseline the derived draft ranges must
  reproduce, and `TestDispatchDraftGatingCoversRows` forces a new row to get one.
- `TestDraftConstantsInSync` (dispatch_test.go): `Draft` is declared twice (the
  public enum and `internal/keywordmeta`'s copy, which the parent cannot import
  in reverse), so their numeric values are pinned equal.
- `internal/tagmodel`'s constraint matrix is guarded in three escalating steps.
  The table is a fixed-size `[opCount][formCount]` array, so a new `Op` or
  `Form` grows every row and column rather than falling through a lookup. The
  package's `init` then walks every cell and panics naming any pair left
  unfilled, any `StatusApply` cell with no applier, and any `StatusIgnore` or
  `StatusReject` cell with no written reason; it runs on first import, so every
  test binary in the module trips it. `TestMatrixGolden` pins a textual dump of
  the whole table, making a deliberate cell change a reviewable diff in the test
  file and an accidental one a failure, and `TestFormClassificationTotal` pins
  that every `reflect.Kind` classifies to a form.

### Type resolution priority (generation)

For each Go type, the first matching step wins: `WithTypeSchema` override ->
`JSONSchemaProvider` -> built-in overrides (exact `reflect.Type` match:
`time.Time`, `json.RawMessage`, `big.Int`, ...) -> marshaler methods promoted
from an embedded field -> direct `encoding.TextMarshaler` -> kind-based
reflection. A direct `json.Marshaler` is deliberately not consulted (its
output shape is unknowable). `JSONSchemaExtender` runs only when reflection
produced the schema. The full behavioral spec lives in `doc.go`.

Type-level hooks (`JSONSchemaProvider`/`TypeSchemaProvider` and their extender
counterparts, plus `WithTypeSchema`) declare intent through a `TypeSchema`
envelope rather than a pre-shaped `*Schema`: a bare `Value` decorated by a
`Nullability` stance and the occurrence's pointer-ness, an opaque `Verbatim`
schema emitted as-is, or a `Ref` alias to another Go type kept reachable through
a node-backed `$ref` edge. Setting more than one is `ErrConflictingTypeSchema`.
The stance replaces hand-shaping an `anyOf[value, null]` wrapper the generator
would otherwise reverse-engineer, and is recorded on the def entry
(`defEntry.nullability`) so it combines with each reference's pointer-ness in
`node.nullableDecision`, keeping `$defs` nullability order-independent.

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
- Two differential fuzz rigs close the loop between the package's halves on one
  property: **the schema generated for a Go type must accept whatever
  `encoding/json` marshals from a value of that type.** A rejection means
  `reflect.go`'s hand-reimplementation of `encoding/json`'s field resolution has
  drifted, which is where past fixes cluster. Both draw their values from
  `internal/fuzzfill`, which turns a fuzzing entropy blob into a populated value
  through a deterministic, zero-extending `Cursor`.
  - `fuzz_reflect_test.go` (rig 1) asserts it over a hand-written roster, one
    `FuzzReflectAccepts<T>` per type. The roster keeps the classes runtime type
    construction cannot express, and so stays permanently: a promoted marshaler,
    an embedded generic instantiation, an embedded interface.
  - `fuzz_shape_test.go` (rig 2) fuzzes the type shape as well as the value.
    `internal/fuzzshape` synthesizes a `reflect.Type` from a blob via
    `reflect.StructOf`, which the non-generic `Generate` entry point takes
    as-is, so the rig needs no new public API. Its callback takes two blobs so
    shape and value entropy evolve independently. `FuzzShapeAccepts` and its
    option variants assert the accept property; `FuzzShapeRejectsNearMiss`
    asserts the complement, that an instance one property away from the
    marshaled shape is refused, so an accept-everything schema cannot pass
    vacuously.
  - Three causes would make a synthesized shape report a divergence the package
    is not guilty of: a direct `json.Marshaler` (whose output shape reflection
    cannot know), `WithNullable(false)` (which drops the null branch by
    design), and `reflect.StructOf`'s inability to reproduce method promotion.
    Each has a reason constant carried as the message of a guard that pins the
    cause down, the same convention `suite_test.go` uses for skips.
    `reasonUnmodeledMarshaler` lives in `internal/fuzzshape`'s test, beside the
    guard over drawn shapes that enforces it; `reasonNullableOff` and
    `reasonStructOfPromotion` live in `fuzz_shape_test.go` with the rig. The
    `StructOf` limitation is a property of the harness, not of the package, and
    deliberately does not appear in `doc.go` or `README.md`.
  - `task go:fuzz` searches for new counterexamples; the seed corpora run on
    every `go test`. A discovered counterexample is committed under
    `testdata/fuzz/<Target>/` as a permanent regression seed before the fix.
