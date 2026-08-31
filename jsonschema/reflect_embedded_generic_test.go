package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

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

	// An embedded non-struct without an explicit name is an error under
	// encoding/json/v2, generic instantiations included.
	_, err := json.Marshal(hasGenericEmbed{GenList: GenList[int]{1}, B: "x"})
	require.ErrorContains(t, err, "must be explicitly given a JSON name")

	_, err = jsonschema.GenerateFor[hasGenericEmbed](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "GenList")
}
