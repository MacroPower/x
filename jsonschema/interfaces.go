package jsonschema

import (
	"context"
	"reflect"
	"strings"
)

// JSONSchemaProvider allows a type to provide its own schema, bypassing
// automatic generation entirely. When a type implements JSONSchemaProvider,
// the returned schema replaces reflection-based generation for that type.
// If JSONSchema returns nil, the type is treated as unrestricted ({}).
type JSONSchemaProvider interface {
	JSONSchema() *Schema
}

// JSONSchemaExtender allows a type to modify its auto-generated schema.
// The method is called after the schema has been generated via reflection,
// allowing the type to add, remove, or modify any fields.
type JSONSchemaExtender interface {
	JSONSchemaExtend(schema *Schema)
}

// TypeSchemaResolver supplies schemas for Go types it recognizes during
// generation. Resolvers are registered with [WithTypeSchemaResolver] and consulted
// at the highest-priority step of the type resolution chain, before
// [JSONSchemaProvider] and the built-in overrides, so a resolver can map
// whole families of types — every type implementing some third-party
// interface, every type in a package — where [WithTypeSchema] names one
// exact [reflect.Type] at a time.
//
// SchemaForType returns ok false when the resolver does not handle t,
// passing resolution to the next resolver and then to the rest of the
// chain. Returning ok true with a nil schema marks the type unrestricted
// ({}), mirroring [JSONSchemaProvider]. A returned schema is copied before
// use with the same discipline [WithTypeSchema] documents, so one schema
// value may be shared across types, calls, and goroutines.
//
// A resolver may be consulted several times for the same type within one
// generation run, so SchemaForType must be deterministic.
type TypeSchemaResolver interface {
	SchemaForType(t reflect.Type) (s *Schema, ok bool)
}

// TypeSchemaResolverFunc adapts a bare resolution function to a
// [TypeSchemaResolver], following [net/http.HandlerFunc].
type TypeSchemaResolverFunc func(t reflect.Type) (*Schema, bool)

// SchemaForType calls f.
func (f TypeSchemaResolverFunc) SchemaForType(t reflect.Type) (*Schema, bool) { return f(t) }

// TypeSchemaExtender modifies reflection-generated schemas during generation,
// registered with [WithTypeSchemaExtender]. It is the registered counterpart
// of [JSONSchemaExtender]: a type's author extends its schema by implementing
// JSONSchemaExtend on the type, while a consumer of a type they do not own
// registers a TypeSchemaExtender. Where a [TypeSchemaResolver] replaces a
// type's schema wholesale, an extender adjusts what reflection produced.
//
// ExtendSchemaForType is called once per type whose schema kind-based
// reflection or a built-in override produced, at the point JSONSchemaExtend
// runs (after comment extraction, before $defs extraction) and after the
// type's own JSONSchemaExtend. Like JSONSchemaExtender, it is not called for
// types whose schema a registered resolver or [JSONSchemaProvider] supplied.
// It modifies s in place; an error aborts generation. An extender that does
// not recognize t leaves s untouched and returns nil.
type TypeSchemaExtender interface {
	ExtendSchemaForType(t reflect.Type, s *Schema) error
}

// TypeSchemaExtenderFunc adapts a bare extending function to a
// [TypeSchemaExtender], following [net/http.HandlerFunc].
type TypeSchemaExtenderFunc func(t reflect.Type, s *Schema) error

// ExtendSchemaForType calls f.
func (f TypeSchemaExtenderFunc) ExtendSchemaForType(t reflect.Type, s *Schema) error {
	return f(t, s)
}

// DescriptionProvider supplies descriptions for types and struct fields during
// generation, registered with [WithDescriptionProvider]. [NewGoCommentProvider]
// constructs the built-in provider, which extracts Go doc comments by
// loading and parsing package sources at generation time; any other
// implementation substitutes another source — for example comments
// pre-extracted at build time and shipped with a binary that deploys
// without source files, or fixed descriptions in tests.
//
// An empty result leaves the description unset, letting later field-level
// processing (the jsonschema struct tag, tag interpreters) supply one. A
// provider must be safe for concurrent use when shared across concurrent
// Generate calls.
type DescriptionProvider interface {
	// TypeDescription returns the description for a named type, or "" for none.
	// The context comes from the Generate call in effect, so a provider
	// doing I/O (the built-in one loads package sources) can honor
	// cancellation and deadlines; a provider that performs no cancellable
	// work can ignore it.
	TypeDescription(ctx context.Context, t reflect.Type) string

	// FieldDescription returns the description for the named Go field of struct
	// type t, or "" for none. T is the type that declares the field: for a
	// field promoted from an embedded struct it is the embedded type, where
	// the field's doc comment lives, not the outer struct. The context
	// follows the TypeDescription contract.
	FieldDescription(ctx context.Context, t reflect.Type, fieldName string) string
}

// TagInterpreter translates struct field tags into JSON Schema constraints.
type TagInterpreter interface {
	// TagKey returns the struct tag key this interpreter reads (e.g., "validate").
	TagKey() string

	// Interpret reads the tag value and the field context, then modifies
	// the schema in place. It is called during field-level processing, after
	// the type schema, comments, and jsonschema struct tag have been applied.
	// The FieldContext provides access to both the field's own schema and the
	// parent object schema, enabling constraints like "required" that modify
	// the parent.
	Interpret(tag string, field FieldContext) error
}

// TagInterpreterFunc adapts a bare interpreting function to a
// [TagInterpreter] for the named struct tag key, following [FormatValidatorFunc].
func TagInterpreterFunc(key string, fn func(tag string, field FieldContext) error) TagInterpreter {
	return tagInterpreterFunc{key: key, fn: fn}
}

// tagInterpreterFunc is the [TagInterpreter] returned by [TagInterpreterFunc].
type tagInterpreterFunc struct {
	fn  func(string, FieldContext) error
	key string
}

func (t tagInterpreterFunc) TagKey() string { return t.key }

func (t tagInterpreterFunc) Interpret(tag string, field FieldContext) error {
	return t.fn(tag, field)
}

// FormatValidator checks string instances against one named format during
// validation. Like [TagInterpreter], the value declares the name it handles,
// so a single registration via [WithFormatValidator] carries both, and an
// implementation can hold state such as a compiled regular expression.
// [FormatValidatorFunc] adapts a bare function for checkers that need none.
type FormatValidator interface {
	// Format returns the format name this validator checks (e.g., "uuid").
	Format() string

	// ValidateFormat checks one string instance against the format,
	// returning nil when the value conforms.
	ValidateFormat(value string) error
}

// FormatValidatorFunc adapts a bare checking function to a [FormatValidator] for the
// named format, following [net/http.HandlerFunc].
func FormatValidatorFunc(name string, fn func(string) error) FormatValidator {
	return formatFunc{name: name, fn: fn}
}

// formatFunc is the [FormatValidator] returned by [FormatValidatorFunc].
type formatFunc struct {
	fn   func(string) error
	name string
}

func (f formatFunc) Format() string { return f.name }

func (f formatFunc) ValidateFormat(value string) error { return f.fn(value) }

// RefResolver resolves remote schema URIs during validation. The resolver
// is called only when local resolution fails to find a target. Successfully
// resolved schemas are cached within the validation run, so the resolver is
// invoked at most once per URI that resolves; a URI for which the resolver
// returns nil or an error is not cached and may be queried again for each ref
// that targets it. Implementations must be safe for concurrent use if passed
// to multiple Validate calls.
//
// The same resolver value serves both validation and inlining via a single
// [WithRefResolver] option.
type RefResolver interface {
	// ResolveRef resolves a remote schema URI under the caller's context, so
	// a resolver that fetches over the network can honor cancellation and
	// deadlines. The context comes from the entry point in effect
	// ([Compile], [Validator.Validate], [Inline]); the Must* entry points
	// pass [context.Background]. A resolver that performs no cancellable
	// work can ignore it.
	ResolveRef(ctx context.Context, uri string) (*Schema, error)
}

// RefResolverFunc adapts a bare resolution function to a [RefResolver],
// following [net/http.HandlerFunc], so a one-off resolver — a closure over
// an HTTP client or a map of preloaded schemas — needs no named type. The
// [RefResolver] contract applies unchanged, including concurrency safety
// when the resolver is shared across Validate calls.
type RefResolverFunc func(ctx context.Context, uri string) (*Schema, error)

// ResolveRef calls f.
func (f RefResolverFunc) ResolveRef(ctx context.Context, uri string) (*Schema, error) {
	return f(ctx, uri)
}

// StripPrefix returns a [RefResolver] that strips prefix from each URI and
// delegates to r, following [net/http.StripPrefix]. It serves refs that
// absolutize against a published remote base (an https $id, for example)
// from a resolver that addresses documents differently, such as a
// [FileResolver] over a local directory of schema files:
//
//	jsonschema.StripPrefix("https://example.com/schemas/",
//		jsonschema.NewFileResolver(os.DirFS("schemas")))
//
// A URI that does not carry the prefix is delegated unchanged.
func StripPrefix(prefix string, r RefResolver) RefResolver {
	return RefResolverFunc(func(ctx context.Context, uri string) (*Schema, error) {
		return r.ResolveRef(ctx, strings.TrimPrefix(uri, prefix))
	})
}

// RefOption is the option type returned by [WithRefResolver] and [WithBaseURI],
// the two options configuring reference resolution: a single option value
// that serves both validation ([ValidateOption]) and inlining
// ([InlineOption]).
type RefOption interface {
	ValidateOption
	InlineOption
}

// refResolverOption is the [RefOption] returned by [WithRefResolver].
type refResolverOption struct {
	r RefResolver
}

func (o refResolverOption) applyValidate(v *validator) { v.refResolver = o.r }

func (o refResolverOption) applyInline(in *inliner) { in.resolver = o.r }

// WithRefResolver sets the [RefResolver] used to resolve remote $ref URIs. The
// returned option serves both validation and inlining, so one value
// configures [Compile], [Validate], and [Inline] alike. [RefResolverFunc]
// adapts a bare function.
//
// During validation the resolver is called when local fragment resolution
// fails, and resolved schemas are cached for the duration of the run. During
// inlining it receives the fragment-stripped absolute URI and is called at
// most once per distinct URI within one Inline call; the schema it returns
// is deep-copied before use and never mutated. In both roles the resolver
// receives the context of the Context entry point in effect. A nil r
// restores the default, where only local refs resolve.
func WithRefResolver(r RefResolver) RefOption {
	return refResolverOption{r: r}
}

// baseURIOption is the [RefOption] returned by [WithBaseURI].
type baseURIOption struct {
	base string
}

func (o baseURIOption) applyValidate(v *validator) { v.baseURI = o.base }

func (o baseURIOption) applyInline(in *inliner) { in.baseURI = o.base }

// WithBaseURI sets the base URI of the root document: the base that
// non-local refs in the root document absolutize against when no enclosing
// $id establishes one, exactly as a root $id would. The returned option
// serves both validation and inlining, so one value configures [Compile],
// [Validate], and [Inline] alike. Any fragment on base is ignored.
//
// A base with no URI scheme is taken as a file path and normalized against
// file:/// ("main.json" becomes "file:///main.json"), so RFC 3986 reference
// joining is well-defined and a ref in a fetched document that absolutizes
// back to the root resolves to the in-memory document instead of
// re-fetching it. [FileResolver] strips the file:// scheme and the leading
// "/", so [io/fs] paths keep working; a custom resolver paired with a
// schemeless base receives the normalized file:/// form.
func WithBaseURI(base string) RefOption {
	return baseURIOption{base: stripFragment(base)}
}

// FieldContext provides context about a struct field to tag interpreters.
type FieldContext struct {
	// Type is the Go reflect.Type of the field. It mirrors StructField.Type,
	// kept as a direct field for the common case.
	Type reflect.Type
	// Schema is the field's own generated schema, modified in place by the interpreter.
	Schema *Schema
	// Parent is the enclosing object schema, so an interpreter can append to its Required list.
	Parent *Schema
	// Name is the JSON property name for the field.
	Name string
	// StructField is the full reflect.StructField, so an interpreter can read
	// other struct tags (for example the json tag's omitempty) or the field's
	// Go name and index.
	StructField reflect.StructField
	// Draft is the target draft of the generation run, so an interpreter can
	// emit draft-appropriate keywords (for example dependentRequired under
	// [Draft2020] versus dependencies under [Draft7]).
	Draft Draft
}
