// Package jsonschema generates JSON Schema documents from Go types via
// reflection and validates JSON instances against schemas.
//
// It builds on top of [github.com/google/jsonschema-go/jsonschema] and adds
// higher-level features: customization interfaces, pluggable struct tag
// interpretation, Go doc comment extraction, multi-draft support, and
// structured instance validation with full path tracking. The upstream
// [Schema] type is re-exported via type alias, and [Ptr] is provided
// as a convenience helper for creating pointer values (e.g.,
// jsonschema.Ptr(float64(0)) for [Schema.Minimum]), so users need only import
// this package. [Raw] and [MustRaw] marshal Go values for the raw-JSON schema
// fields such as [Schema.Default] (e.g., jsonschema.MustRaw("15m") instead of
// a hand-written [encoding/json.RawMessage] literal); MustRaw panics on a marshal error
// and is intended for values known valid at compile time.
//
// # Entry Points
//
// The primary API is the generic function [GenerateFor]:
//
//	schema, err := jsonschema.GenerateFor[MyType](ctx)
//
// A runtime [reflect.Type] variant is also provided for dynamic use cases:
//
//	schema, err := jsonschema.Generate(ctx, reflect.TypeFor[MyType]())
//
// The context is passed to the [DescriptionProvider] with every comment lookup,
// so the built-in provider's package loading can honor cancellation and
// deadlines. [MustGenerateFor] is [GenerateFor] with [context.Background]
// but panics on error, for package-scope variables and init-time generation
// where for a static type and fixed options generation either always
// succeeds or always fails; [MustGenerate] is its [reflect.Type] form.
//
// Both functions accept any Go type as the root, not just structs. For
// example, [GenerateFor] on string produces {"type": "string"}, and on
// []int produces {"type": ["null", "array"], "items": {"type": "integer"}}.
// The root schema always carries the $schema keyword; sub-schemas and $defs
// entries never do.
//
// # Errors
//
// Sentinel errors are defined for error matching with [errors.Is]:
//
//   - [ErrUnsupportedType]: returned when a Go type has no JSON Schema
//     representation (func, chan, complex, [unsafe.Pointer]).
//   - [ErrUnsupportedMapKey]: returned when a map key type is not string,
//     an integer type, or an [encoding.TextMarshaler].
//   - [ErrProviderPanic]: returned when a [JSONSchemaProvider] or
//     [JSONSchemaExtender] method panics; the panic is recovered and wrapped.
//   - [ErrInvalidDefaultsInstance]: returned when the [WithDefaultsFrom]
//     instance does not match the generated root type or does not marshal to
//     a JSON object.
//
// Errors are wrapped with context so callers see the full path
// (e.g., "field \"data\": unsupported type").
//
// # Configuration
//
// Configuration is via functional [GenerateOption] values passed to [GenerateFor] or
// [Generate]:
//
//   - [WithDraft] sets the target JSON Schema draft ([Draft7] or [Draft2020]).
//     The returned [DraftOption] also serves validation and inlining, where
//     it overrides $schema draft detection (see Draft Support below).
//   - [WithTagInterpreter] registers a [TagInterpreter] under the struct tag
//     key it reads, for mapping struct tags to schema constraints.
//   - [WithDescriptionProvider] sets the [DescriptionProvider] used as the source of
//     type and field descriptions; [NewGoCommentProvider] constructs the
//     AST-backed provider that extracts Go doc comments.
//   - [WithTypeSchema] overrides the schema for a specific Go type;
//     [WithTypeSchemaFor] is its generic form for statically known types.
//   - [WithTypeSchemaResolver] registers a [TypeSchemaResolver] that supplies
//     schemas for whole families of types by predicate, sharing the
//     highest-priority resolution step with [WithTypeSchema].
//   - [WithTypeSchemaExtender] registers a [TypeSchemaExtender] that modifies
//     reflection-generated schemas, the way [JSONSchemaExtender] does for
//     types the caller owns; [WithTypeSchemaExtenderFor] is its generic form
//     for extending one statically known type.
//   - [WithNamer] sets a custom definition namer (a [Namer], with [NamerFunc]
//     adapting a bare function); an empty name defers to the built-in namer,
//     so a partial namer renames only the types it recognizes.
//   - [WithDefinitions] controls $defs/$ref extraction (default: true).
//   - [WithAdditionalProperties] controls whether extra keys are allowed on
//     object schemas (default: false, disallowing extra keys).
//   - [WithNullable] controls whether nil-able types (pointers, slices, maps,
//     []byte) accept null (default: true; false drops the null branch).
//   - [WithDefaultsFrom] seeds root property defaults from an instance of the
//     generated type: after generation the instance is marshaled with
//     encoding/json and each top-level key of the output that matches a root
//     property becomes that property's default, overwriting tag defaults.
//     Keys the json tags omit (omitempty, omitzero) contribute nothing, so
//     presence follows the json tags exactly; nested struct, slice, and map
//     values become whole-value defaults on their top-level property. An
//     instance whose pointer-dereferenced type is not the generated type, or
//     that does not marshal to a JSON object, returns an error wrapping
//     [ErrInvalidDefaultsInstance]. A pointer root's nullable anyOf wrapper
//     is resolved to its value branch first, so the defaults reach the
//     object schema (or its $defs entry) inside. When a self-referential
//     root stays in $defs, the defaults apply to that definition, shared by
//     every recursive occurrence. Under [Draft7], a default landing on a
//     $ref'd property moves the $ref into an allOf wrap, the same shape tag
//     defaults produce, because Draft-07 readers ignore $ref siblings.
//   - [WithRootTitle] sets the root schema's title to the root type's name
//     when no title is otherwise present (default: false). The [WithNamer]
//     namer is honored and the root type is pointer-dereferenced first;
//     unnamed roots (anonymous structs, unnamed maps and slices) stay
//     untitled, and an existing title (from [WithTypeSchema],
//     [JSONSchemaProvider], or [JSONSchemaExtender]) is never overwritten.
//     Under [Draft7], a self-referential root stays a bare $ref into
//     definitions, where a sibling title would be ignored; the title is set
//     on the definitions entry instead, shared by every occurrence of the
//     type.
//
// Across every entry point, an option given a nil interface or pointer value
// restores the default behavior: [WithNamer] the built-in namer,
// [WithDescriptionProvider] no descriptions, [WithRefResolver] local-only ref
// resolution, [WithRefFallback] fatal expansion failures, and
// [WithTypeSchema] with a nil schema the type's default resolution
// (unregistering earlier exact registrations for the type). The exception is
// additive registrations that a nil cannot identify anything to remove from
// ([WithTagInterpreter], [WithTypeSchemaResolver], [WithTypeSchemaExtender],
// [WithFormatValidator]); these ignore a nil registration.
//
// # Type Mapping
//
// Go types are mapped to JSON Schema types following these rules:
//
//   - Primitives: string, bool, int, float32/float64 map directly. Bounded
//     integers (int8, int16, int32, int64, uint8, uint16, uint32, uint64)
//     include minimum/maximum constraints. The platform-dependent unsigned
//     types uint and uintptr have minimum: 0 only.
//   - Pointers: *T produces a nullable schema by wrapping the base schema in an
//     anyOf with a {"type": "null"} branch. Multiple levels of pointer
//     indirection (e.g., **T) are treated identically to *T. Pointers to
//     unrestricted types (*interface{}, *[encoding/json.RawMessage]) produce an
//     unrestricted schema ({}) since it already permits null.
//   - Slices: []T produces a nullable array with items schema. []byte is a
//     special case producing a nullable base64-encoded string.
//   - Arrays: [N]T produces a fixed-size array with minItems/maxItems = N.
//   - Maps: map[K]V produces a nullable object with additionalProperties.
//     K must be string, an integer type, or implement [encoding.TextMarshaler];
//     other key types return [ErrUnsupportedMapKey].
//   - Interfaces: any interface type produces an unrestricted schema ({}).
//   - Structs: produce objects with properties, required, and
//     additionalProperties: false by default.
//
// The "nullable" behavior of pointers, slices, maps, and []byte above is the
// default; [WithNullable](false) drops the null branch so *T yields the bare
// value schema, []T yields {"type":"array"}, and map yields {"type":"object"}.
//
// Well-known types have built-in overrides: [time.Time] maps to
// {"type": "string", "format": "date-time"}, [encoding/json.RawMessage] to {},
// [encoding/json.Number] to {"type": "number"}, [math/big.Int] to
// {"type": "integer"} (its MarshalJSON emits a bare number), and
// [math/big.Rat], [math/big.Float] to {"type": "string"} with a numeric
// pattern constraint. Built-in overrides are matched by exact [reflect.Type],
// so named types wrapping a built-in-overridden type (e.g., type MyTime
// [time.Time]) do not receive the override and fall through to subsequent
// resolution steps. [net/url.URL] has no override: it implements no marshaler
// interface, so it reflects as the struct object encoding/json actually emits.
//
// Types implementing [encoding.TextMarshaler] map to {"type": "string"},
// checked before struct reflection.
//
// Unsupported types (func, chan, complex, [unsafe.Pointer]) return
// [ErrUnsupportedType].
//
// Go type aliases (defined with =) are invisible to reflection and are treated
// as their underlying type. Only defined types (e.g., type MyString string)
// produce distinct names.
//
// # Type Resolution Priority
//
// For each type, the schema is determined by the first matching step:
//
//  1. Registered [TypeSchemaResolver] values ([WithTypeSchemaResolver], and the
//     exact-match resolvers [WithTypeSchema] registers), consulted newest
//     registration first (highest priority).
//  2. [JSONSchemaProvider] interface.
//  3. Built-in overrides ([]byte, [time.Time], [encoding/json.Number], etc.).
//  4. Marshaler methods promoted from an embedded field: a promoted
//     [encoding/json.Marshaler] makes the schema unrestricted ({}), and a
//     promoted [encoding.TextMarshaler] makes it {"type": "string"}.
//  5. [encoding.TextMarshaler] interface (direct implementation).
//  6. Kind-based reflection.
//
// A direct [encoding/json.Marshaler] implementation is not in this chain.
// Types directly implementing only [encoding/json.Marshaler] (not
// [encoding.TextMarshaler]) are handled by kind-based reflection, since
// MarshalJSON can return any JSON type — the output type is unknowable via
// reflection. Specific well-known [encoding/json.Marshaler] types are handled
// via built-in overrides. Other [encoding/json.Marshaler] types should use
// [WithTypeSchema] or implement [JSONSchemaProvider]. A marshaler promoted
// from an embedded field is different (step 4): encoding/json resolves
// marshalers through the method set, so the promoted method serializes the
// whole outer struct and reflecting its fields would describe a shape that
// never appears in the output.
//
// # Customization Interfaces
//
// Types may implement [JSONSchemaProvider] to supply their own schema entirely,
// bypassing reflection. Types may implement [JSONSchemaExtender] to modify the
// reflection-generated schema after it is built. If both are implemented, only
// [JSONSchemaProvider] is used. Both value and pointer receivers are checked.
//
// When a registered resolver ([WithTypeSchemaResolver] or [WithTypeSchema]) or
// [JSONSchemaProvider] provides the schema, [JSONSchemaExtender] is not
// called.
//
// [JSONSchemaExtender] requires owning the type. For types the caller does
// not own, [WithTypeSchemaExtender] registers a [TypeSchemaExtender] that
// runs at the same point in the pipeline, after the type's own
// JSONSchemaExtend, under the same not-called-when-replaced rule.
//
// Every single-method extension-point interface has a conversion func type
// adapter following [net/http.HandlerFunc] ([TagInterpreterFunc],
// [FormatValidatorFunc], [TypeSchemaResolverFunc], [TypeSchemaExtenderFunc],
// [RefResolverFunc], [NamerFunc], [RefFallbackFunc]). [DescriptionProvider],
// the one two-method interface, has the struct adapter
// [DescriptionProviderFuncs] instead, whose nil fields answer "". An
// interface serving a named registration ([TagInterpreter] for a struct tag
// key, [FormatValidator] for a format name) takes the name at the
// registration site ([WithTagInterpreter], [WithFormatValidator]), following
// [net/http.Handle], so one implementation can serve several names.
//
// # Tag Interpretation
//
// All struct tag interpretation beyond the json and jsonschema tags is handled
// through the pluggable [TagInterpreter] interface. Interpreters receive the
// Generate call's context, like the other generation-time hooks, and a
// [FieldContext] containing the field's schema, parent schema, JSON name, Go
// type, full [reflect.StructField] (for reading sibling struct tags such
// as the json tag's options), and the target [Draft] (for emitting
// draft-appropriate keywords). Each interpreter is registered under the
// struct tag key it reads; multiple interpreters can be registered and are
// applied in order. [TagInterpreterFunc] adapts a bare function, so a
// one-off interpreter needs no named type.
//
// # Definitions and References
//
// By default, named struct types and named types implementing
// [JSONSchemaProvider] or [JSONSchemaExtender] are extracted into $defs (or
// definitions for [Draft7]) and referenced via $ref. Named primitive and
// composite types (e.g., type Tags []string) without these interfaces are
// inlined. Anonymous struct types are always inlined. Circular types are
// detected and resolved via $ref even when definitions are disabled.
//
// Name collisions are automatically disambiguated using the package's base
// directory name, then the full import path if needed. For generic type
// instantiations, brackets and commas in [reflect.Type.Name] are replaced
// with underscores for use as $defs keys (e.g., "MyStruct[int]" becomes
// "MyStruct_int_"). The [WithNamer] option can override this behavior.
//
// Nullable references (pointer to a $ref'd type) use anyOf wrapping:
// {"anyOf": [{"$ref": "..."}, {"type": "null"}]}.
//
// All $defs entries are placed at the root schema level only, never nested
// within sub-schemas. The root type's own schema is placed directly in the
// root schema object unless it is self-referential (recursive), in which
// case its schema is in $defs and the root uses $ref.
//
// # Struct Field Rules
//
// Struct fields follow [encoding/json] conventions: the json tag determines
// the field name, json:"-" excludes the field (but json:"-," with a trailing
// comma uses the literal name "-" as the JSON key), omitempty and omitzero
// omit the field from required, and json:",string" overrides the field schema
// to {"type": "string"} for applicable types (string, integer, float, bool).
// Unexported non-embedded fields are excluded; unexported embedded struct
// types still have their exported fields promoted.
//
// Embedded structs without a json tag have their fields promoted into the
// parent schema. Embedded pointer-to-struct (no json tag) is treated
// identically, except the promoted fields are not required: a nil embed omits
// them from the output. A struct whose method set includes a MarshalJSON or
// MarshalText promoted from an embedded field is not reflected field by field
// at all — the promoted marshaler serializes the whole outer value (see the
// resolution priority above). Embedded types intercepted by earlier priority
// chain steps (a registered [TypeSchemaResolver] or [JSONSchemaProvider]) are
// composed via allOf rather than having their fields promoted; an embed
// reached through a pointer composes as anyOf[schema, {}] instead, since a
// nil pointer contributes nothing to the marshaled object. A resolver or
// [JSONSchemaProvider] schema used for such an embedded type must leave the
// object open (no additionalProperties: false): allOf evaluates each branch
// against the whole object, so a closed branch rejects the parent's sibling
// properties and the generated schema then rejects the struct's own marshaled
// JSON. Embedded structs with an explicit json name
// (e.g. json:"base") are treated as regular named fields, not promoted; an
// options-only tag with no name (e.g. json:",omitempty") promotes the fields,
// matching encoding/json.
//
// Embedded non-struct named types (e.g., type MyString string) are treated as
// regular fields with the type name as the JSON key, not promoted. Embedded
// pointer-to-non-struct types are handled the same way, with the pointer
// adding nullability.
//
// Embedded interface types produce an unrestricted schema ({}) since their
// concrete type is unknowable at compile time. If the interface type
// implements [JSONSchemaProvider], that schema is composed via allOf.
//
// Field shadowing and ambiguity follow [encoding/json] rules: outer fields
// shadow inner fields of the same name, and ambiguous fields at the same
// depth are silently dropped.
//
// When allOf composition is used, [Draft2020] uses unevaluatedProperties: false
// instead of additionalProperties: false on the parent. [Draft7] omits
// additionalProperties: false from the parent when allOf is in use.
//
// Property ordering in the output matches the order fields appear in the Go
// struct definition (via the upstream PropertyOrder field). Empty structs and
// structs with no exported fields produce
// {"type": "object", "additionalProperties": false} with no properties or
// required fields.
//
// # jsonschema Struct Tag
//
// The jsonschema struct tag sets schema properties directly on a field. A bare
// value (no = sign) is treated as a description. Key-value pairs are
// comma-separated:
//
//	Port int `jsonschema:"description=Server port,minimum=1,maximum=65535"`
//
// Supported keys include type, description, title, default, examples,
// deprecated, readOnly, writeOnly, minimum, maximum, exclusiveMinimum,
// exclusiveMaximum, multipleOf, minLength, maxLength, pattern, format,
// minItems, maxItems, uniqueItems, minProperties, maxProperties, enum, and
// const.
//
// The type key overrides the reflected type entirely: it must name one of
// the seven JSON Schema types, and it removes the nullable anyOf wrapper a
// pointer field generates plus, when the new type is not numeric, the
// numeric bounds derived from the Go kind. A pointer [time.Duration] field
// with jsonschema:"type=string,pattern=..." therefore produces a clean string
// schema without needing [JSONSchemaExtender]. Tag pairs apply in order;
// keys after type= still take effect.
//
// Values for default, const, enum, and examples are parsed using type-aware
// parsing based on the field's Go type. Enum and examples values are
// separated by "|". Unrecognized keys are a parse error. A value containing a
// comma escapes it with a backslash (a literal backslash is "\\"), so
// jsonschema:"description=Hello\, World" sets the description "Hello, World";
// enum and examples values cannot contain "|" (used as value separator). For
// complex values, use [JSONSchemaExtender] or AST doc comments with
// [WithDescriptionProvider].
//
// Because pairs apply in order, a default, const, enum, or examples value
// appearing after a type= pair parses against the overridden JSON type
// rather than the field's Go type: string, integer, number, and boolean
// overrides parse subsequent scalar values as that type, so a [time.Duration]
// field with jsonschema:"type=string,default=15m" yields
// {"type":"string","default":"15m"} where the Go int64 kind would have
// rejected "15m". The same keys before the type= pair still parse against
// the Go type. After an override to array, object, or null there is no
// scalar type to parse against, so those keys are an error, and the literal
// value null is rejected after any override (the overridden type is never
// nullable). An enum after a type= override always constrains the value
// schema itself: the slice/array redirection that normally sends enum values
// to the item schemas keys on the scalar-parse type, which an override
// replaces with a non-sequence stand-in.
//
// On a slice or array field, enum constrains each element rather than the
// array value: the values parse against the element type and land on the item
// schemas. Nested sequences descend to the innermost element schema. Const,
// default, and examples remain whole-value constraints and are still errors
// on sequence fields, as is enum on []byte (a base64 string with no item
// schema).
//
// # Comment Extraction
//
// Type and field descriptions come from a [DescriptionProvider], registered
// with [WithDescriptionProvider]. The built-in [GoCommentProvider] (constructed
// with [NewGoCommentProvider]) extracts Go doc comments from source files
// for struct types, struct fields, and named types using [go/ast] and
// [golang.org/x/tools/go/packages]; when source files cannot be located for
// a type, extraction is silently skipped. Package loading runs in the
// process working directory unless [WithLoadDir] points it at another
// module's directory. Any other implementation
// substitutes another source — comments pre-extracted at build time for a
// binary that deploys without source files, or fixed descriptions in tests
// — and decides its own failure behavior. [ChainDescriptionProviders]
// composes providers, first non-empty description wins, such as overrides
// for specific types backed by AST extraction.
// For a field promoted from an embedded struct, the provider
// receives the embedded type, where the field's doc comment lives. The
// jsonschema struct tag description overrides a provider-supplied comment.
//
// # Draft Support
//
// [Draft7] and [Draft2020] (the default) are supported. The draft affects the
// $schema URI, keyword selection (definitions vs $defs), $ref sibling
// behavior, and additionalProperties/unevaluatedProperties handling with allOf
// composition. In [Draft7], when a $ref'd field has additional annotations
// from struct tags, the $ref is wrapped in an allOf. In [Draft2020], sibling
// keywords are placed directly alongside $ref.
//
// The [WithDraft] option serves generation, validation, and inlining alike:
// generation targets the given draft, while validation and [Inline] use it
// in place of the draft they otherwise detect from the root schema's $schema
// field, for schema documents that omit $schema or carry one that does not
// reflect their dialect.
//
// # Processing Order
//
// Schema generation involves two levels of processing:
//
// Type-level processing is executed once per type, producing the type's
// canonical schema: (1) base type reflection via the priority chain,
// (2) comment extraction if enabled, (3) [JSONSchemaExtender] if implemented,
// (4) registered [TypeSchemaExtender] values in registration order.
//
// Field-level processing is executed per struct field, after the field's type
// schema is resolved: (1) json:",string" override, (2) comment extraction,
// (3) jsonschema struct tag, (4) registered tag interpreters in order.
// Field-level processing always applies, including when the type is referenced
// via $ref.
//
// # Validation
//
// The package validates JSON instances against schemas and returns structured
// errors with full path information and hierarchical multi-error support.
// Two one-shot entry points are provided:
//
//   - [Validate] validates a pre-parsed Go value (map[string]any, []any,
//     string, float64, [encoding/json.Number], bool, nil). Go numeric kinds
//     that encoding/json does not produce — the signed and unsigned integer
//     types and float32 — are accepted too and normalized via [Normalize], so
//     values decoded from YAML or TOML validate directly (integers exactly,
//     at any magnitude).
//   - [ValidateJSON] unmarshals raw JSON bytes with [encoding/json.Decoder]
//     using UseNumber() to preserve integer vs number distinction, then
//     validates.
//
// Both compile the schema on every call. To validate many instances against the
// same schema, call [Compile] once and reuse the returned [Validator]: it
// performs the per-schema work (registry construction, Schema.Resolve, draft and
// vocabulary detection) up front and is safe for concurrent use. [MustCompile]
// is [Compile] but panics on error, for package-scope validators where for a
// static schema and fixed options compilation either always succeeds or always
// fails; it follows [MustGenerateFor], and [MustCompileJSON] is its
// [CompileJSON] counterpart (such as for embedded schema files).
//
// A schema arriving as a JSON document rather than a [*Schema] has symmetric
// entry points. [CompileJSON] decodes data as a single JSON schema document
// (numbers as [encoding/json.Number], trailing data rejected) and compiles it
// with [Compile]; [ParseSchema] is its decode half alone, returning the
// [*Schema] uncompiled for consumers that work with the schema itself ([Inline],
// [Walk], programmatic editing); [ParseSchemaValue] converts an already-decoded
// document — a bool or a map[string]any, such as [Normalize] output — to a
// [*Schema]. With all three, a top-level value that is not an object or
// boolean, including JSON null (which unmarshaling into a [Schema] directly
// silently coerces to the false schema), returns an error wrapping
// [ErrInvalidSchemaDocument]; malformed JSON returns the wrapped decode error
// without the sentinel.
//
// Every compile and validate entry point takes a [context.Context] as its
// first parameter, carried to the [RefResolver] (see Remote References
// below); the Must* forms pass [context.Background], the right context for
// the package-scope use they serve.
//
// On success all return nil. A validation failure returns an error that
// unwraps to [*ValidationError] via [errors.As]. Non-validation failures — JSON
// decoding, an unaccepted instance type, an invalid schema document
// ([ErrInvalidSchemaDocument]), Schema.Resolve errors, [ErrInvalidType], and
// [ErrUnknownVocabulary] — return ordinary wrapped errors that do not unwrap
// to [*ValidationError].
//
// Compile rejects a type keyword naming anything other than the seven JSON
// Schema types ("null", "boolean", "string", "integer", "number", "object",
// "array") with an error wrapping [ErrInvalidType], so a typo'd type surfaces
// at construction instead of silently rejecting every instance. The same
// check is exported standalone as [CheckTypeNames] (see Schema Traversal and
// Predicates below); Compile routes through it, so the two produce textually
// identical errors.
//
// Instance numbers are compared exactly (decoded with UseNumber, compared as
// [math/big.Rat]), with one bound on the work an adversarial literal can demand:
// for a JSON number whose exact value exceeds an internal cap (about 4096
// significant digits or decimal exponent magnitude), the multipleOf check is
// skipped, while minimum, maximum, exclusiveMinimum, and exclusiveMaximum are
// still enforced exactly. Schema-side numeric keyword values are limited to
// float64 precision: integers beyond 2^53 in keywords like const, minimum, or
// multipleOf round when the schema is decoded, even though the instance value
// they are compared against is exact.
//
// Validation is configured via [ValidateOption] values:
//
//   - [WithDraft] overrides the draft otherwise detected from the root
//     schema's $schema field, for schemas that omit $schema (which would
//     default to [Draft2020]) or carry one that does not reflect their
//     dialect.
//   - [WithRefResolver] sets a [RefResolver] for resolving remote $ref URIs.
//     The resolver is called only when local fragment resolution fails. Resolved
//     schemas are cached within the validation run. The resolver receives the
//     caller's context (see Remote References below).
//   - [WithBaseURI] sets the root document's base URI, the base its
//     non-local refs absolutize against when no root $id establishes one,
//     and registers the root under it so a ref absolutizing back to the
//     root resolves in-memory. The returned [RefOption] also serves
//     inlining, the way [WithRefResolver] and [WithDraft] serve several entry
//     points.
//   - [WithFormatValidator] registers a custom format checker (a
//     [FormatValidator]) under the format name it checks, with
//     [FormatValidatorFunc] adapting a bare function.
//   - [WithFormats] forces built-in format assertion on or off. By default
//     format is asserted under Draft-07 and is annotation-only under Draft
//     2020-12 unless the format-assertion vocabulary is active.
//   - [WithContent] asserts contentEncoding (base64) and contentMediaType
//     (application/json) for string instances. Annotation-only by default.
//   - [WithResolveOptions] passes [ResolveOptions] (an alias for the upstream
//     options type, so no second import is needed) for structural
//     pre-validation.
//   - [WithVocabularies] directly specifies the active vocabularies for the
//     validation run: the listed URIs are active, every other vocabulary is
//     inactive.
//   - [WithMetaSchemaResolver] sets a [RefResolver] consulted with the root
//     schema's $schema URI to look up the metaschema whose $vocabulary map
//     controls which keyword groups are active: a [SchemaMap] serves fixed
//     metaschemas by exact $id, and [ChainResolvers] composes resolvers.
//
// The draft is detected from the root schema's $schema field; a [WithDraft]
// option overrides the detection. [Draft7] and
// [Draft2020] semantics are applied for keyword selection (items/additionalItems
// vs prefixItems/items; dependentRequired/dependentSchemas under 2020-12) and
// $ref sibling behavior (Draft-07 ignores siblings; 2020-12 processes them). The
// legacy dependencies keyword is honored under both drafts: under 2020-12 it is
// accepted for backward compatibility alongside dependentRequired/dependentSchemas.
//
// All validation failures are collected — validation does not stop on the first
// error. The returned [*ValidationError] forms a tree: compositional keywords
// (allOf, anyOf, oneOf, if/then/else, $ref, $dynamicRef, unevaluated*) wrap their
// child failures in intermediate [ValidationError.Causes] nodes, while container
// keywords (properties, items, additionalProperties) flatten child failures into
// the parent's Causes, each retaining its full instance and schema path. The not
// keyword produces a childless leaf error. A false subschema failure ("value is
// not allowed") carries the applicator keyword that applied it (for example
// additionalProperties for additionalProperties: false); a standalone boolean
// false schema has no applicator context and leaves Keyword empty. A
// propertyNames violation constrains a key, which has no JSON Pointer of its
// own, so it borrows the property's location: the surfaced error carries
// Keyword "propertyNames" and an InstancePath pointing at the offending
// property, with the inner keyword failure in its Causes.
//
// Alongside the InstancePath and SchemaPath JSON Pointers, every error
// produced by validation carries both paths in typed form:
// [ValidationError.InstanceSegments] and [ValidationError.SchemaSegments]
// return one [Segment] per reference token, each marked as an object key or
// an array index. The pointer strings cannot distinguish array index 1 from
// an object property named "1" (or an allOf branch from a property named
// "allOf"'s member), and member keys carry ~0/~1 escaping; the segments
// resolve both, so source-mapping consumers need not re-parse the pointers
// and guess. Hand-constructed errors return nil from both.
//
// The keyword names validation reports are exported as Keyword* constants
// ([KeywordRequired], [KeywordRef], ...), so code branching on
// [ValidationError.Keyword] needs no raw keyword strings.
//
// Built-in format checkers are provided for: date-time, date, time, duration,
// email, idn-email, hostname, idn-hostname, uri, uri-reference, uri-template,
// iri, iri-reference, uuid, ipv4, ipv6, json-pointer, relative-json-pointer, and
// regex.
//
// # Schema Traversal and Predicates
//
// Helpers are provided for working with [Schema] values directly, independent
// of generation and validation:
//
//   - [SubschemaEntries] returns the direct sub-schemas of a schema: every
//     non-nil schema reachable through one sub-schema-bearing keyword
//     (applicators such as items, properties, allOf, not, if/then/else, plus
//     $defs and definitions), each paired with the RFC 6901 JSON Pointer
//     addressing it from the parent ("/properties/a", "/allOf/0", "/items").
//     Children held in maps are returned in sorted-key order so traversal is
//     deterministic, and appending each visited child's pointer while
//     descending yields the schema path the package's own errors report.
//     Each entry also carries the same location in typed form
//     ([SubschemaEntry.Segments], one [Segment] per reference token,
//     mirroring [ValidationError.InstanceSegments]), so consumers need not
//     re-parse the pointer string. It is the package's single source of
//     truth for which Schema fields hold sub-schemas.
//   - [Walk] calls a function for a schema and every schema transitively
//     reachable through [SubschemaEntries], pre-order: the function runs on a
//     schema before its children are gathered, so it may replace or mutate
//     sub-schema fields and the walk follows the updated children. Each
//     distinct schema pointer is visited once, so aliased or cyclic graphs
//     terminate. Walk stops at and returns the first error from the function,
//     except [SkipChildren], which prunes the walk at that schema and
//     continues with its siblings. The function receives each visited
//     schema's location from the root in both synchronized forms: the JSON
//     Pointer and the typed [Segment] slice, built by appending each
//     descended child's [SubschemaEntry.Pointer] and
//     [SubschemaEntry.Segments]; a traversal with no use for the location
//     ignores the parameters, following [io/fs.WalkDir].
//   - [CheckTypeNames] verifies that every type keyword reachable from a
//     schema names one of the seven JSON Schema type names, returning nil or
//     an error wrapping [ErrInvalidType] that includes the schema path of the
//     first offending keyword. It is the standalone form of the check
//     [Compile] runs before resolution, for vetting structurally messy
//     schemas — cyclic graphs, unresolvable references — without compiling
//     them.
//   - [IsTrueSchema] reports whether a schema is the boolean true schema form:
//     a schema with no fields set, which marshals to JSON true and accepts
//     every instance. Annotation-only schemas (a description but no
//     constraints) return false, as do schemas whose only field is a non-nil
//     empty map or slice (Schema{Enum: []any{}} vacuously rejects every
//     instance).
//   - [IsFalseSchema] reports whether a schema is the boolean false schema
//     form {"not": {}} — the shape the upstream produces when unmarshaling the
//     JSON boolean false — which marshals to JSON false and rejects every
//     instance. Any sibling field next to the not, including annotations,
//     defeats the form.
//
// # Vocabulary Support
//
// Draft 2020-12 introduces $vocabulary, which appears in metaschemas and maps
// vocabulary URIs to booleans indicating whether each vocabulary is required
// (true) or optional (false). The validator respects $vocabulary to gate keyword
// groups: when a vocabulary is inactive, its keywords are silently skipped.
//
// The format-assertion vocabulary is an exception: because this implementation
// recognizes it, its presence in a $vocabulary map asserts format regardless of
// the true/false value. The boolean only governs implementations that do not
// understand the vocabulary, so a metaschema with format-assertion: false still
// asserts format here.
//
// Vocabulary resolution follows this priority:
//  1. [WithVocabularies] direct override (highest).
//  2. [WithMetaSchemaResolver] lookup — the resolver is consulted once per
//     compile with the root schema's $schema URI; a miss (ok false) falls
//     through to the default. A [SchemaMap] serves fixed metaschemas by
//     exact $id, and [ChainResolvers] composes resolvers.
//  3. Default: a built-in standard vocabulary set — every group active except
//     format-assertion, so format is annotation-only by default.
//
// If a schema requires (marks true) a vocabulary URI that this implementation
// does not recognize, [Validate] returns [ErrUnknownVocabulary]. The same error
// is returned when a $vocabulary map marks the 2020-12 core vocabulary as
// optional (false), which the spec does not permit.
//
// # Remote References
//
// By default only local fragment refs are resolved during validation (those
// under #/$defs or #/definitions). Remote and absolute $ref URIs are resolved
// via an optional [RefResolver] set with [WithRefResolver]; [RefResolverFunc]
// adapts a bare function, so a one-off resolver needs no named type. A resolver
// reports a URI it does not serve with ok false, the not-resolved answer that
// passes the URI along (to the next [ChainResolvers] link, and ultimately to
// unresolvable-ref handling). An unresolvable remote or absolute $ref is
// reported as a [*ValidationError] by the validation walk: with no resolver
// (or a resolver reporting ok false) the message begins with
// "cannot resolve $ref" and includes the quoted ref, while a resolver that
// returns an error yields one wrapping [ErrRefResolve]. Only an unresolvable
// local fragment ref is silently skipped.
// Circular refs are detected and treated as passing to avoid infinite recursion.
//
// Non-local refs absolutize against the enclosing resource's base URI — its
// $id, or the root base set with [WithBaseURI], which also registers the
// root document under that URI so a ref absolutizing back to it resolves
// in-memory. The same [WithBaseURI] value serves [Inline] (see Reference
// Inlining below), so one option configures both.
//
// The resolver receives a context with
// every resolution call: the [Compile] context for refs resolved while
// compiling, and the [Validator.Validate] (or other validation entry point)
// context for refs reached during that validation run, so a resolver that
// fetches over the network can honor cancellation and deadlines. The
// context is never retained by a compiled [Validator] — each run carries its
// own — and the Must* entry points pass [context.Background]. The package
// ships no network resolver; fetching remains the caller's concern.
//
// # Reference Inlining
//
// [Inline] returns a deep copy of a schema in which every $ref — in the
// schema body, $defs, and definitions alike — is replaced by a copy of the
// schema it targets, producing a single self-contained document for
// consumers that cannot follow references, such as code generators. The
// input and any resolver-returned schemas are never mutated.
//
// Fragment-only refs (#/pointer, #anchor) resolve within the enclosing
// document using the same $id/$anchor registry the validator builds, and
// every ref resolves against its document's original structure, exactly as
// the validator would: expanding one ref never changes what a later ref's
// JSON Pointer or anchor addresses. Other refs are absolutized against the
// enclosing resource's base URI — its $id, or the base given via
// [WithBaseURI], with a schemeless base normalized against file:///
// so a back-reference to the root document finds the in-memory copy — and
// fetched through the [RefResolver] given via [WithRefResolver]; any
// fragment is then evaluated against the fetched document. Fetched
// documents are inlined recursively using their own base URIs (a relative
// ref inside a fetched document resolves against that document's URI, so
// files can reference each other by relative path), and each document is
// fetched at most once per call. [FileResolver] (constructed with
// [NewFileResolver]) adapts an [io/fs.FS] to this interface, serving
// file-path and relative URIs from the fs root;
// each referenced file must contain a JSON schema document, and [io/fs]
// confines resolution to the fs root, so a ref escaping above it returns
// an error wrapping [ErrRefResolve]. Pair [os.DirFS] with
// [WithBaseURI] to inline a directory of schemas; the same resolver
// also serves file-path and relative refs during validation via
// [WithRefResolver]. [StripPrefix] wraps any resolver to strip a published
// remote base from each URI first, so refs absolutizing against an https
// $id can be served from the fs. Inline's context is passed to the resolver with every
// document fetch, so a resolver that fetches over the network can honor
// cancellation and deadlines.
//
// [WithRetrievalBase] makes refs resolve against each document's
// retrieval URI instead, treating $id as an inert annotation: $id neither
// establishes a base URI nor registers a resolution target, in any
// document, including the Draft 7 fragment-only $id form that otherwise
// acts as an anchor. $anchor and $dynamicAnchor still resolve within their
// document, and $id keywords pass through to the output verbatim.
// Real-world schemas commonly declare a published remote $id while
// shipping the files their refs name alongside the schema; under the
// default RFC behavior those refs absolutize against the remote $id and
// cannot be served from disk. With this option the root document's refs
// absolutize against the base from [WithBaseURI] and each fetched
// document's refs against the URI it was fetched from.
//
// Sibling keywords beside $ref follow draft semantics, with the draft
// detected from the root schema's $schema exactly as the validator detects
// it, and a [WithDraft] option overriding the detection the same way
// (fetched documents follow the root document's draft, matching how
// validation applies one draft throughout). Under Draft 2020-12 the node
// keeps its sibling keywords and the target copy joins the node's allOf,
// preserving both the conjunction and the annotation flow the unevaluated*
// keywords depend on. Under Draft 7 siblings of $ref are ignored, so the
// node is replaced by the target copy alone, as it also is under either
// draft when $ref is the node's only keyword. A spliced copy never carries
// a $schema keyword, and the returned root keeps the input's $schema. Refs
// are inlined only in typed sub-schema positions (those [SubschemaEntries]
// covers); a $ref carried as raw JSON inside an unknown keyword is left
// as-is, although a ref pointing into such a position still resolves.
//
// A ref whose expansion reaches its own target — a recursive schema —
// returns an error wrapping [ErrRefCycle]: a cyclic reference graph has no
// finite expansion. A $dynamicRef under Draft 2020-12 returns an error
// wrapping [ErrRefInline], since its target depends on the dynamic scope at
// validation time and no single replacement preserves that (Draft 7 ignores
// the keyword, as the validator does). A non-local ref with no resolver
// configured, or any ref whose target cannot be found, returns an error
// wrapping [ErrRefResolve].
//
// [WithRefFallback] sets a per-reference failure policy (a
// [RefFallback]) consulted when expanding a reference fails for any of those
// reasons, with a [RefFailure] carrying the JSON Pointer path of the
// referencing schema within its containing document, the reference value,
// and the error. The fallback answers with a [RefAction]: [PropagateRef]
// propagates the original error and ends the Inline call, [DropRef] drops
// the failing reference keyword while keeping the node's remaining keywords,
// and [SubstituteRef] supplies a substitute schema the reference expands to
// as if it had resolved there, with the usual draft sibling semantics. The
// fallback is consulted once per failure, at the reference that directly
// failed: a failure inside a nested expansion consults the innermost
// failing ref with its path in its containing document, and a declined
// consultation propagates outward without re-consulting at the enclosing
// refs. A substitute is deep-copied before splicing and is itself inlined
// recursively, its refs resolving in the context of the document containing
// the failing ref; a cycle introduced by the substitute is an ordinary
// [ErrRefCycle].
package jsonschema
