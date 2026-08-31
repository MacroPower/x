package jsonschema_test

import (
	"context"
	"encoding/json/v2"
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
// schema. A Draft-07 array-form items remote stays valid under a Draft-07 run,
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
				},
			)

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

// TestInlineClosureRejectsBeforeSplicing pins that a violation only the
// closure walk reaches yields no document at all, rather than one spliced
// around the offending branch. It runs the same three-document shape as
// TestRefEnginesAgreeOnTransitiveVetting, which compares the two engines'
// causes; this one asserts what an Inline caller receives, and it
// carries a different violation so the two do not pin one sentinel twice.
func TestInlineClosureRejectsBeforeSplicing(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$schema":"http://json-schema.org/draft-07/schema#",` +
			`"$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`,
	))
	require.NoError(t, err)

	documents := map[string]*jsonschema.Schema{}

	for uri, body := range map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"maxItems": -1}}}`,
	} {
		doc, parseErr := jsonschema.ParseSchema([]byte(body))
		require.NoError(t, parseErr)

		documents[uri] = doc
	}

	out, err := jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(jsonschema.SchemaMap(documents)))

	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	require.ErrorContains(t, err, "a.json", "the message names the document the walk refused")
	assert.Nil(t, out, "a refused closure yields no document")
}

// TestInlineFetchedDocCollisionPrecedesVet pins which fault the engines report
// for a remote carrying two, one claiming the root's URI and misspelling a type
// name. Both report the collision, because prefetchDoc registers at the fetch
// and the strict closure walk vets afterwards. A fetch that vetted first would
// name the other cause, and the two engines would refuse one graph for two
// reasons. TestInlineFallbackCollisionPrecedesVet covers the same graph over
// the path a configured fallback takes.
func TestInlineFetchedDocCollisionPrecedesVet(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "properties": {"p": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	resolver := mapResolver{"https://ex.test/a.json": doc}

	_, err = jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the collision is reported ahead of the structural vet")
	require.NotErrorIs(t, err, jsonschema.ErrInvalidType,
		"the vet does not run on a document the registration refused")

	_, err = jsonschema.Compile(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision, "Compile names the same cause")
}

// TestInlineFallbackDoesNotSuspendCollision pins the one closure-walk refusal a
// configured fallback leaves standing, the identifier collision doc.go's
// WithRefFallback section carves out. The colliding document sits in a $defs
// branch no expansion visits, so only the walk reaches it.
func TestInlineFallbackDoesNotSuspendCollision(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "$defs": {"unused": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "integer"}`,
	))
	require.NoError(t, err)

	consulted := 0
	fallback := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		consulted++

		return jsonschema.DropRef()
	})

	_, err = jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(mapResolver{"https://ex.test/a.json": doc}),
		jsonschema.WithRefFallback(fallback))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"a configured fallback does not suspend the collision refusal")
	assert.Zero(t, consulted, "the walk refuses before any reference reaches the fallback")
}

// TestInlineFallbackCollisionPrecedesVet guards the collision check
// inliner.fetchDoc runs before its vet. A configured fallback stands the strict
// prefetchDoc path down, so fetchDoc is where the document is first seen, and a
// vet running first would hand the fallback a structural failure to drop. The
// run would then succeed with a substitute in place of a graph Compile
// refuses.
func TestInlineFallbackCollisionPrecedesVet(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "properties": {"p": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	// The remote carries both faults. It claims the root's URI and misspells a
	// type name.
	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	fallback := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.DropRef()
	})

	_, err = jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(mapResolver{"https://ex.test/a.json": doc}),
		jsonschema.WithRefFallback(fallback))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the collision is settled before the vet, so the fallback never sees a structural failure to drop")
}
