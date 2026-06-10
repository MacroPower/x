<p align="center">
  <h1 align="center">jsonschema</h1>
</p>

<p align="center">
  <a href="https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema"><img alt="Go Reference" src="https://pkg.go.dev/badge/go.jacobcolvin.com/x/jsonschema.svg"></a>
  <a href="https://goreportcard.com/report/go.jacobcolvin.com/x/jsonschema"><img alt="Go Report Card" src="https://goreportcard.com/badge/go.jacobcolvin.com/x/jsonschema"></a>
  <a href="https://github.com/macropower/x/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/macropower/x"></a>
</p>

<p align="center">Generate JSON Schema from Go types and validate JSON instances, with structured errors.</p>

`jsonschema` generates JSON Schema documents from Go types via reflection and
validates JSON instances against schemas. It builds on
[`github.com/google/jsonschema-go`](https://github.com/google/jsonschema-go) and
adds higher-level features: customization interfaces, pluggable struct-tag
interpretation, Go doc comment extraction, Draft-07 and Draft 2020-12 support,
and structured instance validation with full instance/schema path tracking. The
upstream `Schema` type is re-exported via a type alias, and `Ptr` is provided as
a convenience helper for pointer-valued fields (e.g.
`jsonschema.Ptr(float64(0))` for `Schema.Minimum`), so callers need only import
this package.

## Installation

```sh
go get go.jacobcolvin.com/x/jsonschema
```

## Quick start

### Generate a schema from a Go type

```go
type SimpleStruct struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age,omitempty"`
}

schema, err := jsonschema.GenerateFor[SimpleStruct]()
```

produces:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "name": { "type": "string" },
    "email": { "type": "string" },
    "age": { "type": "integer" }
  },
  "required": ["name", "email"],
  "additionalProperties": false
}
```

### Validate a JSON instance

```go
schema := &jsonschema.Schema{
	Type:     "object",
	Required: []string{"name"},
	Properties: map[string]*jsonschema.Schema{
		"name": {Type: "string", MinLength: jsonschema.Ptr(1)},
		"age":  {Type: "integer", Minimum: jsonschema.Ptr(0.0)},
	},
}

// Compile once, then reuse -- the returned *Validator is safe for concurrent use.
v, err := jsonschema.Compile(schema)
if err != nil {
	log.Fatal(err)
}

if err := v.ValidateJSON([]byte(`{"name":"Ada","age":36}`)); err != nil {
	log.Fatal(err) // valid: not reached
}

// Validation failures unwrap to *ValidationError and carry full paths.
err = v.ValidateJSON([]byte(`{"name":"","age":-1}`))

var ve *jsonschema.ValidationError
if errors.As(err, &ve) {
	// ve is the root of an error tree; every failure keeps its instance path.
	for _, cause := range ve.Causes {
		fmt.Printf("%s at %s: %s\n", cause.Keyword, cause.InstancePath, cause.Message)
	}
	// minLength at /name: ...
	// minimum  at /age:  ...
}
```

## Features

- Schema generation from arbitrary Go types (not just structs) with zero
  configuration.
- Customization interfaces (`JSONSchemaProvider`, `JSONSchemaExtender`) for
  types to control their own schema.
- Pluggable struct-tag interpreters, including a ready-made `validate`-tag
  interpreter.
- Go doc comment extraction into `description` fields.
- Draft-07 and Draft 2020-12 output and validation.
- Structured instance validation: all failures collected as a tree with instance
  and schema paths.
- `$vocabulary` gating and pluggable, opt-in remote `$ref` resolution.
- A build-time code-generation CLI (`jsonschemagen`) for `//go:generate`.

## Generating schemas

The primary entry point is the generic `GenerateFor`. A `reflect.Type` variant,
`Generate`, is provided for dynamic use:

```go
schema, err := jsonschema.GenerateFor[MyType](opts...)
schema, err := jsonschema.Generate(reflect.TypeFor[MyType](), opts...)
```

The root schema always carries the `$schema` keyword; sub-schemas and `$defs`
entries never do.

### Type mapping

| Go type                              | JSON Schema                                                                                                 |
| ------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| `string`, `bool`, `float64`          | `string`, `boolean`, `number`                                                                               |
| `int`                                | `integer`                                                                                                   |
| `int8`...`int64`, `uint8`...`uint64` | `integer` with `minimum`/`maximum` bounds                                                                   |
| `uint`, `uintptr`                    | `integer` with `minimum: 0`                                                                                 |
| `*T`                                 | nullable: base schema wrapped in `anyOf` with a `{"type":"null"}` branch (see `WithNullable`)               |
| `[]T`                                | nullable `array` with an `items` schema (see `WithNullable`)                                                |
| `[]byte`                             | nullable base64-encoded `string` (`contentEncoding`) (see `WithNullable`)                                   |
| `[N]T`                               | fixed-size array via `prefixItems` with `minItems`/`maxItems` = N                                           |
| `map[K]V`                            | nullable `object` with `additionalProperties` (K: string, integer, or `TextMarshaler`) (see `WithNullable`) |
| `any` / interface                    | unrestricted (`{}`)                                                                                         |
| `struct`                             | `object` with `properties`, `required`, and `additionalProperties: false`                                   |

Well-known types have built-in overrides matched by exact `reflect.Type`:
`time.Time` -> `{"type":"string","format":"date-time"}`,
`encoding/json.RawMessage` -> `{}`, `encoding/json.Number` ->
`{"type":"number"}`, `math/big.Int` -> `{"type":"integer"}` (its MarshalJSON
emits a bare number), and `math/big` `Rat`/`Float` -> `{"type":"string"}` with
a numeric pattern. `net/url.URL` has no override: it implements no marshaler
interface, so it reflects as the struct object `encoding/json` actually emits.
Types implementing `encoding.TextMarshaler` map to `{"type":"string"}`.
Unsupported types (`func`, `chan`, `complex`, `unsafe.Pointer`) return
`ErrUnsupportedType`.

### Configuration options

| Option                           | Effect                                                                        |
| -------------------------------- | ----------------------------------------------------------------------------- |
| `WithDraft(Draft)`               | Target draft: `Draft2020` (default) or `Draft7`.                              |
| `WithTagInterpreter(t)`          | Register a `TagInterpreter`; multiple are applied in order.                   |
| `WithComments(bool)`             | Extract Go doc comments as `description` (requires source files).             |
| `WithTypeSchema(t, s)`           | Override the schema for a specific Go type (highest priority).                |
| `WithNamer(fn)`                  | Custom function for naming `$defs` entries.                                   |
| `WithDefinitions(bool)`          | Extract named types into `$defs`/`$ref` (default `true`).                     |
| `WithAdditionalProperties(bool)` | Allow extra object keys (default `false`, disallowing them).                  |
| `WithNullable(bool)`             | Make nil-able types (`*T`, `[]T`, `map`, `[]byte`) nullable (default `true`). |

### Customization interfaces

A type implementing `JSONSchemaProvider` supplies its own schema entirely,
bypassing reflection:

```go
type Status string

func (Status) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Enum: []any{"active", "inactive", "suspended"},
	}
}
```

A type implementing `JSONSchemaExtender` modifies its reflection-generated schema
after it is built:

```go
type Metadata struct {
	Tags map[string]string `json:"tags"`
}

func (Metadata) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Description = "Arbitrary key-value metadata"
	s.MinProperties = jsonschema.Ptr(1)
}
```

For each type, the schema is determined by the first matching step:

1. `WithTypeSchema` override (highest priority).
2. `JSONSchemaProvider`.
3. Built-in overrides (`[]byte`, `time.Time`, `encoding/json.Number`, ...).
4. Marshaler methods promoted from an embedded field: a promoted
   `encoding/json.Marshaler` makes the schema unrestricted (`{}`), and a
   promoted `encoding.TextMarshaler` makes it `{"type":"string"}` — the
   promoted method serializes the whole outer struct, so reflecting its fields
   would describe a shape that never appears.
5. `encoding.TextMarshaler` (direct implementation).
6. Kind-based reflection.

A direct `encoding/json.Marshaler` implementation is not consulted: it falls
through to kind-based reflection, since MarshalJSON can return any JSON type.
Use `WithTypeSchema` or `JSONSchemaProvider` to describe its real shape.

If a type implements both customization interfaces, only `JSONSchemaProvider` is
used. When `WithTypeSchema` or `JSONSchemaProvider` supplies the schema,
`JSONSchemaExtender` is not called.

### The `jsonschema` struct tag

The `jsonschema` tag sets schema properties directly on a field. A bare value
(no `=`) is treated as a description; otherwise keys are comma-separated
`key=value` pairs:

```go
type Config struct {
	Port    int    `json:"port"    jsonschema:"description=Server port,minimum=1,maximum=65535"`
	Pattern string `json:"pattern" jsonschema:"pattern=^[a-z]+$"`
	Mode    string `json:"mode"    jsonschema:"enum=debug|release|test"`
}
```

produces (abridged):

```json
{
  "port": {
    "type": "integer",
    "description": "Server port",
    "minimum": 1,
    "maximum": 65535
  },
  "pattern": { "type": "string", "pattern": "^[a-z]+$" },
  "mode": { "type": "string", "enum": ["debug", "release", "test"] }
}
```

Supported keys include `description`, `title`, `default`, `examples`,
`deprecated`, `readOnly`, `writeOnly`, `minimum`, `maximum`, `exclusiveMinimum`,
`exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `pattern`, `format`,
`minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`, `enum`,
and `const`. Values for `default`, `const`, `enum`, and `examples` are parsed
according to the field's Go type. `enum` and `examples` values are separated by
`|`; commas separate pairs, so a value containing a comma escapes it with a
backslash (`\,`, and `\\` for a literal backslash). For complex values, use
`JSONSchemaExtender` or doc comments with `WithComments`.

On a slice or array field, `enum` constrains each element rather than the
array value: the values parse against the element type and land on the item
schemas, so `Days []string` with `enum=monday|tuesday` produces
`{"items":{"type":"string","enum":["monday","tuesday"]}}`. Nested sequences
(`[][]T`) descend to the innermost element schema. `const`, `default`, and
`examples` remain whole-value constraints and are still errors on sequence
fields, as is `enum` on `[]byte` (which encodes as a base64 string with no
item schema).

### Struct field rules

Fields follow `encoding/json` conventions: the `json` tag sets the property
name, `json:"-"` excludes a field (`json:"-,"` uses the literal name `"-"`),
`omitempty` and `omitzero` drop the field from `required`, and `json:",string"`
forces a `{"type":"string"}` schema for applicable types. Embedded structs
without a `json` tag have their fields promoted; embedded types intercepted by
an earlier resolution step are composed via `allOf` (wrapped as
`anyOf[schema, {}]` for a pointer embed, since a nil pointer contributes
nothing to the marshaled object). A provider or
`WithTypeSchema` override used for such an embedded type must leave the object
open (no `additionalProperties: false`), since `allOf` evaluates each branch
against the whole object: a closed branch rejects the parent's sibling
properties and the generated schema then rejects the struct's own marshaled
JSON.

### Comment extraction

`WithComments(true)` extracts Go doc comments from source files for struct
types, fields, and named types using `go/ast` and
`golang.org/x/tools/go/packages`. The `jsonschema` tag's `description` wins over
an extracted comment. When source files cannot be located for a type, extraction
is silently skipped.

### Definitions and references

By default, named struct types (and named types implementing the customization
interfaces) are extracted into `$defs` (`definitions` for Draft-07) and
referenced via `$ref`; named primitives and anonymous structs are inlined.
Circular types are detected and resolved via `$ref` even when definitions are
disabled. Nullable references use `anyOf` wrapping:
`{"anyOf":[{"$ref":"..."},{"type":"null"}]}`. All `$defs` live at the root
level. `WithDefinitions(false)` inlines everything; the `WithNamer` option
overrides how definition keys are derived.

### Drafts

`Draft2020` (the default) and `Draft7` are supported. The draft affects the
`$schema` URI, keyword selection (`$defs` vs `definitions`), `$ref` sibling
handling, and `unevaluatedProperties` vs `additionalProperties` in `allOf`
compositions. In Draft-07, a `$ref`'d field with extra annotations is wrapped in
an `allOf`; in Draft 2020-12 sibling keywords sit directly alongside `$ref`.

## Tag interpreters

All struct-tag interpretation beyond the `json` and `jsonschema` tags goes
through the `TagInterpreter` interface:

```go
type TagInterpreter interface {
	TagKey() string
	Interpret(tag string, field FieldContext) error
}
```

Interpreters receive a `FieldContext` (the field's schema, parent schema, JSON
name, and Go type) and modify the schema in place. Multiple interpreters can be
registered and run in order, after the `jsonschema` tag.

### The `validate` interpreter

The `interpreters/validate` subpackage maps
[`go-playground/validator`](https://github.com/go-playground/validator) tag
syntax to schema constraints, without depending on the validator library itself:

```go
import "go.jacobcolvin.com/x/jsonschema/interpreters/validate"

type CreateUser struct {
	Name  string `json:"name"  validate:"required,min=1,max=100"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age"   validate:"gte=0,lte=150"`
}

schema, err := jsonschema.GenerateFor[CreateUser](
	jsonschema.WithTagInterpreter(validate.NewInterpreter()),
)
```

produces:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "name": { "type": "string", "minLength": 1, "maxLength": 100 },
    "email": { "type": "string", "minLength": 1, "format": "email" },
    "age": { "type": "integer", "minimum": 0, "maximum": 150 }
  },
  "required": ["name", "email", "age"],
  "additionalProperties": false
}
```

Supported tags (summary):

- **Presence:** `required`.
- **Bounds:** `min`, `max`, `len`, `gt`, `lt`, `gte`, `lte`, `eq`, `ne`, mapped
  to length/numeric keywords for strings and numbers, and to
  `minItems`/`maxItems` or `minProperties`/`maxProperties` for collections.
- **Enumerations:** `oneof` maps to `enum` for strings, numbers, and bools.
- **Collections:** `unique` -> `uniqueItems`; `dive` descends into element or
  value schemas.
- **Formats:** `email`, `url`, `uri`, `uuid`, `ipv4`, `ipv6`, `hostname` ->
  `format`.
- **Patterns:** `alpha`, `alphanum`, `numeric`, `number`, `ascii` -> `pattern`.
- **Content:** `json` -> `contentMediaType`; `base64` -> `contentEncoding`.

Cross-field, conditional, and control tags (`omitempty`, `structonly`, ...) are
silently skipped; only the first group before an `|` operator is interpreted;
unrecognized keys return an error.

## Validating instances

Three entry points are provided:

- `Validate(schema, instance, opts...)` validates a pre-parsed Go value
  (`map[string]any`, `[]any`, `string`, `float64`, `json.Number`, `bool`,
  `nil`). Go numeric kinds that `encoding/json` does not produce — the signed
  and unsigned integer types and `float32` — are accepted too and normalized
  via `Normalize`, so values decoded from YAML or TOML validate directly:
  integers convert to `json.Number` (exact at any magnitude) and `float32`
  widens to `float64`. `Normalize` is exported for callers that want to
  pre-normalize a value once and reuse it.
- `ValidateJSON(schema, data, opts...)` unmarshals raw JSON with a
  `json.Decoder` using `UseNumber()` (preserving the integer-vs-number
  distinction), then validates.
- `Compile(schema, opts...)` performs the per-schema work once (registry
  construction, `Schema.Resolve`, draft and vocabulary detection) and returns a
  reusable `*Validator` with `Validate` and `ValidateJSON` methods.

`Compile` (and therefore the one-shot helpers) rejects a `type` keyword that
names anything other than the seven JSON Schema types with `ErrInvalidType`,
so a typo'd type surfaces at construction instead of silently rejecting every
instance at runtime.

`Validate` and `ValidateJSON` compile a fresh validator on every call; to
validate many instances against the same schema, `Compile` once and reuse the
result. A `*Validator` is safe for concurrent use by multiple goroutines.

On success all return `nil`. A validation failure returns an error that unwraps
to `*ValidationError` via `errors.As`. Non-validation failures (JSON decoding,
an unaccepted instance type, `Schema.Resolve` errors, and
`ErrUnknownVocabulary`) return ordinary wrapped errors that do not unwrap to
`*ValidationError`.

### Numeric precision

Instance numbers are compared exactly (decoded with `UseNumber`, compared as
`big.Rat`), with one bound on the work an adversarial literal can demand: for a
JSON number whose exact value exceeds an internal cap (about 4096 significant
digits or decimal exponent magnitude), the `multipleOf` check is skipped, while
`minimum`/`maximum`/`exclusiveMinimum`/`exclusiveMaximum` are still enforced
exactly. Schema-side numeric keyword values are limited to `float64` precision:
integers beyond 2^53 in keywords like `const`, `minimum`, or `multipleOf` round
when the schema is decoded, even though the instance value they are compared
against is exact. A schema-side `float64` is interpreted at its shortest
decimal value across all numeric keywords, so `const: 0.1` matches the
instance `0.1` exactly, consistent with how `minimum: 0.1` bounds it.

### Structured errors

```go
type ValidationError struct {
	InstancePath string             // JSON Pointer into the instance, e.g. "/address/city"
	SchemaPath   string             // JSON Pointer into the schema
	Keyword      string             // failing keyword, e.g. "type", "minLength", "$ref"
	Message      string             // human-readable message
	Causes       []*ValidationError // child failures
}
```

All failures are collected; validation does not stop at the first error.
Compositional keywords (`allOf`, `anyOf`, `oneOf`, `if`/`then`/`else`, `$ref`,
`$dynamicRef`, `unevaluated*`) wrap their children in intermediate `Causes`
nodes, while container keywords (`properties`, `items`, `additionalProperties`)
flatten child failures into the parent's `Causes`, each retaining its full path.
`Unwrap()` flattens the attached errors across the whole tree for `errors.Is` /
`errors.As`. For example, validating `"hi"` against a `$ref` to a `minLength: 3`
schema yields a root error with `Keyword == "$ref"` whose `Causes[0].Keyword ==
"minLength"`.

A `false` subschema failure ("value is not allowed") carries the applicator
keyword that applied it — `additionalProperties` for
`additionalProperties: false`, and likewise `properties`,
`patternProperties`, `items`, `prefixItems`, and `additionalItems` — so the
common rejected-extra-property case is distinguishable without inspecting
`SchemaPath`. A standalone boolean `false` schema has no applicator context
and leaves `Keyword` empty.

### Validation options

| Option                          | Effect                                                                     |
| ------------------------------- | -------------------------------------------------------------------------- |
| `WithRefResolver(r)`            | Resolve remote/absolute `$ref` URIs (called only when local lookup fails). |
| `WithFormatValidator(name, fn)` | Register a custom `format` checker (`func(string) error`).                 |
| `WithFormats(bool)`             | Force `format` assertion on or off.                                        |
| `WithContent(bool)`             | Assert `contentEncoding`/`contentMediaType` (annotation-only by default).  |
| `WithResolveOptions(opts)`      | Pass upstream `ResolveOptions` to `Schema.Resolve`.                        |
| `WithVocabularies(map)`         | Directly set active vocabularies (highest precedence).                     |
| `WithMetaSchema(ms)`            | Register a metaschema whose `$vocabulary` gates keyword groups.            |

### Formats

The active draft and vocabulary decide whether `format` is asserted: under
Draft-07 it is asserted, under Draft 2020-12 it is annotation-only unless the
format-assertion vocabulary is active. `WithFormats(true)` forces assertion.
Built-in checkers cover `date-time`, `date`, `time`, `duration`, `email`,
`idn-email`, `hostname`, `idn-hostname`, `uri`, `uri-reference`, `uri-template`,
`iri`, `iri-reference`, `uuid`, `ipv4`, `ipv6`, `json-pointer`,
`relative-json-pointer`, and `regex`. Register additional formats with
`WithFormatValidator`.

### Vocabularies

Draft 2020-12 `$vocabulary` gates which keyword groups run: inactive
vocabularies have their keywords silently skipped. Vocabulary resolution
priority is `WithVocabularies` (direct override) > `WithMetaSchema` (matched
against the root schema's `$schema`) > a built-in default set (every group
active except format-assertion). A schema that requires (`true`) a vocabulary
this implementation does not recognize, or marks the 2020-12 core vocabulary
optional, fails with `ErrUnknownVocabulary`. Draft-07 has no `$vocabulary`, so
all groups stay active.

### Remote references

Only local fragment refs (`#/$defs/...`, `#/definitions/...`) are resolved by
default. Remote and absolute `$ref` URIs are resolved through an optional
`RefResolver` set with `WithRefResolver`; the resolver is called only when local
resolution fails, and resolved schemas are cached within the validation run. A
resolver error surfaces as `ErrRefResolve`; an unresolvable remote/absolute ref
with no resolver is reported as a `*ValidationError`. Circular refs are detected
and treated as passing.

## Errors

| Error                  | Trigger                                                                                     |
| ---------------------- | ------------------------------------------------------------------------------------------- |
| `ErrUnsupportedType`   | A Go type with no JSON Schema representation (`func`, `chan`, `complex`, `unsafe.Pointer`). |
| `ErrUnsupportedMapKey` | A map key that is not a string, integer type, or `encoding.TextMarshaler`.                  |
| `ErrUnknownVocabulary` | A required `$vocabulary` URI is unrecognized (or 2020-12 core is marked optional).          |
| `ErrRefResolve`        | A `RefResolver` returns an error resolving a remote `$ref`.                                 |
| `ErrProviderPanic`     | A `JSONSchemaProvider`/`JSONSchemaExtender` method panics (recovered and wrapped).          |

## CLI: `jsonschemagen`

The module ships a build-time code-generation CLI under `cmd/jsonschemagen`,
intended for `//go:generate`. It writes a JSON Schema file for a named Go type
by generating a temporary program that imports the target package and calls
`Generate`, reusing the library's generation pipeline:

```go
//go:generate go run go.jacobcolvin.com/x/jsonschema/cmd/jsonschemagen -type Config -o config.schema.json
```

| Flag                     | Default    | Description                              |
| ------------------------ | ---------- | ---------------------------------------- |
| `-type`                  | (required) | Go type name to generate a schema for.   |
| `-o`                     | stdout     | Output file path.                        |
| `-draft`                 | `2020`     | JSON Schema draft: `7` or `2020`.        |
| `-comments`              | `false`    | Extract Go doc comments as descriptions. |
| `-additional-properties` | `false`    | Allow additional properties.             |
| `-indent`                | `"  "`     | JSON indentation string.                 |
| `-validate`              | `false`    | Enable the `validate` tag interpreter.   |

For example, given a `User` type with `validate` tags:

```sh
go run go.jacobcolvin.com/x/jsonschema/cmd/jsonschemagen -type User -validate
```

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "name": { "type": "string", "minLength": 1, "maxLength": 50 },
    "email": { "type": "string", "minLength": 1, "format": "email" }
  },
  "required": ["name", "email"],
  "additionalProperties": false
}
```

The `-validate` flag enables the `validate` interpreter in the generated
program; it does not validate instances or the emitted schema. This is
forward-direction generation only; schema-to-code generation is a non-goal.

## Design notes

### Relationship to `google/jsonschema-go`

This package re-exports the upstream `Schema` type so users need only import
this package, and reuses the upstream for two things: meta-schema validation /
structural well-formedness (via `Schema.Resolve`, called once per `Compile` with
its result discarded) and JSON-semantic value comparison (`const`, `enum`,
`uniqueItems`, via `Equal`). Everything else is implemented here.

| Concern                                                       | Implementation                               |
| ------------------------------------------------------------- | -------------------------------------------- |
| Schema data model (`Schema` struct)                           | Upstream (re-exported via type alias)        |
| Meta-schema validation, structural well-formedness            | Upstream `Schema.Resolve` (result discarded) |
| `$ref`/`$dynamicRef`/`$anchor` resolution (incl. remote refs) | This package (own URI/anchor registries)     |
| Instance validation walk                                      | This package                                 |
| Error types and path tracking                                 | This package                                 |
| Format validation                                             | This package (pluggable)                     |
| JSON-semantic value comparison (`const`/`enum`/`uniqueItems`) | Upstream `Equal()`                           |

The package implements its own validation walk because the upstream
`Resolved.Validate` returns on the first error within container keywords and
`allOf`, does not track instance paths, returns unstructured string errors, and
does not validate `format`. Because the upstream's resolved reference graph is
unexported, this package resolves references itself: JSON Pointer traversal for
local fragments, URI/anchor registries built from `$id`/`$anchor`, a
dynamic-scope stack for `$dynamicRef`, and the optional `RefResolver` for remote
refs.

### Selected decisions

- **Own reflection pipeline**, because the upstream's inference is too opaque to
  extend with interfaces, tag interpreters, `$defs`, and cycle detection.
- **Circular types via `$ref` to `$defs`**, where the upstream errors on cycles.
- **`anyOf` for nullable `$ref`**: conventional, and avoids `oneOf` overhead.
- **`additionalProperties: false` by default**: a Go struct defines exactly what
  is allowed; opt in to permissive schemas with `WithAdditionalProperties`.
- **Nullable maps and slices**: both emit null-typed schemas by default,
  matching `encoding/json` nil behavior; `WithNullable(false)` drops the null
  branch for callers whose absent values are never serialized as `null`.
- **Hierarchical `ValidationError`**: a tree mirrors the schema/instance
  structure so callers can inspect failures at any depth or flatten them.
- **Pluggable format validation**: formats are checked by registered
  `func(string) error` functions, matching the spec's recommendation that format
  validation be optional and configurable.
- **`unevaluatedProperties`/`unevaluatedItems`** are supported, with annotation
  tracking reimplemented in the walk (the generator emits them for Draft 2020-12
  `allOf` composition).
- **Go RE2 for patterns**: `pattern` and `patternProperties` use Go's `regexp`,
  not ECMA 262; this matches the upstream and is a known deviation from the
  spec.
- **`ValidateJSON` uses `UseNumber`** to preserve the integer-vs-number
  distinction that default `float64` unmarshaling would lose.

### Non-goals

- Meta-schema validation and structural well-formedness checking are delegated
  to the upstream `Schema.Resolve`.
- Code generation _from_ schemas (the reverse direction) is out of scope.
  Forward-direction generation, including the `jsonschemagen` CLI, is supported.

## License

Apache 2.0. See [LICENSE](https://github.com/macropower/x/blob/main/LICENSE).
