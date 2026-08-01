package jsonptr_test

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// materializeSchema is a plain JSON round-trip [jsonptr.Materialize] for tests;
// the production materializer (the parent's ParseSchemaValue) additionally
// keeps const/enum numbers exact, which these structural tests do not exercise.
func materializeSchema(node any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}

	var s jsonschema.Schema

	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

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

		got, base := jsonptr.SchemaAtJSONPointer(
			root, []string{"$defs", "Foo"}, "https://example.com/root", true, materializeSchema,
		)
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Equal(t, "https://example.com/root", base)
	})

	t.Run("navigates an array index", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"prefixItems", "1"}, "", true, materializeSchema)
		require.NotNil(t, got)
		assert.Equal(t, "boolean", got.Type)
	})

	t.Run("missing segment returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"$defs", "Missing"}, "", true, materializeSchema)
		assert.Nil(t, got)
	})

	t.Run("non-schema target returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"type"}, "", true, materializeSchema)
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
			true,
			materializeSchema,
		)
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Contains(t, base, "sub")
	})
}
