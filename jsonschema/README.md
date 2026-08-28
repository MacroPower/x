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
and structured instance validation with full instance/schema path tracking.

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

### Validate a JSON instance

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

// Compile once, then reuse -- the returned *Validator is safe for concurrent use.
v, err := jsonschema.Compile(ctx, schema)
if err != nil {
	log.Fatal(err)
}

if err := v.ValidateJSON(ctx, []byte(`{"name":"Ada","age":36}`)); err != nil {
	log.Fatal(err) // valid: not reached
}

// Validation failures unwrap to *ValidationError and carry full paths.
err = v.ValidateJSON(ctx, []byte(`{"name":"","age":-1}`))

if ve, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
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
- Schema traversal (`SubschemaEntries`, `Walk`) and shape predicates
  (`CheckTypeNames`, `IsTrueSchema`, `IsFalseSchema`) for working with
  `Schema` values directly.
- `$ref` inlining (`Inline`) that flattens a schema and the documents it
  references into one self-contained document.
- A build-time code-generation CLI (`jsonschemagen`) for `//go:generate`.

## Generating schemas

The primary entry point is the generic `GenerateFor`. A `reflect.Type` variant,
`Generate`, is provided for dynamic use, and `MustGenerateFor` (which passes
`context.Background()`) panics on error for package-scope variables, where for
a static type and fixed options generation either always succeeds or always
fails. The context is passed to the `DescriptionProvider` with every comment
lookup:

```go
schema, err := jsonschema.GenerateFor[MyType](ctx, opts...)
schema, err := jsonschema.Generate(ctx, reflect.TypeFor[MyType](), opts...)

var mySchema = jsonschema.MustGenerateFor[MyType](opts...)
var dynSchema = jsonschema.MustGenerate(reflect.TypeFor[MyType](), opts...)
```

These one-shot forms apply their options per call. To generate schemas for
many types under one option set, `NewGenerator` applies the options once and
the returned `Generator` is reused (the generation-side counterpart of
`Compile`/`Validator`), safe for concurrent use provided the configured
hooks are. `GenerateWith` is `GenerateFor` under a reusable `Generator`,
keeping the generic form available (Go methods cannot take type
parameters); `Generator.Generate` is its `reflect.Type` form:

```go
gen := jsonschema.NewGenerator(opts...)
schema, err := jsonschema.GenerateWith[MyType](ctx, gen)
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
| `any` / interface                    | unrestricted (`{}`); an intercepted interface schema admits `null` alongside (see `WithNullable`)           |
| `struct`                             | `object` with `properties`, `required`, and `additionalProperties: false`                                   |

Well-known types have built-in overrides matched by exact `reflect.Type`:
`time.Time` -> `{"type":"string","format":"date-time"}`,
`encoding/json.RawMessage` -> `{}`, `encoding/json.Number` ->
`{"type":"number"}`, `math/big.Int` -> `{"type":"integer"}` (its MarshalJSON
emits a bare number), and `math/big` `Rat`/`Float` -> `{"type":"string"}` with
a numeric pattern (`big.Float`'s pattern also admits the `"+Inf"`/`"-Inf"`
text it marshals for infinities). `log/slog.Level` -> `{"type":"string"}`: it
implements both direct marshalers, and its MarshalJSON emits the level name
as a JSON string, so the override pins the string schema its output
requires. `net/url.URL` has no override: it
implements no marshaler interface, so it reflects as the struct object
`encoding/json` actually emits.
Types implementing `encoding.TextMarshaler` map to `{"type":"string"}`.
Unsupported types (`func`, `chan`, `complex`, `unsafe.Pointer`) return
`ErrUnsupportedType`.

### Configuration options

| Option                           | Effect                                                                                        |
| -------------------------------- | --------------------------------------------------------------------------------------------- |
| `WithDraft(Draft)`               | Target draft: `Draft2020` (default) or `Draft7`; also serves validation and `Inline`.         |
| `WithTagInterpreter(key, t)`     | Register a `TagInterpreter` under the struct tag key it reads; multiple are applied in order. |
| `WithDescriptionProvider(p)`     | Set the `DescriptionProvider` used as the source of descriptions.                             |
| `WithTypeSchema(t, ts)`          | Override a specific Go type with a `TypeSchema` envelope (highest priority).                  |
| `WithTypeSchemaFor[T](ts)`       | `WithTypeSchema` for a statically known type, without `reflect.TypeFor`.                      |
| `WithTypeSchemaProvider(p)`      | Register a `TypeSchemaProvider` that overrides types by predicate.                            |
| `WithTypeSchemaExtender(e)`      | Register a `TypeSchemaExtender` that modifies reflection-generated schemas.                   |
| `WithNamer(n)`                   | Custom `Namer` for `$defs` entries; an empty name defers to the built-in namer.               |
| `WithDefinitions(bool)`          | Extract named types into `$defs`/`$ref` (default `true`).                                     |
| `WithAdditionalProperties(bool)` | Allow extra object keys (default `false`, disallowing them).                                  |
| `WithNullable(bool)`             | Make nil-able types (`*T`, `[]T`, `map`, `[]byte`) nullable (default `true`).                 |
| `WithDefaultsFrom(instance)`     | Seed root property defaults from an instance of the generated type.                           |
| `WithRootTitle(bool)`            | Title the root schema with the root type's name (default `false`).                            |

`WithDefaultsFrom` marshals the instance with `encoding/json` after generation;
each top-level key of the output that matches a root property becomes that
property's `default`, overwriting any default set via struct tags. Keys the
`json` tags omit (`omitempty`, `omitzero`) contribute nothing, so presence
follows the tags exactly, and nested struct, slice, and map values become
whole-value defaults on their top-level property. An instance whose
pointer-dereferenced type is not the generated type, or that does not marshal to
a JSON object, returns an error wrapping `ErrInvalidDefaultsInstance`. A nil
instance instead restores the default, seeding no defaults. A typed nil pointer
is a value, not a reset: it marshals to JSON null and fails as a non-object
instance. A pointer root's nullable `anyOf` wrapper is resolved to its value
branch first, so the defaults reach the object schema (or its `$defs` entry)
inside. When a self-referential root stays in `$defs`, the defaults apply to
that definition, shared by every recursive occurrence. Under `Draft7`, a default
landing on a `$ref`'d property moves the `$ref` into an `allOf` wrap, the same
shape tag defaults produce, because Draft-07 readers ignore `$ref` siblings:

```go
schema, err := jsonschema.GenerateFor[Config](ctx,
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
With `WithDefinitions(false)` the inlined root carries no `$id` or `$defs`
key, so this gives its consumers a name without re-deriving it from the Go
type themselves.

### Customization interfaces

A type implementing `JSONSchemaProvider` supplies its own schema entirely,
bypassing reflection. It returns a `TypeSchema` envelope carrying its intent, so
generation applies the null encoding and resolves references itself; a non-nil
error aborts generation:

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
```

A type implementing `JSONSchemaExtender` modifies its reflection-generated schema
after it is built. It receives the same `TypeSchema`, with `Value` set to the
reflection-generated schema to mutate in place; only `Value` and `Nullability` are
honored (`Verbatim` and `Ref` declare a replacement schema only a provider
supplies, so an extender setting either is `ErrConflictingTypeSchema`), and a
non-nil error aborts generation:

```go
type Metadata struct {
	Tags map[string]string `json:"tags"`
}

func (Metadata) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Value.Description = "Arbitrary key-value metadata"
	ts.Value.MinProperties = new(1)
	return nil
}
```

Both methods receive the same arguments as their registered counterparts
(`TypeSchemaProvider`, `TypeSchemaExtender`): the Generate call's context and
a `TypeContext` carrying the target draft. An implementation needing neither
ignores them.

A `TypeSchema` declares intent instead of a pre-shaped schema. Exactly one of
`Value`, `Verbatim`, or `Ref` is meaningful; setting more than one is
`ErrConflictingTypeSchema`. `Value` is a bare value schema whose nullability is
the `Nullability` stance combined with each occurrence's pointer-ness -- so a hook
declares `NullAllowed` (or `NullForbidden`) rather than hand-shaping an
`anyOf[value, null]` wrapper. `Verbatim` is an opaque escape hatch emitted
exactly as authored (no null encoding), for a fully-formed schema such as one
loaded from a document. `Ref` is a whole-type alias to another Go type, kept
reachable through a node-backed `$ref` edge; a `Ref` naming a type that is not
extractable to `$defs`, or an alias chain that cycles back to its own type, is
`ErrConflictingTypeSchema` too. A zero `TypeSchema` marks the type
unrestricted (`{}`).

For each type, the schema is determined by the first matching step:

1. Registered `TypeSchemaProvider` values (`WithTypeSchemaProvider`, and the
   exact-match providers `WithTypeSchema` registers), consulted newest
   registration first (highest priority).
2. `JSONSchemaProvider`.
3. Built-in overrides (`[]byte`, `time.Time`, `encoding/json.Number`, ...).
4. Marshaler methods promoted from an embedded field: a promoted
   `encoding/json.Marshaler` makes the schema unrestricted (`{}`), and a
   promoted `encoding.TextMarshaler` makes it `{"type":"string"}`. In both
   cases the promoted method serializes the whole outer struct, so reflecting
   its fields would describe a shape that never appears.
5. `encoding.TextMarshaler` (direct implementation, for types not also
   implementing `encoding/json.Marshaler`).
6. Kind-based reflection.

A direct `encoding/json.Marshaler` implementation is not consulted: it falls
through to kind-based reflection, since MarshalJSON can return any JSON type.
That holds even when the type also implements `encoding.TextMarshaler`
(`encoding/json` prefers MarshalJSON, so the text form never appears in the
output). Use `WithTypeSchema` or `JSONSchemaProvider` to describe its real
shape.

A `TypeSchemaProvider` registered with `WithTypeSchemaProvider` supplies
schemas for whole families of types by predicate: every type implementing a
third-party interface, or every type in a package. By contrast,
`WithTypeSchema` names one exact `reflect.Type` at a time:

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

Providers answer `ErrTypeNotHandled` (or an error wrapping it) for a type
they do not handle, passing it to the next provider and then to the rest of
the chain; returning a zero `TypeSchema` with a nil error marks the type
unrestricted (`{}`), mirroring `JSONSchemaProvider`. Any other provider error
aborts generation, for a provider that recognizes a type but cannot produce
its schema (an I/O failure, for example). A provider may be consulted several
times for the same type within one run, so it must be deterministic.
Providers and extenders receive the Generate call's context, so an
implementation doing I/O can honor cancellation and deadlines. Both receive a
`TypeContext` carrying the Go type and the target draft, so an implementation
can emit draft-appropriate keywords, the way tag interpreters use
`FieldContext.Draft`.

If a type implements both customization interfaces, only `JSONSchemaProvider` is
used. When a registered provider (`WithTypeSchemaProvider` or `WithTypeSchema`) or
`JSONSchemaProvider` supplies the schema, `JSONSchemaExtender` is not called.

`JSONSchemaExtender` requires owning the type. For types you do not own, a
`TypeSchemaExtender` registered with `WithTypeSchemaExtender` adjusts the
reflection-generated schema at the same point in the pipeline, after the
type's own `JSONSchemaExtend`, under the same not-called-when-replaced rule.
Like a provider, an extender may run several times for the same type within
one run (once per inline occurrence), so it must be deterministic.
Where a provider replaces a type's schema wholesale, an extender modifies
what reflection produced:

```go
// Add a description to a third-party type without replacing its schema.
descriptions := jsonschema.TypeSchemaExtenderFunc(
	func(_ context.Context, tc jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
		if tc.Type == reflect.TypeFor[netip.Addr]() {
			ts.Value.Description = "An IP address."
		}
		return nil
	},
)

schema, err := jsonschema.GenerateFor[Config](ctx, jsonschema.WithTypeSchemaExtender(descriptions))
```

`WithTypeSchemaExtenderFor` is the generic form for one statically known
type, so the type guard above disappears (and only the guard: the callback
keeps the `TypeContext`, so it stays draft-aware):

```go
schema, err := jsonschema.GenerateFor[Config](ctx,
	jsonschema.WithTypeSchemaExtenderFor[netip.Addr](
		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
			ts.Value.Description = "An IP address."
			return nil
		}))
```

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
against the field's schema _shape_ -- the JSON shape its instance actually
takes -- rather than against its Go kind alone. The two agree for an ordinary
field and diverge wherever the field serializes itself as something else: a
`json:",string"` numeric or bool field, and equally a type that marshals itself
as text, has a string schema, so its scalars parse at the real Go kind (keeping
the range check, so `const=200` on an `int8` is an error) and are then
re-serialized to the text the field emits. A `MarshalText` int whose value `3`
writes as `"L3"` therefore gets `const=3` as `{"const":"L3"}` rather than the
unsatisfiable `{"type":"string","const":3}`, and `default=3` likewise. A
`json:",string"` string field double-encodes -- `encoding/json` encodes the
already-encoded string a second time, so the value `abc` marshals as the JSON
string `"\"abc\""` -- and its scalars serialize the same way (`const=abc` pins
that quoted text), while the keywords that would measure or match the unquoted
value (`pattern`, `format`, and the length bounds) are rejected with an error
rather than silently asserting against the quoted, escaped text.
`encoding/json.Number` is the one string kind exempt from that rule.
`encoding/json` writes it as the number it holds, so a quoted one emits its
literal once-quoted and takes the coerced-numeric rules instead. It emits the
literal verbatim rather than canonicalizing it, so `const=5.0` pins `"5.0"` and
`const=5` pins `"5"`. `enum`
and `examples` values are separated by `|`; commas separate pairs, so a value
containing a comma escapes it with a backslash (`\,`, and `\\` for a literal
backslash). For complex values, use `JSONSchemaExtender` or doc comments with
`WithDescriptionProvider`.

`type=` overrides the reflected type entirely, for a Go type whose JSON
representation differs from its reflection: it must name one of the seven
JSON Schema types. The overridden field is not nullable (it names a concrete
type in place of the one a pointer would make nil-able) and not a reference.
When the new type is not numeric, it also removes the numeric bounds derived
from the Go kind. So a `*time.Duration` field (reflected as a nullable
integer) with `jsonschema:"type=string,pattern=..."` produces a clean
`{"type":"string","pattern":"..."}` without needing
`JSONSchemaExtend`. Tag pairs apply in order; keys after `type=` still take
effect. A numeric bound set by an earlier pair and then dropped by a
non-numeric `type=` override is reported as an error rather than silently
discarded, since it is the author's explicit input.

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
array value: the values land on the item schemas, where each element parses
against its own shape, so `Days []string` with `enum=monday|tuesday` produces
`{"items":{"type":"string","enum":["monday","tuesday"]}}` and a coerced or
text-marshaling element gets the same treatment a coerced field does. Nested
sequences (`[][]T`) descend to the innermost element schema. `const`, `default`,
and `examples` remain whole-value constraints and are still errors on sequence
fields, as is `enum` on `[]byte` (which encodes as a base64 string with no item
schema). This is the same element path a `validate` dive or sequence-wide
`oneof` takes, so the two dialects cannot disagree about what an element is.

A keyword the field's shape cannot carry is an error rather than an inert
keyword nothing enforces: `minItems=3` on a string, or any numeric bound on a
`json:",string"` coerced field, whose instance is a quoted string that `minimum`
cannot constrain. The shape is read from the field's schema, not only its Go
kind, so a field whose type supplies a verbatim or overridden schema is judged by
what that schema declares.

A repeated bound key intersects rather than overwriting: `minimum=5,minimum=3`
keeps `5`, matching how bounds from every other source compose. A second `const`
or `enum`, or one disagreeing with a value the field's type already pins, is
`ErrConstraintConflict`: both fully describe the allowed value, so neither can
silently win. `format` and `pattern` replace what the field's type declared (the
tag names the keyword outright, unlike a tag interpreter, which defers to both),
but naming either twice in one tag is an error, since there no precedence
applies and dropping one of two stated values would be silent.

A `const` or `enum` makes the kind-derived numeric bounds (an `int8`'s
`minimum`/`maximum`, for instance) redundant, so they are dropped. A `const`
also subsumes an explicit bound, since it pins a single value, so that bound is
dropped too. An `enum` only restricts the value to a set, so an explicit bound
narrows it further and is kept: `enum=10|20,minimum=15` keeps `minimum` and so
admits only `20`.

The numeric, length, and count bounds from all three sources (the Go kind, the
`jsonschema` tag, and tag interpreters) merge through one shared constraint
model, so this precedence is defined once and applies uniformly no matter which
source set a bound. The bounds intersect order-independently (a weaker bound
never loosens a stronger one), a conflict in the discrete value set aborts
generation, and an unsatisfiable range (a `minimum` above a `maximum`) is
emitted as its impossible bounds rather than loosened. A numeric bound the
schema-side float64 cannot ship exactly -- an integer its shortest-decimal
rendering, the value the validator enforces, does not reproduce -- is rejected
the same way regardless of which source set it; a bound parsed at an
integer-kind field is capped at 2^53 outright.

A sequence or map element resolves by the same rule, reading its own authored
canvas rather than the field's. Which dialect wrote the element's `const` or
`enum` does not enter into it: `[]int8` with `jsonschema:"enum=1|2|3"` and
`[]int8` with `validate:"oneof=1 2 3"` both drop each element's kind-derived
`-128`/`127` range, and a bound authored on the element itself survives
alongside the pin, mirroring the field rule.

### Struct field rules

Fields follow `encoding/json` conventions: the `json` tag sets the property
name, `json:"-"` excludes a field (`json:"-,"` uses the literal name `"-"`),
`omitempty` and `omitzero` drop the field from `required`, and `json:",string"`
forces a `{"type":"string"}` schema for applicable types (a type implementing
`json.Marshaler` is exempt: `encoding/json` ignores the option for it, so the
field keeps the kind-based schema a direct marshaler otherwise gets). Embedded
structs
without a `json` tag have their fields promoted; embedded struct types
intercepted by
an earlier resolution step are composed via `allOf` (wrapped as
`anyOf[schema, {}]` for a pointer embed, since a nil pointer contributes
nothing to the marshaled object). A provider schema (registered or on-type)
used for such an embedded type must leave the object
open (no `additionalProperties: false`), since `allOf` evaluates each branch
against the whole object: a closed branch rejects the parent's sibling
properties and the generated schema then rejects the struct's own marshaled
JSON. A composed embed's promoted names still take part in field resolution
exactly as `encoding/json`'s flat walk resolves them (shadowing, same-depth
annihilation -- ties inside the embed included -- and the tag tie-break),
even though they never become properties: the embed's branch carries their
assertions. Under `allOf` composition, `Draft2020` puts `unevaluatedProperties: false`
on the parent in place of `additionalProperties: false`, and each promoted
name a composed embed contributes to the marshaled object gets a `true`
property on the parent: the branch is not guaranteed to evaluate the name (an
unrestricted `TypeSchema` renders as `true` and evaluates nothing), and an
unevaluated name would otherwise be rejected. `Draft7` omits
`additionalProperties: false` from the parent altogether. Embedded non-struct
types, interfaces included, are regular leaf fields keyed by the field name,
exactly as `encoding/json` records them: the key is always emitted (`null` for a
nil interface), so an intercepted interface schema admits `null` alongside.

### Comment extraction

Type and field descriptions come from a `DescriptionProvider`, registered with
`WithDescriptionProvider`. The built-in `GoCommentProvider` (constructed with
`NewGoCommentProvider`) extracts Go doc comments from source files for
struct types, fields, and named types using `go/ast` and
`golang.org/x/tools/go/packages`; when source files cannot be located for a
type, extraction is silently skipped, so a binary deployed without sources
generates schemas without descriptions, while a cancelled or expired
Generate context is reported as an error. Loading uses the generator
process's build context (its `GOOS`, `GOARCH`, and active build tags), so a
type declared in a platform- or build-tag-gated file the current context
excludes is also extracted without descriptions even when reflection sees
it. Package loading runs in the process working directory unless `WithLoadDir`
points it at another module's directory. The `jsonschema` tag's `description`
wins over a provider-supplied comment.

```go
schema, err := jsonschema.GenerateFor[MyType](ctx,
	jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
)
```

Any other implementation substitutes another source: comments pre-extracted at
build time for a binary that deploys without source files, or fixed descriptions
in tests. A provider error aborts generation, matching the package's other
generation hooks, so a provider doing I/O reports a failed lookup instead of
silently dropping descriptions. `ChainDescriptionProviders` composes providers,
with the first non-empty description or first error winning. This suits
overrides for specific types backed by AST extraction:

```go
type DescriptionProvider interface {
	// TypeDescription returns the description for a named type, or "" for none.
	TypeDescription(ctx context.Context, tc TypeContext) (string, error)

	// FieldDescription returns the description for the struct field in fc,
	// or "" for none.
	FieldDescription(ctx context.Context, fc FieldContext) (string, error)
}

jsonschema.WithDescriptionProvider(jsonschema.ChainDescriptionProviders(
	overrides, jsonschema.NewGoCommentProvider()))
```

`TypeDescription` receives the same `TypeContext` as the package's other
type-level hooks. `FieldDescription` receives the same `FieldContext` as tag
interpreters. `Owner` carries the type declaring the field, which for a promoted
field is the embedded type where the doc comment lives, and `StructField` names
the Go field. `DescriptionProviderFuncs` adapts a pair of bare functions, so a
one-off provider needs no named type; a nil field answers `""` for its half:

```go
jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
	TypeFunc: func(_ context.Context, tc jsonschema.TypeContext) (string, error) {
		return docs[tc.Type.Name()], nil
	},
})
```

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
an `allOf`; for a nullable `$ref` field the wrap applies to the value branch of
the `anyOf`, where a field's `const`/`enum` lands. In Draft 2020-12 sibling
keywords sit directly alongside `$ref`.

The `WithDraft` option serves generation, validation, and `Inline` alike:
generation targets the given draft, while validation and inlining use it in
place of the draft they otherwise detect from the root schema's `$schema`
field, for schema documents that omit `$schema` (which would default to
`Draft2020`) or carry one that does not reflect their dialect. A `$schema`
declaring an official dialect this package does not implement (2019-09,
draft-06, draft-04, or draft-03) fails `Compile` and `Inline` with
`ErrUnsupportedDraft` rather than being silently processed under different
semantics; a `WithDraft` override processes such a document explicitly. A
custom metaschema URI keeps the `Draft2020` default.

Anchor registration follows the draft in validation and inlining alike:
`$anchor` and `$dynamicAnchor` name resolution targets only under Draft
2020-12; under Draft-07 they are unknown annotations and a `$ref` naming one
does not resolve. Conversely the Draft-07 fragment-only `$id` anchor form
registers only under Draft-07: 2020-12 forbids a fragment in `$id`, so there
the form names nothing.

## Tag interpreters

All struct-tag interpretation beyond the `json` and `jsonschema` tags goes
through the `TagInterpreter` interface:

```go
type TagInterpreter interface {
	Interpret(ctx context.Context, field FieldContext, tag Tag) error
}
```

Interpreters receive three things and declare facts on the field's authored
canvas. The first is the Generate call's context, like the other generation-time
hooks; an interpreter that performs no cancellable work ignores it. The second is
a `Tag` carrying the struct tag key and value the call runs under. The third is a
`FieldContext`, whose `Canvas` is the authored-facts canvas an interpreter writes
to (value-scoped facts like `const` and `enum`, annotations, and numeric, string,
and array bounds). A canvas bound can only tighten the type's own: generation
intersects each canvas bound with the type-derived bound from `Base` and keeps the
stronger side, so a weaker authored bound never widens the type's, and an
interpreter need only intersect its own repeated rules within a tag against the
canvas value. (The string first-wins keywords -- `format`, `pattern`,
`contentEncoding`, `contentMediaType` -- read the field's `EffectiveFormat` and
sibling accessors so a tag never overrides a value the type already set.) The
read-only `Base` is the type-derived schema, for dispatching on the reflected
shape. Generation composes the canvas with `Base` and applies the null encoding
for a nil-able field, so a `const` or `enum` an interpreter declares lands on the
value branch and keeps null valid. Which side a keyword lands on is declared per
keyword in a single table, not spread across the reconciliation. The context also
holds the parent schema, JSON name, and Go type; the declaring struct type, which
for a promoted field is the embedded type; the full `reflect.StructField` for
reading sibling struct tags such as the `json` tag's options; and the target
`Draft` for emitting draft-appropriate keywords.

For bounds and value constraints, an interpreter uses the `Constraints` facade
`FieldContext.Constraints()` returns, the contribution surface over the shared
constraint algebra. Its vocabulary is the model's: `Apply` takes an `Op` and,
for a bound, the `Axis` it targets (`AxisAuto` lets the field's shape choose,
which is what a rule-shaped validator means; naming a family pins it), under
the single 2^53 policy. Named conveniences remain for the value set
(`SetConst`, `SetEnum`, `Const`, `Enum`, `Forbid`, `ForbidSchema`,
`SetMultipleOf`), where an interpreter usually runs its own conflict check
first.

An interpreter that branches on what the field is classifies it once with
`FieldContext.Shape()` (or `ShapeOf(fieldType, base)` when no context is
available, which cannot see the `json:",string"` flag on a string Go kind --
the one coercion, `FormCoercedString`, that type and base alone do not
express). The resulting `Shape` carries the declared Go type,
that type with its pointer chain followed, the kind a scalar literal parses at,
whether the occurrence admits null, and the `Form` -- the JSON shape the
instance actually takes, which is what the model dispatches on. `Form` is
deliberately not the Go kind, so a field that encodes itself as a string
(through `json:",string"` or its own `MarshalText`) reads as
`FormCoercedNumber` or `FormTextString` rather than as a number every branch
has to special-case. Handing that same `Shape` to
`FieldContext.ConstraintsFor` builds the facade without classifying the field
again, which `FieldContext.Constraints()` would.

That one call carries the coercion decision for a field whose schema is a
string, and the retargeting of an element rule onto the item schemas, so an
interpreter states what it wants and re-derives none of it. Bounds are
intersect-only: each writes back only when it would not loosen the effective
bound, so a bound never weakens a stronger one and a widening bound is a no-op.
A rule the field's shape cannot carry is an error naming the reason rather than
a keyword nothing enforces. A conflict surfaces the exported
`ErrConstraintConflict` sentinel, checked against
the canvas and an inline type-derived value; a `const`/`enum` living in a
`$defs`-referenced definition is not visible to the check and instead composes
conjunctively beside the `$ref` (an `enum` intersects and only tightens, a
disagreeing `const` composes to a faithfully unsatisfiable schema).

An element rule (a dive, or a sequence-wide `oneof`) reaches the item schemas
through the model, which re-dispatches on each element's own shape;
`FieldContext.ElementContexts` remains the accessor for walking them directly. Each interpreter is registered under the struct tag key it reads
(following `net/http.Handle`, so one implementation can serve several keys);
multiple interpreters can be registered and run in order, after the `jsonschema`
tag. `TagInterpreterFunc` adapts a bare function, so a one-off interpreter needs
no named type.

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

schema, err := jsonschema.GenerateFor[CreateUser](ctx,
	jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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
- **Enumerations:** `oneof` maps to `enum` for strings, numbers, and bools, and
  on a slice or array to an `enum` on the element schemas. That is the same
  element path `dive` takes, so on those shapes `oneof=a b` and `dive,oneof=a b`
  produce identical schemas. Two exceptions are deliberate: on a map, `dive`
  descends to the values while a bare `oneof` is an error (a `dive` says
  "descend" outright, a bare `oneof` on a map has no go-playground element
  meaning), and on a `[]byte` both are an error, since the field encodes as one
  base64 string with no element schema for either to reach.
- **Collections:** `unique` -> `uniqueItems` (the `unique=<field>` form has no
  JSON Schema equivalent and is an error, as is `unique` on a shape with no
  array to constrain -- a string, number, bool, or struct; a map is the one
  exception and stays a documented no-op, since its go-playground meaning
  "distinct values" is real but has no object-side keyword); `dive` descends
  into element or value schemas.
- **Formats:** `email`, `url`, `uri`, `uuid`, `ipv4`, `ipv6`, `hostname` ->
  `format`.
- **Patterns:** `alpha`, `alphanum`, `numeric`, `number`, `ascii` -> `pattern`.
- **Content:** `json` -> `contentMediaType`; `base64` -> `contentEncoding`.

The interpreter owns this dialect's grammar and nothing else: splitting the tag,
the OR and escape handling, the `dive` and `keys` blocks, and a table naming the
operation each validator spells. What an operation does to a given field is the
shared constraint model's, which the `jsonschema` struct tag runs through too, so
the two dialects cannot drift on a rule they both express. The package holds no
scalar parser and writes no schema keyword directly; every constraint is
contributed through `Constraints`.

Cross-field, conditional, and control tags (`omitempty`, `structonly`, ...) are
silently skipped; within a comma group only the first `|` OR alternative is
interpreted, leaving later comma-separated constraints intact; unrecognized keys
return an error.

## Validating instances

The core entry point is `Compile(ctx, schema, opts...)`: it performs the
per-schema work once (registry construction, the compile-time structure,
identifier, and reference checks, draft and vocabulary detection) and returns
a reusable `*Validator` with one method per instance shape. `MustCompile`
panics on error, for package-scope
validators where for a static schema and fixed options compilation either
always succeeds or always fails (following `regexp.MustCompile` and
`MustGenerateFor`).

- `Validator.Validate(ctx, instance)` validates a pre-parsed Go value
  (`map[string]any`, `[]any`, `string`, `float64`, `json.Number`, `bool`,
  `nil`). Go numeric kinds that `encoding/json` does not produce (the signed
  and unsigned integer types and `float32`) are accepted too and normalized
  via `Normalize`, so values decoded from YAML or TOML validate directly:
  integers convert to `json.Number` (exact at any magnitude) and `float32`
  widens to `float64`. `Normalize` is exported for callers that want to
  pre-normalize a value once and reuse it. A self-referential instance (a map
  or slice that contains itself) is rejected rather than walked.
- `Validator.ValidateJSON(ctx, data)` unmarshals raw JSON with a
  `json.Decoder` using `UseNumber()` (preserving the integer-vs-number
  distinction), then validates.
- `Validator.ValidateValue(ctx, v)` marshals a Go value with
  `encoding/json` and validates its JSON form, closing the loop with
  generation: an instance of the very type a schema was generated for
  validates in one call. `json` tags, `omitempty` and `omitzero`, and
  `MarshalJSON` implementations all apply, so what is validated is exactly
  what a JSON consumer of the value would see. A non-pointer value is
  marshaled through a pointer to a copy, so pointer-receiver
  `MarshalJSON`/`MarshalText` implementations (`big.Int`'s, for example)
  apply as they would for `&v`, and a value instance validates identically
  to a pointer instance. A value `encoding/json` cannot marshal returns the
  wrapped marshal error.

A compiled `Validator` also reports what it validates: `Validator.Schema()`
returns the root schema it was compiled for and `Validator.Draft()` the draft
in effect, so a validator can be passed across package boundaries without the
schema riding alongside. The returned schema is the caller's live value, not a
copy, and the compiled validator's caches key off its nodes. Mutating a schema
after `Compile` is unsupported, and validating through a `Validator` whose
schema has been mutated has undefined behavior. Treat a compiled schema as
immutable, and recompile after any change.

The package-level `Validate(ctx, schema, instance, opts...)` is the one
one-shot form, compiling the schema and validating one pre-parsed instance
in a single call, for the quick check that does not warrant holding a
`Validator`; raw bytes or a marshalable value are one `Compile` away.

Schemas arriving as JSON documents rather than `*Schema` values have
symmetric entry points:

- `CompileJSON(ctx, data, opts...)` decodes `data` as a single JSON schema document
  (numbers as `json.Number`, trailing data rejected) and compiles it with
  `Compile`. It is the schema-side counterpart of `Validator.ValidateJSON`.
  `MustCompileJSON` panics on error, for schema documents fixed at build time
  such as files brought in with `go:embed`.
- `ParseSchema(data)` is the decode half of `CompileJSON` alone: it returns
  the `*Schema` uncompiled, for consumers that work with the schema itself
  (`Inline`, `Walk`, programmatic editing) rather than validating instances
  against it.
- `ParseSchemaValue(doc)` converts an already-decoded document to a `*Schema`:
  a `bool` (`true` is the empty schema, `false` the schema that rejects every
  instance) or a `map[string]any`, such as `Normalize` output with `json.Number`
  leaves.

With all three, a top-level value that is not an object or boolean returns an
error wrapping `ErrInvalidSchemaDocument`. That includes JSON `null`, which
unmarshaling into a `Schema` directly would silently coerce to the `false`
schema. Malformed JSON returns the wrapped decode error without the sentinel.

Every compile and validate entry point takes a `context.Context` as its
first parameter, carried to the `RefResolver` (see
[Remote references](#remote-references)); the `Must*` forms pass
`context.Background()`, the right context for the package-scope use they
serve.

`Compile` (and therefore the one-shot `Validate`) rejects a `type` keyword that
names anything other than the seven JSON Schema types with `ErrInvalidType`,
so a typo'd type surfaces at construction instead of silently rejecting every
instance at runtime. The same check is exported standalone as
`CheckTypeNames` (see [Schema traversal and predicates](#schema-traversal-and-predicates));
`Compile` routes through it, so the two produce textually identical errors.

Under draft 2020-12, `Compile` also rejects the array form of the `items`
keyword (what a JSON `"items": [ ... ]` parses into) with
`ErrItemsArrayUnderDraft2020`. That form is the draft-07 spelling of tuple
validation; 2020-12 spells tuples with `prefixItems`, so an array-form `items`
would otherwise be dropped silently and accept every element. Set the draft-07
`$schema` (or `WithDraft`) for tuple semantics, or use `prefixItems`.

`Compile` also rejects a negative length or count keyword (`minLength`,
`maxLength`, `minItems`, `maxItems`, `minProperties`, `maxProperties`,
`minContains`, `maxContains`) with `ErrNegativeBound`, and a `multipleOf` that
is not strictly greater than zero with `ErrNonPositiveMultipleOf`. The spec
fixes each domain (a non-negative integer; a number > 0); the invalid schema
would otherwise compile and then silently mis-validate: a negative maximum
rejects every instance, a negative minimum never fires, and a non-positive
`multipleOf` rejects every numeric instance while accepting every non-numeric
one. A strictly positive `multipleOf` literal below the smallest positive
`float64` (about 4.9e-324) is spec-valid but underflows to zero when the
document is decoded; `ParseSchema` and `ParseSchemaValue` drop the keyword in
that case -- at `float64` precision it constrains nothing -- rather than
letting the underflowed zero be rejected as an authored one.

Beyond the keyword domains, `Compile` vets the document's Go representation and
identifiers. A schema setting both Go fields of one JSON keyword (`Type` and
`Types`, `Defs` and `Definitions`, `Items` and `ItemsArray`, a `dependencies`
key in both maps) is rejected with `ErrConflictingSchemaFields`; a nil
`*Schema` element inside a sub-schema slice or map with `ErrNilSubschema`; a
duplicate `PropertyOrder` entry with `ErrDuplicatePropertyOrder`; and a root
document whose sub-schema pointers alias or cycle with `ErrSchemaNotTree`. An
`$id` outside the keyword's domain is rejected with `ErrInvalidID`: one that
does not parse, one carrying a fragment under draft 2020-12, or one that does
not resolve to an absolute URI against its enclosing base (the parent `$id`
chain, or `WithBaseURI` for the root, so a relative root `$id` compiles exactly
when a base supplies the absolute prefix). Under draft-07 two forms go
unchecked: an `$id` beside a `$ref` (the draft ignores it) and a
fragment-carrying `$id` (the anchor spelling). An unparsable `WithBaseURI`
value is rejected with `ErrInvalidBaseURI`, and a `$vocabulary` on a node whose
`$schema` does not establish the 2020-12 dialect with `ErrMisplacedVocabulary`
(exact URI match; an empty `$schema` inherits the run's dialect, accepted under
2020-12 and rejected under draft-07).

`Compile` then statically resolves every reference reachable from the root
(`$ref` and, under 2020-12, `$dynamicRef`) through the same resolution core
the validation walk uses. A reference that resolves to nothing while its
document is present can never resolve later, so `Compile` rejects it with an
error wrapping `ErrNotResolved` (or the resolver's reported error); a
reference whose document cannot be located at compile time is tolerated and
reported by the validation walk instead (see
[Remote references](#remote-references)). An uncompilable
`pattern` or `patternProperties` regex is deliberately not a compile error:
`Compile` records each pattern's regex-compile outcome per node, and every
string instance the pattern would judge fails closed at validation time.

The one-shot `Validate` compiles a fresh validator on every call; to
validate many instances against the same schema, `Compile` once and reuse
the result. A `*Validator` is safe for concurrent use by multiple
goroutines.

On success all return `nil`. A validation failure returns an error that unwraps
to `*ValidationError` via `errors.AsType`. Non-validation failures (JSON decoding,
an unaccepted instance type, the compile-time check sentinels, and
`ErrUnknownVocabulary`) return ordinary wrapped errors that do not unwrap to
`*ValidationError`.

### Numeric precision

Instance numbers are compared exactly (decoded with `UseNumber`, compared as
`big.Rat`), with one bound on the work an adversarial literal can demand: for a
JSON number whose exact value exceeds an internal cap (about 4096 significant
digits or decimal exponent magnitude), `minimum`/`maximum`/`exclusiveMinimum`/
`exclusiveMaximum` are still enforced exactly. `multipleOf` is enforced for an
over-cap _integer_ (its divisibility is computed with modular arithmetic, so the
magnitude is never expanded) and skipped only for an over-cap _non-integer_,
whose fractional part cannot be expanded within the cap. The `float64`-typed
bound keywords (`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`,
`multipleOf`) are limited to `float64` precision: integers beyond 2^53 round
when the schema is decoded, even though the instance value they are compared
against is exact. `const` and `enum` values are preserved exactly (decoded as
`json.Number`) through every entry point, including `ParseSchema` and
`CompileJSON`. A schema-side `float64` is interpreted at its shortest
decimal value across all numeric keywords, so `const: 0.1` matches the
instance `0.1` exactly, consistent with how `minimum: 0.1` bounds it. A
`float64` in a pre-parsed instance (JSON decoding always yields `json.Number`)
is interpreted the same way, including under `uniqueItems`, so `float64(0.1)`
and a decoded `0.1` are one value: duplicates under `uniqueItems`, and each
matching `const: 0.1`. A number-shaped value with no numeric value to compare
-- a non-finite `float64` (NaN or an infinity, which JSON cannot represent
but a Go instance can carry) or a `json.Number` whose literal is not a valid
JSON number -- passes a bare `type` assertion but fails every numeric bound
keyword present: a bound written to constrain a number fails closed rather
than silently skipping a value it cannot compare.

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
consumer can highlight the key instead of the value. The keyword names
themselves are exported as `Keyword*` constants (`KeywordRequired`,
`KeywordRef`, ...), so code branching on `Keyword` needs no raw strings.

`InstanceSegments()` returns the `InstancePath` location in typed form: one
`Segment` per reference token, outermost first, each marked as an object key
or an array index. The JSON Pointer string cannot distinguish array index `1`
from an object property named `"1"` (YAML decoders in particular produce
string map keys that look numeric), and its keys are RFC 6901-escaped; the
segments carry the unescaped key and an explicit index/key distinction, so
consumers need not re-parse the pointer and guess with `strconv.Atoi`.
`SchemaSegments()` is the schema-side counterpart for `SchemaPath`,
distinguishing an `allOf` branch index from a property named like a number
and carrying property names under `properties` verbatim. Segments are
populated on every error produced by validation; hand-constructed errors
return `nil`.

```go
type Segment struct {
	Key     string // object property name, when IsIndex is false
	Index   int    // array index, when IsIndex is true
	IsIndex bool   // array element rather than object property
}
```

A `false` subschema failure ("value is not allowed") carries the applicator
keyword that applied it: `additionalProperties` for
`additionalProperties: false`, and likewise `properties`,
`patternProperties`, `items`, `prefixItems`, and `additionalItems`. The
common rejected-extra-property case is therefore distinguishable without
inspecting `SchemaPath`. A standalone boolean `false` schema has no applicator
context and leaves `Keyword` empty.

A `propertyNames` violation constrains a key, which has no JSON Pointer of
its own (RFC 6901), so it borrows the property's location: the surfaced
error carries `Keyword == "propertyNames"` and an `InstancePath` pointing at
the offending property (e.g. `/settings/BadKey`), with the inner keyword
failure (`pattern`, `maxLength`, ...) in `Causes`. The failing key and its
containing object are both identifiable from `InstancePath` alone.

### Validation options

| Option                         | Effect                                                                                                                   |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------ |
| `WithDraft(Draft)`             | Override the draft otherwise detected from the root schema's `$schema`.                                                  |
| `WithRefResolver(r)`           | Resolve remote/absolute `$ref` URIs (called only when local lookup fails); the resolver receives the caller's context.   |
| `WithBaseURI(base)`            | Set the root document's base URI for ref absolutization; also serves `Inline`.                                           |
| `WithFormatValidator(name, f)` | Register a custom `format` checker (a `FormatValidator`; `FormatValidatorFunc` adapts a bare function) under `name`.     |
| `WithFormats(bool)`            | Force `format` assertion on or off.                                                                                      |
| `WithContent(bool)`            | Assert `contentEncoding`/`contentMediaType` (annotation-only by default; base64 rejects line breaks under 2020-12 only). |
| `WithVocabularies(uris...)`    | Directly set the active vocabularies (highest precedence); unlisted ones are inactive.                                   |
| `WithMetaSchemaResolver(r)`    | Set a `RefResolver` that looks up the metaschema (whose `$vocabulary` gates keyword groups) by the root's `$schema` URI. |

### Formats

The active draft and vocabulary decide whether `format` is asserted: under
Draft-07 it is asserted, under Draft 2020-12 it is annotation-only unless the
format-assertion vocabulary is active. `WithFormats(true)` forces assertion.
When the format-assertion vocabulary drives assertion, a format name with no
registered checker rejects every string instance (the 2020-12 spec mandates
failure on unknown formats); assertion via `WithFormats(true)` or Draft-07's
default keeps unknown names annotation-only.
Built-in checkers cover `date-time`, `date`, `time`, `duration`, `email`,
`idn-email`, `hostname`, `idn-hostname`, `uri`, `uri-reference`, `uri-template`,
`iri`, `iri-reference`, `uuid`, `ipv4`, `ipv6`, `json-pointer`,
`relative-json-pointer`, and `regex`. Register additional formats with
`WithFormatValidator`: the checker is registered under the format name it
checks (following `net/http.Handle`, so one implementation can serve several
names), an implementation can carry state such as a compiled regular
expression, and `FormatValidatorFunc` adapts a bare function. Each check
receives the validation run's context and the format name it runs under, the
way an `http.Handler` reads the request path, so a multi-name checker can
tell its names apart and a checker consulting an external system can honor
cancellation. Registering a name again, including a built-in one, replaces
the previous checker.

Each built-in checker asserts its format's defining grammar. Where that grammar
admits more than one reading, or where the string alone cannot decide what the
spec asks, the checker takes the position below. Replace any of them with
`WithFormatValidator` if your application needs the other one.

- **`regex`** is a structural ECMA-262 check, not a compile: balanced groups,
  terminated classes, well-formed escapes. Backreferences and lookaround are
  accepted, since ECMA-262 has them and Go's RE2 does not, and every ASCII
  character is a valid ECMA-262 Annex B identity escape, so `\a` and `\_` are
  accepted (as is a bare `\c`, an Annex B `ExtendedAtom` in its own right).
  The format therefore accepts patterns RE2 rejects. This is independent of the
  `pattern` keyword, which does use RE2; see the deviation note below.
- **`uri-template`** accepts the RFC 6570 `op-reserve` operators (`{=path}`,
  `{!x}`, `{|x*}`) and a prefix modifier on any varspec (`{keys:1}`). Both are
  valid under the RFC's ABNF and fail only during expansion, which a check over
  the template string cannot reach. The literals rule follows errata ID 6937,
  so an apostrophe is a literal.
- **`email`** and **`idn-email`** assert the RFC 5321 `Mailbox` grammar plus the
  §4.5.3.1 size limits (64 octets local, 253 domain, 254 total). The 254-octet
  limit covers the whole address, so a domain long enough to break the
  253-octet limit already breaks the total; only `idn-email` reaches the domain
  limit, counting the domain in its longer A-label form. The RFC 5322 comment,
  folding-whitespace, and obsolete productions that Draft-07's cited §3.4.1
  would permit are rejected. Deliverability is never consulted.
- **`uri`** and **`uri-reference`** accept an IPvFuture authority
  (`http://[v7.x]/`), which RFC 3986 §3.2.2 defines and Go's `net/url` cannot
  parse.
- **`hostname`** accepts a reserved-LDH label (a hyphen in positions 3 and 4, as
  in `ab--cd.com`) per RFC 1123 §2.1, while **`idn-hostname`** rejects it per
  RFC 5890 §2.3.2.2. So `hostname` accepts names `idn-hostname` does not.
  `idn-email` follows `hostname` here: RFC 6531 widens the RFC 5321 domain
  grammar by admitting U-labels rather than importing IDNA's label rules.

### Vocabularies

Draft 2020-12 `$vocabulary` gates which keyword groups run: inactive
vocabularies have their keywords silently skipped. The `$vocabulary` boolean
marks a vocabulary required (`true`) or optional (`false`) for implementations
that do not recognize it and has no impact on ones that do, so a recognized
vocabulary is active whenever its URI is listed, `true` or `false` alike (a
metaschema with `format-assertion: false` still asserts format); a vocabulary
is inactive only when its URI is absent. Vocabulary resolution
priority is `WithVocabularies` (direct override) > `WithMetaSchemaResolver`
(a `RefResolver` consulted with the root schema's `$schema` URI; a
`SchemaMap` serves fixed metaschemas by exact `$id`, and `ChainResolvers`
composes resolvers) > a built-in default set (every
group active except format-assertion). A schema that requires (`true`) a vocabulary
this implementation does not recognize, or marks the 2020-12 core vocabulary
optional or omits it, fails with `ErrUnknownVocabulary`. Draft-07 has no
`$vocabulary`, so all groups stay active and `WithVocabularies` and
`WithMetaSchemaResolver` have no effect.

### Remote references

Local fragment refs (`#/$defs/...`, `#/definitions/...`, `#anchor`) resolve
within the document. Remote and absolute `$ref` URIs are resolved through an
optional `RefResolver` set with `WithRefResolver`. `Compile` resolves every
reference reachable from the root: the first ref naming a remote document
fetches it through the resolver, registers it in the compiled registry, and
vets it. A fetched document persists in the compiled registry, so no later
ref or validation run consults the resolver for it; misses and failures are
negative-cached for the rest of the run that saw them, so an unresolvable
URI costs one resolver call per run however many refs name it. A document
the resolver cannot serve at compile time (a not-resolved answer, or any
other error) does not fail `Compile`: the resolver may serve the document
only after compilation, so the validation walk reports the ref instead. A
fragment that cannot resolve inside a document that is present (the root, or
a fetched document) fails `Compile` with `ErrNotResolved`, since it can
never resolve later.

At validation time, a resolver error surfaces as `ErrRefResolve`; an
unresolvable remote/absolute ref with no resolver is reported as a
`*ValidationError`, and so is an unresolvable local fragment ref inside a
document first fetched during a validation run or inside a JSON-pointer
fallback target, where no compile-time pass vetted it (within a
compile-vetted document such a fragment ref is silently skipped, since
`Compile` already rejects genuinely broken ones). Circular refs are detected
and treated as passing. A document first fetched during a validation run is
vetted with the same structural checks `Compile` applies to compile-time-fetched
documents, and a JSON-pointer fallback target materialized during a run (a
schema carried inside an unknown keyword) is vetted with the same checks
minus the identifier pass, which needs a document base no pointer target
carries; a violation fails the referencing ref with `ErrRefResolve`
wrapping the check's sentinel (`ErrInvalidType`, `ErrNegativeBound`,
`ErrNonPositiveMultipleOf`, `ErrItemsArrayUnderDraft2020`,
`ErrConflictingSchemaFields`, `ErrNilSubschema`, `ErrDuplicatePropertyOrder`,
or, for a fetched document, `ErrInvalidID` or `ErrMisplacedVocabulary`)
instead of silently mis-validating. `Inline`
shares this same vetting policy for the documents it fetches (see
[Inlining references](#inlining-references)). A fetched document, and a
`SubstituteRef` schema, must also hold no pointer cycle, meaning no path that
crosses a schema and returns to a schema or to a container it is already
inside. A container that loops without crossing a schema is left to
`encoding/json`, which reports it as an ordinary error. A cyclic fetched
document fails the referencing ref with `ErrRefResolve` wrapping
`ErrSchemaNotTree`; a cyclic substitute fails with `ErrSchemaNotTree` at the
substitution site. Both sources accept aliasing, unlike a root document,
because every walk that reaches a registered document dedupes pointers. The
boundary refuses a cycle because the JSON-pointer fallback marshals the
document it searches and a cyclic schema graph has no JSON form. Non-local
refs absolutize against the enclosing resource's base URI: its `$id`, or the
root base set with `WithBaseURI`. That base also registers the root document
under its URI, so a ref absolutizing back to it resolves in-memory. The same `WithBaseURI` value serves `Inline`,
so one option configures both.

The resolver receives a context with every resolution call:

```go
type RefResolver interface {
	ResolveRef(ctx context.Context, uri string) (*Schema, error)
}
```

`ErrNotResolved` (or an error wrapping it) is the not-resolved answer,
passing the URI to the next `ChainResolvers` link and ultimately to
unresolvable-ref handling, following `io/fs.ErrNotExist`; any other error
reports a resolution attempt that failed.

Refs resolved while compiling get the `Compile` context; refs reached
during a validation run get that run's `Validate` (or other
entry point) context, so a resolver that fetches over the network can honor
cancellation and deadlines. A compiled `*Validator` never retains a context
(each run carries its own), and the `Must*` entry points pass
`context.Background()`. The package ships no network resolver; fetching
remains the caller's concern. The `WithRefResolver` option value itself serves
both validation and inlining, so one option configures `Compile`, `Validate`,
and `Inline` alike. `RefResolverFunc` adapts a bare function (following
`net/http.HandlerFunc`), so a one-off resolver (a closure over an HTTP
client, for example) needs no named type; `SchemaMap` (a `RefResolver`
serving preloaded schemas from a map keyed by URI) covers fixed sets, and
`ChainResolvers` composes resolvers, with the first answer winning.

## Schema traversal and predicates

Helpers are provided for working with `Schema` values directly, independent of
generation and validation:

```go
// SubschemaEntries returns the direct sub-schemas of s: every non-nil schema
// held by one sub-schema-bearing keyword, each paired with the JSON Pointer
// addressing it from s ("/properties/a", "/allOf/0", "/items"), with map
// children in sorted-key order.
for _, entry := range jsonschema.SubschemaEntries(s) {
	fmt.Println(entry.Pointer, entry.Schema.Type)
}

// Walk visits s and every schema transitively reachable through
// SubschemaEntries, with each schema's Location from the root: the JSON
// Pointer and the typed Segment slice in one value.
err := jsonschema.Walk(s, func(loc jsonschema.Location, s *jsonschema.Schema) error {
	s.Description = "" // strip annotations, rewrite $refs, collect types, ...
	fmt.Println(loc.Pointer, s.Type) // "/properties/a string", ...

	return nil
})

// Schemas is the iterator form of Walk for read-only traversals: the same
// locations and schemas in the same pre-order, to a range loop.
for loc, sub := range jsonschema.Schemas(s) {
	fmt.Println(loc.Pointer, sub.Type)
}
```

`SubschemaEntries` is the package's single source of truth for which `Schema`
fields hold sub-schemas: the applicators (`items`, `prefixItems`,
`additionalItems`, `properties`, `patternProperties`, `additionalProperties`,
`propertyNames`, `allOf`, `anyOf`, `oneOf`, `not`, `if`/`then`/`else`,
`dependentSchemas` and legacy `dependencies`, `contains`, `unevaluated*`,
`contentSchema`) plus the reserved `$defs` and `definitions` locations. Only
typed `Schema` fields are included, not sub-schemas carried as raw JSON in
unknown keywords. Children held in maps are returned in sorted-key order so
traversal is deterministic, and a maintenance test fails when an upstream
`Schema` field addition is not covered. Appending each visited child's
`Pointer` while descending yields the schema path the package's own errors
report. Each entry carries its `Location`, pairing the pointer with the
same location in typed form (`Location.Segments`, one `Segment` per
reference token, mirroring `InstanceSegments` on validation errors): the
member key is verbatim (no `~0`/`~1` escaping to undo) and a list index
is distinguished from a property named like a number, so consumers building
on the location need not re-parse the pointer string.

`Walk` is pre-order: the function runs on a schema before that schema's
children are gathered, so it may replace or mutate sub-schema fields and the
walk follows the updated children. Each distinct schema pointer is visited
once, so aliased or cyclic graphs terminate. `Walk` stops at and returns the
first error from the function; a `nil` schema is a no-op. Returning the
`SkipChildren` sentinel (the `io/fs.SkipDir` convention) prunes the walk at
the current schema and continues with its siblings; the current schema's
sub-schemas are not visited. This suits rewriting passes that splice in a
subtree the walk should not descend into.

The function receives each visited schema's `Location` from the root (the
zero `Location` for the root itself): the JSON Pointer and the typed
`Segment` slice in one value, built by appending each descended child's
`SubschemaEntry` location. Path-tracking traversals need not re-implement
the walk and its cycle guard, and segment-based consumers need not re-parse
the pointer; a traversal with no use for the location ignores the
parameter, following `io/fs.WalkDir`. The segments slice must not be
mutated. A schema reachable through several paths is visited with the first
path the traversal encounters; map-held children walk in sorted-key order,
so that path is deterministic.

`Schemas` yields what `Walk` visits as an `iter.Seq2[Location, *Schema]`,
with the same pre-order, locations, and cycle guard, so a read-only traversal
ranges instead of threading state through a callback, and breaking out of
the loop stops the iteration. Mutating traversals and `SkipChildren` pruning
stay with `Walk`.

Three predicates answer common shape questions:

- `CheckTypeNames(schema)` verifies that every `type` keyword reachable from
  the schema names one of the seven JSON Schema type names, returning `nil` or
  an error wrapping `ErrInvalidType` that includes the schema path of the
  first offending keyword. It is the standalone form of the check `Compile`
  runs before resolution, for vetting structurally messy schemas (cyclic
  graphs, unresolvable references) without compiling them.
- `IsTrueSchema(s)` reports whether `s` is the boolean `true` schema form: a
  schema with no fields set, which marshals to JSON `true` and accepts every
  instance. Annotation-only schemas (a description but no constraints) return
  `false`, as do schemas whose only field is a non-nil empty map or slice
  (`Schema{Enum: []any{}}` vacuously rejects every instance). Returns `false`
  for `nil`.
- `IsFalseSchema(s)` reports whether `s` is the boolean `false` schema form
  `{"not": {}}` (the shape the upstream produces when unmarshaling the JSON
  boolean `false`), which marshals to JSON `false` and rejects every instance.
  Any sibling field next to the `not`, including annotations, defeats the
  form. Returns `false` for `nil`.

## Inlining references

`Inline` returns a deep copy of a schema in which every `$ref` (in the
schema body, `$defs`, and `definitions` alike) is replaced by a copy of the
schema it targets, producing one self-contained document for consumers that
cannot follow references, such as code generators. The input and any
resolver-returned schemas are never mutated.

```go
fsys := os.DirFS("schemas") // main.json references sub/child.json, ...

inlined, err := jsonschema.Inline(ctx, schema,
	jsonschema.WithRefResolver(jsonschema.NewFileResolver(fsys)),
	jsonschema.WithBaseURI("main.json"),
)
```

Resolution mirrors the validator's. Fragment-only refs (`#/pointer`, `#anchor`)
resolve within the enclosing document using the same `$id`/`$anchor` registry
the validator builds, and every ref resolves against its document's original
structure, exactly as the validator would: expanding one ref never changes what
a later ref's JSON Pointer or anchor addresses. Other refs are absolutized
against the enclosing resource's base URI, either its `$id` or the base from
`WithBaseURI`, and then fetched through the `RefResolver` given via
`WithRefResolver`; any fragment is then evaluated against the fetched document.
A schemeless base such as `main.json` is normalized against `file:///`, so RFC
3986 joining is well-defined and a back-reference to the root document finds the
in-memory copy instead of re-fetching it. Fetched documents are inlined
recursively using their own base URIs, so a relative ref inside a fetched
document resolves against that document's URI and files can reference each other
by relative path; each document is fetched at most once per call. `FileResolver`
(constructed with `NewFileResolver`) adapts an `fs.FS`, serving file-path and
relative URIs from the fs root (a leading `file://` scheme and `/` are
stripped); each referenced file must contain a JSON schema document, and `io/fs`
confines resolution to the fs root, so a ref escaping above it returns an error
wrapping `ErrRefResolve`. The same `WithRefResolver` option also serves
file-path and relative refs during validation; refs that absolutize to another
scheme (an http `$id`, for example) are not valid fs paths and resolve to an
error, unless the `StripPrefix` middleware (following `net/http.StripPrefix`)
strips the published remote base from each URI first so those refs can be served
from the fs. `Inline`'s context is passed to the resolver with every document
fetch, so a resolver that fetches over the network can honor cancellation and
deadlines. `Inline` applies its options per call; `NewInliner` applies them once
and the returned `Inliner` is reused, completing the reusable trio with
`Generator` and `Validator`.

`WithRetrievalBase` makes refs resolve against each document's
retrieval URI instead, treating `$id` as an inert annotation: `$id` neither
establishes a base URI nor registers a resolution target, in any document,
including the Draft 7 fragment-only `$id` form that otherwise acts as an
anchor. `$anchor` and `$dynamicAnchor` still resolve within their document,
and `$id` keywords pass through to the output verbatim. Real-world schemas
commonly declare a published remote `$id` while shipping the files their
refs name alongside the schema; under the default RFC behavior those refs
absolutize against the remote `$id` and cannot be served from disk. With
this option the root document's refs absolutize against the base from
`WithBaseURI` and each fetched document's refs against the URI it was
fetched from.

Sibling keywords beside `$ref` follow draft semantics, with the draft
detected from the root schema's `$schema` exactly as the validator detects
it, and a `WithDraft` option overriding the detection the same way (fetched
documents follow the root document's draft, matching how validation applies
one draft throughout):

- **Draft 2020-12**: the node keeps its sibling keywords and the target copy
  joins the node's `allOf`. This preserves both the conjunction and the
  annotation flow the `unevaluated*` keywords depend on, which moving the
  siblings into a separate `allOf` branch would break.
- **Draft 7**: siblings of `$ref` are ignored, so the node is replaced by
  the target copy alone.
- A node whose only keyword is `$ref` is replaced by the target copy alone
  under either draft.

A spliced copy never carries a `$schema` keyword, and the returned root
keeps the input's `$schema`. A spliced copy also carries no `$id`,
`$anchor`, or `$dynamicAnchor` anywhere in its subtree: the names identify
the target at its original position, and duplicating them at each splice
would declare the same identifier several times in one document. The copy
is self-contained, so the names have nothing left to resolve. Refs are
inlined only in typed sub-schema
positions (those `SubschemaEntries` covers); a `$ref` carried as raw JSON inside
an unknown keyword is left as-is, although a ref pointing into such a
position still resolves.

Failure modes:

- A root document whose sub-schema pointers do not form a tree, one `*Schema`
  reached through two paths or through a pointer cycle, returns an error
  wrapping `ErrSchemaNotTree`. This holds the input to the same contract
  `Compile` holds it to, one location per node. A root whose loop closes
  through a value field (`Const`, `Enum`, `Examples`, or `Extra`) returns the
  same error, a shape `Compile`'s own check does not read. A document reached
  through a resolver or a fallback is held to the weaker no-cycle rule instead,
  and a node it shares between two positions is expanded once, at the first
  location the walk reaches. That sharing survives into the result, so an output
  built from an aliased resolver document or substitute is not a tree and
  `Compile` rejects it. Only a hand-built graph can carry such sharing; a parsed
  document never does.
- A ref whose expansion reaches its own target is recursive and returns an
  error wrapping `ErrRefCycle`: a cyclic reference graph has no finite
  expansion.
- A `$dynamicRef` under Draft 2020-12 returns an error wrapping
  `ErrRefInline`, since its target depends on the dynamic scope at validation
  time and no single replacement preserves that (Draft 7 ignores the keyword,
  as the validator does).
- A non-local ref with no resolver configured, or any ref whose target cannot
  be found, returns an error wrapping `ErrRefResolve`.
- A remote document fetched during inlining is structurally vetted before it is
  inlined, through the same policy the validator applies to fetched documents
  (see [Remote references](#remote-references) for the full check list). A
  violation returns an error wrapping `ErrRefResolve` that also wraps the
  check's sentinel (`ErrInvalidType`, `ErrNegativeBound`,
  `ErrNonPositiveMultipleOf`, `ErrItemsArrayUnderDraft2020`,
  `ErrConflictingSchemaFields`, `ErrNilSubschema`, `ErrDuplicatePropertyOrder`,
  `ErrInvalidID`, or `ErrMisplacedVocabulary`), rather than being inlined into
  a malformed output schema. A fetched document holding a pointer cycle fails
  the same way, wrapping `ErrSchemaNotTree`, and a cyclic `SubstituteRef` schema
  returns that sentinel from the substitution site. The fetched document
  follows the root document's draft, so a Draft-07 array-form `items` remote
  inlined under a Draft-07 run is left intact. A JSON-pointer fallback target
  (a schema carried inside an unknown keyword, in the root document or a
  fetched one) is vetted the same way at materialization, minus the identifier
  checks, so an ill-formed target cannot be spliced into the output either.

`WithRefFallback` sets a per-reference failure policy (a `RefFallback`,
with `RefFallbackFunc` adapting a bare function) consulted when
expanding a reference fails for any of those reasons, with a `RefFailure`
carrying the URI of the containing document, the JSON Pointer path of the
referencing schema within that document, the reference value, and the
error; the consultation runs under the `Inline` call's context. The fallback
answers with a `RefAction`: `PropagateRef()` propagates the original error
and ends the `Inline` call, `DropRef()` drops the failing reference keyword
while keeping the node's remaining keywords, and `SubstituteRef(s)` supplies
a substitute schema the reference expands to as if it had resolved there,
with the usual draft sibling semantics. The fallback is
consulted once per failure, at the reference that directly failed: a
failure inside a nested expansion consults the innermost failing ref with
its path in its containing document, and a declined consultation propagates
outward without re-consulting at the enclosing refs. A cycle failure
belongs to the expansion that closed it: a copy truncated by a cycle stays
local to that expansion rather than being reused, so the same source ref
can consult again when another expansion reaches it with a different
in-flight stack. A substitute is
deep-copied before splicing and is itself inlined recursively, its refs
resolving in the context of the document containing the failing ref; a
cycle introduced by the substitute is an ordinary `ErrRefCycle`.

### Inlining options

| Option                    | Effect                                                                                                                                                |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `WithDraft(Draft)`        | Override the draft otherwise detected from the root schema's `$schema`.                                                                               |
| `WithRefResolver(r)`      | Set the `RefResolver` that fetches the documents non-local refs target (called at most once per distinct URI).                                        |
| `WithBaseURI(base)`       | Set the root document's base URI; a schemeless base is normalized against `file:///`. Also serves validation.                                         |
| `WithRetrievalBase(bool)` | Resolve refs against each document's retrieval URI, treating `$id` as an inert annotation that passes through verbatim.                               |
| `WithRefFallback(f)`      | Per-reference failure policy returning a `RefAction`: `PropagateRef()`, `DropRef()`, or `SubstituteRef(s)`. `RefFallbackFunc` adapts a bare function. |

## Errors

| Error                         | Trigger                                                                                                                                                                                                                                               |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ErrUnsupportedType`          | A Go type with no JSON Schema representation (`func`, `chan`, `complex`, `unsafe.Pointer`).                                                                                                                                                           |
| `ErrUnsupportedMapKey`        | A map key that is not a string, integer type, or `encoding.TextMarshaler`.                                                                                                                                                                            |
| `ErrInvalidType`              | A `type` keyword naming something other than the seven JSON Schema type names (returned by `CheckTypeNames` and `Compile`).                                                                                                                           |
| `ErrItemsArrayUnderDraft2020` | The draft-07 array form of `items` used under draft 2020-12, where tuples are spelled with `prefixItems` (returned by `Compile`).                                                                                                                     |
| `ErrConflictingSchemaFields`  | Both Go fields of one JSON keyword set, e.g. `Type`/`Types` or a `dependencies` key in both maps (returned by `Compile`).                                                                                                                             |
| `ErrNilSubschema`             | A nil `*Schema` element inside a sub-schema slice or map (returned by `Compile`).                                                                                                                                                                     |
| `ErrDuplicatePropertyOrder`   | A `PropertyOrder` slice listing the same property twice (returned by `Compile`).                                                                                                                                                                      |
| `ErrSchemaNotTree`            | The root document's sub-schema pointers alias or cycle (`Compile` and `Inline`), a root loop closes through a value field (`Inline`), a fetched document holds a pointer cycle (`Compile` and `Inline`), or a `SubstituteRef` schema does (`Inline`). |
| `ErrInvalidID`                | An `$id` that does not parse, carries a fragment under 2020-12, or does not resolve to an absolute URI (returned by `Compile`).                                                                                                                       |
| `ErrInvalidBaseURI`           | A `WithBaseURI` value that does not parse (returned by `Compile`).                                                                                                                                                                                    |
| `ErrMisplacedVocabulary`      | A `$vocabulary` on a node whose `$schema` does not establish the 2020-12 dialect (returned by `Compile`).                                                                                                                                             |
| `ErrInvalidSchemaDocument`    | A schema document whose top-level value is not a JSON object or boolean (returned by `CompileJSON`, `ParseSchema`, and `ParseSchemaValue`).                                                                                                           |
| `ErrUnknownVocabulary`        | A required `$vocabulary` URI is unrecognized (or 2020-12 core is marked optional).                                                                                                                                                                    |
| `ErrRefResolve`               | A `RefResolver` returns an error resolving a remote `$ref`; in `Inline`, also a non-local ref with no resolver or any unresolvable target.                                                                                                            |
| `ErrRefCycle`                 | `Inline` expands a `$ref` that reaches its own target: the reference graph is cyclic and has no finite expansion.                                                                                                                                     |
| `ErrRefInline`                | `Inline` encounters a reference with no faithful static expansion (`$dynamicRef` under Draft 2020-12).                                                                                                                                                |
| `ErrProviderPanic`            | A `JSONSchemaProvider`/`JSONSchemaExtender` method panics (recovered and wrapped).                                                                                                                                                                    |
| `ErrInvalidDefaultsInstance`  | The `WithDefaultsFrom` instance does not match the generated root type or does not marshal to a JSON object.                                                                                                                                          |

## CLI: `jsonschemagen`

The module ships a build-time code-generation CLI under `cmd/jsonschemagen`,
intended for `//go:generate`. It writes a JSON Schema file for a named Go type
by building a small helper program that imports the target package and calls
`Generate`, reusing the library's generation pipeline. The helper is compiled
inside the target's own module through a build overlay, so module resolution and
checksums are handled by the `go` tool; the module must be able to resolve this
package (via a `require`, a workspace, or a `tool` directive). The helper hands
the schema back through a file rather than its stdout, so anything an `init`
function in the target package (or its dependencies) prints to stdout is
forwarded to stderr instead of corrupting the emitted JSON:

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
this package, and reuses the upstream for exactly one behavior beyond the
type alias: JSON-semantic comparison of hand-built `const`/`enum` values
outside the decoded JSON shapes (via `Equal`, as a recovering fallback).
The alias is an interop commitment. `Schema` is and will remain the upstream
type, so schemas pass directly to and from any package that accepts or
produces `google/jsonschema-go`'s `jsonschema.Schema`, with no conversion. The
package's internal vetted-schema types never appear in the public API; plain
upstream `*Schema` values are what every entry point takes and returns.
Everything else, including structural well-formedness checking at `Compile`
and the `const`/`enum`/`uniqueItems` value comparison for decoded JSON shapes
(exact decimal, with `float64` interpreted at its shortest decimal), is
implemented here.

| Concern                                                       | Implementation                             |
| ------------------------------------------------------------- | ------------------------------------------ |
| Schema data model (`Schema` struct)                           | Upstream (re-exported via type alias)      |
| Structural well-formedness (compile-time checks)              | This package                               |
| `$ref`/`$dynamicRef`/`$anchor` resolution (incl. remote refs) | This package (own URI/anchor registries)   |
| Instance validation walk                                      | This package                               |
| Error types and path tracking                                 | This package                               |
| Format validation                                             | This package (pluggable)                   |
| JSON-semantic value comparison (`const`/`enum`/`uniqueItems`) | This package (upstream `Equal()` fallback) |

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
  `FormatValidator` values, matching the spec's recommendation that format
  validation be optional and configurable.
- **`unevaluatedProperties`/`unevaluatedItems`** are supported, with annotation
  tracking reimplemented in the walk (the generator emits them for Draft 2020-12
  `allOf` composition).
- **Go RE2 for patterns**: `pattern` and `patternProperties` use Go's `regexp`,
  not ECMA 262; this matches the upstream and is a known deviation from the
  spec.
- **`Validator.ValidateJSON` uses `UseNumber`** to preserve the integer-vs-number
  distinction that default `float64` unmarshaling would lose.
- **One operation x shape table for every tag dialect**: the `jsonschema` tag and
  the `validate` tag are two spellings of one constraint vocabulary, so they are
  two grammars over one model (`internal/tagmodel`) rather than two engines. The
  dispatch key is the JSON shape an instance actually takes, not the Go kind, so
  string coercion is a column of the table instead of a gate each operation has
  to remember; the table is a fixed-size array whose every cell is Apply, Ignore,
  or Reject with a written reason, checked at import and pinned by a golden dump.
  Where the dialects genuinely differ -- the numeric-bound literal domain, the
  negative-size question, how a list is spelled, whether a bare key implies a
  value -- the difference is a named parameter or a key-table row, never a second
  implementation.

Two points where the generated schema's model of a Go type differs from what
`encoding/json` emits for that type, each specified in the section that owns it:

- **A direct `encoding/json.Marshaler` is reflected by its Go struct shape**,
  not its marshaled shape, so the schema can reject output `encoding/json`
  produces (see [Customization interfaces](#customization-interfaces)).
- **Draft-07 omits `additionalProperties: false` from the parent under `allOf`
  composition**, which loosens the schema rather than tightening it, so it
  cannot reject valid output (see [Struct field rules](#struct-field-rules)).

### Non-goals

- Full metaschema validation of input schemas. `Compile` checks structure,
  identifiers, keyword domains, and references; validating a schema document
  against its metaschema remains the caller's choice, and the conformance
  tests apply it to generated schemas.
- Code generation _from_ schemas (the reverse direction) is out of scope.
  Forward-direction generation, including the `jsonschemagen` CLI, is supported.

## License

Apache 2.0. See [LICENSE](https://github.com/macropower/x/blob/main/LICENSE).
