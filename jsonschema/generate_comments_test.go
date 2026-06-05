package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
)

// TestPromotedEmbeddedFieldComment covers doc-comment extraction for a field
// promoted from an embedded struct declared in another package: the comment
// lives in the embedded type's source, not the outer type's.
func TestPromotedEmbeddedFieldComment(t *testing.T) {
	t.Parallel()

	type doc struct {
		alpha.Widget
		Extra string `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](jsonschema.WithComments(true))
	require.NoError(t, err)

	require.Contains(t, s.Properties, "size")
	assert.Contains(t, s.Properties["size"].Description, "documents the widget size")
}

// TestGenericTypeComment covers doc-comment extraction for an instantiated
// generic type, whose reflect name ("Box[int]") carries a type-argument list
// that must be stripped to match the source declaration ("Box").
func TestGenericTypeComment(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.Box[int]](jsonschema.WithComments(true))
	require.NoError(t, err)

	assert.Contains(t, s.Description, "documented generic type")
	require.Contains(t, s.Properties, "item")
	assert.Contains(t, s.Properties["item"].Description, "documents the boxed value")
}
