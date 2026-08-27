package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateUnresolvableFragmentRefInLateFetchedDocument locks in that an
// unresolvable fragment-only ref inside a document first fetched at validation
// time is reported like its absolute spelling, not silently skipped. The
// silent skip exists because the compile-time reference walk pre-rejects
// broken fragment refs, but a late-fetched document never passes through that
// check: without the guard the same document that fails Compile when served at
// compile time silently accepts every instance when served only at validation
// time.
func TestValidateUnresolvableFragmentRefInLateFetchedDocument(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc      string
		instance string
		valid    bool
	}{
		"broken anchor ref is reported": {
			doc:      `{"$id": "https://example.test/late.json", "properties": {"x": {"$ref": "#nope"}}}`,
			instance: `{"x": 1}`,
		},
		"broken pointer ref is reported": {
			doc:      `{"$id": "https://example.test/late.json", "properties": {"x": {"$ref": "#/missing/here"}}}`,
			instance: `{"x": 1}`,
		},
		"resolvable fragment ref keeps validating": {
			doc: `{"$id": "https://example.test/late.json",` +
				` "$defs": {"n": {"type": "integer"}}, "properties": {"x": {"$ref": "#/$defs/n"}}}`,
			instance: `{"x": 1}`,
			valid:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated")

			resolver.armed.Store(true)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err,
				"an unresolvable fragment ref in a late-fetched document must not silently pass")
			require.ErrorContains(t, err, "cannot resolve $ref",
				"the fragment spelling must report like the absolute spelling")
		})
	}
}

// TestCompileToleratesFragmentRefToLateDocument locks in the Remote References
// contract for a remote $ref carrying a fragment: a document unresolvable at
// compile time is tolerated exactly like the fragment-less spelling, the
// validation walk reports the miss as a "cannot resolve $ref" error, and the
// ref validates normally once the resolver serves the document. Without the
// compile-time reference walk's document-miss tolerance the validator could
// never be built.
func TestCompileToleratesFragmentRefToLateDocument(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ref string
		doc string
	}{
		"pointer fragment": {
			ref: "https://example.test/late.json#/$defs/x",
			doc: `{"$id": "https://example.test/late.json", "$defs": {"x": {"type": "integer"}}}`,
		},
		"anchor fragment": {
			ref: "https://example.test/late.json#a",
			doc: `{"$id": "https://example.test/late.json",` +
				` "$defs": {"x": {"$anchor": "a", "type": "integer"}}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(`{"properties": {"p": {"$ref": "` + tc.ref + `"}}}`))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema)
			require.NoError(t, err, "a fragment ref to an unresolvable document compiles without a resolver")

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := &lateResolver{doc: doc}

			v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err, "a resolver miss at compile time is tolerated for a fragment ref")

			err = v.ValidateJSON(t.Context(), []byte(`{"p": 1}`))
			require.ErrorContains(t, err, "cannot resolve $ref",
				"an unserved document reports through the validation walk")

			resolver.armed.Store(true)

			require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"p": 1}`)),
				"the fragment resolves once the resolver serves the document")
			require.ErrorContains(t, v.ValidateJSON(t.Context(), []byte(`{"p": "s"}`)),
				`expected "integer"`, "the served document's constraint applies")
		})
	}
}

// TestValidateUnresolvableFragmentRefInFallbackTarget covers the fallback
// variant: a broken fragment ref two levels behind JSON-pointer fallback
// targets. The compile-time reference walk resolves fallback-borne refs
// tolerantly, so Compile tolerates the miss and the validation walk must
// report it instead of silently accepting the instance.
func TestValidateUnresolvableFragmentRefInFallbackTarget(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(
		`{"x-custom": {"sub": {"$ref": "#/x-custom/other"},` +
			` "other": {"properties": {"p": {"$ref": "#/nope/missing"}}}}, "$ref": "#/x-custom/sub"}`,
	))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), schema)
	require.NoError(t, err, "the own-reference gate is one level deep by design")

	err = v.ValidateJSON(t.Context(), []byte(`{"p": 1}`))
	require.Error(t, err,
		"an unresolvable fragment ref in a fallback target must not silently pass")
	require.ErrorContains(t, err, `cannot resolve $ref "#/nope/missing"`)
}
