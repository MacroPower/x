package schemaclone_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

func TestCloneNilSchema(t *testing.T) {
	t.Parallel()

	// Without a guard, the JSON round-trip converts a nil schema into the false
	// (reject-everything) schema: nil marshals as JSON null, and the upstream
	// UnmarshalJSON treats null as the boolean schema false. A nil schema must
	// clone to nil instead.
	cp, err := schemaclone.Clone(nil, children)
	require.NoError(t, err)
	assert.Nil(t, cp)
}
