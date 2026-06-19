package magicschema_test

import (
	"math"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema"
)

func TestSetSchemaType(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		types     []string
		wantType  string
		wantTypes []string
	}{
		"empty leaves the schema untouched": {types: nil},
		"single type collapses to scalar Type": {
			types:    []string{"string"},
			wantType: "string",
		},
		"multiple types become the Types union": {
			types:     []string{"string", "null"},
			wantTypes: []string{"string", "null"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Seed both fields to confirm the sibling is always cleared, so a
			// schema never carries Type and Types at once.
			s := &jsonschema.Schema{Type: "seed", Types: []string{"seed"}}
			if tc.types == nil {
				s = &jsonschema.Schema{}
			}

			magicschema.SetSchemaType(s, tc.types)

			assert.Equal(t, tc.wantType, s.Type)
			assert.Equal(t, tc.wantTypes, s.Types)
		})
	}
}

func TestToSubSchemaArrayAllOrNothing(t *testing.T) {
	t.Parallel()

	// A NaN cannot be JSON-marshaled, so the branch carrying it does not round
	// trip. Dropping just that branch would shrink an anyOf/oneOf and reject
	// values it should accept, so the whole list clears instead.
	got := magicschema.ToSubSchemaArray([]any{
		map[string]any{"type": "string"},
		map[string]any{"const": math.NaN()},
	})
	assert.Nil(t, got, "a branch that cannot round-trip clears the whole combinator")

	// A list whose elements all round-trip is preserved in full.
	got = magicschema.ToSubSchemaArray([]any{
		map[string]any{"type": "string"},
		map[string]any{"type": "integer"},
	})
	assert.Len(t, got, 2)
}
