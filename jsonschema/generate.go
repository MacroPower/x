package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// GenerateOption configures schema generation. Options are produced by this
// package's With* constructors; the interface form (rather than a func type)
// lets one option value serve several entry points, the way [WithResolver]
// serves both [ValidateOption] and [InlineOption].
type GenerateOption interface {
	applyGenerate(g *generator)
}

// generateOptionFunc adapts a function to [GenerateOption].
type generateOptionFunc func(*generator)

func (f generateOptionFunc) applyGenerate(g *generator) { f(g) }

// WithTagInterpreter registers a TagInterpreter that maps struct tags to
// schema constraints. Multiple interpreters can be registered and are
// applied in order. [TagInterpreterFunc] adapts a bare function. A nil t is
// ignored.
func WithTagInterpreter(t TagInterpreter) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if t != nil {
			g.tagInterpreters = append(g.tagInterpreters, t)
		}
	})
}

// WithComments controls extraction of Go doc comments as descriptions.
// WithComments(true) registers the built-in [CommentProvider], which loads
// and parses package sources with [golang.org/x/tools/go/packages] at
// generation time and so requires access to source files; when sources
// cannot be located for a type, it silently supplies no comment.
// WithComments(false) clears any registered provider. Between WithComments
// and [WithCommentProvider], the last registration wins.
func WithComments(enabled bool) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if enabled {
			g.commentProvider = newCommentExtractor()
		} else {
			g.commentProvider = nil
		}
	})
}

// WithCommentProvider sets the [CommentProvider] consulted for type and
// field descriptions, replacing the AST-backed provider [WithComments]
// registers. A nil p is ignored, leaving any earlier registration in place;
// otherwise the last registration wins, so a later WithComments call
// replaces (or, with false, clears) the provider.
func WithCommentProvider(p CommentProvider) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if p != nil {
			g.commentProvider = p
		}
	})
}

// WithTypeResolver registers a [TypeSchemaResolver]. Resolvers occupy the
// highest-priority step of the type resolution chain, overriding even
// [JSONSchemaProvider], and are consulted newest registration first, so a
// later registration takes precedence over an earlier one for the types both
// handle ([WithTypeSchema] registers an exact-match resolver into the same
// chain). A nil r is ignored.
//
// A schema the resolver supplies is copied before use with the same
// discipline [WithTypeSchema] documents, and [JSONSchemaExtender] is not
// called for types it resolves.
func WithTypeResolver(r TypeSchemaResolver) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if r != nil {
			g.typeResolvers = append(g.typeResolvers, r)
		}
	})
}

// exactTypeResolver is the [TypeSchemaResolver] registered by
// [WithTypeSchema]: it offers s for exactly the type t.
type exactTypeResolver struct {
	t reflect.Type
	s *Schema
}

func (r exactTypeResolver) SchemaForType(t reflect.Type) (*Schema, bool) {
	if t != r.t {
		return nil, false
	}

	return r.s, true
}

// WithTypeSchema overrides the generated schema for a specific Go type: it
// registers an exact-match [TypeSchemaResolver], so it shares the
// highest-priority step of the type resolution chain with [WithTypeResolver],
// overriding even [JSONSchemaProvider]. Useful for mapping third-party types
// or overriding types whose [JSONSchemaProvider] schema is undesirable.
// Resolvers are consulted newest registration first, so if called multiple
// times for the same type, the last registration wins. A nil s is ignored,
// leaving any earlier registration for t in place.
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
		if s != nil {
			g.typeResolvers = append(g.typeResolvers, exactTypeResolver{t: t, s: s})
		}
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
// schema's title (where an empty result leaves the title unset). Name
// collisions between types are still disambiguated automatically.
type Namer func(t reflect.Type) string

// WithNamer sets a custom [Namer] for producing definition names from
// Go types. Default: uses the type's short name (e.g., "MyStruct").
// A nil fn is ignored, keeping the default.
func WithNamer(fn Namer) GenerateOption {
	return generateOptionFunc(func(g *generator) {
		if fn != nil {
			g.namer = fn
		}
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
		g.defaultsFromSet = true
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

// GenerateFor generates a JSON Schema for the type parameter T.
func GenerateFor[T any](opts ...GenerateOption) (*Schema, error) {
	return Generate(reflect.TypeFor[T](), opts...)
}

// MustGenerateFor is [GenerateFor] but panics on error; intended for
// package-scope variables and init-time generation, where for a static type
// and fixed options generation either always succeeds or always fails, so a
// failure is a programming error best surfaced at startup. It follows
// [MustRaw].
func MustGenerateFor[T any](opts ...GenerateOption) *Schema {
	s, err := GenerateFor[T](opts...)
	if err != nil {
		panic(err)
	}

	return s
}

// Generate generates a JSON Schema for the given [reflect.Type].
func Generate(t reflect.Type, opts ...GenerateOption) (*Schema, error) {
	g := newGenerator(opts)
	return g.generate(t)
}
