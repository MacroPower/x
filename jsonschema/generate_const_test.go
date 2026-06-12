package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
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

	s, err := jsonschema.GenerateFor[doc](t.Context())
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

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.Validate(t.Context(), map[string]any{"kind": nil}))
	assert.NoError(t, v.Validate(t.Context(), map[string]any{"kind": "a"}))
	assert.Error(t, v.Validate(t.Context(), map[string]any{"kind": "z"}))
}

// TestNullablePointerInterpreterEnumPermitsNull covers a nullable pointer field
// whose enum is set by a tag interpreter (the validate dialect) rather than the
// jsonschema struct tag. The interpreter receives the field schema, which for a
// pointer is the anyOf wrapper; the enum must land on the value branch only, so
// the permitted null stays valid. On the wrapper, enum (which tests the value
// regardless of type) would reject null.
func TestNullablePointerInterpreterEnumPermitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Color *string `json:"color,omitempty" validate:"oneof=red green"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The enum is on the anyOf value branch, not on the wrapper.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"color":{"anyOf":[{"type":"string","enum":["red","green"]},{"type":"null"}]}
		},
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.Validate(t.Context(), map[string]any{"color": nil}),
		"null is the value an omitempty pointer field permits")
	assert.NoError(t, v.Validate(t.Context(), map[string]any{"color": "red"}))
	assert.Error(t, v.Validate(t.Context(), map[string]any{"color": "purple"}))
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

	s, err := jsonschema.GenerateFor[doc](t.Context())
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

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"n":18446744073709551615}`)))
}

// TestTagScalarOutOfRange covers const, enum, and default tag values that lie
// outside the field's integer (or float32) range. Such a value is parsed at the
// field kind's bit size so it overflows and surfaces as a generation error,
// rather than producing a schema that accepts a value the Go type can never
// hold. This matters because an explicit const/enum drops the type-derived
// numeric bounds, so an unchecked out-of-range value would slip through.
func TestTagScalarOutOfRange(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"int8 const above max": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V int8 `json:"v" jsonschema:"const=200"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"int8 const below min": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V int8 `json:"v" jsonschema:"const=-200"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"uint8 enum above max": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V uint8 `json:"v" jsonschema:"enum=100|300"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"uint8 negative": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V uint8 `json:"v" jsonschema:"const=-1"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"int16 default above max": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V int16 `json:"v" jsonschema:"default=40000"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"int32 const above max": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V int32 `json:"v" jsonschema:"const=3000000000"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"uint16 const above max": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V uint16 `json:"v" jsonschema:"const=70000"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
		"float32 const overflow": {
			generate: func() (*jsonschema.Schema, error) {
				type doc struct {
					V float32 `json:"v" jsonschema:"const=1e40"`
				}

				return jsonschema.GenerateFor[doc](t.Context())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.generate()
			require.Error(t, err,
				"out-of-range tag scalar should be rejected at generation")
		})
	}
}

// TestTagScalarInRange confirms that in-range const/enum/default values still
// parse after the bit-size range check, including a value at the sized type's
// own boundary (where the type-derived bounds are dropped).
func TestTagScalarInRange(t *testing.T) {
	t.Parallel()

	type doc struct {
		A int8    `json:"a" jsonschema:"const=127"`
		B uint8   `json:"b" jsonschema:"enum=0|255"`
		C int16   `json:"c" jsonschema:"default=100"`
		D float32 `json:"d" jsonschema:"const=1.5"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The const/enum drop the type-derived numeric bounds, leaving just the
	// pinned value(s); the at-boundary const (int8=127, uint8=255) survives.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"a":{"type":"integer","const":127},
			"b":{"type":"integer","enum":[0,255]},
			"c":{"type":"integer","minimum":-32768,"maximum":32767,"default":100},
			"d":{"type":"number","const":1.5}
		},
		"required":["a","b","c","d"],
		"additionalProperties":false
	}`, string(got))

	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"a":127,"b":255,"c":5,"d":1.5}`)))
	assert.Error(t, v.ValidateJSON(t.Context(), []byte(`{"a":126,"b":255,"c":5,"d":1.5}`)),
		"const pins the value, so a different in-range integer is rejected")
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

				return jsonschema.GenerateFor[doc](t.Context())
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

				return jsonschema.GenerateFor[doc](t.Context())
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

				return jsonschema.GenerateFor[doc](t.Context())
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

				return jsonschema.GenerateFor[doc](t.Context())
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

				return jsonschema.GenerateFor[doc](t.Context())
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

			v, err := jsonschema.Compile(t.Context(), s)
			require.NoError(t, err)

			assert.NoError(t, v.ValidateJSON(t.Context(), []byte(tc.valid)),
				"schema must accept the string-encoded value")

			if tc.invalid != "" {
				assert.Error(t, v.ValidateJSON(t.Context(), []byte(tc.invalid)),
					"schema must reject the unquoted value")
			}
		})
	}
}
