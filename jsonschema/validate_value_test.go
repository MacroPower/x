package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// valueConfig is the struct type the ValidateValue tests generate a schema
// for and validate instances of.
type valueConfig struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty" jsonschema:"minimum=1"`
}

func TestValidateValue(t *testing.T) {
	t.Parallel()

	schema := jsonschema.MustGenerateFor[valueConfig]()

	tests := map[string]struct {
		instance any
		err      string
	}{
		"conforming struct": {
			instance: valueConfig{Host: "localhost", Port: 8080},
		},
		"pointer to conforming struct": {
			instance: &valueConfig{Host: "localhost", Port: 8080},
		},
		"constraint violation": {
			instance: valueConfig{Host: "localhost", Port: -1},
			err:      "/port (minimum)",
		},
		"omitempty omits the violating zero": {
			// Port 0 violates minimum=1 but omitempty drops the key, so the
			// validated JSON form carries no port at all.
			instance: valueConfig{Host: "localhost"},
		},
		"map instance": {
			instance: map[string]any{"host": "localhost", "port": 8080.0},
		},
		"extra key on the marshaled form": {
			instance: map[string]any{"host": "localhost", "unknown": true},
			err:      "additionalProperties",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.ValidateValue(t.Context(), schema, tc.instance)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve, "a validation failure unwraps to *ValidationError")
		})
	}

	t.Run("unmarshalable value returns the marshal error", func(t *testing.T) {
		t.Parallel()

		err := jsonschema.ValidateValue(t.Context(), schema, make(chan int))
		require.Error(t, err)

		var ve *jsonschema.ValidationError

		assert.NotErrorAs(t, err, &ve, "a marshal failure is not a *ValidationError")
	})

	t.Run("compiled validator method", func(t *testing.T) {
		t.Parallel()

		v, err := jsonschema.Compile(t.Context(), schema)
		require.NoError(t, err)

		require.NoError(t, v.ValidateValue(t.Context(), valueConfig{Host: "localhost", Port: 8080}))

		err = v.ValidateValue(t.Context(), valueConfig{Host: "localhost", Port: -1})
		require.Error(t, err)

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)
		assert.Equal(t, "/port", ve.InstancePath)
	})

	t.Run("MarshalJSON form is what validates", func(t *testing.T) {
		t.Parallel()

		// The value marshals to a JSON number via its MarshalJSON, so the
		// string schema sees a number, not the Go value's container shape.
		s := &jsonschema.Schema{Type: "string"}

		err := jsonschema.ValidateValue(t.Context(), s, marshalsToNumber{})
		require.Error(t, err)

		var ve *jsonschema.ValidationError

		require.ErrorAs(t, err, &ve)
		assert.Equal(t, jsonschema.KeywordType, ve.Keyword)
	})
}

// marshalsToNumber marshals to the JSON number 42 regardless of its Go
// shape, exercising ValidateValue's MarshalJSON contract.
type marshalsToNumber struct{}

func (marshalsToNumber) MarshalJSON() ([]byte, error) {
	return []byte("42"), nil
}
