package jsonschema_test

import (
	"context"
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

	// The forbidNames type records NullForbidden through a method-based
	// extender, the registration path shouldExtract routes into $defs.
	forbidNames []string
	// The forbidCyc type holds itself, so its body always lands in $defs
	// through the cycle guard whatever WithDefinitions says.
	forbidCyc []forbidCyc
)

func (forbidNames) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Nullability = jsonschema.NullForbidden

	return nil
}

func (forbidCyc) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
	ts.Nullability = jsonschema.NullForbidden

	return nil
}

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

// TestGenerateFor_NullForbiddenNamedSliceUnderFormatNull pins that a
// NullForbidden stance clears the null FormatNilSliceAsNull folds into a
// named slice's body before that body is shared through $defs. The slice
// builder adds the format null on the bare build, and a reference carries
// the stance only in its own null decision, so without the veto an
// extracted or cyclic body admits the null the inline node refuses.
func TestGenerateFor_NullForbiddenNamedSliceUnderFormatNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		S forbidNames `json:"s"`
	}

	type cyc struct {
		C forbidCyc `json:"c"`
	}

	tests := map[string]struct {
		defs     bool
		cyclic   bool
		defName  string
		instance string
		null     string
	}{
		"definitions on": {
			defs: true, defName: "forbidNames", instance: `{"s":[]}`, null: `{"s":null}`,
		},
		"definitions off": {
			instance: `{"s":[]}`, null: `{"s":null}`,
		},
		"cyclic under definitions on": {
			defs: true, cyclic: true, defName: "forbidCyc", instance: `{"c":[[]]}`, null: `{"c":null}`,
		},
		"cyclic under definitions off": {
			cyclic: true, defName: "forbidCyc", instance: `{"c":[[]]}`, null: `{"c":null}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := []jsonschema.GenerateOption{
				jsonschema.WithDefinitions(tc.defs),
				jsonschema.WithJSONOptions(json.FormatNilSliceAsNull(true)),
			}

			var (
				s   *jsonschema.Schema
				err error
			)

			if tc.cyclic {
				s, err = jsonschema.GenerateFor[cyc](t.Context(), opts...)
			} else {
				s, err = jsonschema.GenerateFor[doc](t.Context(), opts...)
			}

			require.NoError(t, err)

			body := s.Properties["s"]
			if tc.defName != "" {
				require.Contains(t, s.Defs, tc.defName)

				body = s.Defs[tc.defName]
			}

			assert.Equal(t, "array", body.Type, "the shared body must carry no format null")
			assert.Empty(t, body.Types, "the shared body must carry no format null")

			require.NoError(t, validateJSON(t.Context(), s, []byte(tc.instance)))
			require.Error(t, validateJSON(t.Context(), s, []byte(tc.null)),
				"NullForbidden must reject the null the format option would admit")
		})
	}
}
