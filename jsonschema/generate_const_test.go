package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

// TestNullablePointerEnumPermitsNull covers a nullable pointer field carrying an
// enum tag: the enum must constrain the value branch only, leaving null valid.
// On the wrapper, enum (which tests the value regardless of type) would reject
// the permitted null.
func TestNullablePointerEnumPermitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Kind *string `json:"kind,omitempty" jsonschema:"enum=a|b"`
	}

	s, err := jsonschema.GenerateFor[doc]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"kind":{"anyOf":[{"type":"string","enum":["a","b"]},{"type":"null"}]}
		},
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.Validate(map[string]any{"kind": nil}))
	assert.NoError(t, v.Validate(map[string]any{"kind": "a"}))
	assert.Error(t, v.Validate(map[string]any{"kind": "z"}))
}

// TestIntegerConstAtTypeBoundary covers a const set to a sized integer type's
// own maximum: the type-derived maximum rounds that boundary down to stay
// representable as float64, so it must be dropped or the schema would reject its
// own const, accepting nothing.
func TestIntegerConstAtTypeBoundary(t *testing.T) {
	t.Parallel()

	type doc struct {
		N uint64 `json:"n" jsonschema:"const=18446744073709551615"`
	}

	s, err := jsonschema.GenerateFor[doc]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"n":{"type":"integer","const":18446744073709551615}
		},
		"required":["n"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON([]byte(`{"n":18446744073709551615}`)))
}
