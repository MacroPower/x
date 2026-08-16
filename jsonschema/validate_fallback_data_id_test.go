package jsonschema_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestJSONPointerFallbackThroughDataKeepsDocumentBase pins the base URI of a
// $ref target materialized through the JSON-pointer fallback from inside a
// non-schema keyword. The "$id" data member crossed inside examples is plain
// instance data, not a resource boundary, so the target's own relative $ref
// absolutizes against the containing document's base, not the data object's
// "$id". The resolver serves only the document-base URI; compiling
// successfully proves no other URI was fetched.
func TestJSONPointerFallbackThroughDataKeepsDocumentBase(t *testing.T) {
	t.Parallel()

	schema, err := jsonschema.ParseSchema([]byte(`{
		"$id": "https://root.example/s.json",
		"examples": [{"$id": "https://models.example/widget", "payload": {"$ref": "sub.json"}}],
		"properties": {"x": {"$ref": "#/examples/0/payload"}}
	}`))
	require.NoError(t, err)

	resolver := jsonschema.RefResolverFunc(func(_ context.Context, uri string) (*jsonschema.Schema, error) {
		if uri == "https://root.example/sub.json" {
			return &jsonschema.Schema{Type: "string"}, nil
		}

		return nil, fmt.Errorf("%w: %s", jsonschema.ErrNotResolved, uri)
	})

	v, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err,
		"the fallback target's relative $ref must resolve against the document base")

	require.NoError(t, v.Validate(t.Context(), map[string]any{"x": "hello"}))
	require.Error(t, v.Validate(t.Context(), map[string]any{"x": 42}),
		"the fetched sub-schema must constrain x to a string")
}
