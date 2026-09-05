package jsonschema

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// JSONSchemaProvider allows a type to provide its own schema, bypassing
// automatic generation entirely. When a type implements JSONSchemaProvider,
// the returned [TypeSchema] replaces reflection-based generation for that type.
// A zero TypeSchema with a nil error treats the type as unrestricted ({}); a
// non-nil error aborts generation, matching [JSONSchemaExtender]. This lets a
// provider report that it cannot produce its schema, as when it loads a schema
// document. A panic in the method is still recovered and wrapped with
// [ErrProviderPanic], as a backstop for genuine bugs such as dereferencing a
// nil pointer field on the zero value the method is invoked against.
//
// The [TypeSchema] carries the bare value schema plus its intent (a [Nullability]
// stance, an opaque [TypeSchema.Verbatim] escape hatch, or a [TypeSchema.Ref] to a
// named type), so a provider declares whether its schema admits null rather than
// hand-shaping an anyOf[value, null] wrapper the generator would have to recognize.
//
// The method receives the same arguments as its registered counterpart,
// [TypeSchemaProvider.SchemaForType]: the context of the Generate call in
// effect (so a provider loading a schema document can honor cancellation and
// deadlines) and a [TypeContext] carrying the target [Draft] (so a provider
// can emit draft-appropriate keywords). An implementation needing neither
// ignores them.
type JSONSchemaProvider interface {
	JSONSchema(ctx context.Context, tc TypeContext) (TypeSchema, error)
}

// JSONSchemaExtender allows a type to modify its auto-generated schema.
// The method is called after the schema has been generated via reflection,
// allowing the type to add, remove, or modify any fields. It receives a
// [TypeSchema] whose [TypeSchema.Value] is the reflection-generated schema to
// mutate in place; an extender may also set [TypeSchema.Nullability] to declare a
// nullability stance rather than hand-shaping a null wrapper. Only Value and
// Nullability are honored: [TypeSchema.Verbatim] and [TypeSchema.Ref] declare a
// replacement schema, which only a provider supplies, so an extender that sets
// either aborts generation with [ErrConflictingTypeSchema] rather than having
// the declaration silently ignored. A non-nil error
// aborts generation, matching the registered [TypeSchemaExtender] counterpart,
// whose [TypeSchemaExtender.ExtendSchemaForType] arguments the method shares:
// the context of the Generate call in effect and a [TypeContext] carrying the
// target [Draft]. An implementation needing neither ignores them.
type JSONSchemaExtender interface {
	JSONSchemaExtend(ctx context.Context, tc TypeContext, ts *TypeSchema) error
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

// Nullability is a type-level hook's declared stance on whether its schema
// admits a JSON null, so a hook declares its stance rather than hand-shaping an
// anyOf[value, null] wrapper the generator would have to recognize.
type Nullability uint8

const (
	// NullFromReflection defers to reflection: a pointer or interface position is
	// nullable, everything else is not (the pre-envelope default).
	NullFromReflection Nullability = iota
	// NullAllowed makes every occurrence of the type admit null, even a non-pointer
	// field.
	NullAllowed
	// NullForbidden makes no occurrence admit null, even a pointer field.
	NullForbidden
)

// TypeSchema is what a type-level hook declares for a Go type: a bare value
// schema plus its intent, so generation applies the null encoding and resolves
// references itself instead of reverse-engineering a pre-shaped schema. Exactly
// one of Value, Verbatim, or Ref is meaningful; setting more than one is
// [ErrConflictingTypeSchema].
type TypeSchema struct {
	// Value is the bare value schema (no null wrapper). Generation applies the
	// null encoding per Nullability and each occurrence's pointer-ness. A zero
	// TypeSchema (nil Value, NullFromReflection, nil Verbatim/Ref) marks the type
	// unrestricted ({}).
	Value *Schema
	// Verbatim is an opaque escape hatch: the schema is emitted exactly as given,
	// with no null encoding applied. Use it for a fully-formed schema (e.g. one
	// loaded from a document). Reachability still scans it for raw $ref strings.
	Verbatim *Schema
	// Ref declares that this schema is a reference to the named Go type, so
	// generation keeps that type's definition reachable through a node-backed
	// edge instead of a payload $ref-string scan. The named type must be
	// extractable (a struct, or a type extracted to $defs because it implements a
	// provider or extender) so the reference stays a real $ref edge; a
	// non-extractable Ref is [ErrConflictingTypeSchema], as is an alias chain
	// that cycles back to its own type (a self-Ref, or a mutual A -> B -> A
	// chain).
	Ref reflect.Type
	// Nullability is the type's null-admission stance (see [Nullability]). It
	// decorates Value (and Ref); it is ignored for Verbatim.
	Nullability Nullability
}

// TypeSchemaProvider supplies schemas for Go types it recognizes during
// generation, the registered counterpart of [JSONSchemaProvider]: a type's
// author provides its schema by implementing JSONSchema on the type, while a
// consumer of types they do not own registers a TypeSchemaProvider. Providers
// are registered with [WithTypeSchemaProvider] and consulted at the
// highest-priority step of the type resolution chain, before
// [JSONSchemaProvider] and the built-in overrides, so a provider can map
// whole families of types, such as every type implementing some third-party
// interface or every type in a package, where [WithTypeSchema] names one
// exact [reflect.Type] at a time.
//
// SchemaForType returns [ErrTypeNotHandled] (or an error wrapping it) when
// the provider does not handle the type in tc, passing resolution to the
// next provider and then to the rest of the chain, the way a [RefResolver]
// answers [ErrNotResolved]. Because it returns a [TypeSchema] by value, a
// zero TypeSchema with a nil error marks the type unrestricted ({}), and
// decline is only via the error -- there is no nil-schema ambiguity. The
// TypeSchema's Value (or Verbatim) is copied before use with the same
// discipline [WithTypeSchema] documents, so one schema value may be shared
// across types, calls, and goroutines.
//
// The [TypeSchema] carries the bare value schema plus its intent (a
// [Nullability] stance, an opaque [TypeSchema.Verbatim], or a [TypeSchema.Ref]
// to a named type), so generation applies the null encoding and resolves
// references itself instead of recognizing a pre-shaped wrapper.
//
// Any other non-nil error aborts generation. This lets a provider report
// that it recognizes the type but cannot produce its schema, such as on an
// I/O failure while loading a schema document. The context comes from the
// Generate call in effect, so a provider doing such I/O can honor
// cancellation and deadlines; a provider that performs no cancellable work
// can ignore it, following [DescriptionProvider].
//
// A provider may be consulted several times for the same type within one
// generation run, so SchemaForType must be deterministic.
type TypeSchemaProvider interface {
	SchemaForType(ctx context.Context, tc TypeContext) (TypeSchema, error)
}

// TypeSchemaProviderFunc adapts a bare providing function to a
// [TypeSchemaProvider], following [net/http.HandlerFunc].
type TypeSchemaProviderFunc func(ctx context.Context, tc TypeContext) (TypeSchema, error)

// SchemaForType calls f.
func (f TypeSchemaProviderFunc) SchemaForType(ctx context.Context, tc TypeContext) (TypeSchema, error) {
	return f(ctx, tc)
}

// TypeSchemaExtender modifies reflection-generated schemas during generation,
// registered with [WithTypeSchemaExtender]. It is the registered counterpart
// of [JSONSchemaExtender]: a type's author extends its schema by implementing
// JSONSchemaExtend on the type, while a consumer of a type they do not own
// registers a TypeSchemaExtender. Where a [TypeSchemaProvider] replaces a
// type's schema wholesale, an extender adjusts what reflection produced.
//
// ExtendSchemaForType is called for each type whose schema kind-based
// reflection or a built-in override produced, at the point JSONSchemaExtend
// runs (after comment extraction, before $defs extraction) and after the
// type's own JSONSchemaExtend. A $defs-extracted type is extended once, on
// its shared entry; a type that stays inline (an unnamed composite such as
// []string, or a named non-struct that is not extracted) is extended once
// per occurrence, so an extender may run several times for the same type
// within one generation run and must be deterministic, matching
// [TypeSchemaProvider]. Like JSONSchemaExtender, it is not called for
// types whose schema a registered provider or [JSONSchemaProvider] supplied.
// It modifies ts.Value in place (and may set ts.Nullability to declare a
// nullability stance, symmetric with a provider, so it never needs to emit a
// raw null wrapper); an error aborts generation. Only Value and Nullability are
// honored: [TypeSchema.Verbatim] and [TypeSchema.Ref] declare a replacement
// schema, which only a provider supplies, so an extender that sets either
// aborts generation with [ErrConflictingTypeSchema] rather than having the
// declaration silently ignored. An extender that does not
// recognize the type in tc leaves ts untouched and returns nil. The context
// follows the [TypeSchemaProvider.SchemaForType] contract.
type TypeSchemaExtender interface {
	ExtendSchemaForType(ctx context.Context, tc TypeContext, ts *TypeSchema) error
}

// TypeSchemaExtenderFunc adapts a bare extending function to a
// [TypeSchemaExtender], following [net/http.HandlerFunc].
type TypeSchemaExtenderFunc func(ctx context.Context, tc TypeContext, ts *TypeSchema) error

// ExtendSchemaForType calls f.
func (f TypeSchemaExtenderFunc) ExtendSchemaForType(ctx context.Context, tc TypeContext, ts *TypeSchema) error {
	return f(ctx, tc, ts)
}

// DescriptionProvider supplies descriptions for types and struct fields during
// generation, registered with [WithDescriptionProvider]. [NewGoCommentProvider]
// constructs the built-in provider, which extracts Go doc comments by
// loading and parsing package sources at generation time. Any other
// implementation substitutes another source, such as comments pre-extracted
// at build time and shipped with a binary that deploys without source files,
// or fixed descriptions in tests. [ChainDescriptionProviders] composes
// providers; the first non-empty description wins.
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
	// fc.Canvas and fc.Parent as read-only, answering through its return
	// value. The context and error follow the TypeDescription contract.
	FieldDescription(ctx context.Context, fc FieldContext) (string, error)
}

// DescriptionProviderFuncs adapts a pair of bare functions to a
// [DescriptionProvider]. It is the struct-adapter form of the package's
// Func adapters, since a two-method interface has no single-function
// conversion. A nil function answers "" for its half, so a one-off provider
// sets only the function it needs, as when a test supplies fixed type
// descriptions:
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
// The [DescriptionProvider] docs describe one such composition, which this
// builds in a single line. Fixed overrides for specific types are consulted
// first, backed by AST extraction:
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
// stays one value shared by every field-level hook. A [DescriptionProvider],
// for instance, receives the same FieldContext with no tag pair at all.
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
	// Interpret reads the tag pair ([Tag.Value], keyed by [Tag.Key]) and the
	// field context, then declares constraints by writing them to the field's
	// authored canvas ([FieldContext.Canvas]). It is called during field-level
	// processing, after the type schema, comments, and jsonschema struct tag
	// have been applied. The FieldContext also exposes the type-derived
	// [FieldContext.Base] to dispatch on and the parent object schema
	// ([FieldContext.Parent]) to append to, enabling constraints like "required"
	// that modify the parent. The context follows the
	// [TypeSchemaProvider.SchemaForType] contract: it comes from the Generate
	// call in effect, and an interpreter that performs no cancellable work can
	// ignore it.
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
	// ([Validator.Validate], [Validate]; the Must* forms
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
// is called only when local resolution fails to find a target. Every
// outcome is cached for the duration of the validation run: a resolved
// schema is reused, and a not-resolved answer or a resolution error is
// recorded and replayed for every other ref targeting the same URI, so the
// resolver is invoked at most once per distinct URI per run. A resolver
// with transient failures is therefore not re-consulted within a run; a
// fresh run starts with an empty cache and consults it anew.
// Implementations must be safe for concurrent use if passed to multiple
// Validate calls.
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
// following [net/http.HandlerFunc], so a one-off resolver such as a closure
// over an HTTP client needs no named type. [SchemaMap] already covers
// preloaded schemas. The [RefResolver] contract applies unchanged,
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
// A URI that does not carry the prefix is delegated unchanged. A nil r answers
// [ErrNotResolved] for every URI, so an optional resolver can be passed
// unconditionally, matching [ChainResolvers].
func StripPrefix(prefix string, r RefResolver) RefResolver {
	if r == nil {
		return RefResolverFunc(func(_ context.Context, uri string) (*Schema, error) {
			return nil, fmt.Errorf("%w: %q", ErrNotResolved, uri)
		})
	}

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
	return baseURIOption{base: uriref.StripFragment(base)}
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
	// Canvas is the field's authored-facts canvas: a fresh schema the jsonschema
	// tag pre-fills and an interpreter extends, declaring the value-scoped facts
	// (const, enum, the string-content keywords), annotations, and the
	// numeric/string/array bounds the field carries. It is not the field's merged
	// schema: generation composes the final shape from the type-derived
	// [FieldContext.Base] and this canvas, placing the value-scoped facts on the
	// value branch and the annotations and authored bounds on the null wrapper,
	// then applying the null encoding, so a const or enum an interpreter declares
	// lands on the value and keeps a permitted null valid. A tag interpreter
	// declares facts by writing them here; a [DescriptionProvider] must treat it as
	// read-only and answer through its return value instead.
	//
	// An interpreter contributes bounds and value constraints through the
	// [Constraints] facade [FieldContext.Constraints] returns rather than writing
	// the canvas bound fields directly. The facade is intersect-only -- a bound
	// weaker than the one already in effect (the canvas value, or the type-derived
	// one from Base) never lands on the canvas -- and generation resolves the
	// canvas against Base through the same shared algebra, so a canvas bound can
	// only tighten the type's own value. A nil bound field on the canvas means
	// unauthored, not cleared: the kind-derived bound from Base stands, and there
	// is no way to widen or remove it from here.
	Canvas *Schema
	// Base is the field's type-derived reflected schema, read-only: the pristine
	// payload carrying the type's own keywords (its numeric bounds, the
	// json:",string" string coercion, any type= override), before any field-level
	// fact is applied. An interpreter dispatching on the reflected shape (whether
	// the schema permits a string, whether it is a base64 content string) reads it
	// here; the string first-wins Effective accessors ([FieldContext.EffectiveFormat]
	// and its siblings) coalesce it with the canvas so a tag never overrides a
	// keyword the type already set.
	Base *Schema
	// Parent is the enclosing object schema, so an interpreter can append to
	// its Required list. The [DescriptionProvider] read-only contract of
	// Schema applies.
	Parent *Schema
	// The node field is the field or element IR node backing this context,
	// non-nil for a context the generator builds and nil for one a caller
	// constructs. It backs the element accessor ([FieldContext.ElementContexts]).
	node *node
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

// ElementContexts returns a FieldContext for each element schema of a sequence
// or map field: the single element of a slice or map, or one per position of a
// fixed array. It is the accessor an interpreter uses to constrain elements (a
// dive or a sequence-wide oneof), replacing a walk of the field's item schemas.
// Each returned context carries the element's own authored canvas
// ([FieldContext.Canvas]), its type-derived [FieldContext.Base], and its Go
// [FieldContext.Type] (pointer-preserving, so a []*string element still parses a
// null member), and supports [FieldContext.ElementContexts] again for a nested
// sequence. The elements carry an empty Name and a nil Parent, matching a dive's
// element position, and the run's Draft.
//
// A field with no per-element schema returns nil: a []byte or [json.RawMessage]
// (which encodes as a single string, not an array), or a field whose schema a
// provider supplied without element sub-schemas. An interpreter treats a nil
// result as "no item schema to constrain". A FieldContext a caller builds
// directly (rather than one generation supplies) has no backing node and so
// always returns nil, making element constraints inert; drive such an
// interpreter through generation to exercise them.
func (fc FieldContext) ElementContexts() []FieldContext {
	if fc.node == nil {
		return nil
	}

	elemType := elementType(fc.Type)

	build := func(child *node) FieldContext {
		return FieldContext{
			Type:   elemType,
			Canvas: child.authored,
			Base:   child.payload,
			Draft:  fc.Draft,
			node:   child,
		}
	}

	switch fc.node.kind {
	case kindList, kindMap:
		if fc.node.items == nil {
			return nil
		}

		return []FieldContext{build(fc.node.items)}

	case kindTuple:
		out := make([]FieldContext, len(fc.node.prefix))
		for i, c := range fc.node.prefix {
			out[i] = build(c)
		}

		return out

	default:
		return nil
	}
}

// Constraints returns the intersect-only contribution facade for the field: the
// surface a tag interpreter uses to add numeric bounds, string length, container
// counts, multipleOf, and const/enum/forbidden values through the shared
// constraint algebra. It captures the field's authored canvas, its type-derived
// base, the field's classified shape, and its dereferenced kind (so a numeric
// bound parses at the kind go-playground would parse it at). Each call builds a
// fresh facade over the same canvas, so an interpreter may call it repeatedly.
//
// The facade writes to Canvas, so a caller-built context must populate Canvas
// before contributing through it (a facade over a nil canvas has nowhere to
// record a contribution and panics on write). A context the generator builds
// always carries a canvas; the read-only Effective accessors, by contrast,
// tolerate a nil Canvas and fall back to Base.
func (fc FieldContext) Constraints() *Constraints {
	return fc.ConstraintsFor(fc.Shape())
}

// Shape classifies the field, folding in the two facts [ShapeOf] cannot see.
// The first is the json:",string" flag read from [FieldContext.StructField]. A
// quoted string field double-encodes (its instance is the JSON-quoted text),
// which only the flag distinguishes from a plain string field, while every
// other coercion is already visible in [FieldContext.Base]. The second is
// whether the occurrence admits null. A Go type states only its pointer-ness.
// The generator gives a nil-able slice, map, byte slice, or interface a null
// branch too, and the field's node carries that decision, so this method reads
// it there. A caller-built context has no backing node, so its null admission
// falls back to the pointer-derived answer.
//
// A type= pair in the jsonschema tag never reaches this method. The generator
// rebuilds an overridden field as a non-nullable, non-reference node before it
// runs the tag interpreters, so an interpreter classifies against the
// overridden payload and never against the shape the field carried before the
// override.
//
// A [Nullability] stance recorded for a self-referential type after this method
// runs can withdraw the null admission the method reported. The generator
// refuses a null literal written against any reference the final decision
// leaves unadmitted. The check covers the two field-level writers, the
// jsonschema tag and the tag interpreters, and the report names the tag key or
// the canvas keyword holding the literal. No tag interpreter in this package
// writes such a literal. On a referenced definition the built-in validate
// dialect spells no null, because the constraint matrix ignores the dialect's
// non-zero rule there. A third-party interpreter reaches the check either
// through [FieldContext.Canvas] or through [Constraints.SetConst] and
// [Constraints.SetEnum], which compose a value onto the canvas without
// consulting the matrix. Forbidding a null through [Constraints.Forbid] writes
// under not rather than into a value keyword, so the forbid stays. It asserts
// nothing the reference does not assert already.
//
// Prefer this over calling [ShapeOf] directly when a context is available.
func (fc FieldContext) Shape() Shape {
	shape := tagmodel.ShapeOfQuoted(fc.Type, fc.Base, fc.quotedString())

	if fc.node != nil {
		shape.Nullable = fc.node.nullableDecision()
	}

	return shape
}

// quotedString reports whether the field carries the json:",string" option.
// Which types the flag coerces is decided once, in [tagmodel]'s form
// classification (only an [encoding/json.Number] field's string kind
// reclassifies), so the raw flag is handed over and the relevance test is
// not repeated here. It tolerates the zero StructField of a caller-built
// context.
func (fc FieldContext) quotedString() bool {
	if fc.Type == nil {
		return false
	}

	info, err := jsontag.Parse(fc.StructField)
	if err != nil {
		// Generation refuses an invalid declaration before any interpreter
		// runs, so only a hand-built context reaches this, unquoted.
		return false
	}

	return info.JSONString
}

// ConstraintsFor is [FieldContext.Constraints] for a caller that has already
// classified the field with [ShapeOf], so an interpreter that dispatches on the
// shape itself does not pay to classify it a second time. Classifying is not
// free: following a pointer chain allocates a cycle guard.
func (fc FieldContext) ConstraintsFor(shape Shape) *Constraints {
	return &Constraints{target: fc.targetOf(shape)}
}

// target builds the shared model's write destination for the field: its
// classified shape, the canvas facts land on, the type-derived base a conflict
// is checked against, and a lazy element accessor.
//
// The element closure is the seam that lets the model retarget a rule onto
// element schemas without knowing how a caller finds them. It wraps
// [FieldContext.ElementContexts], which stays the node-backed public accessor,
// so a rule written for the field and the same rule written under a dive reach
// the same destinations through one path. A caller-built context has no backing
// node, so it supplies no elements and an element rule reports rather than
// silently doing nothing.
func (fc FieldContext) target() tagmodel.Target {
	return fc.targetOf(fc.Shape())
}

// targetOf builds the target for an already-classified shape, so a caller that
// needed the shape for its own dispatch does not pay to classify the field
// twice.
//
// The element closure captures the three fields it reads rather than the whole
// context: a FieldContext carries a [reflect.StructField] and several schema
// pointers the closure never touches, and capturing the receiver would copy all
// of it to the heap on every call. It is built only for a node that can have
// elements, since most rules never ask for them.
func (fc FieldContext) targetOf(shape tagmodel.Shape) tagmodel.Target {
	var elems func() []tagmodel.Target

	if node, typ, draft := fc.node, fc.Type, fc.Draft; node != nil {
		elems = func() []tagmodel.Target {
			children := FieldContext{node: node, Type: typ, Draft: draft}.ElementContexts()

			out := make([]tagmodel.Target, len(children))
			for i := range children {
				out[i] = children[i].target()
			}

			return out
		}
	}

	target := tagmodel.NewTarget(shape, fc.Canvas, fc.Base, elems)

	// A $defs-extracted type declares its keywords on the definition, not on
	// the provisional $ref payload the base holds, so the model reads the
	// type-declared values through this seam. The read is deferred because a
	// self-referential type's definition body may not be filled yet when the
	// target is built.
	if def := fcNodeDef(fc.node); def != nil {
		target = target.WithRefBase(func() *Schema {
			if def.body == nil {
				return nil
			}

			return def.body.payload
		})
	}

	return target
}

// fcNodeDef returns the definition entry a field node's payload defers to, or
// nil for a node that is not a reference (or a caller-built context with no
// backing node).
func fcNodeDef(n *node) *defEntry {
	if n == nil {
		return nil
	}

	return n.def
}

// effectiveBase returns fc.Base, or an empty schema when a caller built the
// context without one, so the Effective accessors never dereference a nil Base.
func (fc FieldContext) effectiveBase() *Schema {
	if fc.Base != nil {
		return fc.Base
	}

	return &Schema{}
}

// effectiveCanvas returns fc.Canvas, or an empty schema when a caller built the
// context without one, mirroring [FieldContext.effectiveBase]: a nil canvas
// reads as nothing authored, so the Effective accessors fall through to Base.
func (fc FieldContext) effectiveCanvas() *Schema {
	if fc.Canvas != nil {
		return fc.Canvas
	}

	return &Schema{}
}

// EffectiveFormat reports the effective format keyword: the canvas value, or the
// type-derived one when the canvas has none. [FieldContext.EffectivePattern],
// [FieldContext.EffectiveContentEncoding], and
// [FieldContext.EffectiveContentMediaType] read their keywords the same way.
//
// These are read-only accessors, for an interpreter that wants to know what is
// already in force before deciding what to contribute. They no longer gate the
// write: [Constraints.Apply] applies a string keyword first-wins for an
// interpreter, so a tag never overrides a value an explicit jsonschema tag or
// the field's type already set, and an interpreter gets that without asking.
//
// The accessors tolerate a caller-built context missing Canvas or Base: a nil
// side reads as unauthored (empty), so a hand-built context (an interpreter
// unit test, for example) never panics here.
//
// A $defs-extracted type declares its keywords on the definition rather than
// on the provisional $ref payload the Base holds, so the accessors read
// through the reference: a format declared by the definition is in force on
// the field exactly as an inline one is, and the first-wins write gate sees
// the same value these report.
func (fc FieldContext) EffectiveFormat() string {
	if f := fc.effectiveCanvas().Format; f != "" {
		return f
	}

	return cmp.Or(fc.effectiveBase().Format, fc.refBase().Format)
}

// EffectivePattern reports the effective pattern keyword.
// See [FieldContext.EffectiveFormat].
func (fc FieldContext) EffectivePattern() string {
	if p := fc.effectiveCanvas().Pattern; p != "" {
		return p
	}

	return cmp.Or(fc.effectiveBase().Pattern, fc.refBase().Pattern)
}

// EffectiveContentEncoding reports the effective contentEncoding keyword.
// See [FieldContext.EffectiveFormat].
func (fc FieldContext) EffectiveContentEncoding() string {
	if e := fc.effectiveCanvas().ContentEncoding; e != "" {
		return e
	}

	return cmp.Or(fc.effectiveBase().ContentEncoding, fc.refBase().ContentEncoding)
}

// EffectiveContentMediaType reports the effective contentMediaType keyword.
// See [FieldContext.EffectiveFormat].
func (fc FieldContext) EffectiveContentMediaType() string {
	if m := fc.effectiveCanvas().ContentMediaType; m != "" {
		return m
	}

	return cmp.Or(fc.effectiveBase().ContentMediaType, fc.refBase().ContentMediaType)
}

// refBase returns the schema of the definition the field's payload defers to,
// or an empty schema for a non-reference field (or a caller-built context),
// so the string Effective accessors and the first-wins write gate both see a
// keyword the $defs-extracted type declares outright.
func (fc FieldContext) refBase() *Schema {
	if def := fcNodeDef(fc.node); def != nil && def.body != nil && def.body.payload != nil {
		return def.body.payload
	}

	return &Schema{}
}

// elementType returns the element (or map value) Go type of t, dereferencing a
// pointer to the container first but preserving a pointer element, so a
// []*string yields *string. A non-container type yields nil.
func elementType(t reflect.Type) reflect.Type {
	if t == nil {
		return nil
	}

	t = numkind.DerefType(t)

	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return t.Elem()
	default:
		return nil
	}
}
