package jsonschema_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestSubschemas(t *testing.T) {
	t.Parallel()

	propA := &jsonschema.Schema{Type: "string"}
	propB := &jsonschema.Schema{Type: "integer"}
	defY := &jsonschema.Schema{Type: "object"}
	defZ := &jsonschema.Schema{Type: "array"}
	allOf0 := &jsonschema.Schema{MinLength: jsonschema.Ptr(1)}
	allOf1 := &jsonschema.Schema{MaxLength: jsonschema.Ptr(2)}
	items := &jsonschema.Schema{Type: "number"}
	not := &jsonschema.Schema{}

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   []*jsonschema.Schema
	}{
		"nil schema": {
			schema: nil,
			want:   nil,
		},
		"no sub-schemas": {
			schema: &jsonschema.Schema{Type: "string", Title: "leaf"},
			want:   nil,
		},
		"map children in sorted-key order": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"b": propB, "a": propA},
			},
			want: []*jsonschema.Schema{propA, propB},
		},
		"nil map entries skipped": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a": propA, "b": nil},
			},
			want: []*jsonschema.Schema{propA},
		},
		"nil slice entries skipped": {
			schema: &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{allOf0, nil, allOf1},
			},
			want: []*jsonschema.Schema{allOf0, allOf1},
		},
		"maps then slices then singles": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"b": propB, "a": propA},
				Defs:       map[string]*jsonschema.Schema{"z": defZ, "y": defY},
				AllOf:      []*jsonschema.Schema{allOf0, allOf1},
				Items:      items,
				Not:        not,
			},
			want: []*jsonschema.Schema{propA, propB, defY, defZ, allOf0, allOf1, items, not},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonschema.Subschemas(tc.schema))

			// A second call must return the same order: map children are
			// emitted in sorted-key order, so traversal is deterministic.
			assert.Equal(t, tc.want, jsonschema.Subschemas(tc.schema))
		})
	}
}

// TestSubschemasDirectOnly pins that Subschemas returns only direct children:
// a grandchild is reachable through Walk, not through one Subschemas call.
func TestSubschemasDirectOnly(t *testing.T) {
	t.Parallel()

	grandchild := &jsonschema.Schema{Type: "string"}
	child := &jsonschema.Schema{Items: grandchild}
	root := &jsonschema.Schema{Not: child}

	assert.Equal(t, []*jsonschema.Schema{child}, jsonschema.Subschemas(root))
}

func TestWalk(t *testing.T) {
	t.Parallel()

	t.Run("nil schema is a no-op", func(t *testing.T) {
		t.Parallel()

		called := false
		err := jsonschema.Walk(nil, func(*jsonschema.Schema) error {
			called = true

			return nil
		})

		require.NoError(t, err)
		assert.False(t, called)
	})

	t.Run("pre-order over the transitive closure", func(t *testing.T) {
		t.Parallel()

		leaf := &jsonschema.Schema{Type: "string"}
		mid := &jsonschema.Schema{Items: leaf}
		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{"p": mid},
		}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(s *jsonschema.Schema) error {
			visited = append(visited, s)

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root, mid, leaf}, visited)
	})

	t.Run("aliased schema visited once", func(t *testing.T) {
		t.Parallel()

		shared := &jsonschema.Schema{Type: "string"}
		root := &jsonschema.Schema{Items: shared, Not: shared}

		count := 0

		err := jsonschema.Walk(root, func(s *jsonschema.Schema) error {
			if s == shared {
				count++
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("cyclic graph terminates", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{Type: "object"}
		root.Properties = map[string]*jsonschema.Schema{"self": root}

		count := 0

		err := jsonschema.Walk(root, func(*jsonschema.Schema) error {
			count++

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("stops at and returns the first error", func(t *testing.T) {
		t.Parallel()

		errStop := errors.New("stop")
		first := &jsonschema.Schema{Type: "string"}
		second := &jsonschema.Schema{Type: "integer"}
		root := &jsonschema.Schema{AllOf: []*jsonschema.Schema{first, second}}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(s *jsonschema.Schema) error {
			visited = append(visited, s)
			if s == first {
				return errStop
			}

			return nil
		})

		require.ErrorIs(t, err, errStop)
		assert.Equal(t, []*jsonschema.Schema{root, first}, visited)
	})

	t.Run("follows children replaced by fn", func(t *testing.T) {
		t.Parallel()

		original := &jsonschema.Schema{Type: "integer"}
		replacement := &jsonschema.Schema{Type: "string"}
		root := &jsonschema.Schema{Items: original}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(s *jsonschema.Schema) error {
			if s.Items == original {
				s.Items = replacement
			}

			visited = append(visited, s)

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root, replacement}, visited,
			"fn runs before children are gathered, so the walk follows the replacement")
	})
}
