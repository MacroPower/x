package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRetrievalBasePointerFallback asserts that the JSON-pointer
// fallback honors [jsonschema.WithRetrievalBase]: a pointer routed through an
// unknown keyword must not rebase at an intermediate $id it crosses, so a ref
// inside the located schema absolutizes against the document's retrieval base
// and resolves from disk, the same way the typed traversal path treats the
// $id as inert.
func TestInlineRetrievalBasePointerFallback(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"leaf.json": `{"type": "string"}`,
	}

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$ref": "#/x-wrap/mid/inner",
			"x-wrap": {
				"mid": {
					"$id": "https://other.example/pub/",
					"inner": {"$ref": "leaf.json"}
				}
			}
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	got, err := jsonschema.Inline(
		t.Context(),
		&schema,
		jsonschema.WithRefResolver(jsonschema.NewFileResolver(mapFS(files))),
		jsonschema.WithBaseURI("main.json"),
		jsonschema.WithRetrievalBase(true),
	)
	require.NoError(t, err)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"string"`)
}
