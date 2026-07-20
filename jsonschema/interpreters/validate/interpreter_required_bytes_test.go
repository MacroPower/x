package validate_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_RequiredOnRawMessage pins that required on a
// json.RawMessage field adds only the parent's required entry. The built-in
// override for json.RawMessage is the unconstrained schema (any JSON value),
// so a kind-derived minItems: 1 would be inert for every instance type except
// arrays, where it falsely rejects the valid empty array: go-playground's
// required accepts a non-nil RawMessage holding "[]".
func TestValidateInterpreter_RequiredOnRawMessage(t *testing.T) {
	t.Parallel()

	type Doc struct {
		Raw json.RawMessage `json:"raw" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Doc](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Contains(t, s.Required, "raw")

	prop := s.Properties["raw"]
	require.NotNil(t, prop)
	assert.Nil(t, prop.MinItems, "no array size floor on an any-value schema")
	assert.Nil(t, prop.MinLength)

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"raw":[]}`)),
		"an empty JSON array is a valid RawMessage value")
	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"raw":{}}`)),
		"an empty JSON object is a valid RawMessage value")
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{}`)),
		"the field itself stays required")
}

// TestValidateInterpreter_RequiredOnByteSliceNoMinItems pins that required on
// a []byte field does not stamp minItems: the field marshals to a base64
// string, so an array size floor is inert against every instance the field
// produces (the same reason min/max/len/unique on []byte are rejected).
func TestValidateInterpreter_RequiredOnByteSliceNoMinItems(t *testing.T) {
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
	assert.Nil(t, prop.MinItems, "no array size floor on a base64 string schema")
}
