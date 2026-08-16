package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
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
