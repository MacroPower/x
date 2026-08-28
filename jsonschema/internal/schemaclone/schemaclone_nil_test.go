package schemaclone_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// TestCloneNilSchema pins that a nil schema clones to nil. The copy allocates a
// node and dereferences the source into it, so the nil guard is what keeps an
// absent sub-schema absent rather than turning it into an empty schema, which
// accepts every instance.
func TestCloneNilSchema(t *testing.T) {
	t.Parallel()

	assert.Nil(t, schemaclone.Clone(nil))
}
