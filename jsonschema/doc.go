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
// this package.
//
// # Entry Points
//
// The primary API is the generic function [GenerateFor]:
//
//	schema, err := jsonschema.GenerateFor[MyType]()
//
// A runtime [reflect.Type] variant is also provided for dynamic use cases:
//
//	schema, err := jsonschema.Generate(reflect.TypeFor[MyType]())
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
//
// Errors are wrapped with context so callers see the full path
// (e.g., "field \"data\": unsupported type").
//
// # Configuration
//
// Configuration is via functional [Option] values passed to [GenerateFor] or
// [Generate]:
//
//   - [WithDraft] sets the target JSON Schema draft ([Draft7] or [Draft2020]).
//   - [WithTagInterpreter] registers a [TagInterpreter] for mapping struct tags
//     to schema constraints.
//   - [WithComments] enables Go doc comment extraction as description fields.
//   - [WithTypeSchema] overrides the schema for a specific Go type.
//   - [WithNamer] sets a custom definition naming function.
//   - [WithDefinitions] controls $defs/$ref extraction (default: true).
//   - [WithAdditionalProperties] controls whether extra keys are allowed on
//     object schemas (default: false, disallowing extra keys).
//   - [WithNullable] controls whether nil-able types (pointers, slices, maps,
//     []byte) accept null (default: true; false drops the null branch).
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
//  1. [WithTypeSchema] override (highest priority).
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
// When [WithTypeSchema] or [JSONSchemaProvider] provides the schema,
// [JSONSchemaExtender] is not called.
//
// # Tag Interpretation
//
// All struct tag interpretation beyond the json and jsonschema tags is handled
// through the pluggable [TagInterpreter] interface. Interpreters receive a
// [FieldContext] containing the field's schema, parent schema, JSON name, and
// Go type. Multiple interpreters can be registered and are applied in order.
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
// chain steps ([WithTypeSchema] or [JSONSchemaProvider]) are composed via
// allOf rather than having their fields promoted; an embed reached through a
// pointer composes as anyOf[schema, {}] instead, since a nil pointer
// contributes nothing to the marshaled object. A [WithTypeSchema] or
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
// Supported keys include description, title, default, examples, deprecated,
// readOnly, writeOnly, minimum, maximum, exclusiveMinimum, exclusiveMaximum,
// multipleOf, minLength, maxLength, pattern, format, minItems, maxItems,
// uniqueItems, minProperties, maxProperties, enum, and const.
//
// Values for default, const, enum, and examples are parsed using type-aware
// parsing based on the field's Go type. Enum and examples values are
// separated by "|". Unrecognized keys are a parse error. A value containing a
// comma escapes it with a backslash (a literal backslash is "\\"), so
// jsonschema:"description=Hello\, World" sets the description "Hello, World";
// enum and examples values cannot contain "|" (used as value separator). For
// complex values, use [JSONSchemaExtender] or AST doc comments with
// [WithComments].
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
// When [WithComments](true) is set, Go doc comments are extracted from source
// files for struct types, struct fields, and named types using [go/ast] and
// [golang.org/x/tools/go/packages]. The jsonschema struct tag description
// overrides AST-extracted comments. When source files cannot be located for
// a type, comment extraction is silently skipped.
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
// # Processing Order
//
// Schema generation involves two levels of processing:
//
// Type-level processing is executed once per type, producing the type's
// canonical schema: (1) base type reflection via the priority chain,
// (2) comment extraction if enabled, (3) [JSONSchemaExtender] if implemented.
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
// vocabulary detection) up front and is safe for concurrent use.
//
// On success all return nil. A validation failure returns an error that
// unwraps to [*ValidationError] via [errors.As]. Non-validation failures — JSON
// decoding, an unaccepted instance type, Schema.Resolve errors, and
// [ErrUnknownVocabulary] — return ordinary wrapped errors that do not unwrap to
// [*ValidationError].
//
// Compile rejects a type keyword naming anything other than the seven JSON
// Schema types ("null", "boolean", "string", "integer", "number", "object",
// "array") with an error wrapping [ErrInvalidType], so a typo'd type surfaces
// at construction instead of silently rejecting every instance.
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
//   - [WithRefResolver] sets a [RefResolver] for resolving remote $ref URIs.
//     The resolver is called only when local fragment resolution fails. Resolved
//     schemas are cached within the validation run.
//   - [WithFormatValidator] registers a custom format checker.
//   - [WithFormats] forces built-in format assertion on or off. By default
//     format is asserted under Draft-07 and is annotation-only under Draft
//     2020-12 unless the format-assertion vocabulary is active.
//   - [WithContent] asserts contentEncoding (base64) and contentMediaType
//     (application/json) for string instances. Annotation-only by default.
//   - [WithResolveOptions] passes upstream ResolveOptions for structural
//     pre-validation.
//   - [WithVocabularies] directly specifies active vocabularies for the
//     validation run.
//   - [WithMetaSchema] registers a metaschema whose $vocabulary map controls
//     which keyword groups are active.
//
// The draft is detected from the root schema's $schema field. [Draft7] and
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
// Built-in format checkers are provided for: date-time, date, time, duration,
// email, idn-email, hostname, idn-hostname, uri, uri-reference, uri-template,
// iri, iri-reference, uuid, ipv4, ipv6, json-pointer, relative-json-pointer, and
// regex.
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
//  2. [WithMetaSchema] lookup — the root schema's $schema is matched against
//     registered metaschema $id values to extract $vocabulary.
//  3. Default: a built-in standard vocabulary set — every group active except
//     format-assertion, so format is annotation-only by default.
//
// If a schema requires (marks true) a vocabulary URI that this implementation
// does not recognize, [Validate] returns [ErrUnknownVocabulary]. The same error
// is returned when a $vocabulary map marks the 2020-12 core vocabulary as
// optional (false), which the spec does not permit.
//
// By default only local fragment refs are resolved during validation (those
// under #/$defs or #/definitions). Remote and absolute $ref URIs are resolved
// via an optional [RefResolver] set with [WithRefResolver]. An unresolvable
// remote or absolute $ref is reported as a [*ValidationError] by the validation
// walk: with no resolver (or a resolver returning nil) the message begins with
// "cannot resolve $ref" and includes the quoted ref, while a resolver that
// returns an error yields one wrapping [ErrRefResolve]. Only an unresolvable
// local fragment ref is silently skipped.
// Circular refs are detected and treated as passing to avoid infinite recursion.
package jsonschema
