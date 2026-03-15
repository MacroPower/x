# PRD: jsonschema — Go Type to JSON Schema Generator

## Overview

A Go package that generates JSON Schema documents from Go types via reflection and validates JSON instances against schemas. It builds on top of `github.com/google/jsonschema-go/jsonschema` and adds higher-level features: customization interfaces, pluggable struct tag interpretation, Go doc comment extraction, multi-draft support, structured instance validation with full path tracking, JSON Schema vocabulary gating, and pluggable remote `$ref` resolution.

**Module:** `go.jacobcolvin.com/jsonschema`

### Relationship to `google/jsonschema-go`

This package re-exports the upstream `Schema` type via type alias so users need only import this package. The upstream provides basic type-to-schema inference, but lacks the customization pipeline needed here (no interfaces, no tag interpreters, no `$defs`/`$ref`, no draft awareness, no comment extraction, errors on recursive types). This package implements its own reflection pipeline, adding those features. The upstream also provides `Resolved.Validate`, but with limitations: first-error-only returns in most combinators, no instance paths in errors, unstructured error strings, and `format` validation completely ignored. This package implements its own validation walk with structured hierarchical errors, full instance/schema path tracking, and pluggable format validation.

## Goals

1. Generate correct JSON Schema from arbitrary Go types with zero configuration.
2. Support JSON Schema Draft-07 and Draft 2020-12.
3. Allow types to customize their own schema via well-known interfaces.
4. Provide a pluggable system for interpreting struct tags (e.g., `validate` tags) as schema constraints.
5. Attach Go doc comments as `description` fields, sourced from AST parsing or struct tags.
6. Validate JSON instances against schemas, returning structured errors with instance paths, schema paths, and hierarchical multi-error support.

## Non-Goals

- Meta-schema validation and structural well-formedness checking — delegated to upstream `Schema.Resolve` (called once for pre-validation; its `*Resolved` result is discarded). URI resolution and `$ref`/`$dynamicRef` target lookup are _not_ delegated: the validation walk implements them itself because it operates on the original `*Schema` tree rather than upstream's resolved graph (see Design Decisions 12 and 14). Because target lookup is this package's responsibility, a `Schema.Resolve` failure caused _solely_ by a reference upstream cannot follow — for example a `$ref` whose JSON Pointer targets an unknown keyword or the internals of a non-applicator keyword such as `examples` — is not fatal: pre-validation proceeds when the schema is otherwise structurally well-formed (it resolves cleanly with references removed) and this package can resolve the reference itself.
- Code generation _from_ schemas (the reverse direction). Forward-direction generation is supported, including a build-time CLI (see §CLI).

## Thread Safety

`Validate` and `ValidateJSON` are safe for concurrent use with the same `*Schema`. The regex cache uses `sync.Map`; each call copies the caller's `ResolveOptions` before injecting a `Loader` (so a shared `*ResolveOptions` is never mutated), and remotely-fetched schemas are deep-copied before being registered. `Schema.Resolve` is called per invocation and does not observably mutate the input schema.

## Relationship to Upstream

| Concern                                                       | Implementation                                                                                                                            |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| Schema data model (`Schema` struct)                           | Upstream (re-exported via type alias)                                                                                                     |
| Meta-schema validation, structural well-formedness            | Upstream `Schema.Resolve` (result discarded; a failure caused solely by a `$ref`/`$dynamicRef` this package resolves itself is tolerated) |
| `$ref`/`$dynamicRef`/`$anchor` resolution (incl. remote refs) | This package (own URI/anchor registries)                                                                                                  |
| Instance validation walk                                      | This package (own implementation)                                                                                                         |
| Error types and path tracking                                 | This package                                                                                                                              |
| Format validation                                             | This package (pluggable)                                                                                                                  |
| JSON-semantic value comparison (`const`/`enum`/`uniqueItems`) | Upstream `Equal()`                                                                                                                        |

The package implements its own validation walk rather than wrapping upstream's `Resolved.Validate` because the upstream validator (a) collects limited multi-error information via `errors.Join` in `anyOf` but returns on first error within container keywords (`properties`, `items`, `patternProperties`) and `allOf`, (b) does not track instance paths, (c) returns unstructured string errors, and (d) does not validate the `format` keyword. Since the upstream `Resolved` type's internal reference graph (`resolvedInfos`) is unexported, this package cannot reuse compiled regexps or precomputed required-property sets, and performs its own reference resolution: JSON Pointer traversal of the `*Schema` tree for local fragments, registries built from `$id`/`$anchor` for named anchors and absolute-URI targets, a dynamic-scope stack for `$dynamicRef`, and an optional `RefResolver` for remote refs. `Schema.Resolve` is still called first to confirm the schema is well-formed before the validation walk begins; a `Schema.Resolve` failure caused solely by a reference upstream cannot follow but this package can (see Non-Goals and Design Decision 12) is tolerated when the schema is otherwise structurally well-formed. The upstream's exported `Equal` function is used for JSON-semantic value comparison (`const`, `enum`, `uniqueItems`).

## Processing Order

**Type-level** (once per type): base type reflection → comment extraction → `JSONSchemaExtend`.

**Field-level** (per struct field): `json:",string"` override → comment extraction → `jsonschema` struct tag → tag interpreters.

Field-level processing always applies, including for `$ref`'d types.

## Design Decisions

1. **Schema type re-export** via type alias avoids import collisions — users import only this package. Because it aliases the upstream struct, keywords absent from that struct are carried only in the schema's `Extra` map and are not recognized by the validation walk.
2. **Own reflection pipeline** because upstream's inference is too opaque to extend with interfaces, tag interpreters, `$defs`, and cycle detection.
3. **Draft-07 and 2020-12 only**, matching upstream's supported drafts.
4. **Circular types** handled via `$ref` to `$defs` (upstream errors on cycles).
5. **No validator dependency** — `interpreters/validate` adopts tag naming for ecosystem consistency without importing the library.
6. **`anyOf` for nullable `$ref`** — conventional in JSON Schema tooling, avoids `oneOf` validation overhead.
7. **`additionalProperties: false` by default** — Go structs define exactly what's allowed; opt in to permissive schemas.
8. **Nullable maps and slices** — both produce null-typed schemas, matching `encoding/json` nil behavior.
9. **Own validation walk** — upstream's `Resolved.Validate` has limited multi-error support (only `anyOf` collects branch errors) and lacks instance paths; this package implements its own walk to collect all errors with structured path information.
10. **Hierarchical `ValidationError`** — a tree of errors mirrors the schema/instance structure, allowing callers to inspect failures at any depth or flatten them into a list.
11. **Pluggable format validation** — formats are validated by pluggable checker functions (`func(string) error`) registered via `WithFormatValidator`, with built-in checkers for common formats, matching the JSON Schema spec's recommendation that format validation be optional and configurable.
12. **Own `$ref` resolution for validation** — since upstream's resolved reference graph is unexported, validation resolves references itself: JSON Pointer traversal for local fragments (including a JSON-form fallback that reaches sub-schemas held in unknown keywords or non-applicator keyword internals), plus URI/anchor registries built from `$id`/`$anchor` for named anchors and absolute-URI targets. `Schema.Resolve` is still called first for meta-schema validation and structural checks; because target lookup belongs to this package, a Resolve failure caused only by a `$ref`/`$dynamicRef` this package resolves itself is not fatal. The exception is gated: a ref-stripped clone must still resolve cleanly, every reference (and each resolved target) must be well-formed, and any non-ref failure stays fatal.
13. **`unevaluatedProperties`/`unevaluatedItems` support** — required because the generator produces `unevaluatedProperties: false` for Draft 2020-12 allOf composition; annotation tracking is reimplemented in the validation walk.
14. **Self-implemented reference resolution** — the walk resolves local fragment refs by default, remote/absolute refs via the optional `WithRefResolver` hook, and `$dynamicRef`/`$dynamicAnchor` (2020-12) via its own dynamic-anchor registry and dynamic-scope stack. Because the upstream's resolved reference graph is unexported, this package builds its own rather than omitting these features.
15. **Go RE2 for patterns** — `pattern` and `patternProperties` use Go's `regexp` (RE2), not ECMA 262; this matches upstream behavior and is a known deviation from the spec.
16. **`ValidateJSON` uses `UseNumber`** — preserves integer vs number distinction that would be lost with default `float64` unmarshaling.
17. **Vocabulary gating** — `$vocabulary` (resolved from a metaschema registered via `WithMetaSchema`, or overridden by `WithVocabularies`) selects which keyword groups run during the walk; a required but unrecognized vocabulary fails fast with `ErrUnknownVocabulary`. Draft-07 has no `$vocabulary`, so all groups stay active.
18. **Pluggable remote `$ref`** — remote resolution is opt-in via the `RefResolver` interface (`WithRefResolver`) rather than built in, keeping the default walk hermetic; resolver failures surface as `ErrRefResolve`.

## CLI: `jsonschemagen`

The module ships a build-time code-generation CLI under `cmd/jsonschemagen`, intended for use with `//go:generate`. It writes a JSON Schema file for a named Go type by generating a temporary program that imports the target package and calls `Generate`, so it reuses the same generation pipeline as the library rather than duplicating it. Flags: `-type`, `-o`, `-draft`, `-comments`, `-additional-properties`, `-indent`, and `-validate` (which enables the `validate` struct-tag interpreter in the generated program; it does not validate instances or the emitted schema). This is forward-direction generation; schema-to-code generation remains a Non-Goal.
