package jsonptr_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

func TestSchemaAtJSONPointer(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{
		ID:   "https://example.com/root",
		Type: "object",
		Defs: map[string]*jsonschema.Schema{
			"Foo": {Type: "string"},
		},
		PrefixItems: []*jsonschema.Schema{
			{Type: "integer"},
			{Type: "boolean"},
		},
	}

	t.Run("navigates into $defs", func(t *testing.T) {
		t.Parallel()

		got, base := jsonptr.SchemaAtJSONPointer(root, []string{"$defs", "Foo"}, "https://example.com/root")
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Equal(t, "https://example.com/root", base)
	})

	t.Run("navigates an array index", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"prefixItems", "1"}, "")
		require.NotNil(t, got)
		assert.Equal(t, "boolean", got.Type)
	})

	t.Run("missing segment returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"$defs", "Missing"}, "")
		assert.Nil(t, got)
	})

	t.Run("non-schema target returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"type"}, "")
		assert.Nil(t, got)
	})

	t.Run("intermediate $id rebases", func(t *testing.T) {
		t.Parallel()

		nested := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"a": {
					ID: "sub/",
					Properties: map[string]*jsonschema.Schema{
						"b": {Type: "string"},
					},
				},
			},
		}

		got, base := jsonptr.SchemaAtJSONPointer(
			nested,
			[]string{"properties", "a", "properties", "b"},
			"https://example.com/root",
		)
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Contains(t, base, "sub")
	})
}
