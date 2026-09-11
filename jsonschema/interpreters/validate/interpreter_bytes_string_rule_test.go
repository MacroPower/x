package validate_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_ByteSliceStringRules pins the string validators on
// a []byte field to go-playground's reach. Go-playground runs a format or
// pattern validator over reflect's description of the slice, which no base64
// text equals, and panics on uri and url, so every one is refused; json
// reads the raw bytes, as contentMediaType reads the decoded content, so it
// stays. The differential rig found the uri case accepted here and refused
// there.
func TestValidateInterpreter_ByteSliceStringRules(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tag  string
		want error
	}{
		"uri":    {tag: "uri", want: validate.ErrStringRuleKind},
		"email":  {tag: "email", want: validate.ErrStringRuleKind},
		"alpha":  {tag: "alpha", want: validate.ErrStringRuleKind},
		"base64": {tag: "base64", want: validate.ErrStringRuleKind},
		"json":   {tag: "json"},
	}

	// The same reach holds a text-marshaling struct such as time.Time, whose
	// Go kind go-playground reads and whose json it panics on.
	timeTests := map[string]struct {
		tag  string
		want error
	}{
		"json on a time":  {tag: "json", want: validate.ErrStringRuleKind},
		"email on a time": {tag: "email", want: validate.ErrStringRuleKind},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			type doc struct {
				V []byte `json:"v" validate:"placeholder"`
			}

			typ := taggedField(t, doc{}, tc.tag)

			_, err := jsonschema.Generate(t.Context(), typ,
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
		})
	}

	// A byte array takes the same base64 form, but go-playground's isJSON
	// switches on the string and slice kinds alone and panics on it, so the
	// json exception does not reach it.
	t.Run("json on a byte array", func(t *testing.T) {
		t.Parallel()

		typ := taggedField(t, struct{ V [4]byte }{}, "json")

		_, err := jsonschema.Generate(t.Context(), typ,
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
		require.ErrorIs(t, err, validate.ErrStringRuleKind)
	})

	for name, tc := range timeTests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := taggedField(t, struct{ V time.Time }{}, tc.tag)

			_, err := jsonschema.Generate(t.Context(), typ,
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
			require.ErrorIs(t, err, tc.want)
		})
	}
}
