package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRefIntoFallbackTree pins that a $ref reaching a node below the
// root of a JSON-pointer fallback target inlines whichever ref the walk
// expands first. The pointer ref materializes the whole x-defs/sub tree and
// registers the $id its inner node declares, so a ref naming that $id
// resolves to a node the walk has not recorded. The two rows swap the
// property names, which swaps the expansion order, and both inline to the
// same shapes.
func TestInlineRefIntoFallbackTree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		want   string
	}{
		"the $id ref expands before the tree root": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"$ref": "urn:t"},
						"b": {"$ref": "#/x-defs/sub"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"type": "string"},
						"b": {
							"type": "object",
							"properties": {"p": {"type": "string"}}
						}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
		},
		"the tree root expands before the $id ref": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"$ref": "#/x-defs/sub"},
						"b": {"$ref": "urn:t"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {
							"type": "object",
							"properties": {"p": {"type": "string"}}
						},
						"b": {"type": "string"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			got, err := jsonschema.Inline(t.Context(), &schema)
			require.NoError(t, err)

			data, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}
