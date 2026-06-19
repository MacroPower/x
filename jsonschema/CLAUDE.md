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
  cheap per-run state. Self-contained helpers live under `internal/`:
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
  copy-on-change and a cycle guard), `internal/schemashape` (structural
  shape classification of a `Schema`), `internal/jsonequal` (DoS-guarded,
  JSON-semantic value equality for `const`/`enum` and the matching content
  hash for `uniqueItems`, layered on `internal/numrat` for exact decimal
  comparison), and `internal/goast` (doc-comment and type/field-shape
  extraction from a parsed Go package, for the generation half's comment
  provider).

### Relationship to google/jsonschema-go

`Schema` is a type alias to the upstream type (`schema.go`). The upstream is
used for exactly two things: structural well-formedness via `Schema.Resolve`
(called once per `Compile`, result discarded) and JSON-semantic value equality
(`const`/`enum`/`uniqueItems`). Everything else — the reflection pipeline, all
`$ref`/`$dynamicRef`/`$anchor` resolution, the validation walk, path tracking,
format checking — is implemented here, because the upstream's resolved
reference graph is unexported and its validator stops at the first error.

When bumping the upstream dependency, reflection-based maintenance guards in
the external test package enumerate every `Schema` field and fail on
unclassified upstream additions — all through public API (the package has no
in-package test files by policy):

- `TestIsTrueSchemaRejectsEverySetField` (schema_test.go): every exported
  field set alone must defeat `IsTrueSchema`; a new field fails until added
  to the predicate's enumeration. This is the primary alarm — when it fires,
  also revisit the internal `cloneSchema`/`isEmptySchema` classifications.
- `TestSubschemaEntriesFieldCoverage` (walk_test.go): every `*Schema`-shaped
  field must be returned by `SubschemaEntries`, the single traversal field
  list.
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
