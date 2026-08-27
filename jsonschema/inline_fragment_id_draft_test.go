package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineFragmentOnlyIDFollowsDraft pins that the fragment-only $id anchor
// form is a Draft-07 mechanism in the shared ref-resolution registry: Draft
// 2020-12 forbids a fragment in $id (core section 8.2.1), so under it the form
// registers no resolution target and a $ref naming it reports ErrRefResolve
// instead of splicing the target. The validator path rejects such a document
// earlier, at Compile's identifier check; the inliner never runs that pass, so
// the registry walk's own draft gate is what this test exercises.
func TestInlineFragmentOnlyIDFollowsDraft(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		want   string
		err    error
	}{
		"draft 2020-12 fragment-only $id names nothing": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {"x": {"$ref": "#a"}},
					"$defs": {"t": {"$id": "#a", "type": "integer"}}
				}
			`),
			err: jsonschema.ErrRefResolve,
		},
		"draft-07 fragment-only $id still acts as an anchor": {
			schema: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"x": {"$ref": "#a"}},
					"definitions": {"t": {"$id": "#a", "type": "integer"}}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"properties": {"x": {"type": "integer"}},
					"definitions": {"t": {"$id": "#a", "type": "integer"}}
				}
			`),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			got, err := jsonschema.Inline(t.Context(), schema)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			gotJSON, err := json.Marshal(got)
			require.NoError(t, err)

			require.JSONEq(t, tc.want, string(gotJSON))
		})
	}
}
