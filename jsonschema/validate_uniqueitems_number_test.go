package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A pre-parsed instance may mix float64 and json.Number representations of
// the same JSON number (Validate accepts both). The uniqueItems comparison
// must interpret a float64 through its shortest decimal, the way const, enum,
// and the numeric-bound keywords do, so float64(1.1) and the decoded literal
// 1.1 are one value under every keyword rather than duplicates under const
// but distinct under uniqueItems.
func TestValidateUniqueItemsMixedNumberRepresentations(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{UniqueItems: true}

	tests := map[string]struct {
		arr []any
		err string
	}{
		"float64 and json.Number non-integer duplicate": {
			arr: []any{1.1, json.Number("1.1")},
			err: "uniqueItems",
		},
		"float64 and json.Number fraction duplicate": {
			arr: []any{0.1, json.Number("0.1")},
			err: "uniqueItems",
		},
		"float64 and json.Number integer duplicate": {
			arr: []any{1.0, json.Number("1")},
			err: "uniqueItems",
		},
		"distinct mixed representations": {
			arr: []any{1.1, json.Number("1.2")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), schema, tc.arr)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

// The same value pair must be equal under const exactly when it is a
// duplicate under uniqueItems: both elements of the mixed-representation
// pair above individually satisfy const 1.1.
func TestValidateConstAgreesWithUniqueItems(t *testing.T) {
	t.Parallel()

	constSchema := jsonschema.MustCompileJSON([]byte(`{"const": 1.1}`))

	require.NoError(t, constSchema.Validate(t.Context(), 1.1))
	require.NoError(t, constSchema.Validate(t.Context(), json.Number("1.1")))
}
