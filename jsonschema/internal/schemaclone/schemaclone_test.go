package schemaclone_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// children pairs a schema with its direct sub-schemas in a deterministic order,
// matching the lockstep contract Clone relies on. It returns the single Items
// child and each Properties child in a fixed key order.
func children(s *jsonschema.Schema) []*jsonschema.Schema {
	if s == nil {
		return nil
	}

	var out []*jsonschema.Schema

	if s.Items != nil {
		out = append(out, s.Items)
	}

	for _, key := range []string{"a", "b", "c"} {
		if sub, ok := s.Properties[key]; ok {
			out = append(out, sub)
		}
	}

	return out
}

func TestClone(t *testing.T) {
	t.Parallel()

	t.Run("produces an independent deep copy", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			Type: "object",
			Enum: []any{"x", "y"},
			Properties: map[string]*jsonschema.Schema{
				"a": {Type: "string"},
			},
		}

		cp, err := schemaclone.Clone(src, children)
		require.NoError(t, err)
		require.NotNil(t, cp)

		assert.Equal(t, src.Type, cp.Type)
		assert.NotSame(t, src, cp)
		assert.NotSame(t, src.Properties["a"], cp.Properties["a"])

		// Mutating the copy's shared-by-value fields must not reach src.
		cp.Enum[0] = "mutated"
		assert.Equal(t, "x", src.Enum[0])
	})

	t.Run("restores PropertyOrder at every depth", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			PropertyOrder: []string{"a", "b"},
			Items: &jsonschema.Schema{
				PropertyOrder: []string{"deep", "nested"},
			},
			Properties: map[string]*jsonschema.Schema{
				"a": {PropertyOrder: []string{"inner"}},
			},
		}

		cp, err := schemaclone.Clone(src, children)
		require.NoError(t, err)

		assert.Equal(t, []string{"a", "b"}, cp.PropertyOrder)
		assert.Equal(t, []string{"deep", "nested"}, cp.Items.PropertyOrder)
		assert.Equal(t, []string{"inner"}, cp.Properties["a"].PropertyOrder)
	})

	t.Run("cloned PropertyOrder slice is unaliased from src", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{PropertyOrder: []string{"a", "b"}}

		cp, err := schemaclone.Clone(src, children)
		require.NoError(t, err)

		cp.PropertyOrder[0] = "mutated"
		assert.Equal(t, "a", src.PropertyOrder[0])
	})

	t.Run("structural mismatch stops the lockstep restore", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			PropertyOrder: []string{"root"},
			Items:         &jsonschema.Schema{PropertyOrder: []string{"child"}},
		}

		// Children yields a different count for src's root than for its clone
		// (two entries versus one), forcing the length-mismatch bail-out at the
		// root. The root PropertyOrder is restored before the comparison, but the
		// mismatch prevents descent, so the Items child is never restored.
		mismatched := func(s *jsonschema.Schema) []*jsonschema.Schema {
			if s == src {
				return []*jsonschema.Schema{s.Items, s.Items}
			}

			if s.Items != nil {
				return []*jsonschema.Schema{s.Items}
			}

			return nil
		}

		cp, err := schemaclone.Clone(src, mismatched)
		require.NoError(t, err)

		assert.Equal(t, []string{"root"}, cp.PropertyOrder)
		assert.Nil(t, cp.Items.PropertyOrder)
	})
}
