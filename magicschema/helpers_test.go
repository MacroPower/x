package magicschema_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/magicschema"
)

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
