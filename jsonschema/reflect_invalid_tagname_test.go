package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Tag names encoding/json rejects as invalid (characters outside letters,
// digits, and its fixed punctuation set) are discarded: the field marshals
// under its Go name, and an embedded struct with such a tag is promoted.

func TestGenerateFor_InvalidTagNameFallsBackToFieldName(t *testing.T) {
	t.Parallel()

	type doc struct {
		A string `json:"a😀b"`
		C string `json:"x\"y"` //nolint:staticcheck // Intentional: invalid tag name.
	}

	data, err := json.Marshal(doc{A: "1", C: "3"})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":"1","C":"3"}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"A":{"type":"string"},
			"C":{"type":"string"}
		},
		"required":["A","C"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}

type invalidTagInner struct {
	X string `json:"x"`
}

type invalidTagOuter struct {
	invalidTagInner `json:"a😀b"` //nolint:unused // Exercised via reflection; fields promoted.

	Y string `json:"y"`
}

func TestGenerateFor_InvalidTagNameOnEmbeddedStructPromotes(t *testing.T) {
	t.Parallel()

	// Encoding/json discards the invalid name, so the embed stays anonymous and
	// its fields are promoted.
	data, err := json.Marshal(invalidTagOuter{invalidTagInner: invalidTagInner{X: "1"}, Y: "2"})
	require.NoError(t, err)
	require.JSONEq(t, `{"x":"1","y":"2"}`, string(data))

	s, err := jsonschema.GenerateFor[invalidTagOuter](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"x":{"type":"string"},
			"y":{"type":"string"}
		},
		"required":["x","y"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}
