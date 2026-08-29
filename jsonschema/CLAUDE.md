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
  containing-document URI) lives in slices indexed by the assigned id.
  `walkPair` owns the in-flight guard and marks every node the walk is inside,
  the root document included. A cycle therefore truncates at the same depth
  whether the walk reached that node by descending the document or by expanding
  a ref to it. The guard skips a node another visit already marked, since an
  aliased document can carry one node both on the walk's path and inside a ref
  target's subtree, and clearing the mark on the way out of the inner visit
  would leave the rest of the outer one unguarded. A truncated copy is never
  memoized, because two positions can reach the same target with different nodes
  in flight. The inliner clones through `internal/schemaclone` for its pristine
  and working copies. Structural vetting is compiler-enforced through the
  `internal/schemavet` currency: only boundary code holds a bare `*Schema`
  (the public API, the fetch closures, and the shared reference-closure walk,
  whose fetches register a document and whose hooks vet it before either engine
  returns), and everything past the vetter demands the minted
  `schemavet.Doc`/`schemavet.Node`
  proof. `schemaIndex.extend` takes a `Doc`, so an unvetted document cannot
  reach the precompute caches, and `refresolve.Registry.NewSession` requires a
  `FallbackVet`, so every session states its vetting policy at construction.
  Both engines vet alike, over the same set of documents. `refClosure`
  (`refclosure.go`) is the reference-closure walk both drive. It ref-walks a
  document, whose resolutions fetch and register further documents, and
  repeats until the frontier is empty; the caller injects the per-document
  work as one hook (`Compile` vets each document and folds it into its node
  index; `Inline` vets through `closureDoc`, which no-ops on a document
  `fetchDoc` already vetted). A fallback target needs no hook, since every
  session vets each one where it materializes it. `Compile` runs it as
  `compileRefPasses`, and `Inline` runs it in `walkClosure` before expanding
  anything, so both vet a document reachable only through another document's
  reference. `Inline` also runs `VetDoc` over its pristine root and over
  each `WithRefFallback` substitute, which enters resolution space as a
  document of its own; the inliner wraps a substitute's violation to name
  the ref whose failure the fallback answered along with the document and
  path where the inliner consulted the fallback. One compile-time refusal
  has no inline counterpart, `ErrUnknownVocabulary`, so the agreement covers
  the structural vet rather than every error. `doc.go` states two narrowings
  of the inliner's walk. First, the walk resolves a `$dynamicRef` to reach
  the document it names, but a `$dynamicRef` that resolves to nothing never
  refuses the walk, since `walkPair` answers `ErrRefInline` wherever it
  meets one. Second, `WithRefFallback` suspends the walk's refusals apart from
  an identifier collision, because a fallback answers one failing reference at
  a time and a document outside the expansion has no reference in the
  expansion for a policy to answer; the closure is the same either way. One
  attribution gap remains, and `doc.go` states it. The validator's fetch
  walks a fetched document's nested absolute `$id` resources into the shared
  registry, giving each its own frontier entry and locator, while `fetchDoc`
  and `prefetchDoc` route them through `RegisterFallback`, which never touches
  `Registry().URI`. Both engines vet the whole document whichever route a
  nested resource takes, so the refusals agree; only the locator differs,
  which is why the agreement tests assert on the document URI rather than an
  exact pointer. A strict walk fetches through `prefetchDoc`, which registers
  without vetting. The walk's per-document hook then vets each document as the
  sorted frontier reaches it, the order `Compile` vets in. Vetting inside the
  fetch would report whichever document a reference named first. Each engine
  holds one `schemavet.Vetter` over the root and the fallback targets, plus
  the substitutes in the inliner, and `Compile` extends it to the documents
  its walk fetches. `Compile`'s session takes that vetter's `Vet` method as
  its `FallbackVet` directly, and the inliner's session takes
  `inliner.fallbackVet`, which runs the same vetter. Both engines therefore
  vet a pointer target where the walk materializes it and report whichever
  fault the walk meets first. In the inliner that also lets `vetTarget`
  re-mint a materialized pointer target rather than re-checking it where the
  inliner records it. `Compile` builds its vetter in `buildRefReg`, since the
  session needs the method, and drops it beside the compile context; a
  run-time late fetch keeps a fresh vetter, as `fetchDoc` does per remote,
  since each fetched document is independent.
  `inliner.vetProfile` supplies the one narrowing. Under `WithRetrievalBase`
  the resolution walk reads `$id` as inert, so `schemavet.Profile.InertIDs`
  skips the `$id` domain check in every document the run holds. The
  inliner's own index walk takes the currency at its entry points,
  `recordDoc` for a document root and `recordNode` for a materialized
  pointer target, so the walk behind them reaches no schema the run has not
  vetted. The pointer-graph policy is
  separate from that vetting and has rules at two boundaries, the root document
  and the graphs that arrive from outside the package. `checkSchemaTree`
  (`validate.go`) rejects a root whose sub-schema pointers alias or cycle, and
  both `Compile` and `Inline` run it over the document they are given, so the two
  engines make the same demand of a root's sub-schema graph. A loop closing
  through a value field is the shape that check skips, so each engine runs a
  cycle check beside it over the same root. Both word the refusal through one
  `cycleError` helper, which names the pointer where the loop closes and the
  pointer it returns to. `Compile` reads `schemaclone.FindCycle` through
  `checkSchemaCycle` and keeps no copy, since its root stays the caller's own
  value; `Inline` reads the same report off the `cloneCheckedSchema` copy it
  needs anyway. The report names one loop out of a root holding several. The
  field table fixes which keyword the walk descends first and every container it
  descends orders its own members, so the two engines name the same one.
  `cloneCheckedSchema` holds the two graphs that arrive from outside the package
  to the weaker no-cycle rule alone: a document a `RefResolver` returns and a
  `SubstituteRef` schema. Aliasing survives there, because every walk that
  reaches a registered document dedupes pointers, so within this package it
  costs only the accuracy of a location in an error message. It does reach
  `Inline`'s output, which `checkSchemaTree` then rejects if the caller compiles
  it, so an aliased resolver document buys a non-tree result. A cycle is fatal,
  because `refresolve`'s JSON-pointer fallback marshals the document it
  searches and upstream's `MarshalJSON` re-enters `json.Marshal` at every
  nesting level, so no encoder sees the repeat and the marshal recurses into
  a stack overflow no `recover` catches. The deep copy reproduces an aliased
  graph rather than flattening it into a tree, so the inliner's own
  `walkPair` and `stripIdentifiers` walks carry visited sets. Two
  constraints the design works within: `refresolve` is a standalone package
  with no parent import (leaf-to-leaf imports like its `schemavet`
  dependency are fine; the parent is not), so keying its `baseURIs`/`walked`
  by node id means either an extra lookup at the boundary or breaking that
  boundary; and the defensive fetched-document `cloneCheckedSchema` keeps
  every cache independent of the resolver-owned schema (a resolver may hand
  out one shared object to many callers, and cached documents are walked and
  registered long after the resolver returned), an isolation a pointer index
  alone cannot provide.
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
  (`CanonicalizeNumeric`) all live here. In `ValueSet` a forbidden value never
  loses the single `not` slot, whatever else arrives and in whatever order. The
  keyword table scopes `not` to the null wrapper and `allOf` to the value branch,
  so a value forbid that lost the slot would stop applying to a null instance,
  which is the null half of what `required` asserts. A forbidden subschema takes
  the slot only when it arrives first and alone; every caller here names a type
  on the schema it forbids, which is what makes that placement
  agree with the `allOf` one. Its scope stops at the bound algebra
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
  (which makes element retargeting one implementation), `Shape.Nullable` the
  occurrence's null admission, which `ShapeOf` approximates as pointer-ness and
  the parent package's `FieldContext.Shape` answers exactly from the field's
  node,
  `Shape.ParseScalar` the one scalar constructor including the
  convert-and-marshal round-trip a text-marshaling type needs, and a total
  `[opCount][formCount]` matrix the
  dispatch. A coerced float is the one shape whose zero has two serializations,
  since Go's negative zero compares equal to zero and `encoding/json` writes the
  sign bit for it. `Shape.zeroLiterals` names both texts, and the shared
  `forbidLiteral` path behind `required` and `ne` forbids each. `eq` and `oneof`
  constrain the canonical text alone, a divergence `testdata/tags/cases.json`
  records as a fixture row rather than resolving. Dialect divergence is
  expressed as a named `Policy` parameter (the
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
  rebuilds a sub-schema container, handing each child the pointer segment that
  addresses it and walking a map's keys in sorted order, which is what settles
  which loop the clone walk's cycle report names; `CloneDeep` copies a container
  whose interior is mutable and supersedes the `CloneContainer` the same field
  also carries, and `CloneContainer` alone reallocates a header whose interior
  is not. Each column's presence follows from the field's Go type, so the
  table's staleness guard checks all three), `internal/keywordmeta`
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
  `Schema`), `internal/schemaclone` (structural deep copy of a `Schema`, one
  field at a time through the `internal/schemafield` table. The copy is
  faithful rather than normalized. It reproduces the source's pointer graph and
  keeps each value's Go type, so an aliased node stays one node, a cycle copies
  as a cycle, a `json.Number` holds its literal, `PropertyOrder` rides along
  like any other field, and a schema stored in `Extra` stays a schema. No graph
  shape defeats it, so `Clone` has no error return; `CloneChecked` returns the
  same copy plus a `*Cycle` naming the pointer where the source's first loop
  closes and the pointer it returns to, which the three `cloneCheckedSchema`
  boundaries read; `FindCycle` returns that report alone, for `Compile`, which
  keeps no copy of the root it is handed. The copy walk carries a stack of
  decoded path segments and renders it as a JSON pointer only where a loop
  closes, so an acyclic copy renders nothing. The package doc names the two
  values a copy still shares with its source, and `FindCycle`'s own doc comment
  says why it builds a copy it drops), `internal/jsonequal` (DoS-guarded,
  JSON-semantic value equality for `const`/`enum` and the matching content hash
  for `uniqueItems`, layered on `internal/numrat` for exact decimal comparison),
  `internal/goast` (doc-comment and type/field-shape extraction from a parsed Go
  package, for the generation half's comment provider), `internal/regexcache`
  (process-wide compile-once cache for validation-time regular-expression
  patterns, memoizing the compiled expression or the compile error so a pattern
  compiles at most once and fails closed identically across runs), and
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
  each JSON-pointer fallback target the session materializes. Every production
  session passes one, so the resolution marks a refusal on the `Result` and
  the closure walk treats it as settled, unlike an ordinary pointer miss. Only
  a test whose walk materializes no target passes nil, which skips vetting.
  The `ErrRefResolve`, `ErrNotResolved`, and `ErrIDCollision` sentinels live
  here and are re-exported from `errors.go` so `errors.Is` identity holds
  across the boundary. `ErrIDCollision` carries the registration rule both
  engines share. The registration refuses a fetched document claiming a `$id`
  or anchor another document already holds rather than merging it, and judges
  a substitute on its `$id` alone. Detection is a merge-time check against a
  scratch walk, so the walk itself stays infallible and a refused document
  leaves no half-written registry behind. `Session.RegisterFetched` is the one
  path both engines take for a fetched document. A collision settles at the
  fetch, ahead of the structural vet, so a document carrying both faults fails
  with one sentinel wherever it is read; `Session.CheckFetched` serves the two
  callers that vet in between. A configured fallback suspends the closure
  walk's other refusals but not this one. `doc.go` states the rest of the
  rule. Sub-schema traversal, which it cannot name without importing the
  parent, is injected as a `Deps` closure (deep cloning stays parent-side in
  the fetch closures), and the two draft-dependent branches use its own
  two-value `Draft` enum; the sibling leaf `internal/schemavet` is its one
  internal import), and `internal/schemavet` (the single structural-vetting
  policy and the mint for the vetted-currency types. A `Vetter` runs the
  structure, type-name, bound, items-array, and identifier checks, and only
  `Vetter.VetDoc`/`Vetter.Vet` can produce a `Doc`/`Node`, whose unexported
  fields make the compiler enforce that vetting ran. The nine vetting
  sentinels live here, re-exported from `errors.go` on the refresolve
  convention. It carries its own four-flag `Profile` (three draft flags
  converted by `draftProfile.vetProfile`,
  mirroring `toRefDraft`, plus `InertIDs`, which the inliner sets from
  `WithRetrievalBase` so the `$id` domain check follows the resolution walk
  that ignores the keyword) and its own `Entries` traversal, whose pointer
  assembly a lockstep guard test in `walk_test.go` pins to `SubschemaEntries`,
  since vetting errors embed those pointers).

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
- Five differential rigs close the loop on four properties. The two in this
  bullet share the first. **The schema generated for a Go type must accept
  whatever `encoding/json` marshals from a value of that type.** A rejection means
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
  - `task go:fuzz` searches for new counterexamples. A target's seed corpus
    runs on every `go test`. It holds the `f.Add` seeds written in the target
    and any entry committed under `testdata/fuzz/<Target>/` in the directory
    of the package that declares the target. Every rig here seeds in code, and
    a discovered counterexample is committed as a permanent regression seed
    before the fix.
- The `$ref` differential rig closes the loop across the three sites that
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
  - `TestRefEnginesAgreeOnPastFixes` pins one graph per `$ref` fix as
    JSON.
  - `FuzzRefEnginesAgree` (`fuzz_ref_test.go`) synthesizes a multi-document
    graph from a blob, drawing the draft, each document's `$id`, the anchor
    form, and eight reference spellings. A third pipeline withholds one
    document from the resolver and serves it through a `WithRefFallback`
    substitute. Its seeds run the draw cursor from every choice at zero to a
    saturated blob, with mixed patterns between. No seed is written by hand,
    since a corpus entry for a `[]byte` argument is entropy and no blob can be
    written by hand to decode to a chosen graph; the entries under
    `testdata/fuzz/FuzzRefEnginesAgree/` are minimized counterexamples the
    fuzzer found.
  - Six reason constants live in `ref_differential_test.go`. Three are skip
    reasons the rig classifies from the error `Inline` returns, never from a
    test name, so a reason cannot go stale against a renamed suite case:
    `reasonInlineCycle`, `reasonInlineDynamicRef`, and `reasonDeferredRefMiss`
    (`Compile` tolerates a missing remote document and defers, `Inline` fails at
    inline time, so the verdicts are not comparable). `reasonSubstituteBaseURI`,
    `reasonSubstituteNoAnchors`, and `reasonSubstituteTransitiveMalformed` are
    not skips; the generator applies them when choosing which document to
    withhold.
  - The rig found two divergences, and one policy applied at both engines
    closes each. The shared `refClosure` walk closes the transitive-vetting one,
    and `TestRefEnginesAgreeOnTransitiveVetting` asserts the agreement, so a
    malformed leaf may land in any served document, including one reached only
    through another document's reference. One identifier rule closes the
    colliding-`$id` one, and `TestRefEnginesAgreeOnCollidingIDs` asserts it.
    The `$id` pool therefore draws the root's URI and both retrieval URIs, and
    the fuzzer searches colliding graphs for a disagreement. Every malformed
    slot is drawable on every graph, colliding `$id`s included, since both
    engines vet a fallback target where the walk materializes it and so name
    the fault the walk reaches first.
  - `suiteFiles` in `suite_test.go` is the single enumeration of the vendored
    suite. The three conformance tests and the differential all draw their files
    from it, so the differential cannot drift from what the conformance tests
    run.
- `internal/format` pins the built-in string-format checkers in three
  layers: robustness fuzz (`fuzz_format_test.go`), stdlib and cross-format
  differentials (`differential_test.go`, `containment_test.go`), and vendored
  corpora plus curated vectors (`corpus_*_test.go`, `vectors_test.go`).
  Acceptance vectors are data, not Go literals: one
  `testdata/vectors/<format-name>.tsv` per format, basename matching the name
  registered in `format.Validators()` exactly, three mandatory tab-separated
  fields per row (a Go quoted input literal, `true` or `false`, and a note), and
  `TestFormatVectors` walks the directory, so a new file adds a test with no Go
  edit. `loadVectorFile` is the authoritative statement of the row format.
- `TestFormatCoverage` (`internal/format/coverage_test.go`) requires every name
  in `format.Validators()` to carry a differential fuzz target, a vendored
  corpus, or a vector file, so a newly registered format cannot arrive with
  none. A claim names the Go function value rather than its name as a string, so
  renaming a target breaks the build in the coverage table. Two guards beside it
  read the package's own test sources for what a function value cannot carry.
  `TestFormatCoverageSourcesNameTheirFormat` requires each claimed source to
  name its row's format through one of the helpers `formatHelper` lists, and
  refuses a differential claim on a target of another shape.
  `TestFormatCoverageClaimsEveryDifferential` requires a coverage row to claim
  every target shaped like a differential, wherever the package declares it, so
  a differential that names its format the way the others do cannot stay
  invisible. A differential's shape is a `Fuzz` target that names one format and
  does not reach its validator through `fuzzFormatRobust`. A robustness target
  reaches it through that helper and asserts only that the validator neither
  panics nor contradicts itself, so it names a format and covers none.
  Containment targets are deliberately not a coverage source either. A
  containment oracle is another format in the same package rather than an
  independent one, so a containment pair that drifts together stays green, and
  `TestFormatCoverageClaimsEveryDifferential` passes over any target naming more
  than one format. Each allowlist entry carries a reason string; the allowlist
  ships empty.
- `format_deviations_test.go` binds each bullet of the format-deviations list in
  `doc.go` and `README.md` to the behavior it claims: a phrase both files must
  carry, matched over normalized text so a reflow cannot break it, and the
  bullet's own quoted examples run through the public API. The row count must
  equal the bullet count in both lists, so a sixth deviation cannot land with
  nothing asserting it.
- `testdata/tags/cases.json` is the cross-dialect fixture table, run by
  `tags_fixture_test.go`. Each row names a field shape through a small type
  registry, the spelling each dialect gives one rule, the property schema the
  rule must produce, and the instances the compiled schema must accept and
  reject, so tag behavior is data rather than an inline `JSONEq` per scenario
  and a reversal shows up as a fixture diff. A row may state a sentinel instead
  of a schema, naming it through a registry rather than matching message text;
  `anyGenerationError` covers the few rejections the interpreter raises with no
  sentinel. `TestTagFixturesCrossDialectVerdictsAgree` is the data-driven form
  of the equivalence 1f74e42 pinned. Where a row spells one rule in both
  dialects, the two schemas must accept and reject the same instances, which is
  a stronger statement than the schema-text identity
  `TestCrossDialectEquivalence` asserts beside it. `TestTagFixturesCoverage`
  requires a unique name and a note per row and exercises every shape the type
  registry offers.
- The go-playground differential rig lives in the nested
  `interpreters/validate/differentialtest` module and asserts
  `validate.Struct(v) == nil iff schema.ValidateJSON(json.Marshal(v)) == nil`.
  Its agreement property splits on what the marshaled object carries. Where
  every field is present with a non-null value the two validators must agree
  exactly, and where encoding/json drops a field or writes null for one the
  schema has nothing to assert about it and can only be the more permissive of
  the two, so the property weakens to that one-way implication.
  A `required` field is the exception to the weakening, since it is the one rule
  that does assert something about null, so its null stays under the
  biconditional.
  `TestWidenedDifferentialReachesStrictAgreement` guards against the weak half
  swallowing everything. The schema verdict is per object, so a sibling field
  that correctly rejects null can mask one that wrongly accepts it; the
  deterministic `TestRequiredOnNullableRejectsNull` puts each `required` shape
  that admits null in a struct of its own for that reason. It is also the only
  place the nil occurrence is reachable, since `internal/fuzzfill` builds every
  container through `reflect.MakeSlice` or `reflect.MakeMap`, which return a
  non-nil container even at length zero. `required` forbids null on a bare
  slice, map, or `[]byte` beside its size floor, exactly as it forbids null
  alone on a pointer. The nil side agrees with
  go-playground and the empty-but-non-nil side does not, which is why the draw
  pools omit `required` on a collection under
  `reasonRequiredCollectionEmptyFloor`.
  `FuzzValidatorTaggedShapes` fuzzes the shape as well as the value through
  `drawTaggedStruct`, which draws the Go kind, the pointer
  wrapper, the json option, and the validate rule independently. That draw
  deliberately does not come from `internal/fuzzshape`, which synthesizes
  embeds, unexported fields, and colliding JSON names to probe field collection,
  which go-playground reads differently and whose `reflect.StructOf` promoted
  methods panic when called. Every case the rig does not compare is a row in
  `rigExclusions` carrying a reason constant, and
  `TestRigExclusionsMatchTheDraw` pins each rule-bearing row against the draw
  pools so the record cannot drift from what the draw does.
- `tags_shape_oracle_test.go` holds `encoding/json` to a third property, the
  struct-tag one: **the `Form` `internal/tagmodel` classifies a field as must
  agree with the JSON `encoding/json` writes for that field.** `Form` is the
  dispatch column of the constraint matrix, so the matrix silently applies
  the wrong rule set to a field in the wrong column, which is where five past
  fixes cluster (db3c7b5, 679bd8b, 99ba651, 5c04089, 649a6f2, one row each). A
  probe `TagInterpreter` registered under the `json` tag key records
  `FieldContext.Shape()` and `FieldContext.Base` for every field generation
  classifies, so the oracle reads the production classification instead of
  recomputing one. Recomputing is not equivalent. A `json:",string"` string
  field and its pointer carry a quoted flag `ShapeOf` cannot see, and a pointer
  to a text-marshaling numeric or to a `$def`'d type hides its payload behind
  the nullable wrapper the `permits-a-string` and `$ref` tests read. The roster
  runs under both `WithDefinitions` settings, which is what makes the referenced
  and text-marshaled columns reachable at all, and a second leg runs the same
  property over `internal/fuzzshape` shapes. Five reason constants name the
  field classes the probe cannot observe (untagged, `json:"-"`, unexported, an
  allOf-composed embed, and a JSON name two fields claim at one depth); a field
  matching none of them and still unobserved fails the leg.
