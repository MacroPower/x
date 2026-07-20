package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateRejectsCyclicInstance pins that a self-referential instance is
// rejected up front with the not-accepted error rather than mis-validated.
// Normalize never mutates its input, so a cycle edge in the normalized result
// would point back at the original container with raw, un-normalized leaves;
// walking through it would produce a wrong verdict (for example, an integral
// value failing a "type": "integer" assertion).
func TestValidateRejectsCyclicInstance(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"self": {
				Properties: map[string]*jsonschema.Schema{
					"n": {Type: "integer"},
				},
			},
		},
	}

	cyclicMap := map[string]any{"n": 1}
	cyclicMap["self"] = cyclicMap

	cyclicSlice := make([]any, 2)
	cyclicSlice[0] = 1
	cyclicSlice[1] = cyclicSlice

	tests := map[string]struct {
		instance any
	}{
		"self-referential map":   {instance: cyclicMap},
		"self-referential slice": {instance: cyclicSlice},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), schema, tc.instance)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not accepted")

			var verr *jsonschema.ValidationError

			assert.NotErrorAs(t, err, &verr,
				"a cyclic instance is rejected by the funnel, not a validation failure")
		})
	}

	t.Run("acyclic equivalent validates", func(t *testing.T) {
		t.Parallel()

		instance := map[string]any{
			"n":    1,
			"self": map[string]any{"n": 1},
		}

		require.NoError(t, jsonschema.Validate(t.Context(), schema, instance))
	})
}
