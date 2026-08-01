package validate_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// switchText is a bool type that marshals itself to a string ("on"/"off"), so
// its generated schema has type string while its Go kind stays bool.
type switchText bool

// MarshalText implements encoding.TextMarshaler.
func (s switchText) MarshalText() ([]byte, error) {
	if s {
		return []byte("on"), nil
	}

	return []byte("off"), nil
}

// TestValidateInterpreter_RequiredStringCoercedSerializedZero pins that
// required's non-zero check on a string-coerced field forbids the text the
// type's zero value actually serializes to, not a hardcoded "0"/"false". For a
// string-marshaling type (levelText's zero emits "L0", switchText's emits
// "off") the hardcoded literal never occurs in any instance, so the documented
// non-zero requirement would be silently inert: go-playground's required
// rejects the zero value, and the schema must reject its serialized form.
func TestValidateInterpreter_RequiredStringCoercedSerializedZero(t *testing.T) {
	t.Parallel()

	type Form struct {
		L levelText  `json:"l" validate:"required"`
		S switchText `json:"s" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"l":{"type":"string","not":{"const":"L0"}},
			"s":{"type":"string","not":{"const":"off"}}
		},
		"required":["l","s"],
		"additionalProperties":false
	}`, string(got))

	// The instances Go actually marshals for the zero and non-zero values.
	zeroLevel, err := json.Marshal(Form{L: 0, S: true})
	require.NoError(t, err)

	zeroSwitch, err := json.Marshal(Form{L: 1, S: false})
	require.NoError(t, err)

	nonZero, err := json.Marshal(Form{L: 1, S: true})
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(), nonZero),
		"a non-zero serialized value satisfies required")
	require.Error(t, v.ValidateJSON(t.Context(), zeroLevel),
		"the numeric type's serialized zero must be rejected")
	require.Error(t, v.ValidateJSON(t.Context(), zeroSwitch),
		"the bool type's serialized zero must be rejected")
}
