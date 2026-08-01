package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateLateFallbackTargetStructuralChecks locks in that a JSON-pointer
// fallback target materialized during a validation run is vetted with the same
// structural checks Compile applies to the fallback targets its gate
// materializes. The target here hides inside an unknown keyword of a document
// first fetched at validation time, so no compile-time pass ever sees it:
// without the per-run vet a negative bound silently never fires and the
// instance validates.
func TestValidateLateFallbackTargetStructuralChecks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		doc      string
		instance string
		err      error
		valid    bool
	}{
		"negative bound in fallback target": {
			doc:      `{"x-custom": {"sub": {"minItems": -1}}, "$ref": "#/x-custom/sub"}`,
			instance: `[]`,
			err:      jsonschema.ErrNegativeBound,
		},
		"invalid type name in fallback target": {
			doc:      `{"x-custom": {"sub": {"type": "strng"}}, "$ref": "#/x-custom/sub"}`,
			instance: `"hello"`,
			err:      jsonschema.ErrInvalidType,
		},
		"items array in fallback target under 2020-12": {
			doc:      `{"x-custom": {"sub": {"items": [{"type": "string"}]}}, "$ref": "#/x-custom/sub"}`,
			instance: `[123]`,
			err:      jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"well-formed fallback target validates": {
			doc:      `{"x-custom": {"sub": {"type": "array"}}, "$ref": "#/x-custom/sub"}`,
			instance: `[]`,
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

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"an ill-formed fallback target must fail the ref loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the validation error")
		})
	}
}

// TestValidateLateFallbackTargetErrorNamesLocation pins the error path: a
// violation in a fallback target materialized at validation time names the
// document and JSON Pointer that produced it, matching Compile's fallback vet.
func TestValidateLateFallbackTargetErrorNamesLocation(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "https://example.test/late.json"}`))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"x-custom": {"sub": {"minItems": -1}}, "$ref": "#/x-custom/sub"}`,
	))
	require.NoError(t, err)

	resolver := &lateResolver{doc: doc}

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	resolver.armed.Store(true)

	err = v.ValidateJSON(t.Context(), []byte(`[]`))
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	assert.Contains(t, err.Error(), "https://example.test/late.json#/x-custom/sub/minItems")
}
