package jsonschema

import "reflect"

// Option configures schema generation.
type Option func(*generator)

// WithDraft sets the target JSON Schema draft version.
// Default: Draft2020.
func WithDraft(d Draft) Option {
	return func(g *generator) { g.draft = d }
}

// WithTagInterpreter registers a TagInterpreter that maps struct tags to
// schema constraints. Multiple interpreters can be registered and are
// applied in order.
func WithTagInterpreter(t TagInterpreter) Option {
	return func(g *generator) {
		if t != nil {
			g.tagInterpreters = append(g.tagInterpreters, t)
		}
	}
}

// WithComments enables extraction of Go doc comments as descriptions.
// Requires access to source files at generation time.
func WithComments(enabled bool) Option {
	return func(g *generator) { g.comments = enabled }
}

// WithTypeSchema overrides the generated schema for a specific Go type.
// Takes the highest priority in the type resolution chain, overriding even
// JSONSchemaProvider. Useful for mapping third-party types or overriding
// types whose JSONSchemaProvider schema is undesirable.
// If called multiple times for the same type, the last registration wins.
//
// The override is shallow-cloned before use, so non-sub-schema fields (Enum,
// Const, Default, Extra) remain shared with s. Treat s as read-only: a tag
// interpreter or JSONSchemaExtender that mutates those fields in place can
// affect s and any other Generate call that reuses the same override.
func WithTypeSchema(t reflect.Type, s *Schema) Option {
	return func(g *generator) { g.typeSchemas[t] = s }
}

// WithNamer sets a custom function for producing definition names from
// Go types. Default: uses the type's short name (e.g., "MyStruct").
func WithNamer(fn func(reflect.Type) string) Option {
	return func(g *generator) {
		if fn != nil {
			g.namer = fn
		}
	}
}

// WithDefinitions controls whether shared types are extracted into
// $defs (or definitions for Draft-07) and referenced via $ref.
// Default: true.
func WithDefinitions(enabled bool) Option {
	return func(g *generator) { g.definitions = enabled }
}

// WithAdditionalProperties controls whether additional properties are allowed
// on generated object schemas. By default, generated object schemas include
// "additionalProperties": false, disallowing extra keys.
// WithAdditionalProperties(true) omits both "additionalProperties": false and
// "unevaluatedProperties": false.
func WithAdditionalProperties(allowed bool) Option {
	return func(g *generator) { g.additionalProperties = allowed }
}

// GenerateFor generates a JSON Schema for the type parameter T.
func GenerateFor[T any](opts ...Option) (*Schema, error) {
	return Generate(reflect.TypeFor[T](), opts...)
}

// Generate generates a JSON Schema for the given [reflect.Type].
func Generate(t reflect.Type, opts ...Option) (*Schema, error) {
	g := newGenerator(opts)
	return g.generate(t)
}
