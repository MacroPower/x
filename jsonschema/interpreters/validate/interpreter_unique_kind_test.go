package validate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestValidateInterpreter_UniqueOnUnhashableElements pins unique on a slice,
// array, or map whose elements a map cannot key. Go-playground keys a map by the element
// type to find a repeat and panics on a map, slice, or func element, behind
// a pointer or not, so the tag is refused rather than emitted as a
// uniqueItems go-playground could never check. The differential rig found
// the tag accepted here and refused there.
func TestValidateInterpreter_UniqueOnUnhashableElements(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value any
		want  error
	}{
		"slice of maps":            {value: struct{ V []map[string]int }{}, want: validate.ErrUniqueKind},
		"slice of slices":          {value: struct{ V [][]string }{}, want: validate.ErrUniqueKind},
		"slice of pointers to map": {value: struct{ V []*map[string]int }{}, want: validate.ErrUniqueKind},
		"array of slices":          {value: struct{ V [2][]int }{}, want: validate.ErrUniqueKind},
		"map of slices":            {value: struct{ V map[string][]string }{}, want: validate.ErrUniqueKind},
		"map of ints":              {value: struct{ V map[string]int }{}},
		"slice of strings":         {value: struct{ V []string }{}},
		"slice of pointers":        {value: struct{ V []*int }{}},
		"slice of structs":         {value: struct{ V []struct{ X int } }{}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typ := taggedField(t, tc.value, "unique")

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
