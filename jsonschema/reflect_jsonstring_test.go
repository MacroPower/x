package jsonschema_test

import (
	"encoding/json/v2"
	"testing"
	"time"

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

// TestGenerateFor_JSONStringDurationRefused pins that the json:",string"
// override never claims a time.Duration. Encoding/json/v2's duration codec
// has no default representation and refuses the value before it consults the
// flag, so the field takes the same path as an unflagged duration: refused
// under the defaults, and answered by a type override when one is registered.
func TestGenerateFor_JSONStringDurationRefused(t *testing.T) {
	t.Parallel()

	type value struct {
		D time.Duration `json:"d,string"`
	}

	type pointer struct {
		D *time.Duration `json:"d,string"`
	}

	override := jsonschema.WithTypeSchemaFor[time.Duration](jsonschema.TypeSchema{
		Value: &jsonschema.Schema{Type: "string", Pattern: "^[0-9]+s$"},
	})

	t.Run("value refused", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[value](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
	})

	t.Run("pointer refused", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[pointer](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
	})

	t.Run("override applies", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[value](t.Context(), override)
		require.NoError(t, err)
		assert.Equal(t, "^[0-9]+s$", s.Properties["d"].Pattern)
	})
}
