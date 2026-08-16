package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestTagUniqueItemsOnMapIsRejected pins that the jsonschema tag's explicit
// uniqueItems on a map field is an error rather than a silent drop. The shared
// model's cell is an ignore -- distinct map values are a real go-playground
// rule with no object-side keyword -- which only a rule-shaped dialect may
// take: this dialect names the keyword outright, and its documented policy is
// an error rather than an inert keyword nothing enforces.
func TestTagUniqueItemsOnMapIsRejected(t *testing.T) {
	t.Parallel()

	type T struct {
		V map[string]int `json:"v" jsonschema:"uniqueItems=true"`
	}

	_, err := jsonschema.GenerateFor[T](t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "constraint not supported for this shape")
}
