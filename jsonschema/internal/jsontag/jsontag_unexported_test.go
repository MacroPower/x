package jsontag_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsontag"
)

// unexportedTagged carries json tags on unexported non-embedded fields.
// Encoding/json skips such a field before reading its tag, so the tag cannot
// resurrect it; Parse must report the documented exclusion (empty Info).
type unexportedTagged struct {
	hidden  int `json:"h"`           //nolint:unused,govet,staticcheck // Intentional: tag on an unexported field, exercised via reflection.
	options int `json:"o,omitempty"` //nolint:unused,govet,staticcheck // Intentional: tag on an unexported field, exercised via reflection.

	Ok int `json:"ok"`
}

func TestParse_UnexportedTaggedFieldExcluded(t *testing.T) {
	t.Parallel()

	// Encoding/json omits the tagged unexported fields entirely.
	data, err := json.Marshal(unexportedTagged{Ok: 2})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":2}`, string(data))

	tests := map[string]struct {
		field int
		want  jsontag.Info
	}{
		"tagged unexported":              {field: 0, want: jsontag.Info{}},
		"tagged unexported with options": {field: 1, want: jsontag.Info{}},
		"tagged exported": {
			field: 2,
			want:  jsontag.Info{JSONName: "ok", TaggedName: true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := reflect.TypeFor[unexportedTagged]().Field(tc.field)
			assert.Equal(t, tc.want, jsontag.Parse(f))
		})
	}
}
