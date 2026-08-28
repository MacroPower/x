package schemaclone_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// TestCloneNilSchema pins that a nil schema clones to nil. An absent sub-schema
// must stay absent rather than becoming an empty schema, which accepts every
// instance.
func TestCloneNilSchema(t *testing.T) {
	t.Parallel()

	assert.Nil(t, schemaclone.Clone(nil))
}
