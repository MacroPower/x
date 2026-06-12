package jsonschema_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// stringerKind is a named type implementing fmt.Stringer for provider
// predicate tests.
type stringerKind int

func (stringerKind) String() string { return "kind" }

// plainKind is a named type that implements nothing, so it falls through
// every provider predicate to kind-based reflection.
type plainKind int

// stringerProvider resolves every fmt.Stringer to a plain string schema.
func stringerProvider() jsonschema.TypeSchemaProvider {
	return jsonschema.TypeSchemaProviderFunc(
		func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
			if !tc.Type.Implements(reflect.TypeFor[fmt.Stringer]()) {
				return nil, jsonschema.ErrTypeNotHandled
			}

			return &jsonschema.Schema{Type: "string"}, nil
		},
	)
}

func TestWithTypeSchemaProvider(t *testing.T) {
	t.Parallel()

	type doc struct {
		Kind  stringerKind `json:"kind"`
		Plain plainKind    `json:"plain"`
	}

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"predicate provider overrides matching types only": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaProvider(stringerProvider())},
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
		"later WithTypeSchema wins over earlier provider": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaProvider(stringerProvider()),
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
		"later provider wins over earlier WithTypeSchema": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[stringerKind](&jsonschema.Schema{Type: "string", Format: "uri"}),
				jsonschema.WithTypeSchemaProvider(stringerProvider()),
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
		"nil schema with nil error is unrestricted": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
					func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
						if tc.Type != reflect.TypeFor[stringerKind]() {
							return nil, jsonschema.ErrTypeNotHandled
						}

						return nil, nil //nolint:nilnil // The unrestricted answer.
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
		"nil provider is ignored": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaProvider(nil)},
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
// while predicate providers still apply.
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

	t.Run("leaves predicate providers in place", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[stringerKind](t.Context(),
			jsonschema.WithTypeSchemaProvider(stringerProvider()),
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

// TestWithTypeSchemaProvider_ReceivesDraft proves the TypeContext carries the
// generation run's target draft, so a provider can emit draft-appropriate
// keywords.
func TestWithTypeSchemaProvider_ReceivesDraft(t *testing.T) {
	t.Parallel()

	for name, draft := range map[string]jsonschema.Draft{
		"draft7":    jsonschema.Draft7,
		"draft2020": jsonschema.Draft2020,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got []jsonschema.Draft

			_, err := jsonschema.GenerateFor[plainKind](t.Context(),
				jsonschema.WithDraft(draft),
				jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
					func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
						got = append(got, tc.Draft)
						return nil, jsonschema.ErrTypeNotHandled
					},
				)),
			)
			require.NoError(t, err)

			require.NotEmpty(t, got)

			for _, d := range got {
				assert.Equal(t, draft, d)
			}
		})
	}
}

// TestWithTypeSchemaProvider_EmbeddedComposition mirrors the WithTypeSchema embed
// behavior: an embedded struct intercepted by a provider composes via allOf
// rather than having its fields promoted.
func TestWithTypeSchemaProvider_EmbeddedComposition(t *testing.T) {
	t.Parallel()

	type base struct {
		Name string `json:"name"`
	}

	type doc struct {
		base //nolint:unused // Exercised via reflection.

		Extra int `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
			func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
				if tc.Type != reflect.TypeFor[base]() {
					return nil, jsonschema.ErrTypeNotHandled
				}

				return &jsonschema.Schema{Type: "object"}, nil
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

// TestWithTypeSchemaProvider_Error proves a provider error aborts generation
// and reaches the caller wrapped, whether the provider is consulted for the
// root type or for a type reached through a field or an embed.
func TestWithTypeSchemaProvider_Error(t *testing.T) {
	t.Parallel()

	errLoad := errors.New("schema document unavailable")

	failFor := func(target reflect.Type) jsonschema.GenerateOption {
		return jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
			func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
				if tc.Type == target {
					return nil, errLoad
				}

				return nil, jsonschema.ErrTypeNotHandled
			},
		))
	}

	type inner struct {
		Kind stringerKind `json:"kind"`
	}

	type withField struct {
		Kind stringerKind `json:"kind"`
	}

	type withEmbed struct {
		inner //nolint:unused // Exercised via reflection.

		Extra int `json:"extra"`
	}

	tests := map[string]struct {
		generate func(opt jsonschema.GenerateOption) error
		target   reflect.Type
	}{
		"root type": {
			target: reflect.TypeFor[stringerKind](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[stringerKind](t.Context(), opt)
				return err
			},
		},
		"field type": {
			target: reflect.TypeFor[stringerKind](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[withField](t.Context(), opt)
				return err
			},
		},
		"embedded type": {
			target: reflect.TypeFor[inner](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[withEmbed](t.Context(), opt)
				return err
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.generate(failFor(tc.target))
			require.ErrorIs(t, err, errLoad)
		})
	}
}

// TestWithTypeSchemaProvider_SchemaUnaliased proves a provider-supplied schema is
// copied before use: mutating the generated output cannot reach back into the
// schema value the resolver returns across calls.
func TestWithTypeSchemaProvider_SchemaUnaliased(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string", Enum: []any{"a"}}
	provider := jsonschema.TypeSchemaProviderFunc(
		func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
			if tc.Type != reflect.TypeFor[plainKind]() {
				return nil, jsonschema.ErrTypeNotHandled
			}

			return shared, nil
		},
	)

	s, err := jsonschema.GenerateFor[plainKind](t.Context(), jsonschema.WithTypeSchemaProvider(provider))
	require.NoError(t, err)

	s.Enum = append(s.Enum, "b")

	assert.Equal(t, []any{"a"}, shared.Enum)
}
