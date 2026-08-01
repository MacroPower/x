package jsonequal_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonequal"
)

// A hand-built const/enum can carry container shapes the schema parser never
// produces, most commonly the map[any]any gopkg.in/yaml.v2 decodes documents
// into. Upstream jsonschema.Equal checks only reflect.Kind before iterating
// maps, so comparing a map[any]any against a decoded map[string]any of the
// same length panics inside reflect.Value.MapIndex. The delegation must
// convert such a panic into "unequal" instead of letting it escape the
// validation walk, and the non-finite screen must reach inside the map[any]any
// shape so a NaN there stays unequal to everything.

func TestEqualWithRatHandBuiltMapShapes(t *testing.T) {
	t.Parallel()

	fn := func() {}

	tests := map[string]struct {
		schemaVal any
		instance  any
		want      bool
	}{
		"map[any]any against decoded object does not panic": {
			schemaVal: map[any]any{"a": 1},
			instance:  map[string]any{"a": 1.0},
			want:      false,
		},
		"map[any]any against identical map[any]any stays equal": {
			schemaVal: map[any]any{"a": 1},
			instance:  map[any]any{"a": 1.0},
			want:      true,
		},
		"map[any]any nested in an array does not panic": {
			schemaVal: []any{map[any]any{"a": 1}},
			instance:  []any{map[string]any{"a": 1.0}},
			want:      false,
		},
		"NaN inside map[any]any is unequal to itself": {
			schemaVal: map[any]any{"a": math.NaN()},
			instance:  map[any]any{"a": math.NaN()},
			want:      false,
		},
		"NaN inside map[any]any is unequal to zero": {
			schemaVal: map[any]any{"a": math.NaN()},
			instance:  map[any]any{"a": 0.0},
			want:      false,
		},
		"non-nil func does not panic": {
			schemaVal: fn,
			instance:  fn,
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

// A cyclic map[any]any must terminate the walks like the decoded-JSON
// container shapes do, instead of overflowing the stack in the non-finite
// screen or the upstream comparison.
func TestEqualWithRatCyclicMapAnyAny(t *testing.T) {
	t.Parallel()

	m := map[any]any{"k": "v"}
	m["self"] = m

	assert.False(t, jsonequal.EqualWithRat(m, nil, map[string]any{"k": "v"}))
	assert.False(t, jsonequal.EqualWithRat(m, nil, m))
}
