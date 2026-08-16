package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateContentTagsIgnoredOnRawMessage pins that validate:"json" and
// validate:"base64" on a json.RawMessage field are documented no-ops rather
// than generation errors: both are real runtime checks over the raw bytes, but
// the content keywords describe a string carrying an encoded document, and a
// raw field's instance is already decoded JSON, so nothing faithful is emitted
// and nothing false either.
func TestValidateContentTagsIgnoredOnRawMessage(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() (*jsonschema.Schema, error){
		"json": func() (*jsonschema.Schema, error) {
			type T struct {
				V json.RawMessage `json:"v" validate:"json"`
			}

			return jsonschema.GenerateFor[T](t.Context(),
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		},
		"base64": func() (*jsonschema.Schema, error) {
			type T struct {
				V json.RawMessage `json:"v" validate:"base64"`
			}

			return jsonschema.GenerateFor[T](t.Context(),
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := build()
			require.NoError(t, err)

			v := s.Properties["v"]
			require.NotNil(t, v)
			assert.Empty(t, v.ContentMediaType, "no content keyword lands on the raw field")
			assert.Empty(t, v.ContentEncoding, "no content keyword lands on the raw field")
		})
	}
}
