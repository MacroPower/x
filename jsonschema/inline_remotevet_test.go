package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestInlineRefFallbackOnVetFailedRemote pins the [jsonschema.WithRefFallback]
// interaction with a fetched remote that fails structural vetting: the failure
// is an ordinary reference-expansion failure, so the fallback is consulted at
// the referencing node with an error wrapping [jsonschema.ErrRefResolve] and
// the structural sentinel, and its answer decides between propagating that
// error, dropping the reference keyword while keeping the node's siblings, and
// expanding a substitute schema with the usual draft sibling semantics.
func TestInlineRefFallbackOnVetFailedRemote(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	tests := map[string]struct {
		action jsonschema.RefAction
		want   string
		err    error
	}{
		"propagate keeps the vet failure fatal": {
			action: jsonschema.PropagateRef(),
			err:    jsonschema.ErrNegativeBound,
		},
		"drop clears the ref and keeps siblings": {
			action: jsonschema.DropRef(),
			want:   `{"title": "root"}`,
		},
		"substitute joins the node's allOf": {
			action: jsonschema.SubstituteRef(&jsonschema.Schema{Type: "string"}),
			want:   `{"title": "root", "allOf": [{"type": "string"}]}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(`{"title": "root", "$ref": "https://example.test/doc.json"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(`{"maxItems": -1}`))
			require.NoError(t, err)

			var failures []jsonschema.RefFailure

			fallback := jsonschema.RefFallbackFunc(
				func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
					failures = append(failures, f)

					return tc.action
				})

			out, err := jsonschema.Inline(t.Context(), root,
				jsonschema.WithRefResolver(jsonschema.SchemaMap{uri: doc}),
				jsonschema.WithRefFallback(fallback))

			require.Len(t, failures, 1, "the failing ref is consulted exactly once")
			assert.Equal(t, uri, failures[0].Ref)
			require.ErrorIs(t, failures[0].Err, jsonschema.ErrRefResolve,
				"the consultation carries the resolve failure")
			require.ErrorIs(t, failures[0].Err, jsonschema.ErrNegativeBound,
				"the consultation keeps the structural sentinel reachable")

			if tc.err != nil {
				require.ErrorIs(t, err, jsonschema.ErrRefResolve)
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}
