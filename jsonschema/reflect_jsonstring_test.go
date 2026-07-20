package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// intPtr is a named pointer type; encoding/json never dereferences it for the
// json:",string" option, so such a field marshals as a bare number.
type intPtr *int

func TestGenerateFor_JSONStringPointerKinds(t *testing.T) {
	t.Parallel()

	type doc struct {
		A **int  `json:",string"` //nolint:staticcheck // Intentional: encoding/json ignores ",string" here.
		B intPtr `json:",string"`
		C *int   `json:",string"`
		D int    `json:",string"`
	}

	// Encoding/json quotes only D and C (one unnamed pointer deref); the double
	// pointer A and the named pointer B marshal as bare numbers.
	v := 5
	p := &v
	data, err := json.Marshal(doc{A: &p, B: intPtr(&v), C: &v, D: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":5,"B":5,"C":"5","D":"5"}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"A":{"anyOf":[{"type":"integer"},{"type":"null"}]},
			"B":{"anyOf":[{"type":"integer"},{"type":"null"}]},
			"C":{"type":["null","string"]},
			"D":{"type":"string"}
		},
		"required":["A","B","C","D"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)

	nilData, err := json.Marshal(doc{D: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":null,"B":null,"C":null,"D":"5"}`, string(nilData))
	assert.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-pointer serialization: %s", nilData)
}
