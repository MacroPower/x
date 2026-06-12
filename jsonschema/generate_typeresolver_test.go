package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// stringerKind is a named type implementing fmt.Stringer for resolver
// predicate tests.
type stringerKind int

func (stringerKind) String() string { return "kind" }

// plainKind is a named type that implements nothing, so it falls through
// every resolver predicate to kind-based reflection.
type plainKind int

// stringerResolver resolves every fmt.Stringer to a plain string schema.
func stringerResolver() jsonschema.TypeSchemaResolver {
	return jsonschema.TypeSchemaResolverFunc(func(t reflect.Type) (*jsonschema.Schema, bool) {
		if !t.Implements(reflect.TypeFor[fmt.Stringer]()) {
			return nil, false
		}

		return &jsonschema.Schema{Type: "string"}, true
	})
}

func TestWithTypeSchemaResolver(t *testing.T) {
	t.Parallel()

	type doc struct {
		Kind  stringerKind `json:"kind"`
		Plain plainKind    `json:"plain"`
	}

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"predicate resolver overrides matching types only": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaResolver(stringerResolver())},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"later WithTypeSchema wins over earlier resolver": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaResolver(stringerResolver()),
				jsonschema.WithTypeSchemaFor[stringerKind](&jsonschema.Schema{Type: "string", Format: "uri"}),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string", "format": "uri"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"later resolver wins over earlier WithTypeSchema": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[stringerKind](&jsonschema.Schema{Type: "string", Format: "uri"}),
				jsonschema.WithTypeSchemaResolver(stringerResolver()),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"nil schema with ok true is unrestricted": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaResolver(jsonschema.TypeSchemaResolverFunc(
					func(t reflect.Type) (*jsonschema.Schema, bool) {
						return nil, t == reflect.TypeFor[stringerKind]()
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": true,
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"nil resolver is ignored": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaResolver(nil)},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "integer"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(), tc.opts...)
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestWithTypeSchema_LastRegistrationWins(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[plainKind](t.Context(),
		jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
		jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "number"}),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "number"
	}`, string(got))
}

// TestWithTypeSchema_NilUnregisters proves a nil schema restores the type's
// default resolution: earlier exact registrations for the type are removed,
// while predicate resolvers still apply.
func TestWithTypeSchema_NilUnregisters(t *testing.T) {
	t.Parallel()

	t.Run("removes earlier exact registration", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[plainKind](t.Context(),
			jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
			jsonschema.WithTypeSchemaFor[plainKind](nil),
		)
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "integer"
		}`, string(got))
	})

	t.Run("leaves predicate resolvers in place", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[stringerKind](t.Context(),
			jsonschema.WithTypeSchemaResolver(stringerResolver()),
			jsonschema.WithTypeSchemaFor[stringerKind](nil),
		)
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "string"
		}`, string(got))
	})
}

// TestWithTypeSchemaResolver_EmbeddedComposition mirrors the WithTypeSchema embed
// behavior: an embedded struct intercepted by a resolver composes via allOf
// rather than having its fields promoted.
func TestWithTypeSchemaResolver_EmbeddedComposition(t *testing.T) {
	t.Parallel()

	type base struct {
		Name string `json:"name"`
	}

	type doc struct {
		base //nolint:unused // Exercised via reflection.

		Extra int `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaResolver(jsonschema.TypeSchemaResolverFunc(
			func(t reflect.Type) (*jsonschema.Schema, bool) {
				if t != reflect.TypeFor[base]() {
					return nil, false
				}

				return &jsonschema.Schema{Type: "object"}, true
			},
		)),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"$defs": {"base": {"type": "object"}},
		"allOf": [{"$ref": "#/$defs/base"}],
		"properties": {
			"extra": {"type": "integer"}
		},
		"required": ["extra"],
		"unevaluatedProperties": false
	}`, string(got))
}

// TestWithTypeSchemaResolver_SchemaUnaliased proves a resolver-supplied schema is
// copied before use: mutating the generated output cannot reach back into the
// schema value the resolver returns across calls.
func TestWithTypeSchemaResolver_SchemaUnaliased(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string", Enum: []any{"a"}}
	resolver := jsonschema.TypeSchemaResolverFunc(func(t reflect.Type) (*jsonschema.Schema, bool) {
		return shared, t == reflect.TypeFor[plainKind]()
	})

	s, err := jsonschema.GenerateFor[plainKind](t.Context(), jsonschema.WithTypeSchemaResolver(resolver))
	require.NoError(t, err)

	s.Enum = append(s.Enum, "b")

	assert.Equal(t, []any{"a"}, shared.Enum)
}
