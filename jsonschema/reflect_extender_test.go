package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/beta"
)

// replacingExtender swaps the whole reflected schema for a fresh one via
// TypeSchema.Value. The replacement carries no Properties map, so render must
// not write the node-backed fields back into it (a nil-map write panics and a
// non-nil map would resurrect the dropped fields).
type replacingExtender struct{ A int }

func (replacingExtender) JSONSchemaExtend(
	_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema,
) error {
	ts.Value = &jsonschema.Schema{Type: "object", Description: "opaque"}

	return nil
}

// editingExtender removes one reflected property and replaces another with its
// own schema, exercising the documented "add, remove, or modify any fields"
// contract: render keeps both edits instead of restoring the node-backed
// renders over them.
type editingExtender struct {
	A int    `json:"a"`
	B string `json:"b"`
}

func (editingExtender) JSONSchemaExtend(
	_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema,
) error {
	delete(ts.Value.Properties, "a")

	ts.Value.Properties["b"] = &jsonschema.Schema{Type: "integer"}
	ts.Value.Required = []string{"b"}

	return nil
}

func TestGenerateFor_ExtenderPropertyEditsSurviveRender(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func(ctx context.Context) (*jsonschema.Schema, error)
		want     string
	}{
		"value replaced wholesale": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[replacingExtender](ctx)
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"description":"opaque"
			}`,
		},
		"property deleted and property replaced": {
			generate: func(ctx context.Context) (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[editingExtender](ctx)
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"b":{"type":"integer"}},
				"required":["b"],
				"additionalProperties":false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate(t.Context())
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)

			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// extenderIface is an interface whose method set includes JSONSchemaExtender.
// An interface value cannot be instantiated to call JSONSchemaExtend, so the
// declaration must be skipped and the reflected unrestricted schema kept,
// exactly as an interface declaring JSONSchemaProvider is skipped -- not
// aborted as a provider panic from reflect's nil-interface Method call.
type extenderIface interface {
	jsonschema.JSONSchemaExtender

	Named() string
}

type hasExtenderIfaceField struct {
	F extenderIface `json:"f"`
}

func TestGenerateFor_InterfaceWithExtenderSkipped(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"root": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[extenderIface](t.Context())
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"$ref":"#/$defs/extenderIface",
				"$defs":{"extenderIface":true}
			}`,
		},
		"struct field": {
			generate: func() (*jsonschema.Schema, error) {
				return jsonschema.GenerateFor[hasExtenderIfaceField](t.Context())
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"f":{"$ref":"#/$defs/extenderIface"}},
				"$defs":{"extenderIface":true},
				"required":["f"],
				"additionalProperties":false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// TestGenerateFor_ExtenderCopiedRefResolvesUnderCollision pins that a $ref an
// extender copies out of its view into a literal of its own resolves in the
// output. The extender runs before $defs names are final, so the view names
// each definition by a provisional token; under a base-name collision the
// final key differs from it. Generation rewrites the copied token to the
// final key and emits the definition it names, so the schema compiles and
// accepts the marshaled value.
func TestGenerateFor_ExtenderCopiedRefResolvesUnderCollision(t *testing.T) {
	t.Parallel()

	type root struct {
		A alpha.Widget `json:"a"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[root](t.Context(),
		jsonschema.WithTypeSchemaExtenderFor[root](
			func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
				b := ts.Value.Properties["b"]
				ts.Value.Properties["b"] = &jsonschema.Schema{
					AnyOf: []*jsonschema.Schema{{Ref: b.Ref}, {Type: "null"}},
				}

				return nil
			},
		))
	require.NoError(t, err)

	require.Contains(t, s.Defs, "alpha_Widget")
	require.Contains(t, s.Defs, "beta_Widget")
	assert.Equal(t, "#/$defs/alpha_Widget", s.Properties["a"].Ref)

	b := s.Properties["b"]
	require.Len(t, b.AnyOf, 2)
	assert.Equal(t, "#/$defs/beta_Widget", b.AnyOf[0].Ref,
		"the copied provisional reference must be rewritten to the final key")

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err, "every $ref the output carries must resolve")

	instance, err := json.Marshal(root{B: beta.Widget{Color: "red"}})
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(), instance))
	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"a":{"label":"x","size":1},"b":null}`)))
}
