package schemashape_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemashape"
)

func TestRelocateConstEnumToValueBranchPreservesAuthoredAnyOf(t *testing.T) {
	t.Parallel()

	t.Run("type list folds authored anyOf into allOf", func(t *testing.T) {
		t.Parallel()

		authoredA := &jsonschema.Schema{MinLength: new(5)}
		authoredB := &jsonschema.Schema{MaxLength: new(2)}
		s := &jsonschema.Schema{
			Types: []string{"null", "string"},
			AnyOf: []*jsonschema.Schema{authoredA, authoredB},
			Const: new(any("abc")),
		}

		got := schemashape.RelocateConstEnumToValueBranch(s)

		// The type list rewrites into the anyOf[value, null] wrapper with the
		// const on the value branch.
		require.Len(t, s.AnyOf, 2)
		assert.Same(t, s.AnyOf[0], got)
		assert.Equal(t, "string", got.Type)
		assert.NotNil(t, got.Const)
		assert.Nil(t, s.Const)
		assert.Equal(t, "null", s.AnyOf[1].Type)
		assert.Nil(t, s.Types)

		// The authored anyOf survives as an allOf conjunct on the wrapper, so it
		// keeps gating both the value and the null branch conjunctively.
		require.Len(t, s.AllOf, 1)
		require.Len(t, s.AllOf[0].AnyOf, 2)
		assert.Same(t, authoredA, s.AllOf[0].AnyOf[0])
		assert.Same(t, authoredB, s.AllOf[0].AnyOf[1])
	})

	t.Run("type list without authored anyOf gains no allOf", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{
			Types: []string{"null", "integer"},
			Enum:  []any{1, 2},
		}

		got := schemashape.RelocateConstEnumToValueBranch(s)

		require.Len(t, s.AnyOf, 2)
		assert.Same(t, s.AnyOf[0], got)
		assert.Nil(t, s.AllOf)
	})
}
