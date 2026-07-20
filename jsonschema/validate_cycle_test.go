package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema"
)

// Normalize tolerates a self-referential (cyclic) instance, so one can reach
// the keywords that compare values: uniqueItems, const, and enum. Those
// comparisons must terminate rather than abort the process with a fatal stack
// overflow. A cyclic value has no JSON serialization, so like a non-finite
// float it compares unequal to everything, including itself: it never forms a
// uniqueItems duplicate and never matches a const or enum value.
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
		wantErr  bool
	}{
		"uniqueItems with cyclic element passes": {
			schema:   `{"uniqueItems": true}`,
			instance: cyclic(),
			wantErr:  false,
		},
		"uniqueItems with same cyclic value twice passes": {
			schema:   `{"uniqueItems": true}`,
			instance: []any{sharedCycle, sharedCycle},
			wantErr:  false,
		},
		"uniqueItems still catches duplicates beside a cyclic element": {
			schema:   `{"uniqueItems": true}`,
			instance: []any{cyclic(), "x", "x"},
			wantErr:  true,
		},
		"const never matches a cyclic instance": {
			schema:   `{"const": [null, "x"]}`,
			instance: cyclic(),
			wantErr:  true,
		},
		"enum never matches a cyclic instance": {
			schema:   `{"enum": [[null, "x"], "y"]}`,
			instance: cyclic(),
			wantErr:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v := jsonschema.MustCompileJSON([]byte(tc.schema))

			err := v.Validate(t.Context(), tc.instance)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
