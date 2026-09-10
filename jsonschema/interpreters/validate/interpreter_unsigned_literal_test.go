package validate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_UnsignedMinusLiteral pins a minus sign on an
// unsigned kind to go-playground's parser, which reads every numeric
// parameter there through strconv.ParseUint and refuses the sign. The shared
// literal grammar reads -0 as zero, so without the check lt=-0 on a uint64
// emitted a bound go-playground panics on; the differential rig found it.
func TestValidateInterpreter_UnsignedMinusLiteral(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value any
		tag   string
		want  error
	}{
		"bound":         {value: struct{ V uint64 }{}, tag: "lt=-0", want: validate.ErrUnsignedLiteral},
		"value":         {value: struct{ V uint8 }{}, tag: "eq=-0", want: validate.ErrUnsignedLiteral},
		"oneof token":   {value: struct{ V uint }{}, tag: "oneof=1 -0", want: validate.ErrUnsignedLiteral},
		"behind a dive": {value: struct{ V []uint16 }{}, tag: "dive,min=-0", want: validate.ErrUnsignedLiteral},
		"coerced": {value: struct {
			V uint32 `json:",string"`
		}{}, tag: "eq=-0", want: validate.ErrUnsignedLiteral},
		"plain zero":               {value: struct{ V uint64 }{}, tag: "lt=0"},
		"signed minus":             {value: struct{ V int64 }{}, tag: "lt=-0"},
		"slice of unsigned length": {value: struct{ V []uint64 }{}, tag: "min=0"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := taggedField(t, tc.value, tc.tag)

			_, err := jsonschema.Generate(t.Context(), typ,
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
		})
	}
}
