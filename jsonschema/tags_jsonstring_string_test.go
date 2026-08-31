package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestGenerateFor_JSONStringStringFieldScalars pins the fate of the
// json:",string" string field: encoding/json/v2 stringifies numbers only, so
// the option on a string field is a SemanticError and generation refuses the
// type before any tag dialect could interpret its scalars. (V1 double-encoded
// the value instead; that behavior is gone.)
func TestGenerateFor_JSONStringStringFieldScalars(t *testing.T) {
	t.Parallel()

	t.Run("jsonschema const is refused with the field", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" jsonschema:"const=abc"`
		}

		_, err := jsonschema.GenerateFor[Form](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
		require.ErrorContains(t, err, "invalid use of `string` tag option")

		_, err = json.Marshal(Form{F: "abc"})
		require.Error(t, err, "encoding/json/v2 refuses the same declaration")
	})

	t.Run("validate eq is refused with the field", func(t *testing.T) {
		t.Parallel()

		type Form struct {
			F string `json:"f,string" validate:"eq=abc"`
		}

		_, err := jsonschema.GenerateFor[Form](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	})

	t.Run("pointer field is refused too", func(t *testing.T) {
		t.Parallel()

		// The flag survives the pointer level, so a *string under it is the
		// same refusal.
		type Form struct {
			F *string `json:"f,string" jsonschema:"const=abc"`
		}

		_, err := jsonschema.GenerateFor[Form](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
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
