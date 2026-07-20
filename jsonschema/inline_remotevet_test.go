package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineFetchedRemoteStructuralChecks locks in that a remote document
// fetched during Inline is vetted with the same [jsonschema.Compile]-style
// structural checks the validator applies to fetched documents, so a remote
// carrying an invalid type name, a negative bound, or the array form of items
// under a draft that rejects it fails Inline with an error wrapping
// [jsonschema.ErrRefResolve] instead of being inlined into a malformed output
// schema. A Draft-7 array-form items remote stays valid under a Draft-7 run,
// since fetched documents follow the root document's draft.
func TestInlineFetchedRemoteStructuralChecks(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	tests := map[string]struct {
		root  string
		doc   string
		err   error
		valid bool
	}{
		"negative bound in fetched document": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"maxItems": -1}`,
			err:  jsonschema.ErrNegativeBound,
		},
		"items array in fetched document under 2020-12": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"items": [{"type": "string"}]}`,
			err:  jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"invalid type name in fetched document": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"type": "strng"}`,
			err:  jsonschema.ErrInvalidType,
		},
		"items array in fetched document under draft-07 keeps tuple semantics": {
			root:  `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "https://example.test/doc.json"}`,
			doc:   `{"items": [{"type": "string"}]}`,
			valid: true,
		},
		"well-formed fetched document inlines": {
			root:  `{"$ref": "https://example.test/doc.json"}`,
			doc:   `{"type": "string"}`,
			valid: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(tc.root))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := jsonschema.SchemaMap{uri: doc}

			out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(resolver))
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, out)

				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"a structurally invalid fetched document must fail Inline loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the inline error")
		})
	}
}
