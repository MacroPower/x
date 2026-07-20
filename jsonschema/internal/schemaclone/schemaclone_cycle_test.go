package schemaclone_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

func TestCloneCyclicGraph(t *testing.T) {
	t.Parallel()

	t.Run("self cycle returns ErrCyclic", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{Type: "object"}
		s.Items = s

		cp, err := schemaclone.Clone(s, children)
		require.ErrorIs(t, err, schemaclone.ErrCyclic)
		assert.Nil(t, cp)
	})

	t.Run("two-node cycle returns ErrCyclic", func(t *testing.T) {
		t.Parallel()

		a := &jsonschema.Schema{Type: "object"}
		b := &jsonschema.Schema{Type: "array"}
		a.Items = b
		b.Items = a

		cp, err := schemaclone.Clone(a, children)
		require.ErrorIs(t, err, schemaclone.ErrCyclic)
		assert.Nil(t, cp)
	})

	t.Run("diamond-shaped DAG is not a cycle", func(t *testing.T) {
		t.Parallel()

		shared := &jsonschema.Schema{Type: "string"}
		s := &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"a": shared,
				"b": shared,
			},
		}

		cp, err := schemaclone.Clone(s, children)
		require.NoError(t, err)
		require.NotNil(t, cp)
		assert.Equal(t, "string", cp.Properties["a"].Type)
		assert.Equal(t, "string", cp.Properties["b"].Type)
	})
}
