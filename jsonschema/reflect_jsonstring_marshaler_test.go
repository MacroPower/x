package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// quotedJSONMarshaler is an int-kind type that directly implements
// json.Marshaler. Encoding/json/v2 routes a marshaler-bearing field through
// its method, which ignores the json:",string" option, so the generator must
// not coerce such a field to {"type":"string"}.
type quotedJSONMarshaler int

func (quotedJSONMarshaler) MarshalJSON() ([]byte, error) { return []byte("7"), nil }

// quotedPtrJSONMarshaler carries MarshalJSON on the pointer receiver only; the
// addressable-field encoding still routes through it, so ",string" is ignored
// for it too.
type quotedPtrJSONMarshaler int

func (*quotedPtrJSONMarshaler) MarshalJSON() ([]byte, error) { return []byte("8"), nil }

func TestGenerateFor_JSONStringMarshalerKeepsKindSchema(t *testing.T) {
	t.Parallel()

	type doc struct {
		F quotedJSONMarshaler    `json:"f,string"`
		G *quotedJSONMarshaler   `json:"g,string"`
		P quotedPtrJSONMarshaler `json:"p,string"`
	}

	// Encoding/json/v2 routes f, g, and p through MarshalJSON, discarding the
	// quoted option.
	v := quotedJSONMarshaler(3)
	data, err := json.Marshal(&doc{F: 3, G: &v, P: 4})
	require.NoError(t, err)
	require.JSONEq(t, `{"f":7,"g":7,"p":8}`, string(data))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The marshaler fields keep the kind-based integer schema a direct
	// json.Marshaler gets instead of the string coercion its output never
	// satisfies.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"f":{"type":"integer"},
			"g":{"anyOf":[{"type":"integer"},{"type":"null"}]},
			"p":{"type":"integer"}
		},
		"required":["f","g","p"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}
