# CLAUDE.md

Guidance for Claude Code in the `jsonschema` module, one module of the repo's
go.work workspace. Behavior is specified in `doc.go` and `README.md`; any
behavior change updates both, and `prettier` gates every `*.md` edit.

Status: UNSTABLE. Breaking changes allowed.

## Architecture

Two independent halves share the upstream `Schema` type (`schema.go`, a type
alias to `github.com/google/jsonschema-go/jsonschema.Schema`).

### Generation

`generate.go` holds the options (`generatorConfig`, applied once by
`newConfig`) and the entry points; `reflect.go` holds `run`, the per-run state
one generation derives from a config. `run.generate` is eight phases, in order:

1. **Reflect the graph** (`reflect.go`): `schemaForType` builds a `node` tree
   (`ir.go`) from the type resolution priority below. Every node records the
   facts of its occurrence (pointer-ness, container kind, a type hook's
   `Nullability` stance) and decides nothing. Type-level hooks run here, and so
   does the jsonschema tag's `type=` pair, which rewrites the field's node in
   place (`node.overrideType`) and keeps the reflected occurrence as a value
   copy for the directives before the pair.
2. **Resolve nullability** (`ir.go`): `resolveNullability` fills `node.null`
   for every node in one walk, from the `nullFacts` reflection recorded (a
   reference reads its body's container kind and stance off the def entry,
   never the body's decision), through the pure `admitNull` and `wrapNull`.
   Nothing reads the decision earlier and nothing changes it later.
3. **Assign def names** (`names.go`): `emittedDefs` collects the entries
   render will emit (reachable from the root, minus a bare `$ref` root that
   phase 5 inlines), and every one of them gets its final key, disambiguated
   among themselves alone, before any `$ref` string is emitted; an orphaned
   entry keeps a token-shaped name. `finalizeRefs` then rewrites the
   per-entry provisional token phase 1 wrote into every payload (a ref node's
   own, and any literal a type-level hook copied it into) to that key, so no
   later phase sees a token.
4. **Field hooks** (`reflect.go`): the description provider, the rest of the
   jsonschema tag, and the tag interpreters run per field on private
   `node.view` copies. The one write read back is a name appended to
   `FieldContext.Parent.Required`.
5. **Inline the root** (`render.go`): a bare `$ref` root reached from nowhere
   else becomes its body.
6. **Scan canvas literals** (`ir.go`): every field and element canvas is
   refused a null literal against an occurrence that admits none, and an
   empty enum.
7. **Seed defaults** (`generate.go`): `WithDefaultsFrom` writes onto canvases
   and hook-declared literals, gated by the decision from phase 3.
8. **Render** (`render.go`, `reconcile.go`): one fresh `Schema` per node. The
   IR is never mutated and the output shares nothing with a hook.

A `node.payload` holds the type-derived base with no child slots; children live
on the node (`props`, `items`, `prefix`, `embeds`). The `authored` canvas holds
what field hooks declared, and `keywordmeta` decides which side of a null
wrapper each keyword lands on.

### Validation

`Compile` (`validate.go`) freezes the root through `schemavet.Freeze`, vets it,
builds the `refresolve.Registry` from the frozen tables, folds every document
into the node-identity index (`index.go`), precomputes per-node caches, and runs
`refClosure` (`refclosure.go`) to fetch and vet every document a reference
reaches. Fetched and substituted documents take the same path, collision check
first and vet second. `dispatch.go` and `keyword_table.go` drive the walk over
`jsonvalue.Value` instances. `inline.go` freezes the same way and expands
references onto a fresh tree, so its output aliases nothing.

### Type resolution priority

For each Go type the first matching step wins: `WithTypeSchema` override ->
`JSONSchemaProvider` -> built-in overrides (exact `reflect.Type` match) ->
marshaler methods promoted from an embedded field -> direct
`encoding.TextMarshaler` -> kind-based reflection. A direct `json.Marshaler` is
not consulted. `JSONSchemaExtender` runs only on a reflected schema. Every
refusal at the kind-based step is `encoding/json/v2`'s own verdict, taken by
`internal/jsonprobe` on a filled value. The fill leaves an interface-kind map
key nil, so an interface-keyed map is refused although v2 accepts one at run
time when every key holds a string.

### Internal packages

Each owns one invariant, except `testtypes`, which holds test fixtures and
is the only one that imports the parent package.

- `annotations`: the 2020-12 annotation sets the `unevaluated*` walk merges.
- `constraint`: the bound algebra; an authored bound only tightens a type's.
- `content`: `contentEncoding`/`contentMediaType` decoding.
- `fieldset`: v2's field dominance rules; the names it resolves are v2's.
- `format`: the built-in string-format validators.
- `fuzzfill`, `fuzzshape`: deterministic values and struct shapes from a blob.
- `goast`: doc-comment extraction for the comment provider.
- `jsonopts`: the one table classifying every `encoding/json` option.
- `jsonprobe`: the v2 refusal oracle; what it refuses, v2 refuses.
- `jsonptr`: RFC 6901 escaping and the JSON-form pointer walk.
- `jsontag`: a verbatim port of v2's tag grammar.
- `jsonvalue`: the one JSON value model; numbers carry their exact decimal.
- `keyword`, `keywordmeta`: keyword names and their merge, scope, and draft.
- `numkind`, `numrat`, `typename`: kind, decimal, and type-name primitives.
- `reflectkind`: method-set predicates over `reflect.Type`.
- `refresolve`: `$ref` resolution both engines share; every document it holds
  is a `schemavet.Doc`, and every table keys on a `uriref.DocKey` or
  `uriref.AnchorKey`.
- `schemaclone`, `schemafield`, `schemashape`: the field table and the copy,
  traversal, and shape predicates derived from it.
- `schemavet`: `Freeze` copies a document into a private tree and builds its
  identifier tables; only `Vet` mints the `Doc` currency the engines demand.
- `tagmodel`, `tagparse`: one constraint matrix over JSON shape, and the
  jsonschema tag grammar over it.
- `testtypes/alpha`, `testtypes/beta`: fixture types for the comment
  extraction and cross-package name collision tests; real source packages so
  `go/packages` can load their doc comments.
- `regexcache`, `uriref`, `vocab`: pattern cache, RFC 3986, vocabulary gating.
  A `uriref.DocKey` is the canonical, fragment-free identity of one document,
  minted only by `uriref.Resolve` and `uriref.ParseBase`; `Location` and
  `AnchorKey` are the only producers of the `#` joining a document to a
  fragment. `schemavet.identifiers` is the one reading of a node's identifier
  keywords the freeze, the identifier checks, and the JSON-form pointer walk
  share.
- `uriabnf`: the RFC 3986 and 3987 grammars by descent, an oracle for
  `format` that shares nothing with net/url.

## Guard tests

- `TestFieldTableMatchesUpstream` (schemafield): every upstream field has a row.
- `TestCloneSubschemasWritesItsOwnField` (schemafield): setters match getters.
- `TestIsTrueSchemaRejectsEverySetField`, `TestSchemaSerializableFieldCoverage`
  (schema_test.go): each zero predicate reads its field; each field round-trips.
- `TestKeywordMetaCoversSchemaFields`, `TestKeywordMetaColumnsConsistent`,
  `TestKeywordMetaCoversConstants`, `TestKeywordMetaDerivedSets`,
  `TestKeywordMetaApplicatorsMatchSubschemaFields` (keywordmeta): every field
  is claimed, no wrapper-scoped keyword replaces, sets derive, and the
  applicator column matches the sub-schema shapes.
- `TestApplicatorFalseSubschemaKeyword` (validate_keywords_test.go): every
  applicator's false subschema leaf carries the applying keyword.
- `TestReconcileSplitConsistency`, `TestReconcileSplitCoversAuthored`: a `T`
  and a `*T` field accept the same non-null instances, over every keyword.
- `TestAssertionKeywordsCoverage`, `TestDispatchDraftGating`,
  `TestDispatchDraftGatingCoversRows`, `TestDraftConstantsInSync`,
  `TestPublicKeywordConstantsMirrorInternal` (dispatch_test.go): the dispatch
  table agrees with the keyword table and the public constants.
- `TestDispatchRowPresenceMatchesFields`, `TestRowsForKeepsTableOrder`
  (dispatch_internal_test.go): a row's presence predicate is the fields its
  keywords claim, and the per-node row list keeps the table's order.
- `TestTableCoversToolchain` (jsonopts): every toolchain option is classified.
- `TestAgreesWithV2` (jsonprobe): the probe's verdicts are v2's.
- `TestMatrixGolden`, `TestFormClassificationTotal` (tagmodel): the matrix is
  total and every cell change is a reviewed diff.
- `TestFormatCoverage` and its two source guards (format): every format has an
  oracle, and the vector files under `testdata/vectors/` are the data.
- `TestTagFixturesCoverage`, `TestTagFixturesCrossDialectVerdictsAgree`:
  `testdata/tags/cases.json` covers every shape and both dialects agree.
- `TestHookPointersArePrivateCopies` (ir_test.go): a hook write through
  `Parent` or `Base` never reaches the output.
- `TestOverrideTypeClassifiesEveryNodeField`, `TestOverrideTypeMatchesReflection`
  (ir_internal_test.go): every `node` field has a declared fate under a
  `type=` override, and the in-place rewrite honors it for every kind.
- `TestNullDecisionRules` (ir_internal_test.go): the null decision over the
  full cross product of `nullFacts` matches an ordered rule list whose every
  rule fires, and every fact field has an enumerated axis.
- `TestFieldContextZeroValueSurvives` (interfaces_test.go),
  `TestConstraintsFacadeNilCanvasWrites`, `TestConstraintsFacadeInvalidRule`
  (constraints_test.go): every method on the zero context and facade returns,
  a write needs a canvas, and a rule outside the model's table is refused;
  the case counts track the method sets.
- `TestRigExclusionsMatchTheDraw` (differentialtest): every excluded case is
  a recorded reason the draw pools honor.

## Differential rigs

- `FuzzReflectAccepts<T>` and `FuzzShapeAccepts*`: the schema for a type
  accepts whatever v2 marshals from a value of it; `FuzzShapeRejectsNearMiss`
  refuses an instance one property away. Shapes come from `fuzzshape`.
- `FuzzFieldSetKeys` (fieldset): the resolved names are the keys v2 writes.
- `FuzzRefEnginesAgree`, `TestSuiteInlineAgrees`,
  `TestRefEnginesAgreeOnPastFixes`: `Compile`, `Inline`, and the substitute
  path reach one verdict per reference graph.
- `TestCompileOneKeyPerDocument`, `TestRefEnginesFetchOnceWithoutBase`: a
  document named by several canonically-equal spellings is fetched once,
  under one `uriref.DocKey`.
- `FuzzValidatorTaggedShapes`, `FuzzValidatorRequiredNullableShapes`
  (`interpreters/validate/differentialtest`): the validate tag agrees with
  go-playground where the marshaled object lets it.
- `FuzzValidatorTagSpellings` (same module): a fuzzed tag string is usable on
  both sides or neither, and the verdicts agree where both use it. Every
  spelling left out is a reasoned entry in `spellingExclusions`, pinned by
  `TestSpellingExclusionsAreReasoned` and `TestSpellingSeedsReachEveryCell`.
- `FuzzFormat*VsABNF` (format): the four URI and IRI formats agree with
  `uriabnf` in both directions, and `TestURIVectorsAgreeWithGrammar` runs every
  vector row through the grammar.
- `tags_shape_oracle_test.go`: the `Form` a field classifies to is the JSON v2
  writes for it. `suite_test.go` runs the vendored official suite with reasoned
  skips; `conformance_test.go` checks generated schemas against the metaschemas.

## Tooling

- `task check` from the repo root is the gate: lint, race tests, prettier.
- Test files are one per topic, not one per fix. A new regression test joins
  the file whose prefix covers it (`validate_compile_test.go`,
  `reflect_embedded_test.go`, `inline_remote_test.go`, ...), and the general
  `validate_test.go`, `generate_test.go`, or `tags_test.go` when none does. Its
  doc comment carries the regression context the filename used to.
- Run formatting from the repo root. `golangci-lint fmt ./...` inside
  `jsonschema/` reaches the nested `differentialtest` module and reorders its
  alias import group against that module's own config.
- Bare `go vet ./...` reports deliberate `structtag` findings in tests that
  declare faulty tags on purpose; each carries a `//nolint:govet` directive
  that only golangci-lint reads.
