package jsonequal_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
)

// A cyclic instance (a map or slice that contains itself) has no JSON
// serialization, so like a non-finite float it compares unequal to everything,
// including itself. The walks must terminate on such input: internal/normalize
// deliberately tolerates cyclic instances, so they reach the uniqueItems and
// const/enum paths, and an unguarded recursion would abort the process with a
// fatal stack overflow that recover cannot catch.

// cyclicSlice returns a slice whose first element is the slice itself.
func cyclicSlice() []any {
	s := []any{nil, "x"}
	s[0] = s

	return s
}

// cyclicMap returns a map that contains itself under key "self".
func cyclicMap() map[string]any {
	m := map[string]any{"k": "v"}
	m["self"] = m

	return m
}

func TestHasDuplicatesCyclicInstance(t *testing.T) {
	t.Parallel()

	sharedCycle := cyclicSlice()

	// Shared acyclic substructure (a DAG) must not be misclassified as a
	// cycle: the same inner slice appears twice on the path without being its
	// own ancestor.
	sharedInner := []any{"a"}

	tests := map[string]struct {
		arr  []any
		want bool
	}{
		"self-referential slice element terminates": {
			arr:  []any{cyclicSlice()},
			want: false,
		},
		"self-referential map element terminates": {
			arr:  []any{cyclicMap()},
			want: false,
		},
		"same cyclic value twice is not a duplicate": {
			// Like NaN, a cyclic value is unequal to itself.
			arr:  []any{sharedCycle, sharedCycle},
			want: false,
		},
		"acyclic duplicates beside a cyclic element are still found": {
			arr:  []any{cyclicSlice(), "x", "x"},
			want: true,
		},
		"nested cycle under a map terminates": {
			arr:  []any{map[string]any{"inner": cyclicSlice()}},
			want: false,
		},
		"shared acyclic substructure still compares as duplicate": {
			arr:  []any{[]any{sharedInner, sharedInner}, []any{sharedInner, sharedInner}},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonequal.HasDuplicates(tc.arr))
		})
	}
}

func TestEqualWithRatCyclicInstance(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schemaVal any
		instance  any
		want      bool
	}{
		"over-cap schema number against cyclic instance terminates": {
			// An out-of-bounds literal routes through the guarded fallback,
			// which walks the instance and must stop at the cycle.
			schemaVal: json.Number("1e5000"),
			instance:  cyclicSlice(),
			want:      false,
		},
		"schema array against cyclic instance terminates": {
			schemaVal: []any{"x", "y"},
			instance:  cyclicSlice(),
			want:      false,
		},
		"cyclic schema value against cyclic instance terminates": {
			// Both sides cyclic is the shape where the lock-step recursion has
			// no finite side to bottom out on; the up-front schema-side screen
			// must stop it.
			schemaVal: cyclicSlice(),
			instance:  cyclicSlice(),
			want:      false,
		},
		"cyclic schema value against acyclic instance terminates": {
			schemaVal: cyclicSlice(),
			instance:  []any{"x"},
			want:      false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonequal.EqualWithRat(tc.schemaVal, nil, tc.instance))
		})
	}
}
