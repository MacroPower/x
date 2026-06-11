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
this package. `Raw` and `MustRaw` marshal Go values for the raw-JSON fields
such as `Schema.Default` (e.g. `jsonschema.MustRaw("15m")` instead of a
hand-written `json.RawMessage` literal); `MustRaw` panics on a marshal error
and suits values known valid at compile time.

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
- `$vocabulary` gating and pluggable, opt-in, context-aware remote `$ref`
  resolution.
- Schema traversal (`Subschemas`, `Walk`) and shape predicates
  (`CheckTypeNames`, `IsTrueSchema`, `IsFalseSchema`) for working with
  `Schema` values directly.
- `$ref` inlining (`Inline`) that flattens a schema and the documents it
  references into one self-contained document.
- A build-time code-generation CLI (`jsonschemagen`) for `//go:generate`.

## Generating schemas

The primary entry point is the generic `GenerateFor`. A `reflect.Type` variant,
`Generate`, is provided for dynamic use, and `MustGenerateFor` panics on error
for package-scope variables, where for a static type and fixed options
generation either always succeeds or always fails:

```go
schema, err := jsonschema.GenerateFor[MyType](opts...)
schema, err := jsonschema.Generate(reflect.TypeFor[MyType](), opts...)

var mySchema = jsonschema.MustGenerateFor[MyType](opts...)
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
| `WithDefaultsFrom(instance)`     | Seed root property defaults from an instance of the generated type.           |
| `WithRootTitle(bool)`            | Title the root schema with the root type's name (default `false`).            |

`WithDefaultsFrom` marshals the instance with `encoding/json` after generation;
each top-level key of the output that matches a root property becomes that
property's `default`, overwriting any default set via struct tags. Keys the
`json` tags omit (`omitempty`, `omitzero`) contribute nothing, so presence
follows the tags exactly, and nested struct, slice, and map values become
whole-value defaults on their top-level property. An instance whose
pointer-dereferenced type is not the generated type, or that does not marshal
to a JSON object, returns an error wrapping `ErrInvalidDefaultsInstance`. A
pointer root's nullable `anyOf` wrapper is resolved to its value branch first,
so the defaults reach the object schema (or its `$defs` entry) inside. When a
self-referential root stays in `$defs`, the defaults apply to that definition,
shared by every recursive occurrence. Under `Draft7`, a default landing on a
`$ref`'d property moves the `$ref` into an `allOf` wrap — the same shape tag
defaults produce — because Draft-07 readers ignore `$ref` siblings:

```go
schema, err := jsonschema.GenerateFor[Config](
	jsonschema.WithDefaultsFrom(Config{Host: "localhost", Port: 8080}),
)
// properties.host.default == "localhost", properties.port.default == 8080
```

`WithRootTitle(true)` sets the root schema's `title` to the generated root
type's name when nothing else (a `WithTypeSchema` override, a
`JSONSchemaProvider`, or a `JSONSchemaExtender`) supplied one. The `WithNamer`
namer is honored, so root and `$defs` naming stay consistent, and the root type
is pointer-dereferenced first. Unnamed roots (anonymous structs, unnamed maps
and slices) stay untitled. Under `Draft7`, a self-referential root stays a bare
`$ref` into `definitions`, where a sibling title would be ignored; the title is
set on the definitions entry instead, shared by every occurrence of the type.
This gives consumers of `WithDefinitions(false)` output — where the inlined
root carries no `$id` or `$defs` key — a name without re-deriving it from the
Go type themselves.

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

Supported keys include `type`, `description`, `title`, `default`, `examples`,
`deprecated`, `readOnly`, `writeOnly`, `minimum`, `maximum`, `exclusiveMinimum`,
`exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `pattern`, `format`,
`minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`, `enum`,
and `const`. Values for `default`, `const`, `enum`, and `examples` are parsed
according to the field's Go type. `enum` and `examples` values are separated by
`|`; commas separate pairs, so a value containing a comma escapes it with a
backslash (`\,`, and `\\` for a literal backslash). For complex values, use
`JSONSchemaExtender` or doc comments with `WithComments`.

`type=` overrides the reflected type entirely, for a Go type whose JSON
representation differs from its reflection: it must name one of the seven
JSON Schema types, and it removes the nullable `anyOf` wrapper a pointer
field generates plus — when the new type is not numeric — the numeric bounds
derived from the Go kind. So a `*time.Duration` field (reflected as a
nullable integer) with `jsonschema:"type=string,pattern=..."` produces a
clean `{"type":"string","pattern":"..."}` without needing
`JSONSchemaExtend`. Tag pairs apply in order; keys after `type=` still take
effect.

Because pairs apply in order, `default`, `const`, `enum`, and `examples`
values appearing after a `type=` pair parse against the overridden JSON type
rather than the field's Go type: `string`, `integer`, `number`, and `boolean`
overrides parse subsequent scalar values as that type, so a `time.Duration`
field with `jsonschema:"type=string,default=15m"` yields
`{"type":"string","default":"15m"}` where the Go int64 kind would have
rejected `15m`. The same keys before the `type=` pair still parse against the
Go type. After an override to `array`, `object`, or `null` there is no scalar
type to parse against, so those keys are an error, and the literal value
`null` is rejected after any override (the overridden type is never
nullable). An `enum` after a `type=` override always constrains the value
schema itself, even on a slice or array field: the redirection to the item
schemas (next paragraph) keys on the scalar-parse type, which an override
replaces.

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

The core entry points are:

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

Schemas arriving as JSON documents rather than `*Schema` values have
symmetric entry points:

- `CompileJSON(data, opts...)` decodes `data` as a single JSON schema document
  (numbers as `json.Number`, trailing data rejected) and compiles it with
  `Compile`. It is the schema-side counterpart of `ValidateJSON`.
- `SchemaFromJSON(data)` is the decode half of `CompileJSON` alone: it returns
  the `*Schema` uncompiled, for consumers that work with the schema itself —
  `Inline`, `Walk`, programmatic editing — rather than validating instances
  against it.
- `SchemaFromValue(doc)` converts an already-decoded document — a `bool`
  (`true` is the empty schema, `false` the schema that rejects every instance)
  or a `map[string]any`, such as `Normalize` output with `json.Number` leaves —
  to a `*Schema`.

With all three, a top-level value that is not an object or boolean returns an
error wrapping `ErrInvalidSchemaDocument`. That includes JSON `null`, which
unmarshaling into a `Schema` directly would silently coerce to the `false`
schema. Malformed JSON returns the wrapped decode error without the sentinel.

Every compile and validate entry point has a `Context` variant —
`CompileContext`, `CompileJSONContext`, `ValidateContext`,
`ValidateJSONContext`, and the `Validator` methods of the same names — that
carries a caller-supplied context to a `RefResolverContext` resolver (see
[Remote references](#remote-references)); the context-less forms pass
`context.Background()`.

`Compile` (and therefore the one-shot helpers) rejects a `type` keyword that
names anything other than the seven JSON Schema types with `ErrInvalidType`,
so a typo'd type surfaces at construction instead of silently rejecting every
instance at runtime. The same check is exported standalone as
`CheckTypeNames` (see [Schema traversal and predicates](#schema-traversal-and-predicates));
`Compile` routes through it, so the two produce textually identical errors.

`Validate` and `ValidateJSON` compile a fresh validator on every call; to
validate many instances against the same schema, `Compile` once and reuse the
result. A `*Validator` is safe for concurrent use by multiple goroutines.

On success all return `nil`. A validation failure returns an error that unwraps
to `*ValidationError` via `errors.As`. Non-validation failures (JSON decoding,
an unaccepted instance type, `Schema.Resolve` errors, `ErrInvalidType`, and
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

Two helpers support reporting against the source document. `Leaves()` flattens
the wrapper nodes and returns one entry per distinct concrete failure (a
`propertyNames` error counts as a leaf, naming the offending key).
`TargetsKey()` reports whether the failing keyword constrains a key, name, or
collection structure (`required`, `additionalProperties`, `propertyNames`,
`minItems`, `minProperties`, ...) rather than a value, so a source-mapping
consumer can highlight the key instead of the value.

`InstanceSegments()` returns the `InstancePath` location in typed form: one
`Segment` per reference token, outermost first, each marked as an object key
or an array index. The JSON Pointer string cannot distinguish array index `1`
from an object property named `"1"` (YAML decoders in particular produce
string map keys that look numeric), and its keys are RFC 6901-escaped; the
segments carry the unescaped key and an explicit index/key distinction, so
consumers need not re-parse the pointer and guess with `strconv.Atoi`.
Segments are populated on every error produced by validation; hand-constructed
errors return `nil`.

```go
type Segment struct {
	Key     string // object property name, when IsIndex is false
	Index   int    // array index, when IsIndex is true
	IsIndex bool   // array element rather than object property
}
```

A `false` subschema failure ("value is not allowed") carries the applicator
keyword that applied it — `additionalProperties` for
`additionalProperties: false`, and likewise `properties`,
`patternProperties`, `items`, `prefixItems`, and `additionalItems` — so the
common rejected-extra-property case is distinguishable without inspecting
`SchemaPath`. A standalone boolean `false` schema has no applicator context
and leaves `Keyword` empty.

A `propertyNames` violation constrains a key, which has no JSON Pointer of
its own (RFC 6901), so it borrows the property's location: the surfaced
error carries `Keyword == "propertyNames"` and an `InstancePath` pointing at
the offending property (e.g. `/settings/BadKey`), with the inner keyword
failure (`pattern`, `maxLength`, ...) in `Causes`. The failing key and its
containing object are both identifiable from `InstancePath` alone.

### Validation options

| Option                          | Effect                                                                                                                                |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `WithRefResolver(r)`            | Resolve remote/absolute `$ref` URIs (called only when local lookup fails); a `RefResolverContext` also receives the caller's context. |
| `WithFormatValidator(name, fn)` | Register a custom `format` checker (`func(string) error`).                                                                            |
| `WithFormats(bool)`             | Force `format` assertion on or off.                                                                                                   |
| `WithContent(bool)`             | Assert `contentEncoding`/`contentMediaType` (annotation-only by default).                                                             |
| `WithResolveOptions(opts)`      | Pass `ResolveOptions` (aliased from the upstream package) to `Schema.Resolve`.                                                        |
| `WithVocabularies(map)`         | Directly set active vocabularies (highest precedence).                                                                                |
| `WithMetaSchema(ms)`            | Register a metaschema whose `$vocabulary` gates keyword groups.                                                                       |

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

A resolver that also implements `RefResolverContext` receives a context with
every resolution call:

```go
type RefResolverContext interface {
	RefResolver
	ResolveRefContext(ctx context.Context, uri string) (*Schema, error)
}
```

Refs resolved while compiling get the `CompileContext` context; refs reached
during a validation run get that run's `ValidateContext` (or other `Context`
entry point) context, so a resolver that fetches over the network can honor
cancellation and deadlines. A compiled `*Validator` never retains a context —
each run carries its own — and the context-less entry points pass
`context.Background()`. The package ships no network resolver; fetching
remains the caller's concern.

## Schema traversal and predicates

Helpers are provided for working with `Schema` values directly, independent of
generation and validation:

```go
// Subschemas returns the direct sub-schemas of s: every non-nil schema held
// by one sub-schema-bearing keyword, with map children in sorted-key order.
children := jsonschema.Subschemas(s)

// SubschemaRefs is the keyword-labeled form: the same children in the same
// order, each paired with the JSON Pointer addressing it from s
// ("/properties/a", "/allOf/0", "/items"), for path-tracking traversals.
for _, ref := range jsonschema.SubschemaRefs(s) {
	fmt.Println(ref.Pointer, ref.Schema.Type)
}

// Walk visits s and every schema transitively reachable through Subschemas.
err := jsonschema.Walk(s, func(s *jsonschema.Schema) error {
	s.Description = "" // strip annotations, rewrite $refs, collect types, ...

	return nil
})
```

`Subschemas` is the package's single source of truth for which `Schema` fields
hold sub-schemas: the applicators (`items`, `prefixItems`,
`additionalItems`, `properties`, `patternProperties`, `additionalProperties`,
`propertyNames`, `allOf`, `anyOf`, `oneOf`, `not`, `if`/`then`/`else`,
`dependentSchemas` and legacy `dependencies`, `contains`, `unevaluated*`,
`contentSchema`) plus the reserved `$defs` and `definitions` locations. Only
typed `Schema` fields are included, not sub-schemas carried as raw JSON in
unknown keywords. Children held in maps are returned in sorted-key order so
traversal is deterministic, and a maintenance test fails when an upstream
`Schema` field addition is not covered. `Subschemas` delegates to
`SubschemaRefs`, so the labeled and unlabeled forms can never disagree on
field coverage or order; appending each visited child's `Pointer` while
descending yields the schema path the package's own errors report.

`Walk` is pre-order: the function runs on a schema before that schema's
children are gathered, so it may replace or mutate sub-schema fields and the
walk follows the updated children. Each distinct schema pointer is visited
once, so aliased or cyclic graphs terminate. `Walk` stops at and returns the
first error from the function; a `nil` schema is a no-op. Returning the
`SkipChildren` sentinel (the `io/fs.SkipDir` convention) prunes the walk at
the current schema — its sub-schemas are not visited — and continues with its
siblings, which suits rewriting passes that splice in a subtree the walk
should not descend into.

Three predicates answer common shape questions:

- `CheckTypeNames(schema)` verifies that every `type` keyword reachable from
  the schema names one of the seven JSON Schema type names, returning `nil` or
  an error wrapping `ErrInvalidType` that includes the schema path of the
  first offending keyword. It is the standalone form of the check `Compile`
  runs before resolution, for vetting structurally messy schemas — cyclic
  graphs, unresolvable references — without compiling them.
- `IsTrueSchema(s)` reports whether `s` is the boolean `true` schema form: a
  schema with no fields set, which marshals to JSON `true` and accepts every
  instance. Annotation-only schemas (a description but no constraints) return
  `false`, as do schemas whose only field is a non-nil empty map or slice
  (`Schema{Enum: []any{}}` vacuously rejects every instance). Returns `false`
  for `nil`.
- `IsFalseSchema(s)` reports whether `s` is the boolean `false` schema form
  `{"not": {}}` — the shape the upstream produces when unmarshaling the JSON
  boolean `false` — which marshals to JSON `false` and rejects every instance.
  Any sibling field next to the `not`, including annotations, defeats the
  form. Returns `false` for `nil`.

## Inlining references

`Inline` returns a deep copy of a schema in which every `$ref` — in the
schema body, `$defs`, and `definitions` alike — is replaced by a copy of the
schema it targets, producing one self-contained document for consumers that
cannot follow references, such as code generators. The input and any
resolver-returned schemas are never mutated.

```go
fsys := os.DirFS("schemas") // main.json references sub/child.json, ...

inlined, err := jsonschema.Inline(schema,
	jsonschema.WithInlineResolver(jsonschema.NewFileResolver(fsys)),
	jsonschema.WithInlineBaseURI("main.json"),
)
```

Resolution mirrors the validator's. Fragment-only refs (`#/pointer`,
`#anchor`) resolve within the enclosing document using the same
`$id`/`$anchor` registry the validator builds, and every ref resolves
against its document's original structure, exactly as the validator would:
expanding one ref never changes what a later ref's JSON Pointer or anchor
addresses. Other refs are absolutized against the enclosing resource's base
URI — its `$id`, or the base from `WithInlineBaseURI`, with a schemeless
base such as `main.json` normalized against `file:///` so RFC 3986 joining
is well-defined and a back-reference to the root document finds the
in-memory copy instead of re-fetching it — and fetched through the
`RefResolver` given via `WithInlineResolver`; any fragment is then evaluated
against the fetched document. Fetched documents are inlined recursively
using their own base URIs, so a relative ref inside a fetched document
resolves against that document's URI and files can reference each other by
relative path; each document is fetched at most once per call.
`FileResolver` (constructed with `NewFileResolver`) adapts an `fs.FS`,
serving file-path and relative URIs from
the fs root (a leading `file://` scheme and `/` are stripped); each
referenced file must contain a JSON schema document, and `io/fs` confines
resolution to the fs root, so a ref escaping above it returns an error
wrapping `ErrRefResolve`. The same resolver also serves file-path and
relative refs during validation via `WithRefResolver`; refs that absolutize
to another scheme (an http `$id`, for example) are not valid fs paths and
resolve to an error. `InlineContext` is `Inline` with a caller-supplied
context, passed to a resolver that also implements `RefResolverContext` with
every document fetch, so a resolver that fetches over the network can honor
cancellation and deadlines; `Inline` passes `context.Background()`, and a
plain `RefResolver` is called without one.

`WithInlineRetrievalBase` makes refs resolve against each document's
retrieval URI instead, treating `$id` as an inert annotation: `$id` neither
establishes a base URI nor registers a resolution target, in any document,
including the Draft 7 fragment-only `$id` form that otherwise acts as an
anchor. `$anchor` and `$dynamicAnchor` still resolve within their document,
and `$id` keywords pass through to the output verbatim. Real-world schemas
commonly declare a published remote `$id` while shipping the files their
refs name alongside the schema; under the default RFC behavior those refs
absolutize against the remote `$id` and cannot be served from disk. With
this option the root document's refs absolutize against the base from
`WithInlineBaseURI` and each fetched document's refs against the URI it was
fetched from.

Sibling keywords beside `$ref` follow draft semantics, with the draft
detected from the root schema's `$schema` exactly as the validator detects
it (fetched documents follow the root document's draft, matching how
validation applies one draft throughout):

- **Draft 2020-12**: the node keeps its sibling keywords and the target copy
  joins the node's `allOf`. This preserves both the conjunction and the
  annotation flow the `unevaluated*` keywords depend on, which moving the
  siblings into a separate `allOf` branch would break.
- **Draft 7**: siblings of `$ref` are ignored, so the node is replaced by
  the target copy alone.
- A node whose only keyword is `$ref` is replaced by the target copy alone
  under either draft.

A spliced copy never carries a `$schema` keyword, and the returned root
keeps the input's `$schema`. Refs are inlined only in typed sub-schema
positions (those `Subschemas` covers); a `$ref` carried as raw JSON inside
an unknown keyword is left as-is, although a ref pointing into such a
position still resolves.

Failure modes:

- A ref whose expansion reaches its own target — a recursive schema — returns
  an error wrapping `ErrRefCycle`: a cyclic reference graph has no finite
  expansion.
- A `$dynamicRef` under Draft 2020-12 returns an error wrapping
  `ErrRefInline`, since its target depends on the dynamic scope at validation
  time and no single replacement preserves that (Draft 7 ignores the keyword,
  as the validator does).
- A non-local ref with no resolver configured, or any ref whose target cannot
  be found, returns an error wrapping `ErrRefResolve`.

`WithInlineRefFallback` sets a per-reference failure policy consulted when
expanding a reference fails for any of those reasons, with the JSON Pointer
path of the referencing schema within its containing document, the
reference value, and the error. The fallback declines (propagating the
original error and ending the `Inline` call), drops the failing
reference keyword while keeping the node's remaining keywords (a nil
schema), or supplies a substitute schema the reference expands to as if it
had resolved there, with the usual draft sibling semantics. The fallback is
consulted once per failure, at the reference that directly failed: a
failure inside a nested expansion consults the innermost failing ref with
its path in its containing document, and a declined consultation propagates
outward without re-consulting at the enclosing refs. A substitute is
deep-copied before splicing and is itself inlined recursively, its refs
resolving in the context of the document containing the failing ref; a
cycle introduced by the substitute is an ordinary `ErrRefCycle`.

### Inlining options

| Option                          | Effect                                                                                                                                     |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `WithInlineResolver(r)`         | Set the `RefResolver` that fetches the documents non-local refs target (called at most once per distinct URI).                             |
| `WithInlineBaseURI(base)`       | Set the root document's base URI; a schemeless base is normalized against `file:///`.                                                      |
| `WithInlineRetrievalBase(bool)` | Resolve refs against each document's retrieval URI, treating `$id` as an inert annotation that passes through verbatim.                    |
| `WithInlineRefFallback(fn)`     | Per-reference failure policy: decline (propagate), drop the failing reference keyword (nil schema), or expand a substitute schema instead. |

## Errors

| Error                        | Trigger                                                                                                                                       |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `ErrUnsupportedType`         | A Go type with no JSON Schema representation (`func`, `chan`, `complex`, `unsafe.Pointer`).                                                   |
| `ErrUnsupportedMapKey`       | A map key that is not a string, integer type, or `encoding.TextMarshaler`.                                                                    |
| `ErrInvalidType`             | A `type` keyword naming something other than the seven JSON Schema type names (returned by `CheckTypeNames` and `Compile`).                   |
| `ErrInvalidSchemaDocument`   | A schema document whose top-level value is not a JSON object or boolean (returned by `CompileJSON`, `SchemaFromJSON`, and `SchemaFromValue`). |
| `ErrUnknownVocabulary`       | A required `$vocabulary` URI is unrecognized (or 2020-12 core is marked optional).                                                            |
| `ErrRefResolve`              | A `RefResolver` returns an error resolving a remote `$ref`; in `Inline`, also a non-local ref with no resolver or any unresolvable target.    |
| `ErrRefCycle`                | `Inline` expands a `$ref` that reaches its own target: the reference graph is cyclic and has no finite expansion.                             |
| `ErrRefInline`               | `Inline` encounters a reference with no faithful static expansion (`$dynamicRef` under Draft 2020-12).                                        |
| `ErrProviderPanic`           | A `JSONSchemaProvider`/`JSONSchemaExtender` method panics (recovered and wrapped).                                                            |
| `ErrInvalidDefaultsInstance` | The `WithDefaultsFrom` instance does not match the generated root type or does not marshal to a JSON object.                                  |

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
