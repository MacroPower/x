package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRootUnvetted pins the inliner's documented vetting exception: its
// own root schema is not vetted (only the remotes it fetches are), so Inline
// accepts a root that Compile rejects. The root here carries a negative
// minLength, a structural violation Compile reports as ErrNegativeBound.
func TestInlineRootUnvetted(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{
		Type:      "string",
		MinLength: new(-1),
	}

	_, err := jsonschema.Compile(t.Context(), root)
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)

	out, err := jsonschema.Inline(t.Context(), root)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.MinLength)
	assert.Equal(t, -1, *out.MinLength)
}
