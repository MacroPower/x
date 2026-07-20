package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateURNEncodedRef exercises registration/lookup symmetry for a
// percent-encoded relative $id under an opaque URN base: the embedded resource
// registers under the same key the absolute $ref spells, so the ref resolves
// and validation reports the leaf type failure rather than an unresolved ref.
func TestValidateURNEncodedRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:root",
			"$defs": {"s": {"$id": "sub%2Fx", "type": "string"}},
			"properties": {"x": {"$ref": "urn:example:sub%2Fx"}}
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	err = compiled.Validate(t.Context(), map[string]any{"x": 5})
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "string"`)
}
