# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This file covers the `jsonschema` module only. It is part of the repo's go.work workspace.

## Architecture

The package has two independent halves sharing the `Schema` type:

- **Generation** (`generate.go`, `reflect.go`, `tags.go`, `names.go`,
  `comments.go`): Go types -> JSON Schema via reflection. `generator` in
  `reflect.go` is the core; `generate.go` holds the functional options and
  the `GenerateFor`/`Generate` entry points. Struct-field resolution lives in
  `internal/fieldset`; `generator.fields` is the collector the generation half
  drives.
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
  copies. Structural vetting is compiler-enforced through the
  `internal/schemavet` currency: only boundary code holds a bare `*Schema`
  (the public API, the fetch closures, the compile reference walk -- which
  registers a fetched document and vets it before Compile returns -- and the
  inliner's resolution space), and everything past the vetter demands the
  minted `schemavet.Doc`/`schemavet.Node` proof. `schemaIndex.extend` takes a
  `Doc`, so an unvetted document cannot reach the precompute caches, and
  `refresolve.Registry.NewSession` requires a `FallbackVet`, so every session
  states its vetting policy at construction. The one deliberate exception is
  the inliner, whose own root and `WithRefFallback` substitutes stay unvetted
  (each site carries an "Unvetted by design" marker and
  `inline_root_unvetted_test.go` pins the behavior). The pointer-graph policy
  is separate from that vetting and has rules at two boundaries.
  `checkSchemaTree` (`validate.go`) rejects a root whose sub-schema pointers
  alias or cycle, and both `Compile` and `Inline` run it over the document they
  are given, so the two engines make the same demand of a root's sub-schema
  graph. `cloneCheckedSchema` holds the rest to the weaker no-cycle rule: the
  inliner's pristine root, where it reports a loop closing through a value
  field that the tree check does not read, plus a document a `RefResolver`
  returns and a `SubstituteRef` schema. Aliasing survives there, because every
  walk that reaches a registered document dedupes pointers, so within this
  package it costs only the accuracy of a location in an error message. It does
  reach `Inline`'s output, which `checkSchemaTree` then rejects if the caller
  compiles it, so an aliased resolver document buys a non-tree result. A cycle is fatal,
  because `refresolve`'s JSON-pointer fallback marshals the document it
  searches and upstream's `MarshalJSON` re-enters `json.Marshal` at every
  nesting level, so no encoder sees the repeat and the marshal recurses into a
  stack overflow no `recover` catches. The deep copy reproduces an aliased
  graph rather than flattening it into a tree, so the inliner's own `walkPair`
  and `stripIdentifiers` walks carry visited sets. Two constraints the design
  works within: `refresolve` is a standalone package with no parent import
  (leaf-to-leaf imports like its `schemavet` dependency are fine; the parent is
  not), so keying its `baseURIs`/`walked` by node id means either an extra
  lookup at the boundary or breaking that boundary; and the defensive
  fetched-document `cloneCheckedSchema` keeps every cache independent of the
  resolver-owned schema (a resolver may hand out one shared object to many
  callers, and cached documents are walked and registered long after the
  resolver returned), an isolation a pointer index alone cannot provide.
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
  `internal/fieldset` (the `encoding/json` field-resolution parity core for the
  generation half, in three phases. Two mirror the standard library: `Collect`
  walks embeds breadth-first and records every sighting of a JSON name, and
  `Resolve` applies the shallowest-depth rule and the same-depth tag tie-break.
  The third is this package's own. `Classify` turns each winner into a property,
  an allOf-composed embed, or a ghost, then marks the composed embeds whose
  promoted names the resolution took away. `Resolve` and `Classify` are pure,
  which is what lets the phase-level rig test one phase directly. Composition
  detection stays parent-side as an injected `ComposedFunc`, since
  `needsAllOfComposition` reads `resolveTypeSchema` and the public
  `JSONSchemaProvider`; the package therefore reflects over a type without
  importing the parent. A composed embed's subtree still joins the walk as a
  ghost, because `encoding/json` promotes its fields like any other, and a
  `Collector`-owned in-flight set bounds the recursion that resolves the embed's
  own fields for the shadow marking. That set's skip leaves the embed's branch
  unconditional. `Collection.Order` is load-bearing, since `Classify` emits
  `GhostWon` in that order and the generator appends those names to the object's
  property order),
  `internal/typename` (the seven
  canonical JSON Schema type-name constants and their predicate, shared by
  both halves and schemashape), `internal/uriref` (RFC 3986 URI-reference
  resolution and fragment handling for the `$ref` absolutization layer,
  including the opaque/URN merge that corrects `net/url.ResolveReference`),
  `internal/normalize` (Go value -> JSON-shaped value normalization: integer
  widths to `json.Number`, float32 widening, recursive container coercion with
  copy-on-change and a cycle guard), `internal/schemafield` (the canonical
  field-metadata table for the upstream `Schema` type: one row per exported
  field carrying its class, sub-schema shape, zero predicate, and the clone
  closures its Go type calls for, from which the true/empty/ref-sibling
  predicates, the sub-schema traversal, the container-clone pass, and
  `internal/schemaclone`'s structural deep copy all derive. `CloneSubschemas`
  rebuilds a sub-schema container, `CloneDeep` copies a container whose
  interior is mutable and supersedes the `CloneContainer` the same field also
  carries, and `CloneContainer` alone reallocates a header whose interior is
  not. Each column's presence follows from the field's Go type, so the table's
  staleness guard checks all three), `internal/keywordmeta` (schemafield's
  keyword-side sibling: one declared row per keyword name in
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
  `Schema`), `internal/schemaclone` (structural deep copy of a `Schema`, one
  field at a time through the `internal/schemafield` table. The copy is
  faithful rather than normalized. It reproduces the source's pointer graph and
  keeps each value's Go type, so an aliased node stays one node, a cycle copies
  as a cycle, a `json.Number` holds its literal, `PropertyOrder` rides along
  like any other field, and a schema stored in `Extra` stays a schema. No graph
  shape defeats it, so `Clone` has no error return; `CloneChecked` returns the
  same copy plus a report of whether the source held a pointer cycle, which the
  three resolution boundaries read. The package doc names the two values a copy
  still shares with its source),
  `internal/jsonequal` (DoS-guarded, JSON-semantic value equality for
  `const`/`enum` and the matching content hash for `uniqueItems`, layered on
  `internal/numrat` for exact decimal comparison), `internal/goast`
  (doc-comment and type/field-shape extraction from a parsed Go package, for
  the generation half's comment provider), `internal/regexcache` (process-wide
  compile-once cache for validation-time regular-expression patterns,
  memoizing the compiled expression or the compile error so a pattern compiles
  at most once and fails closed identically across runs), and
  `internal/annotations` (the 2020-12 annotation collection --
  evaluated-property set, matched-item index set, items watermark, saturation
  flags -- with the nil-safe `Set` type whose
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
  `Session` that copies the registry on write, and `Registry.NewSession`
  requires a `FallbackVet` -- the vet, minting `schemavet.Node`, applied to
  each JSON-pointer fallback target the session materializes; nil is reserved
  for the compile-time session, whose targets the compiler vets in one
  shared pass. The `ErrRefResolve` and `ErrNotResolved` sentinels live here
  and are re-exported from `errors.go` so `errors.Is` identity holds across
  the boundary. Sub-schema traversal, which it cannot name without importing
  the parent, is injected as a `Deps` closure (deep cloning stays
  parent-side in the fetch closures), and the two draft-dependent branches
  use its own two-value `Draft` enum; the sibling leaf `internal/schemavet`
  is its one internal import), and `internal/schemavet` (the single
  structural-vetting policy and the mint for the vetted-currency types: a
  `Vetter` runs the structure, type-name, bound, items-array, and identifier
  checks, and only `Vetter.VetDoc`/`Vetter.Vet` can produce a `Doc`/`Node`,
  whose unexported fields make the compiler enforce that vetting ran. The
  nine vetting sentinels live here, re-exported from `errors.go` on the
  refresolve convention. It carries its own three-flag `Profile` (converted
  by `draftProfile.vetProfile`, mirroring `toRefDraft`) and its own
  `Entries` traversal, whose pointer assembly a lockstep guard test in
  `walk_test.go` pins to `SubschemaEntries`, since vetting errors embed
  those pointers).

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
`internal/schemashape`'s `IsEmpty` and `HasRefSiblings`, `SubschemaEntries`, the
container-clone pass, and the structural deep copy all derive from it and do not
carry their own field enumerations. Add the new field to the table (one row)
and the derived predicates and traversals pick it up. The second is the
per-keyword semantics table in `internal/keywordmeta`: every new field's
keyword needs a row there, naming the schemafield rows it claims. The
generation half's movable, authorable, and bound sets and the dispatch table's
draft ranges all derive from it. Reflection-based maintenance guards fail on an
unclassified upstream addition; the main-package guards below run through the
public API (that package has no in-package test files by policy), while
`TestFieldTableMatchesUpstream` and the `TestKeywordMeta*` guards are
in-package tests in `internal/schemafield` and `internal/keywordmeta`, where
that policy does not apply:

- `TestFieldTableMatchesUpstream` (internal/schemafield): the primary staleness
  alarm. It reflects over the upstream `Schema` and asserts every exported field
  appears in the table exactly once with a `Shape` matching its Go type, a valid
  `Class`, the right sub-schema accessor, and the right clone-closure presence
  for each of the three columns. A new upstream field fails this until it is
  added to the table.
- `TestCloneSubschemasWritesItsOwnField` (internal/schemafield): each
  sub-schema field's setter writes the field its getter reads, which a presence
  check cannot catch (a row copied from another would read one field and write
  the other, and the clone would drop children).
- `TestIsTrueSchemaRejectsEverySetField` (`schema_test.go`): every exported
  field set alone must defeat `IsTrueSchema`. It is the only guard that each
  field's zero predicate reads the correct field (a presence-only table check
  cannot catch a wrong-field accessor); when it fires, fix the field's `IsZero`
  accessor in the `internal/schemafield` table.
- `TestSchemaSerializableFieldCoverage` (schema_test.go): every field must
  carry a json tag or be allowlisted, guarding the JSON round-trip
  `ParseSchemaValue` relies on.
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
- The package checks `encoding/json` parity at two altitudes. Two schema-level
  differential rigs close the loop between the package's halves on one property:
  **the schema generated for a Go type must accept whatever `encoding/json`
  marshals from a value of that type.** A rejection means `internal/fieldset`'s
  reimplementation of `encoding/json`'s field resolution has drifted, which is
  where past fixes cluster. Both draw their values from `internal/fuzzfill`,
  which turns a fuzzing entropy blob into a populated value through a
  deterministic, zero-extending `Cursor`. The phase-level rig in
  `internal/fieldset` checks the same drift one layer down, against
  `encoding/json` itself rather than through the validator.
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
  - The phase-level rig (`internal/fieldset/fieldset_test.go`, in-package on the
    `internal/schemafield` precedent) asserts two properties over a hand-written
    embed and tag roster and over every `internal/fuzzshape` shape, each crossed
    with a set of composed-embed predicates. **Key-set parity** asserts that the
    names the phases resolve are exactly the keys `encoding/json` marshals, and
    that the value under each key is what the winning field marshals to, so a
    wrong dominance verdict fails the rig even when the name set matches. It
    carries three reason constants, each pinned by a guard test:
    `reasonPromotedMarshaler` (the marshaler replaces the object, so the
    resolved fields describe nothing in the output), `reasonMarshalFailed` (the
    value has no marshaled form to compare against), and `reasonNotObject` (the
    output has no top-level keys). **Composition invariance** asserts that
    composing an embed rather than promoting it moves a name between the emitted
    properties and the ghost-won list without changing the set. Composition
    invariance needs no reason constants, so it covers the types key-set parity
    skips. `FuzzFieldSetKeys` searches the parity property, drawing its composed
    predicate from the shape blob so a counterexample minimizes down to the
    composition that produced it. The injected `ComposedFunc` is what lets these
    reach the ghost machinery at all, since composition is a provider decision
    and `internal/fuzzshape`'s pools are provider-free.
  - Three tests in the same file pin what the oracle cannot see, since it
    compares name sets and values. `TestClassificationPins` pins `Result.Fields`
    with their index paths and composed-embed flags, and `Result.GhostWon` in
    order; both feed the generated schema, the field order as the object's
    property order and the marks as whether a composed branch is conditional.
    `TestPhasesComposeIntoOf` asserts the three phases run separately reproduce
    `Of`. `TestCycleSkipKeepsBranchUnconditional` pins the mutually composed
    cycle, whose skip leaves `promoted` short of the root's names and so leaves
    that branch unconditional.
  - `task go:fuzz` searches for new counterexamples; the seed corpora run on
    every `go test`. A discovered counterexample is committed under
    `testdata/fuzz/<Target>/` as a permanent regression seed before the fix.
- A third differential rig closes the loop across the three sites that
  materialize a `$ref` target -- `Compile`'s reference fixpoint, `Inline`'s own
  registry and index, and the JSON-pointer fallback both reach through
  `internal/refresolve` -- on one property: **Compile, Inline, and the
  substitute path must reach the same verdict on every instance of one
  reference graph.** A disagreement is a bug at one of the sites. The shared
  harness lives in `ref_differential_test.go` (`refPipeline`, `refEngines`,
  `inlinePipeline`, `assertRefEnginesAgree`, and the `refBuildSentinels` table
  that makes two build failures comparable by cause rather than by message
  text).
  - `TestSuiteInlineAgrees` (`suite_inline_differential_test.go`) runs every
    vendored suite group through both engines. It ignores `suiteSkips`, which
    record where this package diverges from the suite's expected answer, a
    different question from whether the two engines agree.
  - `TestRefEnginesAgreeOnPastFixes` pins one graph per past `$ref` fix as
    JSON.
  - `FuzzRefEnginesAgree` (`fuzz_ref_test.go`, rig 3) synthesizes a
    multi-document graph from a blob, drawing the draft, each document's `$id`,
    the anchor form, and eight reference spellings. A third pipeline withholds one document from
    the resolver and serves it through a `WithRefFallback` substitute. It has no
    seed corpus, since a corpus entry for a `[]byte` argument is entropy and no
    blob can be written by hand to decode to a chosen graph.
  - Five reason constants live in `ref_differential_test.go`. Three are skip
    reasons the rig classifies from the error `Inline` returns, never from a
    test name, so a reason cannot go stale against a renamed suite case:
    `reasonInlineCycle`, `reasonInlineDynamicRef`, and `reasonDeferredRefMiss`
    (`Compile` tolerates a missing remote document and defers, `Inline` fails at
    inline time, so the verdicts are not comparable). `reasonSubstituteBaseURI`
    and `reasonSubstituteNoAnchors` are not skips; the generator applies them
    when choosing which document to withhold.
  - Two divergences the rig found are pinned as tests rather than fixed, since
    resolving either is a policy decision: `TestCompileVetsTransitivelyInlineDoesNot`
    (Compile vets a document reached only through another document's reference,
    which Inline never fetches) and `TestRefEnginesDisagreeOnCollidingIDs` (with
    three documents colliding on `$id`, one anchor reference resolves to two
    different targets). The generator stays off both shapes, which is why the
    `$id` pool excludes the root's own URI and a malformed leaf lands only in
    the root or a directly referenced document.
  - `suiteFiles` in `suite_test.go` is the single enumeration of the vendored
    suite. The three conformance tests and the differential all draw their files
    from it, so the differential cannot drift from what the conformance tests
    run.
- `internal/format` pins the built-in string-format checkers in three layers:
  robustness fuzz (`fuzz_format_test.go`), stdlib and cross-format differentials
  (`differential_test.go`, `containment_test.go`), and vendored corpora plus
  curated vectors (`corpus_*_test.go`, `vectors_test.go`). Acceptance vectors are
  data, not Go literals: one `testdata/vectors/<format-name>.tsv` per format,
  basename matching the name registered in `format.Validators()` exactly, three
  mandatory tab-separated fields per row (a Go quoted input literal, `true` or
  `false`, and a note), and `TestFormatVectors` walks the directory, so a new
  file adds a test with no Go edit. `loadVectorFile` carries the authoritative
  statement of the row format.
- `TestFormatCoverage` (`internal/format/coverage_test.go`) requires every name
  in `format.Validators()` to carry a differential fuzz target, a vendored
  corpus, or a vector file, so a newly registered format cannot arrive with none.
  A claim names the Go function value rather than its name as a string, so
  renaming a target breaks the build there. Containment targets are deliberately
  not a coverage source, since a one-way subset relation cannot catch
  over-acceptance. The allowlist mechanism carries a reason string and is empty.
- `format_deviations_test.go` binds each bullet of the format-deviations list in
  `doc.go` and `README.md` to the behavior it claims: a phrase both files must
  carry, matched over normalized text so a reflow cannot break it, and the
  bullet's own quoted examples run through the public API. The row count must
  equal the bullet count in both lists, so a sixth deviation cannot land with
  nothing asserting it.
