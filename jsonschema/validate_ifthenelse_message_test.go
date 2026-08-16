package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateIfThenElseMessages pins the then/else branch messages to the
// package's prevailing wording: the repo convention keeps "failed" and
// "error" out of library error messages, so the branch outcome is stated as a
// did-not-validate clause like the other applicator keywords.
func TestValidateIfThenElseMessages(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema   string
		instance string
		want     string
	}{
		"then branch": {
			schema:   `{"if": {"type": "integer"}, "then": {"minimum": 5}}`,
			instance: `1`,
			want:     "if condition was true but did not validate against then subschema",
		},
		"else branch": {
			schema:   `{"if": {"type": "integer"}, "else": {"maxLength": 0}}`,
			instance: `"x"`,
			want:     "if condition was false but did not validate against else subschema",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			require.NoError(t, err)

			err = v.ValidateJSON(t.Context(), []byte(tc.instance))
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "failed",
				`library error messages avoid the word "failed"`)
		})
	}
}
