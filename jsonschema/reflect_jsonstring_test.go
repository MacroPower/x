package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// intPtr is a named pointer type; encoding/json/v2's json:",string" flag
// survives every pointer level, named or not, so every pointer chain over a
// numeric payload is quoted.
type intPtr *int

func TestGenerateFor_JSONStringPointerKinds(t *testing.T) {
	t.Parallel()

	type doc struct {
		A **int  `json:",string"` //nolint:staticcheck // SA5008 models v1; v2 carries the flag down the whole pointer chain.
		B intPtr `json:",string"`
		C *int   `json:",string"`
		D int    `json:",string"`
	}

	// Encoding/json/v2 quotes every chain, the double pointer included (v1
	// dereferenced exactly one level and left A bare).
	v := 5
	p := &v
	data, err := json.Marshal(doc{A: &p, B: intPtr(&v), C: &v, D: 5})
	require.NoError(t, err)
	require.JSONEq(t, `{"A":"5","B":"5","C":"5","D":"5"}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"A":{"type":["null","string"]},
			"B":{"type":["null","string"]},
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
