package jsontag_test

import (
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

// unexportedTagged carries json tags on unexported non-embedded fields.
// Encoding/json/v2 skips such a field before reading its tag, so the tag
// cannot resurrect it, and reports the tag as an error; Parse must report the
// documented exclusion (empty Info) beside the same error.
type unexportedTagged struct {
	hidden  int `json:"h"`           //nolint:unused,govet,staticcheck // Intentional: tag on an unexported field, exercised via reflection.
	options int `json:"o,omitempty"` //nolint:unused,govet,staticcheck // Intentional: tag on an unexported field, exercised via reflection.

	Ok int `json:"ok"`
}

func TestParse_UnexportedTaggedFieldExcluded(t *testing.T) {
	t.Parallel()

	// Encoding/json/v2 refuses the struct outright for the same fault Parse
	// reports per field.
	_, err := json.Marshal(unexportedTagged{Ok: 2})
	require.ErrorContains(t, err, "unexported Go struct field hidden cannot have non-ignored")

	tests := map[string]struct {
		field int
		want  jsontag.Info
		err   string
	}{
		"tagged unexported": {
			field: 0, want: jsontag.Info{},
			err: "unexported Go struct field hidden cannot have non-ignored",
		},
		"tagged unexported with options": {
			field: 1, want: jsontag.Info{},
			err: "unexported Go struct field options cannot have non-ignored",
		},
		"tagged exported": {
			field: 2,
			want:  jsontag.Info{JSONName: "ok", TaggedName: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := reflect.TypeFor[unexportedTagged]().Field(tc.field)

			info, err := jsontag.Parse(f)
			assert.Equal(t, tc.want, info)

			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.err)
			}
		})
	}
}
