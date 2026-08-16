package jsonschema_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// hookAnyOfDefaults is generated through a WithTypeSchemaFor override whose
// schema already admits null and carries its own two-element anyOf. A nullable
// pointer root over it skips the generator's anyOf[value, null] wrapper (the
// base admits null already), so the surviving anyOf is the hook's own and
// WithDefaultsFrom must seed the root schema's properties, not a hook branch.
type hookAnyOfDefaults struct {
	A int `json:"a"`
}

func TestWithDefaultsFromNullAdmittingHookAnyOf(t *testing.T) {
	t.Parallel()

	minProps := 0
	maxProps := 10

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"hook anyOf kept unwrapped": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[hookAnyOfDefaults](jsonschema.TypeSchema{
					Value: &jsonschema.Schema{
						Types:      []string{"null", "object"},
						Properties: map[string]*jsonschema.Schema{"a": {Type: "integer"}},
						AnyOf: []*jsonschema.Schema{
							{MinProperties: &minProps},
							{MaxProperties: &maxProps},
						},
					},
				}),
			},
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":["null","object"],
				"properties":{"a":{"type":"integer","default":42}},
				"anyOf":[{"minProperties":0},{"maxProperties":10}]
			}`,
		},
		"generator null wrapper": {
			want: `{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"anyOf":[
					{
						"type":"object",
						"properties":{"a":{"type":"integer","default":42}},
						"required":["a"],
						"additionalProperties":false
					},
					{"type":"null"}
				]
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := append([]jsonschema.GenerateOption{
				jsonschema.WithDefinitions(false),
				jsonschema.WithDefaultsFrom(hookAnyOfDefaults{A: 42}),
			}, tc.opts...)

			s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[*hookAnyOfDefaults](), opts...)
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)

			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
