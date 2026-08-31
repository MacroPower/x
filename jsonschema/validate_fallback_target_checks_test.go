package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileChecksJSONPointerFallbackTargets locks in that the compile-time
// structural checks (type names, non-negative bounds, and the Draft-07 items
// array under Draft 2020-12) extend to $ref targets materialized through the
// JSON-pointer fallback: schemas carried inside unknown keywords, which the
// typed root pass never reaches. Without the extension such a target compiles
// cleanly and then silently mis-validates.
func TestCompileChecksJSONPointerFallbackTargets(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		err    error
	}{
		"items array in fallback target": {
			schema: `{"$ref": "#/x", "x": {"type": "array", "items": [{"type": "string"}]}}`,
			err:    jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"negative bound in fallback target": {
			schema: `{"$ref": "#/x", "x": {"minLength": -5}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"invalid type name in fallback target": {
			schema: `{"$ref": "#/x", "x": {"type": "strng"}}`,
			err:    jsonschema.ErrInvalidType,
		},
		"violation one ref deeper than the first target": {
			// The compile-time reference walk resolves #/x directly and #/y
			// only through x's own $ref, on the pass that ref-walks the
			// targets tolerantly. A refused target is settled, so that pass
			// fails on it too. The checks therefore cover every materialized
			// target, not just the first level.
			schema: `{"$ref": "#/x", "x": {"$ref": "#/y"}, "y": {"maxItems": -1}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"violation nested inside a fallback target": {
			schema: `{"$ref": "#/x", "x": {"properties": {"a": {"minLength": -3}}}}`,
			err:    jsonschema.ErrNegativeBound,
		},
		"well-formed fallback target compiles": {
			schema: `{"$ref": "#/x", "x": {"type": "string"}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := jsonschema.ParseSchema([]byte(tc.schema))
			require.NoError(t, err)

			_, err = jsonschema.Compile(t.Context(), schema)
			if tc.err == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.err,
				"a fallback-materialized target must fail the same compile checks as the typed tree")
		})
	}
}

// TestCompileFallbackTargetErrorNamesLocation pins the error path. The error
// names the JSON Pointer that materialized the target, so the offending
// keyword is addressable. It also names the reference that reached the target,
// since the closure walk reports the vet's refusal where the reference
// resolves.
func TestCompileFallbackTargetErrorNamesLocation(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{"$ref": "#/x", "x": {"minLength": -5}}`))
	require.NoError(t, err)

	_, err = jsonschema.Compile(t.Context(), schema)
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	assert.Contains(t, err.Error(), "#/x/minLength",
		"the message names the pointer that materialized the target")
	assert.Contains(t, err.Error(), `cannot resolve $ref "#/x"`,
		"the message names the reference the walk refused the target at")
}
