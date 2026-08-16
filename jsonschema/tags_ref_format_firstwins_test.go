package jsonschema_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// refFormatName is a named string type declaring no format of its own, the
// control case for the first-wins gate below.
type refFormatName string

// TestInterpreterFormatDefersToRefDeclared pins the first-wins contract through
// a $defs-extracted type: an interpreter's inferred format never overrides --
// or conjoins with -- one the referenced definition declares outright. Before
// the ref read-through, validate:"email" on a time.Time field landed format:
// "email" as a $ref sibling beside the definition's date-time, and the two
// asserted conjunctively, so the field rejected the very RFC 3339 text it
// marshals.
func TestInterpreterFormatDefersToRefDeclared(t *testing.T) {
	t.Parallel()

	t.Run("declared format wins through the ref", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V time.Time `json:"v" validate:"email"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		require.NoError(t, err)

		v := s.Properties["v"]
		assert.Empty(t, v.Format, "the inferred format defers to the definition's date-time")
		assert.NotEmpty(t, v.Ref)

		compiled, err := jsonschema.Compile(t.Context(), s, jsonschema.WithFormats(true))
		require.NoError(t, err)

		instance := map[string]any{"v": time.Now().UTC().Format(time.RFC3339)}
		require.NoError(t, compiled.Validate(t.Context(), instance),
			"the text the field marshals validates against its own schema")
	})

	t.Run("inferred format applies where the type declares none", func(t *testing.T) {
		t.Parallel()

		type T struct {
			V refFormatName `json:"v" validate:"email"`
		}

		s, err := jsonschema.GenerateFor[T](t.Context(),
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		require.NoError(t, err)

		assert.Equal(t, "email", s.Properties["v"].Format)
	})
}
