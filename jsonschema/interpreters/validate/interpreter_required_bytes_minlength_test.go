package validate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_RequiredOnByteSliceMinLength pins that required on a
// []byte field raises a minLength: 1 floor on its base64 string schema. The
// package maps required-on-collections to a non-empty floor (minItems: 1 for
// []string); a []byte marshals to a base64 string, so that same floor lands on
// minLength, where it constrains the serialized empty value "" instead of
// riding along as an inert array keyword.
func TestValidateInterpreter_RequiredOnByteSliceMinLength(t *testing.T) {
	t.Parallel()

	type Doc struct {
		Data []byte `json:"data" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Doc](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Contains(t, s.Required, "data")

	prop := s.Properties["data"]
	require.NotNil(t, prop)
	assert.Equal(t, new(1), prop.MinLength,
		"the non-empty floor lands on the serialized base64 string")
	assert.Nil(t, prop.MinItems)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"data":"AQ=="}`)),
		"a non-empty base64 string satisfies required")
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"data":""}`)),
		"the serialized empty byte slice is rejected")
}
