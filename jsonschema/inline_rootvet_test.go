package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineVetsRootLikeCompile pins that the two engines make the same
// structural demand of a root document. Every sentinel [jsonschema.Compile]
// reports for a malformed root, [jsonschema.Inline] reports for the same
// document. The rows cover each vetting sentinel a root can carry.
func TestInlineVetsRootLikeCompile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root *jsonschema.Schema
		err  error
	}{
		"invalid type name": {
			root: &jsonschema.Schema{Type: "strng"},
			err:  jsonschema.ErrInvalidType,
		},
		"array form of items under 2020-12": {
			root: &jsonschema.Schema{ItemsArray: []*jsonschema.Schema{{Type: "string"}}},
			err:  jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"negative bound": {
			root: &jsonschema.Schema{Type: "string", MinLength: new(-1)},
			err:  jsonschema.ErrNegativeBound,
		},
		"non-positive multipleOf": {
			root: &jsonschema.Schema{Type: "number", MultipleOf: new(0.0)},
			err:  jsonschema.ErrNonPositiveMultipleOf,
		},
		"nil sub-schema element": {
			root: &jsonschema.Schema{AllOf: []*jsonschema.Schema{nil}},
			err:  jsonschema.ErrNilSubschema,
		},
		"both Go fields of one keyword": {
			root: &jsonschema.Schema{Type: "string", Types: []string{"string"}},
			err:  jsonschema.ErrConflictingSchemaFields,
		},
		"duplicate property order": {
			root: &jsonschema.Schema{
				Properties:    map[string]*jsonschema.Schema{"a": {Type: "string"}},
				PropertyOrder: []string{"a", "a"},
			},
			err: jsonschema.ErrDuplicatePropertyOrder,
		},
		"fragment in $id under 2020-12": {
			root: &jsonschema.Schema{
				Defs: map[string]*jsonschema.Schema{"t": {ID: "#a", Type: "integer"}},
			},
			err: jsonschema.ErrInvalidID,
		},
		"$vocabulary under a foreign dialect": {
			root: &jsonschema.Schema{
				Schema:     "http://json-schema.org/draft-07/schema#",
				Vocabulary: map[string]bool{"https://example.test/vocab": true},
			},
			err: jsonschema.ErrMisplacedVocabulary,
		},
		"well-formed root": {
			root: &jsonschema.Schema{Type: "string", MinLength: new(1)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, compileErr := jsonschema.Compile(t.Context(), tc.root)
			out, inlineErr := jsonschema.Inline(t.Context(), tc.root)

			if tc.err == nil {
				require.NoError(t, compileErr)
				require.NoError(t, inlineErr)
				require.NotNil(t, out)

				return
			}

			require.ErrorIs(t, compileErr, tc.err)
			require.ErrorIs(t, inlineErr, tc.err,
				"Inline vets its root under the policy Compile applies to the same document")
		})
	}
}

// TestInlineVetsSubstitute pins the same demand on a [jsonschema.SubstituteRef]
// schema. It enters resolution space as a document, so [jsonschema.Inline] vets
// it as one, and the violation names the reference whose failure the fallback
// answered. The sentinel stays reachable, so a caller matching on it still
// matches.
func TestInlineVetsSubstitute(t *testing.T) {
	t.Parallel()

	const ref = "https://example.test/absent.json"

	tests := map[string]struct {
		substitute *jsonschema.Schema
		err        error
		want       string
	}{
		"negative bound in the substitute": {
			substitute: &jsonschema.Schema{Type: "string", MinLength: new(-1)},
			err:        jsonschema.ErrNegativeBound,
		},
		"invalid type name in the substitute": {
			substitute: &jsonschema.Schema{Type: "strng"},
			err:        jsonschema.ErrInvalidType,
		},
		"well-formed substitute inlines": {
			substitute: &jsonschema.Schema{Type: "string"},
			want:       `{"title": "root", "allOf": [{"type": "string"}]}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := &jsonschema.Schema{Title: "root", Ref: ref}

			fallback := jsonschema.RefFallbackFunc(
				func(_ context.Context, _ jsonschema.RefFailure) jsonschema.RefAction {
					return jsonschema.SubstituteRef(tc.substitute)
				})

			out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefFallback(fallback))

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.ErrorContains(t, err, ref,
					"the violation names the reference the substitute answered")

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

// TestInlineRetrievalBaseKeepsIDsInert pins the one place the inliner's vet
// stops short of Compile's, and why. Under [jsonschema.WithRetrievalBase] an
// $id establishes no base URI and names no resolution target, so the keyword
// addresses nothing and its domain goes unchecked; a document carrying a
// published $id its refs do not use still inlines from disk, which is what the
// option is for.
func TestInlineRetrievalBaseKeepsIDsInert(t *testing.T) {
	t.Parallel()

	const leaf = "leaf.json"

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "#published", "properties": {"a": {"$ref": "leaf.json"}}}`))
	require.NoError(t, err)

	document, err := jsonschema.ParseSchema([]byte(`{"$id": "#remote", "type": "string"}`))
	require.NoError(t, err)

	_, err = jsonschema.Compile(t.Context(), root)
	require.ErrorIs(t, err, jsonschema.ErrInvalidID,
		"a live $id carrying a fragment is outside the keyword's domain under 2020-12")

	out, err := jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(jsonschema.SchemaMap{leaf: document}),
		jsonschema.WithRetrievalBase(true))
	require.NoError(t, err, "an inert $id has no domain to violate, in the root or a remote")

	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"$id": "#published", "properties": {"a": {"type": "string"}}}`,
		string(data),
		"the root's inert $id passes through, while the splice drops the remote's as always")
}

// TestCompileChecksTheBaseURIInlineDoesNot pins one of the two compile-time
// refusals with no Inline counterpart, the reason the engines agree on the
// structural vet rather than on every error. [jsonschema.Compile] parses the
// [jsonschema.WithBaseURI] value and rejects one that is not a URI reference;
// Inline takes the same option without that check, and an unparsable base
// normalizes to something its resolution walk carries. The other refusal,
// ErrUnknownVocabulary, has no test here because vocabulary resolution reads
// options Inline does not take.
func TestCompileChecksTheBaseURIInlineDoesNot(t *testing.T) {
	t.Parallel()

	const malformed = "http://[::1"

	root := &jsonschema.Schema{Type: "string"}

	_, err := jsonschema.Compile(t.Context(), root, jsonschema.WithBaseURI(malformed))
	require.ErrorIs(t, err, jsonschema.ErrInvalidBaseURI)

	out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithBaseURI(malformed))
	require.NoError(t, err, "Inline never parses the base it is given")
	assert.Equal(t, "string", out.Type)
}
