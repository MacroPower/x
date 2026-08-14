package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestGenerateFor_JSONStringStringFieldScalars pins the double-encoding a
// json:",string" string field performs: encoding/json encodes the
// already-encoded string a second time, so the value abc marshals as the JSON
// string "\"abc\"". Scalar literals from either tag dialect must serialize the
// same way, or the schema pins a const the field's own output never satisfies.
func TestGenerateFor_JSONStringStringFieldScalars(t *testing.T) {
	t.Parallel()

	t.Run("jsonschema const serializes double-encoded", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" jsonschema:"const=abc"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context())
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(Form{F: "abc"})
		require.NoError(t, err)
		require.JSONEq(t, `{"f":"\"abc\""}`, string(data),
			"encoding/json double-encodes a json:\",string\" string field")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must accept the field's own marshaled output")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"f":"abc"}`)),
			"the unquoted text never occurs in marshaled output")
	})

	t.Run("validate eq serializes double-encoded", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" validate:"eq=abc"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(Form{F: "abc"})
		require.NoError(t, err)

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must accept the field's own marshaled output")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"f":"abc"}`)),
			"the unquoted text never occurs in marshaled output")
	})

	t.Run("pointer field keeps the coercion and null", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F *string `json:"f,string" jsonschema:"const=abc"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context())
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		abc := "abc"
		data, err := json.Marshal(Form{F: &abc})
		require.NoError(t, err)
		require.JSONEq(t, `{"f":"\"abc\""}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must accept the field's own marshaled output")
		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"f":null}`)),
			"a nil pointer marshals as null and stays admitted")
	})

	t.Run("string keywords are rejected rather than mis-anchored", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" jsonschema:"format=email"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context())
		require.Error(t, err,
			"a format would assert against the quoted serialized text, not the value")
		assert.Contains(t, err.Error(), "not supported")
		assert.Nil(t, s)
	})

	t.Run("length bounds are rejected rather than mis-measured", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" jsonschema:"minLength=3"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context())
		require.Error(t, err,
			"a length bound would measure the quoted, escaped text, not the value")
		assert.Contains(t, err.Error(), "not supported")
		assert.Nil(t, s)
	})

	t.Run("plain string field is unaffected", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f" jsonschema:"const=abc"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context())
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"f":"abc"}`)))
	})
}
