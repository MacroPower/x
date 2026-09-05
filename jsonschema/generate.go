package jsonschema

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonopts"
	"go.jacobcolvin.com/x/jsonschema/internal/numkind"
	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
	"go.jacobcolvin.com/x/jsonschema/internal/typename"
)

// GenerateOption configures schema generation. Options are produced by this
// package's With* constructors; the interface form (rather than a func type)
// lets one option value serve several entry points, the way [WithRefResolver]
// serves both [ValidateOption] and [InlineOption].
type GenerateOption interface {
	applyGenerate(c *generatorConfig)
}

// generateOptionFunc adapts a function to [GenerateOption].
type generateOptionFunc func(*generatorConfig)

func (f generateOptionFunc) applyGenerate(c *generatorConfig) { f(c) }

// WithTagInterpreter registers a [TagInterpreter] under the struct tag key
// it reads (e.g. "validate"), following [net/http.Handle]: the name lives at
// the registration site, so one interpreter implementation can serve several
// keys. Multiple interpreters can be registered and are applied in order.
// [TagInterpreterFunc] adapts a bare function. A nil t or an empty key is
// ignored.
func WithTagInterpreter(key string, t TagInterpreter) GenerateOption {
	return generateOptionFunc(func(g *generatorConfig) {
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
	return generateOptionFunc(func(g *generatorConfig) {
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
	return generateOptionFunc(func(g *generatorConfig) {
		if p != nil {
			g.typeProviders = append(g.typeProviders, p)
		}
	})
}

// WithTypeSchemaExtender registers a [TypeSchemaExtender] that modifies
// reflection-generated schemas. It is the extending counterpart to
// [WithTypeSchemaProvider]: a provider replaces a type's schema wholesale,
// while an extender adjusts what reflection produced for types the caller does
// not own. For those types it serves the purpose that [JSONSchemaExtender]
// serves for a type's own author. Multiple extenders can be registered and are
// applied in registration order, each running after the type's own
// JSONSchemaExtend. Like JSONSchemaExtender, an extender is not called for
// types whose schema a registered provider or [JSONSchemaProvider] supplied.
// [TypeSchemaExtenderFunc] adapts a bare function. A nil e is ignored.
func WithTypeSchemaExtender(e TypeSchemaExtender) GenerateOption {
	return generateOptionFunc(func(g *generatorConfig) {
		if e != nil {
			g.typeExtenders = append(g.typeExtenders, e)
		}
	})
}

// WithTypeSchemaExtenderFor is [WithTypeSchemaExtender] for a statically
// known type, so call sites need not guard on [reflect.TypeFor] themselves:
// f runs only for T, receiving the [TypeSchema] whose Value is the
// reflection-generated schema to modify in place (and whose Nullability it may
// set), and every other type passes through untouched. The signature is
// [TypeSchemaExtenderFunc]'s, eliding only the type guard, so f still
// receives the [TypeContext] and can emit draft-appropriate keywords.
//
//	jsonschema.WithTypeSchemaExtenderFor[pkg.Money](
//		func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
//			ts.Value.Pattern = `^\d+\.\d{2}$`
//			return nil
//		})
//
// The registration-order and not-called-when-replaced semantics of
// [WithTypeSchemaExtender] apply unchanged. A nil f is ignored.
func WithTypeSchemaExtenderFor[T any](
	f func(ctx context.Context, tc TypeContext, ts *TypeSchema) error,
) GenerateOption {
	if f == nil {
		return WithTypeSchemaExtender(nil)
	}

	target := reflect.TypeFor[T]()

	return WithTypeSchemaExtender(TypeSchemaExtenderFunc(
		func(ctx context.Context, tc TypeContext, ts *TypeSchema) error {
			if tc.Type != target {
				return nil
			}

			return f(ctx, tc, ts)
		},
	))
}

// exactTypeProvider is the [TypeSchemaProvider] registered by
// [WithTypeSchema]: it offers ts for exactly the type t.
type exactTypeProvider struct {
	t  reflect.Type
	ts TypeSchema
}

func (p exactTypeProvider) SchemaForType(_ context.Context, tc TypeContext) (TypeSchema, error) {
	if tc.Type != p.t {
		return TypeSchema{}, fmt.Errorf("%w: %s", ErrTypeNotHandled, tc.Type)
	}

	return p.ts, nil
}

// WithTypeSchema overrides the generated schema for a specific Go type: it
// registers an exact-match [TypeSchemaProvider], so it shares the
// highest-priority step of the type resolution chain with [WithTypeSchemaProvider],
// overriding even [JSONSchemaProvider]. Useful for mapping third-party types
// or overriding types whose [JSONSchemaProvider] schema is undesirable.
// Providers are consulted newest registration first, so if called multiple
// times for the same type, the last registration wins. Every call adds a
// registration; a zero [TypeSchema] marks the type unrestricted ({}).
//
// The override's [TypeSchema.Value] (or [TypeSchema.Verbatim]) is copied before
// use: its sub-schemas are deep-copied and its Enum, Const, Default, and Extra
// containers are cloned, so a tag interpreter or [JSONSchemaExtender] that
// appends to Enum, reassigns Const, or writes into Extra during generation
// cannot reach back into the override or into another Generate call reusing it.
// Only the top-level containers are cloned: nested values keep their identity,
// so mutating through a pointer, slice, or map element held inside one of those
// values can still leak.
func WithTypeSchema(t reflect.Type, ts TypeSchema) GenerateOption {
	return generateOptionFunc(func(g *generatorConfig) {
		g.typeProviders = append(g.typeProviders, exactTypeProvider{t: t, ts: ts})
	})
}

// WithTypeSchemaFor is [WithTypeSchema] for a statically known type, so call
// sites need not spell out [reflect.TypeFor]:
//
//	jsonschema.WithTypeSchemaFor[time.Duration](jsonschema.TypeSchema{
//		Value: &jsonschema.Schema{Type: "string"}})
//
// The copying and last-registration-wins semantics of [WithTypeSchema] apply
// unchanged.
func WithTypeSchemaFor[T any](ts TypeSchema) GenerateOption {
	return WithTypeSchema(reflect.TypeFor[T](), ts)
}

// Namer produces the definition name for a Go type: the key the type's
// schema is stored under in $defs (or definitions for [Draft7]) and the
// reference token its $ref uses, plus, with [WithRootTitle], the root
// schema's title. An empty result defers to the built-in namer, so a Namer
// can rename the types it recognizes and pass the rest through (unnamed
// types produce an empty name from the built-in namer too, which leaves a
// [WithRootTitle] title unset). Name collisions between types are still
// disambiguated automatically. [NamerFunc] adapts a bare function. The
// returned name is sanitized for $defs-key and $ref-token safety: characters
// invalid in a JSON Pointer token, such as '/' and '~' (and the generic-type
// brackets and commas the default namer replaces), become underscores.
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
	return generateOptionFunc(func(g *generatorConfig) {
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
	return generateOptionFunc(func(g *generatorConfig) { g.definitions = enabled })
}

// WithAdditionalProperties controls whether additional properties are allowed
// on generated object schemas. By default, generated object schemas include
// "additionalProperties": false, disallowing extra keys.
// WithAdditionalProperties(true) omits both "additionalProperties": false and
// "unevaluatedProperties": false, and an embedded fallback's value schema
// with them. The option asks for no extra-member constraint, so the object
// stays fully open. The fallback's value type is still resolved, so one with
// no representation refuses generation under every option.
func WithAdditionalProperties(allowed bool) GenerateOption {
	return generateOptionFunc(func(g *generatorConfig) { g.additionalProperties = allowed })
}

// WithJSONOptions makes generation honor [encoding/json/v2] marshal options
// that change the marshaled shape, so the generated schema accepts exactly
// what [json.Marshal] emits for a value under the same options. Repeated
// calls join with [json.JoinOptions] semantics, where later options win.
// Three options are honored:
//
//   - [json.FormatNilSliceAsNull]: every slice and []byte occurrence admits
//     null, since the marshal writes null for a nil one.
//   - [json.FormatNilMapAsNull]: every map occurrence admits null likewise.
//   - [json.OmitZeroStructFields]: every struct field behaves as ,omitzero,
//     so no required entries are emitted.
//
// A [NullForbidden] stance on a type takes the null off every occurrence of
// that type, format options included. [WithDefaultsFrom] marshals its
// instance under these options, so seeded defaults match the caller's
// marshal.
// [Validator.ValidateValue] marshals with the defaults, so under
// non-default options marshal the value with them and validate the bytes
// through [Validator.ValidateJSON] or [Validator.ValidateReader].
//
// Options that change the marshaled shape in ways generation does not model
// are refused with [ErrUnsupportedJSONOption] when set: [json.StringifyNumbers]
// with true (it stringifies numbers inside containers, beyond what the
// per-field ,string tag machinery reaches), a non-nil [json.WithMarshalers]
// (its output shape is unknowable), and the [encoding/json] v1 compat options
// that alter the marshaled shape ([encoding/json.OmitEmptyWithLegacySemantics],
// [encoding/json.FormatByteArrayAsArray], [encoding/json.FormatBytesWithLegacySemantics],
// [encoding/json.StringifyWithLegacySemantics],
// [encoding/json.CallMethodsWithLegacySemantics],
// [encoding/json.ReportErrorsWithLegacySemantics], which marshals a declaration v2
// otherwise refuses in a best-effort form, and
// [encoding/json.FormatDurationAsNano] -- so [encoding/json.DefaultOptionsV1], which
// bundles them, is refused too). Options with no effect on the marshaled
// shape are ignored: unmarshal-side options such as
// [json.MatchCaseInsensitiveNames] and [json.RejectUnknownMembers],
// [json.Deterministic], and jsontext formatting.
func WithJSONOptions(opts ...json.Options) GenerateOption {
	return generateOptionFunc(func(g *generatorConfig) {
		// JoinOptions skips nil sources, so the first call needs no guard.
		joined := json.JoinOptions(g.jsonOpts, json.JoinOptions(opts...))
		g.jsonOpts = joined

		// Re-derive every flag and the refusal from the full joined value,
		// so a later call can also unset what an earlier one set. The
		// classification of every constructor lives in internal/jsonopts.
		flags := jsonopts.Honored(joined)
		g.nilSliceNull = flags.NilSliceNull
		g.nilMapNull = flags.NilMapNull
		g.omitZeroFields = flags.OmitZeroFields

		g.jsonOptsErr = nil

		if name, ok := jsonopts.Refused(joined); ok {
			g.jsonOptsErr = fmt.Errorf("%w: %s", ErrUnsupportedJSONOption, name)
		}
	})
}

// WithDefaultsFrom seeds property defaults on the root object schema from an
// instance of the generated type. After generation, instance is marshaled
// with [encoding/json/v2] under the [WithJSONOptions] options, so seeded
// defaults match the caller's marshal; each top-level key in the output that
// matches a root property gets its value as that property's Default,
// overwriting any default set via struct tags. Keys omitted by omitempty or
// omitzero leave Default unset, so presence follows the json tags exactly.
// Map values marshal in sorted key order, so a map-valued default seeds
// identical bytes on every run.
//
// A nil instance restores the default, where no defaults are seeded,
// following the package's nil convention. A typed nil pointer is a value,
// not a reset; it marshals to JSON null rather than to an object, and
// Generate then reports the non-object error below.
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
	return generateOptionFunc(func(g *generatorConfig) {
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
	return generateOptionFunc(func(g *generatorConfig) { g.rootTitle = enabled })
}

// seedDefaults seeds the [WithDefaultsFrom] instance's top-level keys onto the
// root object's properties before render. The instance's pointer-dereferenced
// dynamic type must be rootType (the pointer-dereferenced generated type) and
// the marshaled output must be a JSON object; either violation returns an
// error wrapping [ErrInvalidDefaultsInstance]. A key absent from the output
// (omitted by omitempty or omitzero) leaves its property untouched, a key with
// no matching property is ignored, and a key marshaling to JSON null against a
// property that admits no null is skipped. A skipped key writes nothing, so
// the property keeps whatever default its tag wrote.
//
// A property has one of two homes. A field node takes the default on its
// canvas, where render composes it like a tag default (onto the null wrapper
// of a pointer field, into the Draft-07 allOf wrap beside a $ref), and the
// node's own null decision gates a null. A property a hook declared as a
// literal (an override root's TypeSchema.Value, or a slot an extender
// replaced) takes it on the literal, gated by [run.declaredAdmitsNull].
func (g *run) seedDefaults(root *node, rootType reflect.Type) error {
	values, err := g.marshalDefaults(rootType)
	if err != nil {
		return err
	}

	// A self- or mutually recursive root stayed a $ref: seed the shared $defs
	// body so every occurrence of the type carries the defaults. A pointer
	// root's null wrapper is a render-time encoding, so the node beneath it
	// is the target either way.
	target := root
	if root.kind == kindRef {
		target = root.def.body
	}

	// A target that is itself a bare $ref a hook declared has no properties to
	// seed; surface that rather than silently applying nothing.
	if target.payload.Ref != "" {
		return fmt.Errorf("%w: root of type %s resolved to a bare $ref (%s) with no seedable properties",
			ErrInvalidDefaultsInstance, rootType, target.payload.Ref)
	}

	for key, raw := range values {
		if p := target.prop(key); p != nil {
			if isRawNull(raw) && !p.null.admit {
				continue
			}

			p.authored.Default = raw

			continue
		}

		prop, ok := target.payload.Properties[key]
		if !ok || prop == nil {
			continue
		}

		if isRawNull(raw) && !g.declaredAdmitsNull(prop, map[*Schema]bool{}) {
			continue
		}

		prop.Default = raw
		// The default may now sit beside a $ref the hook declared, where
		// Draft-07 readers would ignore it; wrap the $ref in allOf, the same
		// shape the field path renders.
		g.wrapRefForDraft7(prop)
	}

	return nil
}

// marshalDefaults marshals the [WithDefaultsFrom] instance and decodes its
// top-level members, checking that the instance is a value of rootType that
// marshals to a JSON object.
func (g *run) marshalDefaults(rootType reflect.Type) (map[string]jsontext.Value, error) {
	instance := g.defaultsFrom

	// DerefType guards against pointer cycles (type P *P, or a mutually
	// recursive pair), returning the still-unresolved pointer type; that can
	// never equal the non-pointer rootType, so the mismatch check below
	// rejects it.
	instType := reflect.TypeOf(instance)
	if instType != nil {
		instType = numkind.DerefType(instType)
	}

	if instType != rootType {
		return nil, fmt.Errorf("%w: instance type %v does not match root type %s",
			ErrInvalidDefaultsInstance, instType, rootType)
	}

	// The WithJSONOptions options apply, so a seeded default is what the
	// caller's own marshal would emit (a nil slice seeds null under
	// FormatNilSliceAsNull, say). Deterministic keeps map members sorted, so
	// a map-valued default seeds the same bytes on every run and committed
	// schema output stays stable; it changes ordering only, never shape, so
	// forcing it after the caller's options is safe.
	data, err := json.Marshal(instance, g.jsonOpts, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("marshal defaults instance: %w", err)
	}

	// The top-level shape is settled once, before the decode: null gets its
	// own message, every other non-object kind shares one, and only a
	// genuine object proceeds.
	if kind := jsontext.Value(data).Kind(); kind != '{' {
		reason := "does not marshal to a JSON object"
		if kind == 'n' {
			reason = "marshals to JSON null, not an object"
		}

		return nil, fmt.Errorf("%w: instance of type %s %s", ErrInvalidDefaultsInstance, instType, reason)
	}

	var values map[string]jsontext.Value

	err = json.Unmarshal(data, &values)
	if err != nil {
		// The marshal above can emit bytes the default-options decode here
		// refuses (a jsontext.Value field carrying duplicate member names
		// under AllowDuplicateNames, say). A schema default must survive the
		// document's own default-options marshal, so the refusal stands.
		return nil, fmt.Errorf("%w: instance of type %s does not decode under default options: %w",
			ErrInvalidDefaultsInstance, instType, err)
	}

	return values, nil
}

// declaredAdmitsNull reports whether a schema a hook declared as a literal
// accepts a JSON null, carrying the set of schemas already asked. A
// build-time extender's literal keeps its aliases, so a schema aliased into
// its own anyOf would otherwise recurse until the stack gives out. A schema
// asked twice answers no, the conservative direction. A union caller stops at
// the first yes, so a revisit cannot change its answer. The allOf scan keeps
// asking after a yes, so a branch that revisits a schema an earlier branch
// marked reads no where a fresh visit would read yes. That miss drops a
// usable default and never admits a null the property forbids.
//
// Five encodings answer yes. The schema admits null itself (an empty schema,
// or a type keyword naming null). Its const is a null, or its enum holds one.
// One anyOf or oneOf branch admits null. Every allOf branch admits null,
// since an intersection accepts an instance only when all of its branches
// do. Or the schema carries a $ref whose def body admits null: a container
// body that folds the null into its own type list, or a body whose declared
// base admits it, since a hook can put the null in the shared def body rather
// than on the reference.
//
// A not goes unread, since it inverts its subschema's answer.
func (g *run) declaredAdmitsNull(s *Schema, seen map[*Schema]bool) bool {
	if s == nil || seen[s] {
		return false
	}

	seen[s] = true

	if schemashape.IsEmpty(s) || schemashape.DeclaresType(s, typename.Null) {
		return true
	}

	if s.Const != nil && isJSONNull(*s.Const) {
		return true
	}

	if slices.ContainsFunc(s.Enum, isJSONNull) {
		return true
	}

	branch := func(b *Schema) bool { return g.declaredAdmitsNull(b, seen) }
	if slices.ContainsFunc(s.AnyOf, branch) || slices.ContainsFunc(s.OneOf, branch) {
		return true
	}

	// An empty allOf constrains nothing, so it answers for no instance and the
	// remaining encodings decide.
	if len(s.AllOf) > 0 && !slices.ContainsFunc(s.AllOf, func(b *Schema) bool {
		return !g.declaredAdmitsNull(b, seen)
	}) {
		return true
	}

	if s.Ref != "" {
		if e, ok := g.payloadRefTargets()[s.Ref]; ok && e.body != nil {
			body := e.body

			return (body.null.admit && body.nilableContainer()) || g.declaredAdmitsNull(body.payload, seen)
		}
	}

	return false
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
	config *generatorConfig
}

// NewGenerator returns a [Generator] with the given options applied. Nil
// options are skipped, so an optional option can be passed unconditionally.
func NewGenerator(opts ...GenerateOption) *Generator {
	return &Generator{config: newConfig(opts)}
}

// Generate generates a JSON Schema for the given [reflect.Type] under the
// Generator's options. The context follows the [GenerateFor] contract. For
// a statically known type, [GenerateWith] is the generic form:
//
//	jsonschema.GenerateWith[MyType](ctx, gen)
func (gn *Generator) Generate(ctx context.Context, t reflect.Type) (*Schema, error) {
	return gn.config.forRun(ctx).generate(t)
}

// GenerateFor generates a JSON Schema for the type parameter T.
//
// The context is passed to the [DescriptionProvider] (see [WithDescriptionProvider])
// with every comment lookup, so the built-in provider's package loading can
// honor cancellation and deadlines.
func GenerateFor[T any](ctx context.Context, opts ...GenerateOption) (*Schema, error) {
	return Generate(ctx, reflect.TypeFor[T](), opts...)
}

// GenerateWith is [GenerateFor] under a reusable [Generator]: gen's options
// apply and the type comes from the type parameter, so a caller who built
// the Generator once keeps the generic entry point instead of spelling out
// [reflect.TypeFor] at every [Generator.Generate] call. It exists as a
// package function because Go methods cannot take type parameters.
//
//	gen := jsonschema.NewGenerator(opts...)
//	schema, err := jsonschema.GenerateWith[MyType](ctx, gen)
func GenerateWith[T any](ctx context.Context, gen *Generator) (*Schema, error) {
	return gen.Generate(ctx, reflect.TypeFor[T]())
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
