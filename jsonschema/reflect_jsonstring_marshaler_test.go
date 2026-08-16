package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// quotedJSONMarshaler is an int-kind type that directly implements
// json.Marshaler. The encoding/json package ignores the json:",string" option for a
// marshaler-bearing field and emits the marshaler's raw bytes, so the
// generator must not coerce such a field to {"type":"string"}.
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
		S string                 `json:"s,string"`
	}

	// The encoding/json package routes f, g, and p through MarshalJSON, discarding the
	// quoted option; the plain string field s stays double-encoded.
	v := quotedJSONMarshaler(3)
	data, err := json.Marshal(&doc{F: 3, G: &v, P: 4, S: "abc"})
	require.NoError(t, err)
	require.JSONEq(t, `{"f":7,"g":7,"p":8,"s":"\"abc\""}`, string(data))

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
			"p":{"type":"integer"},
			"s":{"type":"string"}
		},
		"required":["f","g","p","s"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}
