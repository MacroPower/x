package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestCompileMultipleOfUnderflow locks in the float64-precision contract for a
// multipleOf literal below the smallest positive float64: the authored value
// is spec-valid (strictly greater than zero) but underflows to zero when the
// document is decoded, so the parse drops the keyword instead of letting the
// domain vet report the underflowed zero as an authored one. An authored zero
// and a negative underflowing literal stay rejected with
// [jsonschema.ErrNonPositiveMultipleOf].
func TestCompileMultipleOfUnderflow(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  string
		valid   []string
		invalid []string
		err     error
	}{
		"positive underflowing literal compiles and asserts nothing": {
			schema: `{"multipleOf": 1e-400}`,
			valid:  []string{`7`, `0.3`, `"s"`},
		},
		"nested positive underflowing literal compiles": {
			schema: `{"properties": {"p": {"multipleOf": 1e-400}}}`,
			valid:  []string{`{"p": 0.3}`},
		},
		"authored zero stays rejected": {
			schema: `{"multipleOf": 0}`,
			err:    jsonschema.ErrNonPositiveMultipleOf,
		},
		"negative underflowing literal stays rejected": {
			schema: `{"multipleOf": -1e-400}`,
			err:    jsonschema.ErrNonPositiveMultipleOf,
		},
		"representable literal keeps asserting": {
			schema:  `{"multipleOf": 0.5}`,
			valid:   []string{`1.5`},
			invalid: []string{`0.3`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.CompileJSON(t.Context(), []byte(tc.schema))
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err, "a spec-valid multipleOf literal must compile")

			for _, instance := range tc.valid {
				require.NoError(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must validate", instance)
			}

			for _, instance := range tc.invalid {
				require.Error(t, v.ValidateJSON(t.Context(), []byte(instance)),
					"instance %s must not validate", instance)
			}
		})
	}
}
