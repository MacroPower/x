package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

type (
	namedNames []string
	namedTable map[string]int
	namedBytes []byte
)

// TestGenerateFor_PointerToNamedContainerAdmitsNull pins the pointer
// occurrence null on a named slice, map, and byte slice. A named container
// is built bare for the cycle guard, and the withheld pointer occurrence has
// to be restored on the inline node, or the schema rejects the null the
// marshal writes for a nil pointer.
func TestGenerateFor_PointerToNamedContainerAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		PN *namedNames   `json:"pn"`
		PT *namedTable   `json:"pt"`
		PB *namedBytes   `json:"pb"`
		E  []*namedNames `json:"e"`
		N  namedNames    `json:"n"`
	}

	tests := map[string]struct {
		gen     []jsonschema.GenerateOption
		marshal []json.Options
	}{
		"defaults":        {},
		"definitions off": {gen: []jsonschema.GenerateOption{jsonschema.WithDefinitions(false)}},
		"nil slice as null": {
			gen:     []jsonschema.GenerateOption{jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true))},
			marshal: []json.Options{json.FormatNilSliceAsNull(true)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(), tc.gen...)
			require.NoError(t, err)

			for _, key := range []string{"pn", "pt", "pb"} {
				assert.Equal(t, []string{"null", propertyBase(t, s, key)}, s.Properties[key].Types,
					"property %q must admit the nil pointer's null", key)
			}

			assert.Equal(t, []string{"null", "array"}, s.Properties["e"].Items.Types,
				"a pointer element must admit null")

			data, err := json.Marshal(doc{E: []*namedNames{nil}}, tc.marshal...)
			require.NoError(t, err)
			require.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected the struct's own serialization: %s", data)
		})
	}
}

// propertyBase names the non-null type a nullable property schema carries.
func propertyBase(t *testing.T, s *jsonschema.Schema, key string) string {
	t.Helper()

	types := s.Properties[key].Types
	require.Len(t, types, 2, "property %q types: %v", key, types)

	for _, typ := range types {
		if typ != "null" {
			return typ
		}
	}

	t.Fatalf("property %q has no non-null type: %v", key, types)

	return ""
}
