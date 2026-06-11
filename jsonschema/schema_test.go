package jsonschema_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestIsTrueSchema(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   bool
	}{
		"nil": {
			schema: nil,
			want:   false,
		},
		"zero schema": {
			schema: &jsonschema.Schema{},
			want:   true,
		},
		"annotation only": {
			schema: &jsonschema.Schema{Description: "anything"},
			want:   false,
		},
		"title only": {
			schema: &jsonschema.Schema{Title: "t"},
			want:   false,
		},
		"constraint": {
			schema: &jsonschema.Schema{Type: "string"},
			want:   false,
		},
		"false schema": {
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			want:   false,
		},
		"empty non-nil enum counts as set": {
			// Schema{Enum: []any{}} vacuously rejects every instance, so the
			// nil-versus-empty distinction matters: only nil is unset.
			schema: &jsonschema.Schema{Enum: []any{}},
			want:   false,
		},
		"empty non-nil properties counts as set": {
			schema: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{}},
			want:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, jsonschema.IsTrueSchema(tc.schema))
		})
	}
}

func TestIsFalseSchema(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   bool
	}{
		"nil": {
			schema: nil,
			want:   false,
		},
		"not of true schema": {
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{}},
			want:   true,
		},
		"zero schema": {
			schema: &jsonschema.Schema{},
			want:   false,
		},
		"non-empty not": {
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{Type: "string"}},
			want:   false,
		},
		"constraint sibling defeats the form": {
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{}, Type: "string"},
			want:   false,
		},
		"annotation sibling defeats the form": {
			// A title sibling makes the schema marshal to an object, not to
			// the JSON boolean false, so the strict form excludes it.
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{}, Title: "t"},
			want:   false,
		},
		"annotation inside not defeats the form": {
			schema: &jsonschema.Schema{Not: &jsonschema.Schema{Description: "d"}},
			want:   false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var before *jsonschema.Schema

			if tc.schema != nil {
				before = tc.schema.Not
			}

			assert.Equal(t, tc.want, jsonschema.IsFalseSchema(tc.schema))

			if tc.schema != nil {
				assert.Same(t, before, tc.schema.Not, "IsFalseSchema must not mutate the schema's Not pointer")
			}
		})
	}
}

// TestIsTrueSchemaRejectsEverySetField verifies IsTrueSchema consults every
// exported Schema field: a schema with only that field set to a non-zero
// value must not be the true schema. Maps and slices are planted non-nil but
// empty, pinning the strict nil-versus-empty semantics — Schema{Enum: []any{}}
// vacuously rejects every instance, so present-but-empty counts as set.
//
// Because the fields are enumerated by reflection, a new upstream Schema
// field automatically gains a case here, and IsTrueSchema's enumeration must
// be extended before it passes. This is the package's maintenance alarm for
// upstream field additions; see CLAUDE.md for the classifications to revisit
// when it fires.
func TestIsTrueSchemaRejectsEverySetField(t *testing.T) {
	t.Parallel()

	schemaType := reflect.TypeFor[jsonschema.Schema]()

	for i := range schemaType.NumField() {
		field := schemaType.Field(i)
		if !field.IsExported() {
			continue
		}

		t.Run(field.Name, func(t *testing.T) {
			t.Parallel()

			probe := &jsonschema.Schema{}
			fieldValue := reflect.ValueOf(probe).Elem().FieldByName(field.Name)

			switch field.Type.Kind() {
			case reflect.String:
				fieldValue.SetString("x")
			case reflect.Bool:
				fieldValue.SetBool(true)
			case reflect.Pointer:
				fieldValue.Set(reflect.New(field.Type.Elem()))
			case reflect.Map:
				fieldValue.Set(reflect.MakeMap(field.Type))
			case reflect.Slice:
				fieldValue.Set(reflect.MakeSlice(field.Type, 0, 0))
			default:
				t.Fatalf("Schema field %q has unhandled kind %s; extend this probe", field.Name, field.Type.Kind())
			}

			assert.False(t, jsonschema.IsTrueSchema(probe),
				"a schema with only %q set must not be the boolean true schema form", field.Name)
		})
	}
}

// jsonUntaggedFields allowlists exported Schema fields without a usable
// `json` struct tag for TestSchemaSerializableFieldCoverage. The upstream
// *Schema implements a custom MarshalJSON/UnmarshalJSON that renders these
// under their real keywords ("type", "items", "dependencies", arbitrary
// Extra keys), so a JSON round-trip still carries them. PropertyOrder is the
// lone exception: a render-only ordering hint the custom marshaler drops,
// which is acceptable because it carries no validation semantics. Each
// entry's value documents the reason.
var jsonUntaggedFields = map[string]string{
	"Type":              "custom MarshalJSON renders as \"type\"",
	"Types":             "custom MarshalJSON renders as \"type\" (array form)",
	"Items":             "custom MarshalJSON renders as \"items\"",
	"ItemsArray":        "custom MarshalJSON renders as \"items\" (array form)",
	"Extra":             "custom MarshalJSON inlines each key as a top-level keyword",
	"DependencySchemas": "custom MarshalJSON renders under \"dependencies\"",
	"DependencyStrings": "custom MarshalJSON renders under \"dependencies\"",
	"PropertyOrder":     "render-only ordering hint dropped by JSON round-trips (no validation semantics)",
}

// TestSchemaSerializableFieldCoverage is a maintenance guard over the JSON
// round-trip contract that the package's public behavior relies on:
// [jsonschema.ParseSchemaValue] converts documents through encoding/json, and
// [jsonschema.Inline] deep-copies documents the same way. Every exported
// Schema field must either carry a non-empty `json` struct tag (so a plain
// Marshal/Unmarshal cannot silently drop it) or be explicitly allowlisted in
// jsonUntaggedFields with a reason. When upstream adds a new exported field
// with `json:"-"` and no custom-marshaler coverage, this test fails and
// forces a maintainer to decide whether round-trips still carry it.
func TestSchemaSerializableFieldCoverage(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[jsonschema.Schema]()

	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		tag, ok := field.Tag.Lookup("json")
		if !ok || tag == "" || tag == "-" {
			reason, allowlisted := jsonUntaggedFields[field.Name]
			require.True(t, allowlisted,
				"exported Schema field %q has no usable json tag (%q) and is not allowlisted; "+
					"classify it: either it round-trips via the custom marshaler or JSON round-trips drop it",
				field.Name, tag)
			assert.NotEmpty(t, reason,
				"allowlist entry for %q must document why the missing json tag is acceptable", field.Name)

			continue
		}

		// A tagged field carries at least a key (e.g. "minimum,omitempty"); the
		// leading segment before any comma must be non-empty.
		name, _, _ := strings.Cut(tag, ",")
		assert.NotEmpty(t, name, "exported Schema field %q has a json tag with an empty name: %q", field.Name, tag)
	}
}

// TestBoolSchemaPredicatesMatchJSONForms ties the predicates to the upstream
// JSON representation: a parsed JSON true or false schema satisfies the
// matching predicate, and the recognized shapes marshal back to the JSON
// booleans.
func TestBoolSchemaPredicatesMatchJSONForms(t *testing.T) {
	t.Parallel()

	t.Run("unmarshaled true", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{}
		require.NoError(t, json.Unmarshal([]byte(`true`), s))

		assert.True(t, jsonschema.IsTrueSchema(s))
		assert.False(t, jsonschema.IsFalseSchema(s))
	})

	t.Run("unmarshaled false", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{}
		require.NoError(t, json.Unmarshal([]byte(`false`), s))

		assert.True(t, jsonschema.IsFalseSchema(s))
		assert.False(t, jsonschema.IsTrueSchema(s))
	})

	t.Run("true schema marshals to true", func(t *testing.T) {
		t.Parallel()

		data, err := json.Marshal(&jsonschema.Schema{})
		require.NoError(t, err)
		assert.JSONEq(t, `true`, string(data))
	})

	t.Run("false schema marshals to false", func(t *testing.T) {
		t.Parallel()

		data, err := json.Marshal(&jsonschema.Schema{Not: &jsonschema.Schema{}})
		require.NoError(t, err)
		assert.JSONEq(t, `false`, string(data))
	})
}

func TestRaw(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		v    any
		want string
		err  bool
	}{
		"string": {
			v:    "15m",
			want: `"15m"`,
		},
		"int": {
			v:    42,
			want: `42`,
		},
		"bool": {
			v:    true,
			want: `true`,
		},
		"nil": {
			v:    nil,
			want: `null`,
		},
		"map": {
			v:    map[string]any{"replicas": 3},
			want: `{"replicas":3}`,
		},
		"slice": {
			v:    []string{"a", "b"},
			want: `["a","b"]`,
		},
		"unmarshalable": {
			v:   make(chan int),
			err: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := jsonschema.Raw(tc.v)
			if tc.err {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestMustRaw(t *testing.T) {
	t.Parallel()

	t.Run("valid value", func(t *testing.T) {
		t.Parallel()

		s := &jsonschema.Schema{
			Type:    "string",
			Default: jsonschema.MustRaw("15m"),
		}

		data, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{"type":"string","default":"15m"}`, string(data))
	})

	t.Run("panics on marshal error", func(t *testing.T) {
		t.Parallel()

		assert.Panics(t, func() {
			jsonschema.MustRaw(make(chan int))
		})
	})
}
