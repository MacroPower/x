package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// GenList is a generic named slice type. Embedded as GenList[int], its
// [reflect.StructField.Name] is the bare identifier "GenList", while
// [reflect.Type.Name] is "GenList[int]"; encoding/json keys the field by the
// former.
type GenList[T any] []T

type hasGenericEmbed struct {
	GenList[int]

	B string `json:"b"`
}

func TestGenerateFor_EmbeddedGenericNonStructType(t *testing.T) {
	t.Parallel()

	// Encoding/json keys the embed by the field name, without type arguments.
	setData, err := json.Marshal(hasGenericEmbed{GenList: GenList[int]{1}, B: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"GenList":[1],"b":"x"}`, string(setData))

	nilData, err := json.Marshal(hasGenericEmbed{B: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"GenList":null,"b":"x"}`, string(nilData))

	s, err := jsonschema.GenerateFor[hasGenericEmbed](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"GenList":{"type":["null","array"],"items":{"type":"integer"}},
			"b":{"type":"string"}
		},
		"required":["GenList","b"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the struct's own serialization: %s", setData)
	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-embed serialization: %s", nilData)
}
