package jsonptr_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// TestSchemaAtJSONPointerInertIDs asserts the trackIDs switch: with tracking
// disabled (a retrieval-base walk, where $id is an inert annotation) crossing
// an intermediate $id leaves the base untouched, while the same walk with
// tracking enabled rebases below the crossed $id.
func TestSchemaAtJSONPointerInertIDs(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"a": {
				ID: "https://other.example/pub/",
				Properties: map[string]*jsonschema.Schema{
					"b": {Type: "string"},
				},
			},
		},
	}
	segments := []string{"properties", "a", "properties", "b"}

	tests := map[string]struct {
		trackIDs bool
		want     string
	}{
		"inert ids keep the starting base": {
			trackIDs: false,
			want:     "https://example.com/root",
		},
		"tracked ids rebase at the crossed $id": {
			trackIDs: true,
			want:     "https://other.example/pub/",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, base := jsonptr.SchemaAtJSONPointer(root, segments, "https://example.com/root", tc.trackIDs)
			require.NotNil(t, got)
			assert.Equal(t, "string", got.Type)
			assert.Equal(t, tc.want, base)
		})
	}
}
