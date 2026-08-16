package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineFallbackTargetStructuralChecks locks in that a $ref target
// materialized from an unknown-keyword (Extra) position, reached only through
// the resolution core's JSON-pointer fallback, is vetted with the same
// structural checks a fetched document gets, so an ill-formed target fails
// Inline with an error wrapping [jsonschema.ErrRefResolve] instead of being
// spliced into a malformed output schema [jsonschema.Compile] rejects. Both
// the remote-document and same-document spellings route through the fallback.
func TestInlineFallbackTargetStructuralChecks(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	tests := map[string]struct {
		root string
		doc  string
		want string
		err  error
	}{
		"invalid type name in a remote unknown-keyword target": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"type": "nteger"}}`,
			err:  jsonschema.ErrInvalidType,
		},
		"negative bound in a remote unknown-keyword target": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"maxItems": -1}}`,
			err:  jsonschema.ErrNegativeBound,
		},
		"invalid type name in a same-document unknown-keyword target": {
			root: `{"x-shared": {"type": "nteger"}, "properties": {"p": {"$ref": "#/x-shared"}}}`,
			err:  jsonschema.ErrInvalidType,
		},
		"well-formed remote unknown-keyword target inlines": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"type": "integer"}}`,
			want: `{"properties": {"p": {"type": "integer"}}}`,
		},
		"well-formed same-document unknown-keyword target inlines": {
			root: `{"x-shared": {"type": "integer"}, "properties": {"p": {"$ref": "#/x-shared"}}}`,
			want: `{"x-shared": {"type": "integer"}, "properties": {"p": {"type": "integer"}}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(tc.root))
			require.NoError(t, err)

			opts := []jsonschema.InlineOption{}

			if tc.doc != "" {
				doc, docErr := jsonschema.ParseSchema([]byte(tc.doc))
				require.NoError(t, docErr)

				opts = append(opts, jsonschema.WithRefResolver(jsonschema.SchemaMap{uri: doc}))
			}

			out, err := jsonschema.Inline(t.Context(), root, opts...)
			if tc.err != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, jsonschema.ErrRefResolve,
					"a structurally invalid fallback target must fail Inline loudly")
				require.ErrorIs(t, err, tc.err,
					"the structural sentinel must stay reachable through the inline error")

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}
