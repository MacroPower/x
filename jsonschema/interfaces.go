package jsonschema

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// JSONSchemaProvider allows a type to provide its own schema, bypassing
// automatic generation entirely. When a type implements JSONSchemaProvider,
// the returned schema replaces reflection-based generation for that type.
// If JSONSchema returns a nil schema and a nil error, the type is treated as
// unrestricted ({}). A non-nil error aborts generation, matching
// [JSONSchemaExtender], for a provider that cannot produce its schema — one
// loading a schema document, for example. A panic in the method is still
// recovered and wrapped with [ErrProviderPanic], as a backstop for genuine
// bugs such as dereferencing a nil pointer field on the zero value the
// method is invoked against.
//
// The method receives the same arguments as its registered counterpart,
// [TypeSchemaProvider.SchemaForType]: the context of the Generate call in
// effect (so a provider loading a schema document can honor cancellation and
// deadlines) and a [TypeContext] carrying the target [Draft] (so a provider
// can emit draft-appropriate keywords). An implementation needing neither
// ignores them.
type JSONSchemaProvider interface {
	JSONSchema(ctx context.Context, tc TypeContext) (*Schema, error)
}

// JSONSchemaExtender allows a type to modify its auto-generated schema.
// The method is called after the schema has been generated via reflection,
// allowing the type to add, remove, or modify any fields. A non-nil error
// aborts generation, matching the registered [TypeSchemaExtender]
// counterpart, whose [TypeSchemaExtender.ExtendSchemaForType] arguments the
// method shares: the context of the Generate call in effect and a
// [TypeContext] carrying the target [Draft]. An implementation needing
// neither ignores them.
type JSONSchemaExtender interface {
	JSONSchemaExtend(ctx context.Context, tc TypeContext, schema *Schema) error
}

// TypeContext provides context about the Go type whose schema a
// [TypeSchemaProvider] or [TypeSchemaExtender] is consulted for. It is the
// type-level counterpart of [FieldContext]: a struct rather than positional
// parameters, so the context can grow without changing the hook signatures.
type TypeContext struct {
	// Type is the Go type whose schema is being resolved or extended.
	Type reflect.Type
	// Draft is the target draft of the generation run, so a hook can emit
	// draft-appropriate keywords (for example dependentRequired under
	// [Draft2020] versus dependencies under [Draft7]).
	Draft Draft
}

// TypeSchemaProvider supplies schemas for Go types it recognizes during
// generation, the registered counterpart of [JSONSchemaProvider]: a type's
// author provides its schema by implementing JSONSchema on the type, while a
// consumer of types they do not own registers a TypeSchemaProvider. Providers
// are registered with [WithTypeSchemaProvider] and consulted at the
// highest-priority step of the type resolution chain, before
// [JSONSchemaProvider] and the built-in overrides, so a provider can map
// whole families of types — every type implementing some third-party
// interface, every type in a package — where [WithTypeSchema] names one
// exact [reflect.Type] at a time.
//
// SchemaForType returns [ErrTypeNotHandled] (or an error wrapping it) when
// the provider does not handle the type in tc, passing resolution to the
// next provider and then to the rest of the chain, the way a [RefResolver]
// answers [ErrNotResolved]. Returning a nil schema with a nil error marks
// the type unrestricted ({}), mirroring [JSONSchemaProvider]. A returned
// schema is copied before use with the same discipline [WithTypeSchema]
// documents, so one schema value may be shared across types, calls, and
// goroutines.
//
// Any other non-nil error aborts generation, for a provider that recognizes
// the type but cannot produce its schema — an I/O failure while loading a
// schema document, for example. The context comes from the Generate call in
// effect, so a provider doing such I/O can honor cancellation and deadlines;
// a provider that performs no cancellable work can ignore it, following
// [DescriptionProvider].
//
// A provider may be consulted several times for the same type within one
// generation run, so SchemaForType must be deterministic.
type TypeSchemaProvider interface {
	SchemaForType(ctx context.Context, tc TypeContext) (*Schema, error)
}

// TypeSchemaProviderFunc adapts a bare providing function to a
// [TypeSchemaProvider], following [net/http.HandlerFunc].
type TypeSchemaProviderFunc func(ctx context.Context, tc TypeContext) (*Schema, error)

// SchemaForType calls f.
func (f TypeSchemaProviderFunc) SchemaForType(ctx context.Context, tc TypeContext) (*Schema, error) {
	return f(ctx, tc)
}

// TypeSchemaExtender modifies reflection-generated schemas during generation,
// registered with [WithTypeSchemaExtender]. It is the registered counterpart
// of [JSONSchemaExtender]: a type's author extends its schema by implementing
// JSONSchemaExtend on the type, while a consumer of a type they do not own
// registers a TypeSchemaExtender. Where a [TypeSchemaProvider] replaces a
// type's schema wholesale, an extender adjusts what reflection produced.
//
// ExtendSchemaForType is called once per type whose schema kind-based
// reflection or a built-in override produced, at the point JSONSchemaExtend
// runs (after comment extraction, before $defs extraction) and after the
// type's own JSONSchemaExtend. Like JSONSchemaExtender, it is not called for
// types whose schema a registered provider or [JSONSchemaProvider] supplied.
// It modifies s in place; an error aborts generation. An extender that does
// not recognize the type in tc leaves s untouched and returns nil. The
// context follows the [TypeSchemaProvider.SchemaForType] contract.
type TypeSchemaExtender interface {
	ExtendSchemaForType(ctx context.Context, tc TypeContext, s *Schema) error
}

// TypeSchemaExtenderFunc adapts a bare extending function to a
// [TypeSchemaExtender], following [net/http.HandlerFunc].
type TypeSchemaExtenderFunc func(ctx context.Context, tc TypeContext, s *Schema) error

// ExtendSchemaForType calls f.
func (f TypeSchemaExtenderFunc) ExtendSchemaForType(ctx context.Context, tc TypeContext, s *Schema) error {
	return f(ctx, tc, s)
}

// DescriptionProvider supplies descriptions for types and struct fields during
// generation, registered with [WithDescriptionProvider]. [NewGoCommentProvider]
// constructs the built-in provider, which extracts Go doc comments by
// loading and parsing package sources at generation time; any other
// implementation substitutes another source — for example comments
// pre-extracted at build time and shipped with a binary that deploys
// without source files, or fixed descriptions in tests.
// [ChainDescriptionProviders] composes providers, first non-empty
// description wins.
//
// An empty result leaves the description unset, letting later field-level
// processing (the jsonschema struct tag, tag interpreters) supply one. A
// provider must be safe for concurrent use when shared across concurrent
// Generate calls.
type DescriptionProvider interface {
	// TypeDescription returns the description for the named type in tc, or
	// "" for none. The [TypeContext] is the same value the package's other
	// type-level hooks receive, carrying the Go type and the target [Draft].
	// The context comes from the Generate call in effect, so a provider
	// doing I/O (the built-in one loads package sources) can honor
	// cancellation and deadlines; a provider that performs no cancellable
	// work can ignore it. A non-nil error aborts generation, matching the
	// package's other generation hooks, so a provider doing I/O reports a
	// failed lookup instead of silently dropping descriptions; a provider
	// with no failure mode returns nil.
	TypeDescription(ctx context.Context, tc TypeContext) (string, error)

	// FieldDescription returns the description for the struct field in fc,
	// or "" for none. The [FieldContext] is the same value tag interpreters
	// receive: [FieldContext.Owner] carries the type that declares the field
	// (for a field promoted from an embedded struct, the embedded type,
	// where the field's doc comment lives, not the outer struct) and
	// [FieldContext.StructField] names the Go field. A provider must treat
	// fc.Schema and fc.Parent as read-only, answering through its return
	// value. The context and error follow the TypeDescription contract.
	FieldDescription(ctx context.Context, fc FieldContext) (string, error)
}

// DescriptionProviderFuncs adapts a pair of bare functions to a
// [DescriptionProvider]. It is the struct-adapter form of the package's
// Func adapters, since a two-method interface has no single-function
// conversion. A nil function answers "" for its half, so a one-off provider
// — fixed type descriptions in a test, for example — sets only the function
// it needs:
//
//	jsonschema.WithDescriptionProvider(jsonschema.DescriptionProviderFuncs{
//		TypeFunc: func(_ context.Context, tc jsonschema.TypeContext) (string, error) {
//			return docs[tc.Type.Name()], nil
//		},
//	})
type DescriptionProviderFuncs struct {
	// TypeFunc backs TypeDescription. A nil TypeFunc leaves every type
	// description unset.
	TypeFunc func(ctx context.Context, tc TypeContext) (string, error)

	// FieldFunc backs FieldDescription. A nil FieldFunc leaves every field
	// description unset.
	FieldFunc func(ctx context.Context, fc FieldContext) (string, error)
}

// TypeDescription calls TypeFunc, or answers "" when TypeFunc is nil.
func (p DescriptionProviderFuncs) TypeDescription(ctx context.Context, tc TypeContext) (string, error) {
	if p.TypeFunc == nil {
		return "", nil
	}

	return p.TypeFunc(ctx, tc)
}

// FieldDescription calls FieldFunc, or answers "" when FieldFunc is nil.
func (p DescriptionProviderFuncs) FieldDescription(ctx context.Context, fc FieldContext) (string, error) {
	if p.FieldFunc == nil {
		return "", nil
	}

	return p.FieldFunc(ctx, fc)
}

// ChainDescriptionProviders returns a [DescriptionProvider] that consults
// each provider in order and answers with the first non-empty description
// or the first error; when every provider answers "" (including an empty
// or all-nil chain), the description stays unset. Nil providers are
// skipped, so optional links can be passed unconditionally, following
// [ChainResolvers].
//
// It makes the composition the [DescriptionProvider] docs describe a
// one-liner — fixed overrides for specific types consulted first, backed by
// AST extraction:
//
//	jsonschema.WithDescriptionProvider(jsonschema.ChainDescriptionProviders(
//		overrides, jsonschema.NewGoCommentProvider()))
func ChainDescriptionProviders(providers ...DescriptionProvider) DescriptionProvider {
	return descriptionProviderChain(providers)
}

// descriptionProviderChain is the [DescriptionProvider] returned by
// [ChainDescriptionProviders].
type descriptionProviderChain []DescriptionProvider

// TypeDescription returns the first non-empty type description or the
// first error in the chain.
func (c descriptionProviderChain) TypeDescription(ctx context.Context, tc TypeContext) (string, error) {
	for _, p := range c {
		if p == nil {
			continue
		}

		d, err := p.TypeDescription(ctx, tc)
		if d != "" || err != nil {
			//nolint:wrapcheck // The chain is transparent: a link's error reaches the caller verbatim.
			return d, err
		}
	}

	return "", nil
}

// FieldDescription returns the first non-empty field description or the
// first error in the chain.
func (c descriptionProviderChain) FieldDescription(ctx context.Context, fc FieldContext) (string, error) {
	for _, p := range c {
		if p == nil {
			continue
		}

		d, err := p.FieldDescription(ctx, fc)
		if d != "" || err != nil {
			//nolint:wrapcheck // The chain is transparent: a link's error reaches the caller verbatim.
			return d, err
		}
	}

	return "", nil
}

// Tag is one struct tag pair handed to a [TagInterpreter]: the tag key the
// interpreter was registered under and the field's value for it. It rides
// beside the [FieldContext] rather than inside it, so the field context
// stays one value shared by every field-level hook — a
// [DescriptionProvider] receives the same FieldContext with no tag pair at
// all.
type Tag struct {
	// Key is the struct tag key the interpreter was registered under and
	// the field carries (e.g. "validate"), so an implementation serving
	// several keys can tell which one fired, the way an [net/http.Handler]
	// reads the request path.
	Key string

	// Value is the field's value for Key, the input an interpreter
	// translates into schema constraints.
	Value string
}

// TagInterpreter translates struct field tags into JSON Schema constraints.
// It is registered with [WithTagInterpreter] under the struct tag key it
// reads, following [net/http.Handle]'s name-at-registration shape, so one
// implementation can serve several keys.
type TagInterpreter interface {
	// Interpret reads the tag pair ([Tag.Value], keyed by [Tag.Key]) and
	// the field context, then modifies the schema in place. It is called
	// during field-level processing, after the type schema, comments, and
	// jsonschema struct tag have been applied. The FieldContext provides
	// access to both the field's own schema and the parent object schema,
	// enabling constraints like "required" that modify the parent. The
	// context follows the [TypeSchemaProvider.SchemaForType] contract: it
	// comes from the Generate call in effect, and an interpreter that
	// performs no cancellable work can ignore it.
	Interpret(ctx context.Context, field FieldContext, tag Tag) error
}

// TagInterpreterFunc adapts a bare interpreting function to a
// [TagInterpreter], following [net/http.HandlerFunc], so a one-off
// interpreter needs no named type.
type TagInterpreterFunc func(ctx context.Context, field FieldContext, tag Tag) error

// Interpret calls f.
func (f TagInterpreterFunc) Interpret(ctx context.Context, field FieldContext, tag Tag) error {
	return f(ctx, field, tag)
}

// FormatValidator checks string instances against one format during
// validation. It is registered with [WithFormatValidator] under the format
// name it checks, following [net/http.Handle]'s name-at-registration shape,
// so one implementation can serve several names. An implementation can hold
// state such as a compiled regular expression; [FormatValidatorFunc] adapts
// a bare function for checkers that need none.
type FormatValidator interface {
	// ValidateFormat checks one string instance against the named format,
	// returning nil when the value conforms. Name is the format name the
	// checker was registered under, so an implementation serving several
	// names can tell them apart, the way an [net/http.Handler] reads the
	// request path.
	//
	// The context comes from the validation entry point in effect
	// ([Validator.Validate], [Validate], [ValidateJSON]; the Must* forms
	// pass [context.Background]), so a checker that consults an external
	// system can honor cancellation and deadlines; a checker that performs
	// no cancellable work can ignore it.
	ValidateFormat(ctx context.Context, name, value string) error
}

// FormatValidatorFunc adapts a bare checking function to a
// [FormatValidator], following [net/http.HandlerFunc].
type FormatValidatorFunc func(ctx context.Context, name, value string) error

// ValidateFormat calls f.
func (f FormatValidatorFunc) ValidateFormat(ctx context.Context, name, value string) error {
	return f(ctx, name, value)
}

// RefResolver resolves remote schema URIs during validation. The resolver
// is called only when local resolution fails to find a target. Successfully
// resolved schemas are cached within the validation run, so the resolver is
// invoked at most once per URI that resolves; a URI the resolver reports as
// not resolved or fails on is not cached and may be queried again for each
// ref that targets it. Implementations must be safe for concurrent use if
// passed to multiple Validate calls.
//
// The same resolver value serves both validation and inlining via a single
// [WithRefResolver] option.
type RefResolver interface {
	// ResolveRef resolves a remote schema URI. [ErrNotResolved] (or an
	// error wrapping it) is the not-resolved answer, passing the URI along
	// (to the next [ChainResolvers] link, and ultimately to the
	// unresolvable-ref handling of the entry point in effect), following
	// [io/fs.ErrNotExist]; any other error reports a resolution attempt
	// that failed. A nil schema with a nil error is treated as not
	// resolved, so no caller dereferences a nil document.
	//
	// The resolution runs under the caller's context, so a resolver that
	// fetches over the network can honor cancellation and deadlines. The
	// context comes from the entry point in effect ([Compile],
	// [Validator.Validate], [Inline]); the Must* entry points pass
	// [context.Background]. A resolver that performs no cancellable work
	// can ignore it.
	ResolveRef(ctx context.Context, uri string) (*Schema, error)
}

// RefResolverFunc adapts a bare resolution function to a [RefResolver],
// following [net/http.HandlerFunc], so a one-off resolver — a closure over
// an HTTP client, for example — needs no named type ([SchemaMap] already
// covers preloaded schemas). The [RefResolver] contract applies unchanged,
// including concurrency safety when the resolver is shared across Validate
// calls.
type RefResolverFunc func(ctx context.Context, uri string) (*Schema, error)

// ResolveRef calls f.
func (f RefResolverFunc) ResolveRef(ctx context.Context, uri string) (*Schema, error) {
	return f(ctx, uri)
}

// SchemaMap is a [RefResolver] serving preloaded schemas from a map keyed
// by URI. A URI absent from the map answers [ErrNotResolved], so a
// SchemaMap composes with other resolvers via [ChainResolvers]. The map is
// read directly, never mutated; callers sharing one SchemaMap across
// goroutines must not modify it concurrently.
//
// A SchemaMap serves anywhere a resolver is accepted: preloaded remote
// schemas for [WithRefResolver], or metaschemas for
// [WithMetaSchemaResolver], keyed by the $schema URI they serve:
//
//	jsonschema.WithMetaSchemaResolver(jsonschema.SchemaMap{meta.ID: meta})
type SchemaMap map[string]*Schema

// ResolveRef returns the schema stored under uri. A URI absent from the
// map, or stored with a nil schema, answers an error wrapping
// [ErrNotResolved] that names the URI.
func (m SchemaMap) ResolveRef(_ context.Context, uri string) (*Schema, error) {
	if s := m[uri]; s != nil {
		return s, nil
	}

	return nil, fmt.Errorf("%w: %q", ErrNotResolved, uri)
}

// ChainResolvers returns a [RefResolver] that consults each resolver in
// order and answers with the first schema or non-[ErrNotResolved] error; a
// resolver answering [ErrNotResolved] passes the URI to the next. When
// every resolver declines (including an empty or all-nil chain), the chain
// answers an error wrapping ErrNotResolved. Nil resolvers are skipped, so
// optional links can be passed unconditionally.
func ChainResolvers(resolvers ...RefResolver) RefResolver {
	return RefResolverFunc(func(ctx context.Context, uri string) (*Schema, error) {
		for _, r := range resolvers {
			if r == nil {
				continue
			}

			s, err := r.ResolveRef(ctx, uri)
			if errors.Is(err, ErrNotResolved) {
				continue
			}

			if err != nil {
				//nolint:wrapcheck // The chain is transparent: a link's error reaches the caller verbatim.
				return nil, err
			}

			if s != nil {
				return s, nil
			}
		}

		return nil, fmt.Errorf("%w: %q", ErrNotResolved, uri)
	})
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

// FieldContext provides context about a struct field to the field-level
// hooks: tag interpreters ([TagInterpreter.Interpret]) and description
// providers ([DescriptionProvider.FieldDescription]). It is the field-level
// counterpart of [TypeContext]: a struct rather than positional parameters,
// so the context can grow without changing the hook signatures. Both hooks
// receive the same value; the tag pair an interpreter runs under travels
// separately as a [Tag].
type FieldContext struct {
	// Type is the Go reflect.Type of the field. It mirrors StructField.Type,
	// kept as a direct field for the common case.
	Type reflect.Type
	// Owner is the struct type declaring the field. For a field promoted from
	// an embedded struct it is the embedded type, where the field is declared
	// (and where a [GoCommentProvider] finds its doc comment), not the outer
	// struct.
	Owner reflect.Type
	// Schema is the field's own generated schema. A tag interpreter modifies
	// it in place; a [DescriptionProvider] must treat it as read-only and
	// answer through its return value instead.
	Schema *Schema
	// Parent is the enclosing object schema, so an interpreter can append to
	// its Required list. The [DescriptionProvider] read-only contract of
	// Schema applies.
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
