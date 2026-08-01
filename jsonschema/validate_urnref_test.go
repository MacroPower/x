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

// TestValidateURNDotSegmentRef exercises RFC 3986 5.2.2 for an opaque URN
// base: a dot-segmented relative $id registers under the canonical
// dot-segment-free key, so the absolute $ref spelling resolves to it the same
// way it does under a hierarchical base.
func TestValidateURNDotSegmentRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:a/b/c",
			"$defs": {"d": {"$id": "../d", "type": "integer"}},
			"$ref": "urn:example:a/d"
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	require.NoError(t, compiled.Validate(t.Context(), 5))

	err = compiled.Validate(t.Context(), "not an integer")
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "integer"`)
}

// TestValidateURNRootedRef exercises RFC 3986 5.2.2 for a rooted reference
// path against an opaque URN base: a path beginning with "/" replaces the base
// path outright rather than merging with it, so the $ref targets the key the
// absolute $id spelling registers under (urn:/c, not urn:example:a//c).
func TestValidateURNRootedRef(t *testing.T) {
	t.Parallel()

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "urn:example:a/b",
			"$defs": {"A": {"$id": "urn:/c", "type": "integer"}},
			"$ref": "/c"
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	compiled, err := jsonschema.Compile(t.Context(), &schema)
	require.NoError(t, err)

	require.NoError(t, compiled.Validate(t.Context(), 5))

	err = compiled.Validate(t.Context(), "not an integer")
	require.Error(t, err)

	var verr *jsonschema.ValidationError

	require.ErrorAs(t, err, &verr)
	assert.NotContains(t, verr.Error(), "cannot resolve")
	assert.Contains(t, verr.Error(), `expected "integer"`)
}
