package schemashape_test

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestCloneOverrideExtras(t *testing.T) {
	t.Parallel()

	t.Run("reallocates slice backing arrays", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			Enum:          []any{"a", "b"},
			Required:      []string{"a"},
			Types:         []string{"string", "null"},
			PropertyOrder: []string{"a", "b"},
		}

		enum0 := &src.Enum[0]
		required0 := &src.Required[0]

		schemashape.CloneOverrideExtras(src)

		// CloneOverrideExtras reassigns each slice to a fresh backing array, so
		// the address of element zero changes.
		assert.NotSame(t, enum0, &src.Enum[0])
		assert.NotSame(t, required0, &src.Required[0])
	})

	t.Run("mutating a clone does not reach the original", func(t *testing.T) {
		t.Parallel()

		constVal := any("c")
		original := &jsonschema.Schema{
			Enum:              []any{"a"},
			Const:             &constVal,
			Default:           json.RawMessage(`{"x":1}`),
			Extra:             map[string]any{"k": "v"},
			Examples:          []any{1},
			Required:          []string{"a"},
			Types:             []string{"string"},
			PropertyOrder:     []string{"a"},
			Vocabulary:        map[string]bool{"https://x": true},
			DependencyStrings: map[string][]string{"a": {"b"}},
			DependentRequired: map[string][]string{"c": {"d"}},
		}

		clone := *original // shallow value copy: containers still aliased
		schemashape.CloneOverrideExtras(&clone)

		clone.Enum[0] = "mutated"
		*clone.Const = "mutated"
		clone.Extra["k"] = "mutated"
		clone.Examples[0] = "mutated"
		clone.Required[0] = "mutated"
		clone.Types[0] = "mutated"
		clone.Vocabulary["https://x"] = false
		clone.DependencyStrings["a"] = []string{"mutated"}
		clone.DependentRequired["c"] = []string{"mutated"}

		assert.Equal(t, "a", original.Enum[0])
		assert.Equal(t, "c", *original.Const)
		assert.Equal(t, "v", original.Extra["k"])
		assert.Equal(t, 1, original.Examples[0])
		assert.Equal(t, "a", original.Required[0])
		assert.Equal(t, "string", original.Types[0])
		assert.True(t, original.Vocabulary["https://x"])
		assert.Equal(t, []string{"b"}, original.DependencyStrings["a"])
		assert.Equal(t, []string{"d"}, original.DependentRequired["c"])
	})

	t.Run("nil containers are left nil", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{}
		schemashape.CloneOverrideExtras(s)

		assert.Nil(t, s.Enum)
		assert.Nil(t, s.Extra)
		assert.Nil(t, s.Const)
		assert.Nil(t, s.Vocabulary)
	})
}
