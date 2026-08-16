package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// inlineBoundsInner is the named spelling of the anonymous struct below, so the
// test can pin that both spellings take the same object-count bound.
type inlineBoundsInner struct {
	A int `json:"a"`
}

// TestTagObjectCountBoundsOnInlineStruct pins that an object-count bound lands
// on an anonymous (inline) struct field exactly as it does on the $defs-backed
// named spelling: the inline payload declares an object outright, so it is
// judged by what that schema declares rather than rejected as an opaque value.
// Bounds from the other keyword families still report, with the declared-object
// shape named in the reason.
func TestTagObjectCountBoundsOnInlineStruct(t *testing.T) {
	t.Parallel()

	t.Run("count bounds apply", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V struct {
				A int `json:"a"`
			} `json:"v" jsonschema:"minProperties=1,maxProperties=4"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		v := s.Properties["v"]
		require.NotNil(t, v.MinProperties)
		require.NotNil(t, v.MaxProperties)
		assert.Equal(t, int64(1), int64(*v.MinProperties))
		assert.Equal(t, int64(4), int64(*v.MaxProperties))
	})

	t.Run("named spelling agrees", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V inlineBoundsInner `json:"v" jsonschema:"minProperties=1"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context())
		require.NoError(t, err)

		v := s.Properties["v"]
		require.NotNil(t, v.MinProperties)
		assert.Equal(t, int64(1), int64(*v.MinProperties))
	})

	t.Run("other families still report", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V struct {
				A int `json:"a"`
			} `json:"v" jsonschema:"minimum=1"`
		}

		_, err := jsonschema.GenerateFor[T](t.Context())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a declared object cannot carry a numeric bound")
	})
}
