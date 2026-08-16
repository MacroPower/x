package jsonptr_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// TestSchemaAtJSONPointerDataIDsInert asserts that $id tracking rebases only
// at objects occupying schema positions. A "$id" string inside a non-schema
// keyword's payload (examples) or an unknown keyword is plain instance data,
// not a resource boundary, so a target materialized through it keeps the base
// of its nearest enclosing schema resource; a $id in a genuine schema
// position on the same kind of walk still rebases.
func TestSchemaAtJSONPointerDataIDsInert(t *testing.T) {
	t.Parallel()

	const rootBase = "https://example.com/root"

	tests := map[string]struct {
		root     *jsonschema.Schema
		segments []string
		want     string
	}{
		"$id inside examples data is inert": {
			root: &jsonschema.Schema{
				Examples: []any{
					map[string]any{
						"$id":     "https://models.example/widget",
						"payload": map[string]any{"type": "string"},
					},
				},
			},
			segments: []string{"examples", "0", "payload"},
			want:     rootBase,
		},
		"$id inside an unknown keyword is inert": {
			root: &jsonschema.Schema{
				Extra: map[string]any{
					"x-wrap": map[string]any{
						"$id":   "https://models.example/widget",
						"child": map[string]any{"type": "string"},
					},
				},
			},
			segments: []string{"x-wrap", "child"},
			want:     rootBase,
		},
		"schema-shaped object below data does not re-enter schema position": {
			root: &jsonschema.Schema{
				Examples: []any{
					map[string]any{
						"properties": map[string]any{
							"a": map[string]any{
								"$id":  "https://models.example/widget",
								"type": "string",
							},
						},
					},
				},
			},
			segments: []string{"examples", "0", "properties", "a"},
			want:     rootBase,
		},
		"$id on a draft-07 items array element rebases": {
			root: &jsonschema.Schema{
				ItemsArray: []*jsonschema.Schema{
					{
						ID: "sub/",
						Properties: map[string]*jsonschema.Schema{
							"b": {Type: "string"},
						},
					},
				},
			},
			segments: []string{"items", "0", "properties", "b"},
			want:     "https://example.com/sub/",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, base := jsonptr.SchemaAtJSONPointer(
				tc.root, tc.segments, rootBase, true, materializeSchema,
			)
			require.NotNil(t, got)
			assert.Equal(t, tc.want, base)
		})
	}
}
