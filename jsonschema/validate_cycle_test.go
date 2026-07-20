package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A self-referential (cyclic) instance is rejected by the instance funnel
// before any keyword runs, so the value-comparison keywords -- uniqueItems,
// const, and enum -- can never walk a cycle and abort the process with a fatal
// stack overflow. The rejection arrives as an ordinary error, not a crash and
// not a *ValidationError. The comparison walks themselves keep their own cycle
// guards as defense in depth, pinned in internal/jsonequal.
func TestValidateCyclicInstanceValueComparisons(t *testing.T) {
	t.Parallel()

	cyclic := func() []any {
		s := []any{nil, "x"}
		s[0] = s

		return s
	}

	sharedCycle := cyclic()

	tests := map[string]struct {
		schema   string
		instance any
	}{
		"uniqueItems with cyclic element": {
			schema:   `{"uniqueItems": true}`,
			instance: cyclic(),
		},
		"uniqueItems with same cyclic value twice": {
			schema:   `{"uniqueItems": true}`,
			instance: []any{sharedCycle, sharedCycle},
		},
		"uniqueItems with duplicates beside a cyclic element": {
			schema:   `{"uniqueItems": true}`,
			instance: []any{cyclic(), "x", "x"},
		},
		"const with cyclic instance": {
			schema:   `{"const": [null, "x"]}`,
			instance: cyclic(),
		},
		"enum with cyclic instance": {
			schema:   `{"enum": [[null, "x"], "y"]}`,
			instance: cyclic(),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v := jsonschema.MustCompileJSON([]byte(tc.schema))

			err := v.Validate(t.Context(), tc.instance)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not accepted")

			var verr *jsonschema.ValidationError

			assert.NotErrorAs(t, err, &verr,
				"a cyclic instance is rejected by the funnel, not a validation failure")
		})
	}
}
