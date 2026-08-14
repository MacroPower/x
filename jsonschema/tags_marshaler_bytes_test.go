package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// byteLevel is a uint8 element that marshals itself as text, so a []byteLevel
// marshals as a real JSON array of strings, not a base64 string. The shape
// classifier must apply the same marshaler exemption the generator does, or
// the slice classifies as raw bytes and array constraints are rejected.
type byteLevel uint8

func (l byteLevel) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "L%d", int(l)), nil
}

// TestTagsMarshalerByteSlice pins that a slice of marshaler-bearing uint8
// elements takes array-family constraints in both dialects, exactly like
// []string: the generator already emits an array schema for it, so the tag
// model's byte-slice short-circuit must honor the same encoding/json
// exemption instead of classifying the field as raw bytes.
func TestTagsMarshalerByteSlice(t *testing.T) {
	t.Parallel()

	t.Run("jsonschema tag minItems", func(t *testing.T) {
		t.Parallel()

		type doc struct {
			Levels []byteLevel `json:"levels" jsonschema:"minItems=1"`
		}

		s, err := jsonschema.GenerateFor[doc](t.Context())
		require.NoError(t, err)

		prop := s.Properties["levels"]
		require.NotNil(t, prop.MinItems)
		assert.Equal(t, 1, *prop.MinItems)
		assert.Equal(t, []string{"null", "array"}, prop.Types)
	})

	t.Run("validate tag parity with string slice", func(t *testing.T) {
		t.Parallel()

		type levelsDoc struct {
			V []byteLevel `json:"v" validate:"required,unique"`
		}

		type stringsDoc struct {
			V []string `json:"v" validate:"required,unique"`
		}

		opts := []jsonschema.GenerateOption{
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
		}

		fromLevels, err := jsonschema.GenerateFor[levelsDoc](t.Context(), opts...)
		require.NoError(t, err)

		fromStrings, err := jsonschema.GenerateFor[stringsDoc](t.Context(), opts...)
		require.NoError(t, err)

		// Both slices generate the identical array-of-strings schema, so the
		// identical tag must land the identical constraints.
		got, err := json.Marshal(fromLevels.Properties["v"])
		require.NoError(t, err)

		want, err := json.Marshal(fromStrings.Properties["v"])
		require.NoError(t, err)

		assert.JSONEq(t, string(want), string(got))
	})
}
