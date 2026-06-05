# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

This file covers the `jsonschema` module only. It is part of the repo's go.work workspace.

## Architecture

The package has two independent halves sharing the `Schema` type:

- **Generation** (`generate.go`, `reflect.go`, `tags.go`, `names.go`,
  `comments.go`): Go types -> JSON Schema via reflection. `generator` in
  `reflect.go` is the core; `generate.go` holds the functional options and
  the `GenerateFor`/`Generate` entry points.
- **Validation** (`validate.go`, `validate_formats.go`, `vocabulary.go`,
  `errors.go`): JSON instances -> structured `*ValidationError` trees.
  `Compile` builds a `validator` once (registry construction from
  `$id`/`$anchor`, precomputed numeric bounds and compiled regexes, draft and
  vocabulary detection); `validator.forInstance` derives cheap per-run state.

### Relationship to google/jsonschema-go

`Schema` is a type alias to the upstream type (`schema.go`). The upstream is
used for exactly two things: structural well-formedness via `Schema.Resolve`
(called once per `Compile`, result discarded) and JSON-semantic value equality
(`const`/`enum`/`uniqueItems`). Everything else — the reflection pipeline, all
`$ref`/`$dynamicRef`/`$anchor` resolution, the validation walk, path tracking,
format checking — is implemented here, because the upstream's resolved
reference graph is unexported and its validator stops at the first error.

When bumping the upstream dependency, the whitebox clone tests
(`clone_*_whitebox_test.go`) act as maintenance guards: they enumerate every
`Schema` field and fail if a newly added upstream field is not classified in
`cloneSchema`/`isEmptySchema`.

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
