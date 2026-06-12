package jsonschema

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// GenerateOption configures schema generation. Options are produced by this
// package's With* constructors; the interface form (rather than a func type)
// lets one option value serve several entry points, the way [WithRefResolver]
// serves both [ValidateOption] and [InlineOption].
type GenerateOption interface {
	applyGenerate(g *generator)
}

// generateOptionFunc adapts a function to [GenerateOption].
type generateOptionFunc func(*generator)

func (f generateOptionFunc) applyGenerate(g *generator) { f(g) }

// WithTagInterpreter registers a [TagInterpreter] under the struct tag key
// it reads (e.g. "validate"), following [net/http.Handle]: the name lives at
// the registration site, so one interpreter implementation can serve several
// keys. Multiple interpreters can be registered and are applied in order.
// [TagInterpreterFunc] adapts a bare function. A nil t or an empty key is
// ignored.
func WithTagInterpreter(key string, t TagInterpreter) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if t != nil && key != "" {
			g.tagInterpreters = append(g.tagInterpreters, tagInterpreterRegistration{key: key, interp: t})
		}
	})
}

// tagInterpreterRegistration pairs a [TagInterpreter] with the struct tag
// key it was registered under.
type tagInterpreterRegistration struct {
	interp TagInterpreter
	key    string
}

// WithDescriptionProvider sets the [DescriptionProvider] consulted for type and
// field descriptions. [NewGoCommentProvider] constructs the AST-backed
// provider that extracts Go doc comments; any other implementation
// substitutes another source. The last registration wins, and a nil p
// restores the default (no provider), leaving descriptions unset.
func WithDescriptionProvider(p DescriptionProvider) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		g.descriptionProvider = p
	})
}

// WithTypeSchemaProvider registers a [TypeSchemaProvider]. Providers occupy the
// highest-priority step of the type resolution chain, overriding even
// [JSONSchemaProvider], and are consulted newest registration first, so a
// later registration takes precedence over an earlier one for the types both
// handle ([WithTypeSchema] registers an exact-match provider into the same
// chain). A nil p is ignored.
//
// A schema the provider supplies is copied before use with the same
// discipline [WithTypeSchema] documents, and [JSONSchemaExtender] is not
// called for types it provides.
func WithTypeSchemaProvider(p TypeSchemaProvider) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if p != nil {
			g.typeProviders = append(g.typeProviders, p)
		}
	})
}

// WithTypeSchemaExtender registers a [TypeSchemaExtender] that modifies
// reflection-generated schemas, the extend counterpart of
// [WithTypeSchemaProvider]: a provider replaces a type's schema wholesale,
// while an extender adjusts what reflection produced — the way
// [JSONSchemaExtender] does for a type's author — for types the caller does
// not own. Multiple extenders can be registered and are applied in
// registration order, each running after the type's own JSONSchemaExtend.
// Like JSONSchemaExtender, an extender is not called for types whose schema
// a registered provider or [JSONSchemaProvider] supplied.
// [TypeSchemaExtenderFunc] adapts a bare function. A nil e is ignored.
func WithTypeSchemaExtender(e TypeSchemaExtender) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if e != nil {
			g.typeExtenders = append(g.typeExtenders, e)
		}
	})
}

// WithTypeSchemaExtenderFor is [WithTypeSchemaExtender] for a statically
// known type, so call sites need not guard on [reflect.TypeFor] themselves:
// f runs only for T, receiving the reflection-generated schema to modify in
// place, and every other type passes through untouched. The signature is
// [TypeSchemaExtenderFunc]'s, eliding only the type guard, so f still
// receives the [TypeContext] and can emit draft-appropriate keywords.
//
//	jsonschema.WithTypeSchemaExtenderFor[pkg.Money](
//		func(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
//			s.Pattern = `^\d+\.\d{2}$`
//			return nil
//		})
//
// The registration-order and not-called-when-replaced semantics of
// [WithTypeSchemaExtender] apply unchanged. A nil f is ignored.
func WithTypeSchemaExtenderFor[T any](
	f func(ctx context.Context, tc TypeContext, s *Schema) error,
) GenerateOption {
	if f == nil {
		return WithTypeSchemaExtender(nil)
	}

	target := reflect.TypeFor[T]()

	return WithTypeSchemaExtender(TypeSchemaExtenderFunc(
		func(ctx context.Context, tc TypeContext, s *Schema) error {
			if tc.Type != target {
				return nil
			}

			return f(ctx, tc, s)
		}))
}

// exactTypeProvider is the [TypeSchemaProvider] registered by
// [WithTypeSchema]: it offers s for exactly the type t.
type exactTypeProvider struct {
	t reflect.Type
	s *Schema
}

func (p exactTypeProvider) SchemaForType(_ context.Context, tc TypeContext) (*Schema, bool, error) {
	if tc.Type != p.t {
		return nil, false, nil
	}

	return p.s, true, nil
}

// WithTypeSchema overrides the generated schema for a specific Go type: it
// registers an exact-match [TypeSchemaProvider], so it shares the
// highest-priority step of the type resolution chain with [WithTypeSchemaProvider],
// overriding even [JSONSchemaProvider]. Useful for mapping third-party types
// or overriding types whose [JSONSchemaProvider] schema is undesirable.
// Providers are consulted newest registration first, so if called multiple
// times for the same type, the last registration wins. A nil s restores the
// type's default resolution: earlier WithTypeSchema registrations for t are
// removed, while predicate providers ([WithTypeSchemaProvider]) and the rest
// of the chain still apply.
//
// The override is copied before use: its sub-schemas are deep-copied and its
// Enum, Const, Default, and Extra containers are cloned, so a tag interpreter
// or [JSONSchemaExtender] that appends to Enum, reassigns Const, or writes into
// Extra during generation cannot reach back into s or into another Generate
// call reusing the same override. Only the top-level containers are cloned:
// nested values keep their identity, so mutating through a pointer, slice, or
// map element held inside one of those values can still leak.
func WithTypeSchema(t reflect.Type, s *Schema) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if s == nil {
			g.typeProviders = slices.DeleteFunc(g.typeProviders, func(p TypeSchemaProvider) bool {
				ep, ok := p.(exactTypeProvider)
				return ok && ep.t == t
			})

			return
		}

		g.typeProviders = append(g.typeProviders, exactTypeProvider{t: t, s: s})
	})
}

// WithTypeSchemaFor is [WithTypeSchema] for a statically known type, so call
// sites need not spell out [reflect.TypeFor]:
//
//	jsonschema.WithTypeSchemaFor[time.Duration](&jsonschema.Schema{Type: "string"})
//
// The copying and last-registration-wins semantics of [WithTypeSchema] apply
// unchanged.
func WithTypeSchemaFor[T any](s *Schema) GenerateOption {
	return WithTypeSchema(reflect.TypeFor[T](), s)
}

// Namer produces the definition name for a Go type: the key the type's
// schema is stored under in $defs (or definitions for [Draft7]) and the
// reference token its $ref uses, plus, with [WithRootTitle], the root
// schema's title. An empty result defers to the built-in namer, so a Namer
// can rename the types it recognizes and pass the rest through (unnamed
// types produce an empty name from the built-in namer too, which leaves a
// [WithRootTitle] title unset). Name collisions between types are still
// disambiguated automatically. [NamerFunc] adapts a bare function.
//
// SchemaName receives the same [TypeContext] as the package's other
// type-level hooks ([TypeSchemaProvider], [TypeSchemaExtender],
// [DescriptionProvider]), carrying the Go type to name and the target
// [Draft] of the generation run.
type Namer interface {
	SchemaName(tc TypeContext) string
}

// NamerFunc adapts a bare naming function to a [Namer], following
// [net/http.HandlerFunc].
type NamerFunc func(tc TypeContext) string

// SchemaName calls f.
func (f NamerFunc) SchemaName(tc TypeContext) string { return f(tc) }

// WithNamer sets a custom [Namer] for producing definition names from
// Go types. Default: uses the type's short name (e.g., "MyStruct").
// A nil n restores the default namer.
func WithNamer(n Namer) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if n == nil {
			n = defaultNamerFunc()
		}

		g.namer = n
	})
}

// WithDefinitions controls whether shared types are extracted into
// $defs (or definitions for Draft-07) and referenced via $ref.
// Default: true.
func WithDefinitions(enabled bool) GenerateOption {
	return generateOptionFunc(func(g *generator) { g.definitions = enabled })
}

// WithAdditionalProperties controls whether additional properties are allowed
// on generated object schemas. By default, generated object schemas include
// "additionalProperties": false, disallowing extra keys.
// WithAdditionalProperties(true) omits both "additionalProperties": false and
// "unevaluatedProperties": false.
func WithAdditionalProperties(allowed bool) GenerateOption {
	return generateOptionFunc(func(g *generator) { g.additionalProperties = allowed })
}

// WithNullable controls whether nil-able Go types (slices, maps, pointers,
// []byte) are made nullable. Default: true. When false, []T -> {"type":"array"},
// map -> {"type":"object"}, *T -> the bare value schema, no null branch.
func WithNullable(allowed bool) GenerateOption {
	return generateOptionFunc(func(g *generator) { g.nullable = allowed })
}

// WithDefaultsFrom seeds property defaults on the root object schema from an
// instance of the generated type. After generation, instance is marshaled
// with encoding/json; each top-level key in the output that matches a root
// property gets its value as that property's Default, overwriting any
// default set via struct tags. Keys omitted by omitempty or omitzero leave
// Default unset, so presence follows the json tags exactly.
//
// A nil instance restores the default, where no defaults are seeded,
// following the package's nil convention. A typed nil pointer is a value,
// not a reset: it marshals to JSON null rather than to an object, so it
// returns the error below.
//
// Generate returns an error wrapping [ErrInvalidDefaultsInstance] when the
// pointer-dereferenced dynamic type of instance is not the
// pointer-dereferenced generated type, or when the instance does not marshal
// to a JSON object. Nested struct, slice, and map values become whole-value
// defaults on their top-level property. A pointer root's nullable anyOf
// wrapper is resolved to its value branch first, so the defaults reach the
// object schema (or its $defs entry) inside. When the root schema remains a
// $defs entry because the type references itself, the defaults are applied to
// that definition's properties, so every recursive occurrence of the type
// shares them. Under [Draft7], a default landing on a $ref'd property moves
// the $ref into an allOf wrap, the same shape tag defaults produce, because
// Draft-07 readers ignore keywords beside $ref.
func WithDefaultsFrom(instance any) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		g.defaultsFrom = instance
		g.defaultsFromSet = instance != nil
	})
}

// WithRootTitle controls whether the root schema's title is set to the
// generated root type's name when no title is otherwise present. The
// configured namer ([WithNamer]) is honored, so root and $defs naming stay
// consistent. Unnamed roots (anonymous structs, maps, slices) leave the
// title unset. Under [Draft7], a self-referential root stays a bare $ref
// into definitions, where a sibling title would be ignored; the title is set
// on the definitions entry instead, shared by every occurrence of the type.
// Defaults to false.
func WithRootTitle(enabled bool) GenerateOption {
	return generateOptionFunc(func(g *generator) { g.rootTitle = enabled })
}

// applyInstanceDefaults marshals the [WithDefaultsFrom] instance and copies
// each top-level key of the output onto the matching property's Default in
// schema. The instance's pointer-dereferenced dynamic type must be rootType
// (the pointer-dereferenced generated type) and the marshaled output must be
// a JSON object; either violation returns an error wrapping
// [ErrInvalidDefaultsInstance]. Keys absent from the output (omitted by
// omitempty or omitzero) leave their property untouched, and keys without a
// matching property are ignored. Under [Draft7], a property that receives a
// default beside a non-empty $ref has the $ref moved into an allOf wrap
// (see [generator.wrapRefForDraft7]); Draft-07 readers ignore keywords
// beside $ref, so the sibling default would otherwise be discarded.
func (g *generator) applyInstanceDefaults(instance any, rootType reflect.Type, schema *Schema) error {
	instType := reflect.TypeOf(instance)
	for instType != nil && instType.Kind() == reflect.Pointer {
		instType = instType.Elem()
	}

	if instType != rootType {
		return fmt.Errorf("%w: instance type %v does not match root type %s",
			ErrInvalidDefaultsInstance, instType, rootType)
	}

	data, err := json.Marshal(instance)
	if err != nil {
		return fmt.Errorf("marshal defaults instance: %w", err)
	}

	var values map[string]json.RawMessage

	err = json.Unmarshal(data, &values)
	// Unmarshaling JSON null into a map leaves it nil without an error, so a
	// nil map means the instance marshaled to null rather than to an object.
	if err != nil || values == nil {
		return fmt.Errorf("%w: instance of type %s does not marshal to a JSON object",
			ErrInvalidDefaultsInstance, instType)
	}

	for key, raw := range values {
		if prop, ok := schema.Properties[key]; ok && prop != nil {
			prop.Default = raw
			// The default may now sit beside a $ref (a definitions-extracted
			// field), where Draft-07 readers would ignore it; wrap the $ref
			// in allOf, the same shape the tag-default path produces. This
			// runs after disambiguateDefs, so repointing the tracked ref
			// record inside the wrap is harmless. No-op for other drafts and
			// for properties without a $ref.
			g.wrapRefForDraft7(prop)
		}
	}

	return nil
}

// Generator generates schemas from one fixed option set, the
// generation-side counterpart of [Validator]: [NewGenerator] applies the
// options once and the returned Generator is reused, so a caller generating
// schemas for many types neither re-passes nor re-applies the option slice
// per call.
//
// A Generator is safe for concurrent use by multiple goroutines, provided
// the configured hooks are: the configuration is only read during
// generation, every run keeps its own state, and the hook interfaces
// document their own concurrency contracts ([DescriptionProvider],
// [RefResolver]).
type Generator struct {
	proto *generator
}

// NewGenerator returns a [Generator] with the given options applied. Nil
// options are skipped, so an optional option can be passed unconditionally.
func NewGenerator(opts ...GenerateOption) *Generator {
	return &Generator{proto: newGenerator(opts)}
}

// Generate generates a JSON Schema for the given [reflect.Type] under the
// Generator's options. The context follows the [GenerateFor] contract. For
// a statically known type, pass [reflect.TypeFor]:
//
//	gen.Generate(ctx, reflect.TypeFor[MyType]())
func (gn *Generator) Generate(ctx context.Context, t reflect.Type) (*Schema, error) {
	return gn.proto.forRun(ctx).generate(t)
}

// GenerateFor generates a JSON Schema for the type parameter T.
//
// The context is passed to the [DescriptionProvider] (see [WithDescriptionProvider])
// with every comment lookup, so the built-in provider's package loading can
// honor cancellation and deadlines.
func GenerateFor[T any](ctx context.Context, opts ...GenerateOption) (*Schema, error) {
	return Generate(ctx, reflect.TypeFor[T](), opts...)
}

// MustGenerateFor is [GenerateFor] with [context.Background] but panics on
// error; intended for package-scope variables and init-time generation,
// where for a static type and fixed options generation either always
// succeeds or always fails, so a failure is a programming error best
// surfaced at startup. It follows [MustRaw].
func MustGenerateFor[T any](opts ...GenerateOption) *Schema {
	s, err := GenerateFor[T](context.Background(), opts...)
	if err != nil {
		panic(err)
	}

	return s
}

// Generate generates a JSON Schema for the given [reflect.Type]. The
// context follows the [GenerateFor] contract. It is one-shot sugar for
// [NewGenerator] plus [Generator.Generate]; to generate schemas for many
// types under one option set, build the [Generator] once and reuse it.
func Generate(ctx context.Context, t reflect.Type, opts ...GenerateOption) (*Schema, error) {
	return NewGenerator(opts...).Generate(ctx, t)
}

// MustGenerate is [Generate] with [context.Background] but panics on error;
// it is the [reflect.Type] form of [MustGenerateFor], for package-scope
// variables and init-time generation from dynamically obtained types.
func MustGenerate(t reflect.Type, opts ...GenerateOption) *Schema {
	s, err := Generate(context.Background(), t, opts...)
	if err != nil {
		panic(err)
	}

	return s
}
