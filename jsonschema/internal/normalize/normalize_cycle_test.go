package normalize_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/normalize"
)

// TestValueCheckedRejectsCyclicInstance pins that ValueChecked reports a
// self-referential instance as not accepted. Copy-on-change means a back-edge
// in the normalized result still points at the original input container, whose
// leaves may be un-normalized (a raw Go int instead of json.Number); the
// validation funnel must reject such an instance rather than mis-validate
// through the cycle edge.
func TestValueCheckedRejectsCyclicInstance(t *testing.T) {
	t.Parallel()

	cyclicMap := map[string]any{"n": 1}
	cyclicMap["self"] = cyclicMap

	cyclicSlice := make([]any, 2)
	cyclicSlice[0] = 1
	cyclicSlice[1] = cyclicSlice

	// The back-edge sits inside an intermediate container that itself needs no
	// scalar change, so the cycle is only reachable transitively.
	transitive := map[string]any{"n": 1}
	transitive["a"] = map[string]any{"x": transitive}

	tests := map[string]struct {
		in any
	}{
		"self-referential map":   {in: cyclicMap},
		"self-referential slice": {in: cyclicSlice},
		"transitive map cycle":   {in: transitive},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, accepted := normalize.ValueChecked(tc.in)
			assert.False(
				t,
				accepted,
				"a cyclic instance must be reported not accepted: its back-edge points at the original, un-normalized container",
			)
		})
	}
}
