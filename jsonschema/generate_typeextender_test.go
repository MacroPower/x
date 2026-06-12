package jsonschema_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// extendedKind is a named type whose author sets a description via
// JSONSchemaExtend, for ordering tests against registered extenders.
type extendedKind int

func (extendedKind) JSONSchemaExtend(s *jsonschema.Schema) { s.Description = "by author" }

// describePlainKind extends plainKind with a description and leaves every
// other type untouched.
func describePlainKind() jsonschema.TypeSchemaExtender {
	return jsonschema.TypeSchemaExtenderFunc(func(t reflect.Type, s *jsonschema.Schema) error {
		if t == reflect.TypeFor[plainKind]() {
			s.Description = "extended"
		}

		return nil
	})
}

func TestWithTypeSchemaExtender(t *testing.T) {
	t.Parallel()

	type doc struct {
		Plain plainKind `json:"plain"`
	}

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"extender adjusts matching reflected types only": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaExtender(describePlainKind())},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer", "description": "extended"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"extenders apply in registration order": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaExtender(describePlainKind()),
				jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
					func(t reflect.Type, s *jsonschema.Schema) error {
						if t == reflect.TypeFor[plainKind]() {
							s.Description += ", then refined"
						}

						return nil
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer", "description": "extended, then refined"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"not called for resolver-supplied schemas": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
				jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
					func(t reflect.Type, _ *jsonschema.Schema) error {
						if t == reflect.TypeFor[plainKind]() {
							return errors.New("extender reached a replaced type")
						}

						return nil
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "string"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"nil extender is ignored": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaExtender(nil)},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer"}
				},
				"required": ["plain"],
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

// TestWithTypeSchemaExtender_AfterJSONSchemaExtend proves the ordering
// contract: a registered extender sees the schema after the type's own
// JSONSchemaExtend has run, so it can adjust what the author produced.
func TestWithTypeSchemaExtender_AfterJSONSchemaExtend(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[extendedKind](t.Context(),
		jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
			func(t reflect.Type, s *jsonschema.Schema) error {
				if t == reflect.TypeFor[extendedKind]() {
					s.Description += ", then extended"
				}

				return nil
			},
		)),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "integer",
		"description": "by author, then extended"
	}`, string(got))
}

// TestWithTypeSchemaExtender_Error proves an extender error aborts generation
// and surfaces with the failing type named.
func TestWithTypeSchemaExtender_Error(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	_, err := jsonschema.GenerateFor[plainKind](t.Context(),
		jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
			func(reflect.Type, *jsonschema.Schema) error { return errBoom },
		)),
	)
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "extend type")
	assert.Contains(t, err.Error(), "plainKind")
}
