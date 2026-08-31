package validate_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateInterpreter_StringCoercedPointer pins that a pointer
// json:",string" field dispatches through the same coerced-form column as its
// non-pointer sibling: the coercion lives on the node's null-branch base
// rather than the payload, and the interpreter must still see it. Without
// that view the scalar rules compare native numbers against the quoted string
// the field serializes to, and the schema rejects the field's own marshaled
// output.
func TestValidateInterpreter_StringCoercedPointer(t *testing.T) {
	t.Parallel()

	t.Run("scalars serialize like the field", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			Eq *int `json:"eq,string" validate:"eq=5"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		five := 5
		data, err := json.Marshal(Form{Eq: &five})
		require.NoError(t, err)
		require.JSONEq(t, `{"eq":"5"}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must accept the field's own marshaled output")
		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"eq":null}`)),
			"a nil pointer marshals as null and stays admitted")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"eq":"6"}`)),
			"a different serialized value is rejected")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"eq":5}`)),
			"the unquoted number never occurs in marshaled output")
	})

	t.Run("bound is rejected like the non-pointer field", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			V *int `json:"v,string" validate:"min=3"`
		}

		s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
		require.Error(t, err,
			"a numeric bound is inert against the quoted string and must be rejected")
		assert.Contains(t, err.Error(), "not supported")
		assert.Nil(t, s)
	})
}
