package tagparse_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/tagparse"
)

// TestKeysAreTheParsedVocabulary pins that every key Keys lists enters
// key-value mode in Parse, and that the list is sorted, so a rig drawing from
// it draws deterministically.
func TestKeysAreTheParsedVocabulary(t *testing.T) {
	t.Parallel()

	keys := tagparse.Keys()
	require.NotEmpty(t, keys)
	assert.True(t, slices.IsSorted(keys), "Keys must be sorted")

	for _, key := range keys {
		directives, description, err := tagparse.Parse(key + "=x")
		require.NoError(t, err, key)
		assert.Empty(t, description, "%s=x must parse as a pair, not a description", key)
		require.Len(t, directives, 1, key)
		assert.Equal(t, key, directives[0].Key)
	}
}
