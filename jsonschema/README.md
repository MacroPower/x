<p align="center">
  <h1 align="center">jsonschema</h1>
</p>

<p align="center">
  <a href="https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema"><img alt="Go Reference" src="https://pkg.go.dev/badge/go.jacobcolvin.com/x/jsonschema.svg"></a>
  <a href="https://goreportcard.com/report/go.jacobcolvin.com/x/jsonschema"><img alt="Go Report Card" src="https://goreportcard.com/badge/go.jacobcolvin.com/x/jsonschema"></a>
  <a href="https://github.com/macropower/x/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/macropower/x"></a>
</p>

<p align="center">Generate JSON Schema from Go types and validate JSON instances, with structured errors.</p>

`jsonschema` generates JSON Schema documents from Go types by reflection and
validates JSON instances against schemas. It builds on
[`github.com/google/jsonschema-go`](https://github.com/google/jsonschema-go) and
adds customization interfaces, pluggable struct-tag interpretation, Go doc
comment extraction, Draft-07 and Draft 2020-12 output, and instance validation
that reports every failure with its instance and schema path.

This README is a tour. The
[package documentation](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema)
holds the full contract for every option, error, and edge case.

## Installation

```sh
go get go.jacobcolvin.com/x/jsonschema
```

## Quick Start

### Schema Generation

```go
type SimpleStruct struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age,omitzero"`
}

schema, err := jsonschema.GenerateFor[SimpleStruct](ctx)
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

### Instance Validation

```go
schema := &jsonschema.Schema{
	Type:     "object",
	Required: []string{"name"},
	Properties: map[string]*jsonschema.Schema{
		"name": {Type: "string", MinLength: new(1)},
		"age":  {Type: "integer", Minimum: new(0.0)},
	},
}

ctx := context.Background()

// Compile once and reuse it. The returned *Validator is safe for concurrent use.
v, err := jsonschema.Compile(ctx, schema)
if err != nil {
	log.Fatal(err)
}

if err := v.ValidateJSON(ctx, []byte(`{"name":"Ada","age":36}`)); err != nil {
	log.Fatal(err) // valid, so not reached
}

// Validation failures unwrap to *ValidationError and carry full paths.
err = v.ValidateJSON(ctx, []byte(`{"name":"","age":-1}`))

if ve, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
	// ve is the root of an error tree. Every failure keeps its instance path.
	for _, cause := range ve.Causes {
		fmt.Printf("%s at %s: %s\n", cause.Keyword, cause.InstancePath, cause.Message)
	}
	// minLength at /name: ...
	// minimum  at /age:  ...
}
```

## Features

- Schema generation from any Go type, struct or otherwise, with zero
  configuration.
- Customization interfaces for types to control their own schema.
- Pluggable struct-tag interpreters, including a ready-made `validate`-tag
  interpreter.
- Go doc comment extraction into `description` fields.
- Draft-07 and Draft 2020-12 output and validation.
- Instance validation that collects every failure into a tree with instance
  and schema paths.
- `$vocabulary` gating and pluggable, opt-in remote `$ref` resolution.
- Schema traversal helpers and `$ref` inlining.
- A build-time code-generation CLI (`gen`) for `//go:generate`.

## Generation

`GenerateFor` is the primary entry point. `Generate` takes a `reflect.Type`
for dynamic use, and `NewGenerator` applies one option set once for generating
many types:

```go
schema, err := jsonschema.GenerateFor[MyType](ctx, opts...)
schema, err := jsonschema.Generate(ctx, reflect.TypeFor[MyType](), opts...)

gen := jsonschema.NewGenerator(opts...)
schema, err := jsonschema.GenerateWith[MyType](ctx, gen)
```

Whether a Go type generates is `encoding/json/v2`'s verdict on a value of that
type, so a type that marshals generates and a type v2 refuses does not. See
[Options](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Options) for
how to configure a run.

### Type Mapping

| Go type                              | JSON Schema                                                               |
| ------------------------------------ | ------------------------------------------------------------------------- |
| `string`, `bool`, `float64`          | `string`, `boolean`, `number`                                             |
| `int`                                | `integer`                                                                 |
| `int8`...`int64`, `uint8`...`uint64` | `integer` with `minimum`/`maximum` bounds                                 |
| `uint`, `uintptr`                    | `integer` with `minimum: 0`                                               |
| `*T`                                 | `anyOf` of the base schema and `{"type":"null"}`                          |
| `[]T`                                | `array` with an `items` schema                                            |
| `[]byte`                             | base64-encoded `string`                                                   |
| `[N]T`                               | fixed-size array via `prefixItems` with `minItems`/`maxItems` = N         |
| `map[K]V`                            | `object` with `additionalProperties`                                      |
| `any` / interface                    | unrestricted (`{}`)                                                       |
| `struct`                             | `object` with `properties`, `required`, and `additionalProperties: false` |
| `time.Time`                          | `string` with `format: date-time`                                         |
| `encoding.TextMarshaler`             | `string`                                                                  |

[Type Mapping](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Type_Mapping)
and
[Type Resolution](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Type_Resolution)
give the full rules, the other built-in overrides, and the order in which the
rules apply.

### Customization

A type implementing `JSONSchemaProvider` supplies its own schema, and a type
implementing `JSONSchemaExtender` adjusts its reflection-generated schema:

```go
type Status string

func (Status) JSONSchema(context.Context, jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
	return jsonschema.TypeSchema{
		Value: &jsonschema.Schema{
			Type: "string",
			Enum: []any{"active", "inactive", "suspended"},
		},
	}, nil
}

type Metadata struct {
	Tags map[string]string `json:"tags"`
}

func (Metadata) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Description = "Arbitrary key-value metadata"
	return nil
}
```

For types you do not own, `WithTypeSchema` overrides one type,
`WithTypeSchemaProvider` answers for whole families of types by predicate, and
`WithTypeSchemaExtender` adjusts reflection-generated schemas:

```go
// Every type implementing fmt.Stringer serializes as a string.
stringers := jsonschema.TypeSchemaProviderFunc(
	func(_ context.Context, tc jsonschema.TypeContext) (jsonschema.TypeSchema, error) {
		if !tc.Type.Implements(reflect.TypeFor[fmt.Stringer]()) {
			return jsonschema.TypeSchema{}, jsonschema.ErrTypeNotHandled
		}
		return jsonschema.TypeSchema{Value: &jsonschema.Schema{Type: "string"}}, nil
	},
)

schema, err := jsonschema.GenerateFor[Config](ctx, jsonschema.WithTypeSchemaProvider(stringers))
```

See
[Customization](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Customization).

### Struct Tag

The `jsonschema` tag sets schema keywords directly on a field. A bare value is
a description. Otherwise the tag is a comma-separated list of `key=value`
pairs, with `|` separating `enum` and `examples` values:

```go
type Config struct {
	Port    int    `json:"port"    jsonschema:"description=Server port,minimum=1,maximum=65535"`
	Pattern string `json:"pattern" jsonschema:"pattern=^[a-z]+$"`
	Mode    string `json:"mode"    jsonschema:"enum=debug|release|test"`
}
```

The tag accepts every annotation and constraint keyword a field's schema can
carry, and a keyword the field's shape cannot carry is an error. See
[Struct Tag](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Struct_Tag)
for the keys and the parsing rules, and
[Struct Fields](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Struct_Fields)
for how the `json` tag and embedding shape the output.

### Tag Interpreters

Any other struct tag reaches the schema through a `TagInterpreter`, registered
under the tag key it reads. The `interpreters/validate` subpackage maps
[`go-playground/validator`](https://github.com/go-playground/validator) tag
syntax to schema constraints, without depending on the validator library:

```go
import "go.jacobcolvin.com/x/jsonschema/interpreters/validate"

type CreateUser struct {
	Name  string `json:"name"  validate:"required,min=1,max=100"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age"   validate:"gte=0,lte=150"`
}

schema, err := jsonschema.GenerateFor[CreateUser](ctx,
	jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
)
```

See
[Tag Interpreters](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Tag_Interpreters)
for writing an interpreter and the
[`validate` package](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema/interpreters/validate)
for the tags it maps.

### Descriptions

`WithDescriptionProvider` supplies type and field descriptions. The built-in
provider reads Go doc comments from source files and skips a type whose
sources it cannot locate:

```go
schema, err := jsonschema.GenerateFor[MyType](ctx,
	jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
)
```

See
[Descriptions](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Descriptions).

### Definitions and Drafts

Generation extracts named struct types into `$defs` and refers to each through
`$ref`; `WithDefinitions(false)` inlines them. `Draft2020` is the default and
`Draft7` the alternative, and `WithDraft` serves generation, validation, and
inlining alike. See
[Definitions](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Definitions)
and [Drafts](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Drafts).

## Validation

`Compile` checks a schema once and returns a reusable `*Validator` with one
method per instance shape: `Validate` for a pre-parsed Go value (JSON, YAML,
or TOML decoder output), `ValidateJSON` and `ValidateReader` for raw JSON, and
`ValidateValue` for a Go value, which marshals it first. `CompileJSON` compiles
a schema arriving as a JSON document.

`Compile` rejects a malformed schema before any instance is validated. Each
refusal wraps a sentinel error matched with `errors.Is`. A validation failure
returns a `*ValidationError` tree that collects every failure with its
instance and schema path:

```go
type ValidationError struct {
	InstancePath jsontext.Pointer   // JSON Pointer into the instance, e.g. "/address/city"
	SchemaPath   jsontext.Pointer   // JSON Pointer into the schema
	Keyword      string             // failing keyword, e.g. "type", "minLength", "$ref"
	Message      string             // human-readable message
	Causes       []*ValidationError // child failures
}
```

`format` is asserted under Draft-07 and annotation-only under Draft 2020-12
unless the format-assertion vocabulary or `WithFormats(true)` turns it on.
Remote `$ref` URIs resolve through a `RefResolver` set with `WithRefResolver`,
which receives the caller's context; the package ships no network resolver.
See
[Validation](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Validation),
[Vocabularies](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Vocabularies),
and
[Remote References](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Remote_References).

## Traversal

`SubschemaEntries`, `Walk`, and `Schemas` work on `Schema` values directly,
visiting each distinct schema once so cyclic graphs terminate:

```go
err := jsonschema.Walk(s, func(loc jsonschema.Location, s *jsonschema.Schema) error {
	fmt.Println(loc.Pointer, s.Type) // "/properties/a string", ...
	return nil
})
```

See
[Traversal](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Traversal).

## Inlining

`Inline` returns a fresh schema tree in which a copy of the target schema
replaces every `$ref`, for consumers that cannot follow references, such as
code generators:

```go
inlined, err := jsonschema.Inline(ctx, schema,
	jsonschema.WithRefResolver(jsonschema.NewFileResolver(os.DirFS("schemas"))),
	jsonschema.WithBaseURI("main.json"),
)
```

See
[Inlining](https://pkg.go.dev/go.jacobcolvin.com/x/jsonschema#hdr-Inlining).

## Usage with `//go:generate`

`cmd/gen` writes a JSON Schema file for a named Go type at build
time, intended for `//go:generate`. It builds a small helper program inside the
target's own module, so the module must be able to resolve this package (via a
`require`, a workspace, or a `tool` directive):

```go
//go:generate go run go.jacobcolvin.com/x/jsonschema/cmd/gen -type Config -o config.schema.json
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

## Design Notes

This package re-exports the upstream `Schema` type, and that alias is a
compatibility commitment. Schemas pass directly to and from any package that
uses `google/jsonschema-go`'s `jsonschema.Schema`, with no conversion.
Everything else, including the validation walk, reference resolution, and
schema generation, is this package's own, so it can collect every failure with
its path, resolve remote references, and support the customization hooks above.

## License

Apache 2.0. See [LICENSE](https://github.com/macropower/x/blob/main/LICENSE).
