package jsonschema_test

import (
	"encoding/json"
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

			assert.Equal(t, tc.want, jsonschema.IsFalseSchema(tc.schema))
		})
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
