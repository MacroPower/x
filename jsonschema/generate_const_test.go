package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

// TestNullablePointerEnumPermitsNull covers a nullable pointer field carrying an
// enum tag: the enum must constrain the value branch only, leaving null valid.
// On the wrapper, enum (which tests the value regardless of type) would reject
// the permitted null.
func TestNullablePointerEnumPermitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Kind *string `json:"kind,omitempty" jsonschema:"enum=a|b"`
	}

	s, err := jsonschema.GenerateFor[doc]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"kind":{"anyOf":[{"type":"string","enum":["a","b"]},{"type":"null"}]}
		},
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.Validate(map[string]any{"kind": nil}))
	assert.NoError(t, v.Validate(map[string]any{"kind": "a"}))
	assert.Error(t, v.Validate(map[string]any{"kind": "z"}))
}

// TestIntegerConstAtTypeBoundary covers a const set to a sized integer type's
// own maximum: the type-derived maximum rounds that boundary down to stay
// representable as float64, so it must be dropped or the schema would reject its
// own const, accepting nothing.
func TestIntegerConstAtTypeBoundary(t *testing.T) {
	t.Parallel()

	type doc struct {
		N uint64 `json:"n" jsonschema:"const=18446744073709551615"`
	}

	s, err := jsonschema.GenerateFor[doc]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"n":{"type":"integer","const":18446744073709551615}
		},
		"required":["n"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON([]byte(`{"n":18446744073709551615}`)))
}

// TestJSONStringTagScalarsParseAsStrings covers const, enum, and default tags on
// json:",string" fields. The override coerces the field schema to type string,
// and encoding/json also serializes the value as a quoted string, so the tag's
// scalar values must be parsed as strings. Parsing them against the original Go
// type would yield numbers/booleans the string-encoded instance can never match.
func TestJSONStringTagScalarsParseAsStrings(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		field    string
		want     string // the field schema
		valid    string // serialized instance the schema must accept
		invalid  string // serialized instance the schema must reject
	}{
		"int const": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					N int `json:"n,string" jsonschema:"const=5"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			field:   "n",
			want:    `{"type":"string","const":"5"}`,
			valid:   `{"n":"5"}`,
			invalid: `{"n":5}`,
		},
		"int enum": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					N int `json:"n,string" jsonschema:"enum=1|2|3"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			field:   "n",
			want:    `{"type":"string","enum":["1","2","3"]}`,
			valid:   `{"n":"2"}`,
			invalid: `{"n":2}`,
		},
		"int default": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					N int `json:"n,string" jsonschema:"default=7"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			field:   "n",
			want:    `{"type":"string","default":"7"}`,
			valid:   `{"n":"7"}`,
			invalid: ``, // default does not constrain the instance
		},
		"bool const": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					B bool `json:"b,string" jsonschema:"const=true"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			field:   "b",
			want:    `{"type":"string","const":"true"}`,
			valid:   `{"b":"true"}`,
			invalid: `{"b":true}`,
		},
		"bool enum": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					B bool `json:"b,string" jsonschema:"enum=true|false"`
				}

				return jsonschema.GenerateFor[doc]()
			},
			field:   "b",
			want:    `{"type":"string","enum":["true","false"]}`,
			valid:   `{"b":"false"}`,
			invalid: `{"b":false}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s.Properties[tc.field])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))

			v, err := jsonschema.Compile(s)
			require.NoError(t, err)

			assert.NoError(t, v.ValidateJSON([]byte(tc.valid)),
				"schema must accept the string-encoded value")
			if tc.invalid != "" {
				assert.Error(t, v.ValidateJSON([]byte(tc.invalid)),
					"schema must reject the unquoted value")
			}
		})
	}
}
