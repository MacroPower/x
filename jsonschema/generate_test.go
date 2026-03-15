package jsonschema_test

import (
	"encoding/json"
	"log/slog"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
	"go.jacobcolvin.com/jsonschema/internal/testtypes/alpha"
	"go.jacobcolvin.com/jsonschema/internal/testtypes/beta"
	"go.jacobcolvin.com/jsonschema/interpreters/validate"
)

// marshalSchema marshals a schema to a JSON string for comparison.
func marshalSchema(t *testing.T, s *jsonschema.Schema) string {
	t.Helper()

	b, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)

	return string(b)
}

func TestGenerateFor_PrimitiveTypes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"string": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[string]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`,
		},
		"bool": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[bool]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"boolean"}`,
		},
		"int": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[int]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer"}`,
		},
		"int8": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[int8]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer","minimum":-128,"maximum":127}`,
		},
		"uint16": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[uint16]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer","minimum":0,"maximum":65535}`,
		},
		"float64": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[float64]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"number"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestGenerateFor_Pointer(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[*string]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","anyOf":[{"type":"string"},{"type":"null"}]}`,
		string(got),
	)
}

func TestGenerateFor_Slice(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[]int]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":["null","array"],
		"items":{"type":"integer"}
	}`, string(got))
}

func TestGenerateFor_Array(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[3]string]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"array",
		"prefixItems":[{"type":"string"},{"type":"string"},{"type":"string"}],
		"minItems":3,
		"maxItems":3
	}`, string(got))
}

func TestGenerateFor_Map(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[map[string]int]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":["null","object"],
		"additionalProperties":{"type":"integer"}
	}`, string(got))
}

func TestGenerateFor_Interface(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[any]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`, string(got))
}

type SimpleStruct struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age,omitempty"`
}

func TestGenerateFor_SimpleStruct(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[SimpleStruct]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"email":{"type":"string"},
			"age":{"type":"integer"}
		},
		"required":["name","email"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_BuiltinOverrides(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"time.Time": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[time.Time]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","format":"date-time"}`,
		},
		"url.URL": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[url.URL]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","format":"uri"}`,
		},
		"json.RawMessage": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[json.RawMessage]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`,
		},
		"json.Number": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[json.Number]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"number"}`,
		},
		"[]byte": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[[]byte]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":["null","string"],"contentEncoding":"base64"}`,
		},
		"big.Int": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Int]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","pattern":"^-?[0-9]+$"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestGenerateFor_UnsupportedTypes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		t   reflect.Type
		err error
	}{
		"func": {
			t:   reflect.TypeFor[func()](),
			err: jsonschema.ErrUnsupportedType,
		},
		"chan": {
			t:   reflect.TypeFor[chan int](),
			err: jsonschema.ErrUnsupportedType,
		},
		"complex128": {
			t:   reflect.TypeFor[complex128](),
			err: jsonschema.ErrUnsupportedType,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Generate(tc.t)
			require.ErrorIs(t, err, tc.err)
		})
	}
}

func TestGenerateFor_UnsupportedMapKey(t *testing.T) {
	t.Parallel()

	type BadMap map[float64]string

	_, err := jsonschema.GenerateFor[BadMap]()
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedMapKey)
}

func TestGenerateFor_Draft7(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[SimpleStruct](jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)
	assert.Equal(t, "http://json-schema.org/draft-07/schema#", s.Schema)
}

type Address struct {
	City  string `json:"city"`
	State string `json:"state"`
}

type UserWithAddress struct {
	Name string   `json:"name"`
	Home Address  `json:"home"`
	Work *Address `json:"work,omitempty"`
}

func TestGenerateFor_DefsAndRef(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[UserWithAddress]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Address should be in $defs, home should be $ref, work should be anyOf with null.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"home":{"$ref":"#/$defs/Address"},
			"work":{
				"anyOf":[
					{"$ref":"#/$defs/Address"},
					{"type":"null"}
				]
			}
		},
		"required":["name","home"],
		"additionalProperties":false,
		"$defs":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_Draft7DefsAndRef(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[UserWithAddress](jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Should use "definitions" and "#/definitions/" for Draft-07.
	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"home":{"$ref":"#/definitions/Address"},
			"work":{
				"anyOf":[
					{"$ref":"#/definitions/Address"},
					{"type":"null"}
				]
			}
		},
		"required":["name","home"],
		"additionalProperties":false,
		"definitions":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_WithDefinitionsFalse(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[UserWithAddress](jsonschema.WithDefinitions(false))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Address should be inlined, no $defs.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"home":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			},
			"work":{
				"anyOf":[
					{
						"type":"object",
						"properties":{
							"city":{"type":"string"},
							"state":{"type":"string"}
						},
						"required":["city","state"],
						"additionalProperties":false
					},
					{"type":"null"}
				]
			}
		},
		"required":["name","home"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_WithAdditionalProperties(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[SimpleStruct](jsonschema.WithAdditionalProperties(true))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// No additionalProperties key.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"email":{"type":"string"},
			"age":{"type":"integer"}
		},
		"required":["name","email"]
	}`, string(got))
}

// TextMarshaler type.
type MyEnum int

func (MyEnum) MarshalText() ([]byte, error) { return nil, nil }

func TestGenerateFor_TextMarshaler(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[MyEnum]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`, string(got))
}

// JSONSchemaProvider type.
type Status string

func (Status) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Enum: []any{"active", "inactive", "suspended"},
	}
}

func TestGenerateFor_JSONSchemaProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[Status]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"string",
		"enum":["active","inactive","suspended"]
	}`, string(got))
}

// JSONSchemaExtender type.
type Metadata struct {
	Tags map[string]string `json:"tags"`
}

func (Metadata) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Description = "Arbitrary key-value metadata"
	s.MinProperties = jsonschema.Ptr(1)
}

func TestGenerateFor_JSONSchemaExtender(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[Metadata]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"description":"Arbitrary key-value metadata",
		"properties":{
			"tags":{
				"type":["null","object"],
				"additionalProperties":{"type":"string"}
			}
		},
		"required":["tags"],
		"additionalProperties":false,
		"minProperties":1
	}`, string(got))
}

func TestGenerateFor_WithTypeSchema(t *testing.T) {
	t.Parallel()

	override := &jsonschema.Schema{
		Type:   "string",
		Format: "date",
	}
	s, err := jsonschema.GenerateFor[time.Time](
		jsonschema.WithTypeSchema(reflect.TypeFor[time.Time](), override),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"string",
		"format":"date"
	}`, string(got))
}

func TestGenerateFor_JsonStringTag(t *testing.T) {
	t.Parallel()

	type Config struct {
		Port    int    `json:"port,string"`
		Enabled bool   `json:"enabled,string"`
		Name    string `json:"name,string"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"port":{"type":"string"},
			"enabled":{"type":"string"},
			"name":{"type":"string"}
		},
		"required":["port","enabled","name"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_OmitzeroTag(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name  string `json:"name"`
		Value int    `json:"value,omitzero"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"value":{"type":"integer"}
		},
		"required":["name"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_JSONTagDash(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name   string `json:"name"`
		Hidden string `json:"-"`
		DashID string `json:"-,"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"-":{"type":"string"}
		},
		"required":["name","-"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_EmptyStruct(t *testing.T) {
	t.Parallel()

	type Empty struct{}

	s, err := jsonschema.GenerateFor[Empty]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"additionalProperties":false
	}`, string(got))
}

type RecursiveNode struct {
	Name     string           `json:"name"`
	Children []*RecursiveNode `json:"children,omitempty"`
}

func TestGenerateFor_RecursiveType(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[RecursiveNode]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The root should be $ref to $defs/RecursiveNode.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$ref":"#/$defs/RecursiveNode",
		"$defs":{
			"RecursiveNode":{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"children":{
						"type":["null","array"],
						"items":{
							"anyOf":[
								{"$ref":"#/$defs/RecursiveNode"},
								{"type":"null"}
							]
						}
					}
				},
				"required":["name"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_IntegerMapKeys(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[map[int]string]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":["null","object"],
		"additionalProperties":{"type":"string"}
	}`, string(got))
}

func TestGenerateFor_PointerToUnrestricted(t *testing.T) {
	t.Parallel()

	// *interface{} → {}.
	s, err := jsonschema.Generate(reflect.TypeFor[*any]())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`, string(got))
}

func TestGenerateFor_NullableByteSlice(t *testing.T) {
	t.Parallel()

	type Msg struct {
		Data []byte `json:"data"`
	}

	s, err := jsonschema.GenerateFor[Msg]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"data":{
				"type":["null","string"],
				"contentEncoding":"base64"
			}
		},
		"required":["data"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_WithNamer(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[UserWithAddress](
		jsonschema.WithNamer(func(t reflect.Type) string {
			return "custom_" + t.Name()
		}),
	)
	require.NoError(t, err)
	// Check that the custom namer was used.
	assert.NotNil(t, s.Defs["custom_Address"])
}

func TestGenerateFor_ValidateInterpreter(t *testing.T) {
	t.Parallel()

	type CreateUser struct {
		Name  string `json:"name"  validate:"required,min=1,max=100"`
		Email string `json:"email" validate:"required,email"`
		Age   int    `json:"age"   validate:"gte=0,lte=150"`
	}

	s, err := jsonschema.GenerateFor[CreateUser](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{
				"type":"string",
				"minLength":1,
				"maxLength":100
			},
			"email":{
				"type":"string",
				"minLength":1,
				"format":"email"
			},
			"age":{
				"type":"integer",
				"minimum":0,
				"maximum":150
			}
		},
		"required":["name","email","age"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_JSONSchemaTag_KeyValue(t *testing.T) {
	t.Parallel()

	type Config struct {
		Port    int    `json:"port"    jsonschema:"description=Server port,minimum=1,maximum=65535"`
		Pattern string `json:"pattern" jsonschema:"pattern=^[a-z]+$"`
		Mode    string `json:"mode"    jsonschema:"enum=debug|release|test"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"port":{
				"type":"integer",
				"description":"Server port",
				"minimum":1,
				"maximum":65535
			},
			"pattern":{
				"type":"string",
				"pattern":"^[a-z]+$"
			},
			"mode":{
				"type":"string",
				"enum":["debug","release","test"]
			}
		},
		"required":["port","pattern","mode"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_JSONSchemaTag_BareDescription(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name string `json:"name" jsonschema:"The user's display name"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{
				"type":"string",
				"description":"The user's display name"
			}
		},
		"required":["name"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_JSONSchemaTag_DescriptionWithEqualsAfterSpace(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name string `json:"name" jsonschema:"Formula: x=y"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)
	require.NotNil(t, s.Properties, "schema: %s", marshalSchema(t, s))
	require.NotNil(t, s.Properties["name"], "properties: %v", s.Properties)
	assert.Equal(t, "Formula: x=y", s.Properties["name"].Description)
}

func TestGenerateFor_JSONSchemaTag_UnrecognizedKey(t *testing.T) {
	t.Parallel()

	type Config struct {
		Name string `json:"name" jsonschema:"descrption=typo"`
	}

	_, err := jsonschema.GenerateFor[Config]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key")
}

func TestGenerateFor_MultiplePointerIndirection(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.Generate(reflect.TypeFor[**string]())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","anyOf":[{"type":"string"},{"type":"null"}]}`,
		string(got),
	)
}

func TestGenerateFor_MapNullable(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[map[string]string]()
	require.NoError(t, err)
	// Maps should be nullable.
	assert.Equal(t, []string{"null", "object"}, s.Types)
}

func TestGenerateFor_SliceNullable(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[]string]()
	require.NoError(t, err)
	// Slices should be nullable.
	assert.Equal(t, []string{"null", "array"}, s.Types)
}

func TestGenerateFor_Draft7_RefWithAnnotation(t *testing.T) {
	t.Parallel()

	type Container struct {
		Home Address `json:"home" jsonschema:"description=Home address"`
	}

	s, err := jsonschema.GenerateFor[Container](jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// In Draft-07, $ref with annotations should be wrapped in allOf.
	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"home":{
				"allOf":[{"$ref":"#/definitions/Address"}],
				"description":"Home address"
			}
		},
		"required":["home"],
		"additionalProperties":false,
		"definitions":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_Draft2020_RefWithAnnotation(t *testing.T) {
	t.Parallel()

	type Container struct {
		Home Address `json:"home" jsonschema:"description=Home address"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// In Draft 2020-12, $ref siblings are allowed.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"home":{
				"$ref":"#/$defs/Address",
				"description":"Home address"
			}
		},
		"required":["home"],
		"additionalProperties":false,
		"$defs":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_PropertyOrder(t *testing.T) {
	t.Parallel()

	type Ordered struct {
		First  string `json:"first"`
		Second int    `json:"second"`
		Third  bool   `json:"third"`
	}

	s, err := jsonschema.GenerateFor[Ordered]()
	require.NoError(t, err)

	// PropertyOrder should match Go struct field order.
	assert.Equal(t, []string{"first", "second", "third"}, s.PropertyOrder)
}

func TestGenerateFor_JSONSchemaTag_Default(t *testing.T) {
	t.Parallel()

	type Config struct {
		Port int    `json:"port" jsonschema:"default=8080"`
		Host string `json:"host" jsonschema:"default=localhost"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	assert.JSONEq(t, `8080`, string(s.Properties["port"].Default))
	assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
}

func TestGenerateFor_JSONSchemaTag_Const(t *testing.T) {
	t.Parallel()

	type Config struct {
		Version string `json:"version" jsonschema:"const=v1"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	constVal := *s.Properties["version"].Const
	assert.Equal(t, "v1", constVal)
}

// NonStructExtender is a named non-struct type implementing JSONSchemaExtender.
type NonStructExtender []string

func (NonStructExtender) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Description = "A list of tags"
}

func TestGenerateFor_NonStructExtender(t *testing.T) {
	t.Parallel()

	// Non-struct types implementing JSONSchemaExtender should have
	// the extender called and be extracted to $defs.
	type Container struct {
		Tags NonStructExtender `json:"tags"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"tags":{
				"$ref":"#/$defs/NonStructExtender"
			}
		},
		"required":["tags"],
		"additionalProperties":false,
		"$defs":{
			"NonStructExtender":{
				"type":["null","array"],
				"items":{"type":"string"},
				"description":"A list of tags"
			}
		}
	}`, string(got))
}

func TestGenerateFor_NonStructExtender_Root(t *testing.T) {
	t.Parallel()

	// Non-struct type with JSONSchemaExtender used as root type.
	s, err := jsonschema.GenerateFor[NonStructExtender]()
	require.NoError(t, err)

	assert.Equal(t, "A list of tags", s.Description)
	assert.Equal(t, []string{"null", "array"}, s.Types)
}

// NonStructProvider is a named non-struct type implementing JSONSchemaProvider.
type NonStructProvider int

func (NonStructProvider) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "integer",
		Enum: []any{1, 2, 3},
	}
}

func TestGenerateFor_NonStructProvider_ExtractedToDefs(t *testing.T) {
	t.Parallel()

	type Container struct {
		Level NonStructProvider `json:"level"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// NonStructProvider implements JSONSchemaProvider, so it should be extracted
	// to $defs and referenced via $ref.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"level":{"$ref":"#/$defs/NonStructProvider"}
		},
		"required":["level"],
		"additionalProperties":false,
		"$defs":{
			"NonStructProvider":{
				"type":"integer",
				"enum":[1,2,3]
			}
		}
	}`, string(got))
}

func TestGenerateFor_NamedPrimitiveInlined(t *testing.T) {
	t.Parallel()

	// Named primitive types without Provider/Extender are inlined, not extracted.
	type Priority int

	type Config struct {
		P1 Priority `json:"p1"`
		P2 Priority `json:"p2"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	// No $defs — named primitive types are inlined.
	assert.Nil(t, s.Defs)
	assert.Equal(t, "integer", s.Properties["p1"].Type)
	assert.Equal(t, "integer", s.Properties["p2"].Type)
}

func TestGenerateFor_RecursiveType_WithDefinitionsFalse(t *testing.T) {
	t.Parallel()

	// Cyclic types must still emit $defs/$ref even when WithDefinitions(false).
	s, err := jsonschema.GenerateFor[RecursiveNode](jsonschema.WithDefinitions(false))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$ref":"#/$defs/RecursiveNode",
		"$defs":{
			"RecursiveNode":{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"children":{
						"type":["null","array"],
						"items":{
							"anyOf":[
								{"$ref":"#/$defs/RecursiveNode"},
								{"type":"null"}
							]
						}
					}
				},
				"required":["name"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_WithComments_TypeDescription(t *testing.T) {
	t.Parallel()

	// Draft has a doc comment: "Draft represents a JSON Schema draft version.".
	s, err := jsonschema.GenerateFor[jsonschema.Draft](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)

	assert.Equal(t, "Draft represents a JSON Schema draft version.", s.Description)
}

func TestGenerateFor_WithComments_StructDescription(t *testing.T) {
	t.Parallel()

	// FieldContext has a type-level doc comment.
	s, err := jsonschema.GenerateFor[jsonschema.FieldContext](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)

	assert.Equal(t, "FieldContext provides context about a struct field to tag interpreters.", s.Description)
}

func TestGenerateFor_BigRatAndBigFloat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"big.Rat": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Rat]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","pattern":"^-?[0-9]+(/[0-9]+)?$"}`,
		},
		"big.Float": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Float]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","pattern":"^-?[0-9]+(\\.[0-9]+)?([eE][-+]?[0-9]+)?$"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestGenerateFor_UnsafePointer(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.Generate(reflect.TypeFor[unsafe.Pointer]())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

func TestGenerateFor_Complex64(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.GenerateFor[complex64]()
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

// NamedTime is a named type wrapping time.Time. It should NOT get the
// time.Time built-in override (format: "date-time").
type NamedTime time.Time

func TestGenerateFor_NamedTypeWrappingBuiltinNoOverride(t *testing.T) {
	t.Parallel()

	// NamedTime wraps time.Time but is a distinct type — it should NOT get
	// the time.Time override. Since time.Time is a struct, NamedTime will
	// also be reflected as a struct.
	s, err := jsonschema.GenerateFor[NamedTime]()
	require.NoError(t, err)

	// Should NOT have format: "date-time" (that's the time.Time override).
	assert.Empty(t, s.Format)
	assert.Equal(t, "object", s.Type)
}

func TestGenerateFor_JsonStringOnNonApplicableType(t *testing.T) {
	t.Parallel()

	type Config struct {
		Data []int `json:"data,string"` //nolint:staticcheck // Intentional: exercises ",string" on a non-applicable type.
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// json:",string" on a slice should be silently ignored — schema is
	// the normal slice schema.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"data":{"type":["null","array"],"items":{"type":"integer"}}
		},
		"required":["data"],
		"additionalProperties":false
	}`, string(got))
}

func TestGenerateFor_JsonStringOnPointerType(t *testing.T) {
	t.Parallel()

	type Config struct {
		Port *int `json:"port,string"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// json:",string" on a pointer to a stringable type produces nullable string.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"port":{"type":["null","string"]}
		},
		"required":["port"],
		"additionalProperties":false
	}`, string(got))
}

// BothProviderAndExtender implements both JSONSchemaProvider and JSONSchemaExtender.
// Provider should take priority — Extender should NOT be called.
type BothProviderAndExtender struct {
	Value string `json:"value"`
}

func (BothProviderAndExtender) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "from provider",
	}
}

func (BothProviderAndExtender) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Description = "from extender"
}

func TestGenerateFor_ProviderTakesPriorityOverExtender(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[BothProviderAndExtender]()
	require.NoError(t, err)

	// Provider takes priority — description should be "from provider", not "from extender".
	assert.Equal(t, "from provider", s.Description)
}

func TestGenerateFor_WithTypeSchema_NamedStructExtractedToDefs(t *testing.T) {
	t.Parallel()

	override := &jsonschema.Schema{
		Type:        "object",
		Description: "custom override",
	}

	type Container struct {
		Addr Address `json:"addr"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithTypeSchema(reflect.TypeFor[Address](), override),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Address is a named struct → still extracted to $defs even with WithTypeSchema.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"addr":{"$ref":"#/$defs/Address"}
		},
		"required":["addr"],
		"additionalProperties":false,
		"$defs":{
			"Address":{
				"type":"object",
				"description":"custom override"
			}
		}
	}`, string(got))
}

func TestGenerateFor_JSONSchemaTagOverridesExtractedComment(t *testing.T) {
	t.Parallel()

	// Alpha.Widget is a real package-level type, so its doc comments are
	// extracted by go/packages (a function-local type's comments are not).
	s, err := jsonschema.GenerateFor[alpha.Widget](jsonschema.WithComments(true))
	require.NoError(t, err)

	// The unannotated field gets its extracted doc comment.
	require.Contains(t, s.Properties["size"].Description, "Size documents the widget size")

	// The annotated field's jsonschema tag overrides its doc comment.
	require.Equal(t, "tag wins over comment", s.Properties["label"].Description)
}

func TestGenerateFor_TagInterpreterOverridesJSONSchemaTag(t *testing.T) {
	t.Parallel()

	// When both jsonschema tag and validate interpreter set the same property,
	// the interpreter's value should take effect (it runs after).
	type Config struct {
		Value string `json:"value" jsonschema:"minLength=5" validate:"min=3"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// validate:"min=3" runs after jsonschema:"minLength=5", so minLength should be 3.
	assert.Equal(t, jsonschema.Ptr(3), s.Properties["value"].MinLength)
}

// GenericPair is a generic type for testing $defs name transformation.
type GenericPair[A, B any] struct {
	First  A `json:"first"`
	Second B `json:"second"`
}

func TestGenerateFor_GenericTypeNaming(t *testing.T) {
	t.Parallel()

	type Container struct {
		Pair GenericPair[int, string] `json:"pair"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Generic type name: "GenericPair[int,string]" → "GenericPair_int_string_".
	// Brackets and commas are replaced with underscores.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"pair":{"$ref":"#/$defs/GenericPair_int_string_"}
		},
		"required":["pair"],
		"additionalProperties":false,
		"$defs":{
			"GenericPair_int_string_":{
				"type":"object",
				"properties":{
					"first":{"type":"integer"},
					"second":{"type":"string"}
				},
				"required":["first","second"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_NullableRefWithAnnotation(t *testing.T) {
	t.Parallel()

	type Container struct {
		Work *Address `json:"work" jsonschema:"description=Work address"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Nullable $ref uses anyOf wrapping. Annotations are siblings of anyOf.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"work":{
				"anyOf":[
					{"$ref":"#/$defs/Address"},
					{"type":"null"}
				],
				"description":"Work address"
			}
		},
		"required":["work"],
		"additionalProperties":false,
		"$defs":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

// slog.Level implements encoding.TextMarshaler, so it should produce
// {"type": "string"} without a dedicated built-in override.
func TestGenerateFor_SlogLevel(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.Generate(reflect.TypeFor[slog.Level]())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`, string(got))
}

// NilProvider implements JSONSchemaProvider and returns nil, which should
// produce an unrestricted schema ({}).
type NilProvider struct {
	Field string `json:"field"`
}

func (NilProvider) JSONSchema() *jsonschema.Schema { return nil }

func TestGenerateFor_JSONSchemaProviderReturnsNil(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[NilProvider]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	// Nil return → unrestricted schema.
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`, string(got))
}

func TestGenerateFor_PointerToJSONRawMessage(t *testing.T) {
	t.Parallel()

	// *json.RawMessage → {} (unrestricted, not nullable-wrapped).
	s, err := jsonschema.Generate(reflect.TypeFor[*json.RawMessage]())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`, string(got))
}

// PointerReceiverMarshaler is a struct whose *T implements TextMarshaler.
// T itself does NOT implement it (only pointer receiver).
type PointerReceiverMarshaler struct {
	Value string
}

func (*PointerReceiverMarshaler) MarshalText() ([]byte, error) { return nil, nil }

func TestGenerateFor_TextMarshalerPointerReceiver(t *testing.T) {
	t.Parallel()

	// *T implements TextMarshaler → T should produce {"type": "string"}.
	s, err := jsonschema.GenerateFor[PointerReceiverMarshaler]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`, string(got))
}

func TestGenerateFor_NamedCompositeTypesInlined(t *testing.T) {
	t.Parallel()

	// Named non-struct composite types (slices, maps, arrays) without
	// Provider/Extender should be inlined, not extracted to $defs.
	type Tags []string

	type Config map[string]any

	type Matrix [3][3]float64

	type Container struct {
		Tags   Tags   `json:"tags"`
		Config Config `json:"config"`
		Matrix Matrix `json:"matrix"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	// No $defs — named composite types are inlined.
	assert.Nil(t, s.Defs)
	assert.Equal(t, []string{"null", "array"}, s.Properties["tags"].Types)
	assert.Equal(t, []string{"null", "object"}, s.Properties["config"].Types)
	assert.Equal(t, "array", s.Properties["matrix"].Type)
}

// TextMarshalerKey is a type that implements encoding.TextMarshaler,
// making it a valid map key.
type TextMarshalerKey struct{ ID int }

func (k TextMarshalerKey) MarshalText() ([]byte, error) { return nil, nil }

func TestGenerateFor_TextMarshalerMapKey(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[map[TextMarshalerKey]string]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	// TextMarshaler map keys produce the same schema as map[string]V.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":["null","object"],
		"additionalProperties":{"type":"string"}
	}`, string(got))
}

func TestGenerateFor_JSONSchemaTag_Examples(t *testing.T) {
	t.Parallel()

	type Config struct {
		Port int    `json:"port" jsonschema:"examples=8080|3000|443"`
		Mode string `json:"mode" jsonschema:"examples=debug|release"`
	}

	s, err := jsonschema.GenerateFor[Config]()
	require.NoError(t, err)

	// Integer examples should be parsed as integers (precision-preserving).
	assert.Equal(t, []any{8080, 3000, 443}, s.Properties["port"].Examples)
	// String examples should be strings.
	assert.Equal(t, []any{"debug", "release"}, s.Properties["mode"].Examples)
}

func TestGenerateFor_ExtenderDescriptionPreservedWithComments(t *testing.T) {
	t.Parallel()

	// Per the PRD processing order, JSONSchemaExtend (step 3) runs after
	// comment extraction (step 2), so the extender's description should
	// take precedence over the AST doc comment.
	s, err := jsonschema.GenerateFor[NonStructExtender](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)

	// The extender sets Description to "A list of tags" — this must not be
	// overwritten by comment re-extraction during $defs placement.
	assert.Equal(t, "A list of tags", s.Description)
}

func TestGenerateFor_ExtenderDescriptionPreservedWithComments_InDefs(t *testing.T) {
	t.Parallel()

	// Same as above but when the type is referenced from another struct
	// (exercising the $defs extraction path).
	type Container struct {
		Tags NonStructExtender `json:"tags"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// NonStructExtender's $defs entry must preserve the extender's description.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"tags":{
				"$ref":"#/$defs/NonStructExtender"
			}
		},
		"required":["tags"],
		"additionalProperties":false,
		"$defs":{
			"NonStructExtender":{
				"type":["null","array"],
				"items":{"type":"string"},
				"description":"A list of tags"
			}
		}
	}`, string(got))
}

func TestGenerateFor_Draft7_RefWithValidationKeywords(t *testing.T) {
	t.Parallel()

	// In Draft-07, $ref siblings (including validation keywords, not just
	// annotations) must be wrapped in allOf to prevent silent loss.
	type Container struct {
		Home Address `json:"home" jsonschema:"pattern=^[A-Z],format=custom"`
	}

	s, err := jsonschema.GenerateFor[Container](jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"home":{
				"allOf":[{"$ref":"#/definitions/Address"}],
				"pattern":"^[A-Z]",
				"format":"custom"
			}
		},
		"required":["home"],
		"additionalProperties":false,
		"definitions":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_Draft7_RefWithValidateInterpreter(t *testing.T) {
	t.Parallel()

	// Validation keywords from tag interpreters on $ref fields must also
	// be preserved via allOf wrapping in Draft-07.
	type Container struct {
		Home Address `json:"home" validate:"required"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// validate:"required" adds to required but doesn't add type-specific
	// constraints on struct types, so the $ref should remain bare (no allOf
	// needed since only the parent's required array was modified).
	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"home":{"$ref":"#/definitions/Address"}
		},
		"required":["home"],
		"additionalProperties":false,
		"definitions":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

// WithTypeSchemaProvider implements JSONSchemaProvider but will be overridden
// by WithTypeSchema.
type WithTypeSchemaProvider struct {
	Value string `json:"value"`
}

func (WithTypeSchemaProvider) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "from provider",
	}
}

func TestGenerateFor_WithTypeSchemaOverridesProvider(t *testing.T) {
	t.Parallel()

	// WithTypeSchema has the highest priority, overriding even JSONSchemaProvider.
	override := &jsonschema.Schema{
		Type:        "object",
		Description: "from override",
	}
	s, err := jsonschema.GenerateFor[WithTypeSchemaProvider](
		jsonschema.WithTypeSchema(reflect.TypeFor[WithTypeSchemaProvider](), override),
	)
	require.NoError(t, err)

	assert.Equal(t, "from override", s.Description)
	assert.Equal(t, "object", s.Type)
}

func TestGenerateFor_JsonStringOverridesRef(t *testing.T) {
	t.Parallel()

	// json:",string" is a field-level override that takes precedence regardless
	// of the type-level schema, including $ref extraction. The field uses
	// {type: string} directly, not a $ref. The type's $defs entry may still
	// exist as a side effect of type-level processing (orphaned but harmless).
	type Container struct {
		S Status `json:"s,string"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	// The field schema should be {type: string}, NOT a $ref to Status.
	assert.Equal(t, "string", s.Properties["s"].Type)
	assert.Empty(t, s.Properties["s"].Ref)
}

func TestGenerateFor_WithTypeSchemaNamedNonStructInlined(t *testing.T) {
	t.Parallel()

	// Named primitive types overridden via WithTypeSchema are still inlined
	// (not extracted to $defs), unless they also implement JSONSchemaProvider
	// or JSONSchemaExtender.
	type Priority int

	type Container struct {
		P1 Priority `json:"p1"`
		P2 Priority `json:"p2"`
	}

	override := &jsonschema.Schema{
		Type: "integer",
		Enum: []any{1, 2, 3},
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithTypeSchema(reflect.TypeFor[Priority](), override),
	)
	require.NoError(t, err)

	// Named non-struct type with WithTypeSchema but no Provider/Extender
	// → still inlined, no $defs.
	assert.Nil(t, s.Defs)
	assert.Equal(t, []any{1, 2, 3}, s.Properties["p1"].Enum)
	assert.Equal(t, []any{1, 2, 3}, s.Properties["p2"].Enum)
}

func TestGenerateFor_Draft7_NullableRefWithAnnotation(t *testing.T) {
	t.Parallel()

	// In Draft-07, nullable $ref uses anyOf wrapping. Since the $ref is nested
	// inside anyOf (not at the top level), no allOf wrapping is needed for
	// sibling annotations.
	type Container struct {
		Work *Address `json:"work" jsonschema:"description=Work address"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithDraft(jsonschema.Draft7),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"work":{
				"anyOf":[
					{"$ref":"#/definitions/Address"},
					{"type":"null"}
				],
				"description":"Work address"
			}
		},
		"required":["work"],
		"additionalProperties":false,
		"definitions":{
			"Address":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"state":{"type":"string"}
				},
				"required":["city","state"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

func TestGenerateFor_WithComments_FieldDescription(t *testing.T) {
	t.Parallel()

	// FieldContext has doc comments on both the type and its fields.
	s, err := jsonschema.GenerateFor[jsonschema.FieldContext](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)

	// The "Name" field has a doc comment above it.
	assert.Equal(t, "Name is the JSON property name for the field.", s.Properties["Name"].Description)
}

func TestGenerateFor_WithComments_SilentlySkipsMissingSource(t *testing.T) {
	t.Parallel()

	// Types from packages without available source should not cause errors.
	// Time.Time is a stdlib type whose source is available, but we use
	// a built-in override so comments are not extracted for it anyway.
	// This test ensures WithComments doesn't error when processing
	// types from external packages.
	type Container struct {
		T time.Time `json:"t"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithComments(true),
	)
	require.NoError(t, err)
	assert.NotNil(t, s)
}

// TextMarshalerWithExtender implements both TextMarshaler (producing "string")
// and JSONSchemaExtender (adding enum constraints). Per the PRD processing
// order, the extender runs after base type reflection.
type TextMarshalerWithExtender int

func (TextMarshalerWithExtender) MarshalText() ([]byte, error) { return nil, nil }

func (TextMarshalerWithExtender) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Enum = []any{"active", "inactive", "pending"}
}

func TestGenerateFor_TextMarshalerWithExtender(t *testing.T) {
	t.Parallel()

	// TextMarshaler produces {"type": "string"}, then JSONSchemaExtend
	// should add enum values. The type also implements JSONSchemaExtender,
	// so it should be extracted to $defs when used in a struct field.
	s, err := jsonschema.GenerateFor[TextMarshalerWithExtender]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"string",
		"enum":["active","inactive","pending"]
	}`, string(got))
}

func TestGenerateFor_TextMarshalerWithExtender_ExtractedToDefs(t *testing.T) {
	t.Parallel()

	// When used in a struct, a TextMarshaler type with JSONSchemaExtender
	// should be extracted to $defs (since it implements JSONSchemaExtender).
	type Container struct {
		Status TextMarshalerWithExtender `json:"status"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"status":{"$ref":"#/$defs/TextMarshalerWithExtender"}
		},
		"required":["status"],
		"additionalProperties":false,
		"$defs":{
			"TextMarshalerWithExtender":{
				"type":"string",
				"enum":["active","inactive","pending"]
			}
		}
	}`, string(got))
}

func TestGenerateFor_TextMarshalerNamedStructExtractedToDefs(t *testing.T) {
	t.Parallel()

	// Named struct types implementing TextMarshaler should still be
	// extracted to $defs when used in another struct, since all named
	// struct types are extracted.
	type Container struct {
		TM PointerReceiverMarshaler `json:"tm"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"tm":{"$ref":"#/$defs/PointerReceiverMarshaler"}
		},
		"required":["tm"],
		"additionalProperties":false,
		"$defs":{
			"PointerReceiverMarshaler":{"type":"string"}
		}
	}`, string(got))
}

func TestGenerateFor_BuiltinOverrideExtractedToDefs(t *testing.T) {
	t.Parallel()

	// Named struct types with built-in overrides (time.Time, url.URL, etc.)
	// should be extracted to $defs when used as struct fields, consistent
	// with how all named struct types are handled.
	type Event struct {
		Start time.Time  `json:"start"`
		End   *time.Time `json:"end,omitempty"`
	}

	s, err := jsonschema.GenerateFor[Event]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"start":{"$ref":"#/$defs/Time"},
			"end":{"anyOf":[{"$ref":"#/$defs/Time"},{"type":"null"}]}
		},
		"required":["start"],
		"additionalProperties":false,
		"$defs":{
			"Time":{"type":"string","format":"date-time"}
		}
	}`, string(got))
}

func TestUint64MaxNotExpressible(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[uint64]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// Uint64 should have a maximum, but math.MaxUint64 exceeds float64 precision.
	// Float64 cannot represent MaxUint64 exactly: it rounds up to 2^64, which is
	// then indistinguishable from MaxUint64-1. (The uint64 round-trip is avoided
	// because converting an out-of-range float is platform-dependent.)
	maxUint64 := uint64(math.MaxUint64)
	assert.InDelta(t, float64(maxUint64), float64(maxUint64-1), 0,
		"demonstrates float64 cannot represent MaxUint64 exactly")

	assert.Contains(t, got, `"maximum"`,
		"uint64 schema should express a maximum bound")
}

func TestInt64GeneratesUnboundedSchema(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[int64]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// Int64 has known bounds representable as float64.
	assert.Contains(t, got, `"minimum"`,
		"int64 schema should have minimum bound")
	assert.Contains(t, got, `"maximum"`,
		"int64 schema should have maximum bound")
}

func TestArrayGenerationDoesntUsePrefixItems(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[3]string]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// For 2020-12, fixed-length arrays should use prefixItems + items:false.
	assert.Contains(t, got, `"prefixItems"`,
		"2020-12 fixed array should use prefixItems")
}

func TestCollectStructFieldsMissingTagShadowing(t *testing.T) {
	t.Parallel()

	// Encoding/json tiebreaker: at the same depth, a field with an explicit
	// JSON tag wins over one without. This requires two embedded structs
	// at the same depth, each with a field of the same JSON name, where
	// one has an explicit json tag and the other does not.
	type Tagged struct {
		Name int `json:"name"` // Explicit tag, int type to distinguish
	}

	type Untagged struct {
		Name string // No json tag, produces JSON key "Name" (capital N)
	}
	// Both are embedded at depth 1 in Outer.
	type Outer struct {
		Tagged
		Untagged
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	// Encoding/json resolves same-depth ambiguity by preferring the tagged
	// field. The schema should include "name" (from Tagged) not drop both.
	require.NotNil(t, s.Properties["name"],
		"tagged field should win over untagged at same depth")
	assert.Equal(t, "integer", s.Properties["name"].Type,
		"tagged field (int) should win over untagged field (string)")
}

func TestDraft07AdditionalPropertiesDroppedWithAllOf(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Inner
		Name string `json:"name"`
	}

	s, err := jsonschema.Generate(reflect.TypeFor[Outer](), jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// When allOf is present in Draft7, additionalProperties is silently dropped.
	// The schema should still have additionalProperties for closed-schema guarantee.
	assert.Contains(t, got, `"additionalProperties"`,
		"Draft7 with allOf should retain additionalProperties, not silently drop it")
}

type providerMutationTestType struct{}

func (providerMutationTestType) JSONSchema() *jsonschema.Schema {
	return &todoProviderSchema
}

var todoProviderSchema = jsonschema.Schema{
	Type:        "string",
	Description: "original",
}

func TestProviderSchemaNotCloned(t *testing.T) {
	t.Parallel()

	// First generation call.
	s1, err := jsonschema.GenerateFor[providerMutationTestType]()
	require.NoError(t, err)

	desc1 := s1.Description

	// Second generation call -- should not see mutations from first.
	s2, err := jsonschema.GenerateFor[providerMutationTestType]()
	require.NoError(t, err)

	assert.Equal(t, desc1, s2.Description,
		"provider schema should not be mutated across calls")
	assert.Equal(t, "original", todoProviderSchema.Description,
		"original provider schema should not be modified")
}

func TestBigIntSchemaLacksFormatOrPattern(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
		"big.Int": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Int]() },
		},
		"big.Rat": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Rat]() },
		},
		"big.Float": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Float]() },
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			got := marshalSchema(t, s)

			// Should have a format or pattern hint to distinguish from arbitrary strings.
			hasHint := s.Format != "" || s.Pattern != ""
			assert.True(t, hasHint,
				"big numeric type should have format or pattern hint, got: %s", got)
		})
	}
}

func TestDisambiguateDefsNoSeparator(t *testing.T) {
	t.Parallel()

	// This issue requires types from different packages with names that collide
	// after base-dir prefix: package "foo" type "BarBaz" and package "fooBar"
	// type "Baz" both produce "fooBarBaz" without a separator.
	// Cannot test directly without cross-package types.
	//
	// When fixed, the intermediate disambiguation step should use a separator
	// (e.g., "foo_BarBaz" vs "fooBar_Baz") to avoid unnecessary fallback to
	// verbose full-path names.
	t.Log("documentation-only: requires cross-package types to reproduce")
}

func TestNullableInconsistency(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Ref   *Inner  `json:"ref"`
		Plain *string `json:"plain"`
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// Both nullable fields should use the same pattern for nullability.
	// Currently, $ref types use anyOf while non-ref types use Types array.
	refProp := s.Properties["ref"]
	plainProp := s.Properties["plain"]

	require.NotNil(t, refProp)
	require.NotNil(t, plainProp)

	refUsesAnyOf := len(refProp.AnyOf) > 0
	plainUsesAnyOf := len(plainProp.AnyOf) > 0

	assert.Equal(t, refUsesAnyOf, plainUsesAnyOf,
		"both nullable fields should use the same nullability pattern, got schema: %s", got)
}

func TestProcessAllOfFieldIgnoresNullablePointer(t *testing.T) {
	t.Parallel()

	type Embedded struct {
		Value string `json:"value"`
	}

	type Outer struct {
		*Embedded
		Name string `json:"name"`
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	_ = marshalSchema(t, s)

	// A nil *Embedded causes encoding/json to omit embedded fields.
	// The schema should reflect this optionality.
	data := `{"name": "test"}`
	err = jsonschema.ValidateJSON(s, []byte(data))
	require.NoError(t, err, "nil pointer-to-embedded-struct fields should be optional")
}

func TestDefaultNamerSpacesForGenerics(t *testing.T) {
	t.Parallel()

	// Go's reflect.Type.Name() includes a space after commas in generic types.
	// DefaultNamer replaces [ ] , with underscores but not spaces.
	type Generic[T, U any] struct {
		A T `json:"a"`
		B U `json:"b"`
	}

	type Container struct {
		G Generic[int, string] `json:"g"`
	}

	s, err := jsonschema.GenerateFor[Container]()
	require.NoError(t, err)

	// Check that no $defs key contains a space.
	for name := range s.Defs {
		assert.NotContains(t, name, " ",
			"$defs key %q should not contain spaces", name)
	}
}

func TestWithNamerNilPanics(t *testing.T) {
	t.Parallel()

	type Simple struct {
		Name string `json:"name"`
	}

	// WithNamer(nil) should produce a clear error, not a panic.
	assert.NotPanics(t, func() {
		//nolint:errcheck // Asserting only that the call does not panic.
		_, _ = jsonschema.GenerateFor[Simple](jsonschema.WithNamer(nil))
	}, "WithNamer(nil) should not panic")
}

func TestWithTagInterpreterNilPanics(t *testing.T) {
	t.Parallel()

	type Simple struct {
		Name string `json:"name"`
	}

	// WithTagInterpreter(nil) should produce a clear error, not a panic.
	assert.NotPanics(t, func() {
		//nolint:errcheck // Asserting only that the call does not panic.
		_, _ = jsonschema.GenerateFor[Simple](jsonschema.WithTagInterpreter(nil))
	}, "WithTagInterpreter(nil) should not panic")
}

func TestJSONStringOrphanedDefs(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Data Inner `json:"data,string"` //nolint:staticcheck // Intentional: exercises ",string" on a non-applicable type.
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	// The field schema should be {type: "string"} due to json:",string".
	// But Inner may leave an orphaned entry in $defs.
	b, err := json.Marshal(s)
	require.NoError(t, err)

	if s.Defs != nil {
		for name := range s.Defs {
			// Check that all $defs entries are actually referenced.
			assert.Contains(t, string(b), `"$ref":"#/$defs/`+name+`"`,
				"$defs entry %q should be referenced", name)
		}
	}
}

// mutualA and mutualB are mutually recursive: A references B and B references A.
type mutualA struct {
	B *mutualB `json:"b,omitempty"`
}

type mutualB struct {
	A *mutualA `json:"a,omitempty"`
}

func TestMutualRecursionNoDanglingRef(t *testing.T) {
	t.Parallel()

	// A root reached only through another definition (here mutualA is reached
	// via mutualB) must stay in $defs rather than being inlined and deleted,
	// which would leave mutualB's $ref to it dangling.
	s, err := jsonschema.GenerateFor[mutualA]()
	require.NoError(t, err)

	require.NotNil(t, s.Defs)
	assert.Contains(t, s.Defs, "mutualA", "mutualA must remain in $defs (referenced by mutualB)")
	assert.Contains(t, s.Defs, "mutualB", "mutualB must remain in $defs")

	// A dangling $ref would surface as a resolve error during validation.
	err = jsonschema.Validate(s, map[string]any{"b": map[string]any{"a": nil}})
	require.NoError(t, err, "generated schema must resolve with no dangling $ref")
}

func TestSliceSchemaIgnoresNullable(t *testing.T) {
	t.Parallel()

	// The nullable parameter is accepted but unused in schemaForSlice.
	// Both []T and *[]T produce identical schemas because slices always
	// emit ["null", "array"]. The issue is that the nullable param is dead code.
	sliceSchema, err := jsonschema.GenerateFor[[]string]()
	require.NoError(t, err)

	ptrSliceSchema, err := jsonschema.GenerateFor[*[]string]()
	require.NoError(t, err)

	sliceJSON, err := json.Marshal(sliceSchema)
	require.NoError(t, err)

	ptrSliceJSON, err := json.Marshal(ptrSliceSchema)
	require.NoError(t, err)

	// These are identical because nullable is ignored -- *[]T should arguably
	// differ or at minimum the dead parameter should be removed.
	assert.JSONEq(t, string(sliceJSON), string(ptrSliceJSON),
		"[]T and *[]T produce identical schemas because nullable param is ignored")
}

func TestMapSchemaIgnoresNullable(t *testing.T) {
	t.Parallel()

	// Same issue as slices: the nullable parameter is accepted but unused.
	mapSchema, err := jsonschema.GenerateFor[map[string]string]()
	require.NoError(t, err)

	ptrMapSchema, err := jsonschema.GenerateFor[*map[string]string]()
	require.NoError(t, err)

	mapJSON, err := json.Marshal(mapSchema)
	require.NoError(t, err)

	ptrMapJSON, err := json.Marshal(ptrMapSchema)
	require.NoError(t, err)

	// These are identical because nullable is ignored.
	assert.JSONEq(t, string(mapJSON), string(ptrMapJSON),
		"map and *map produce identical schemas because nullable param is ignored")
}

func TestHasRefSiblingsFragility(t *testing.T) {
	t.Parallel()

	// HasRefSiblings manually enumerates schema keywords. If the upstream
	// Schema struct adds new keywords, hasRefSiblings won't know about them.
	// A $ref schema with only a new keyword would fail to get allOf-wrapped
	// in Draft-07, silently losing the keyword.
	//
	// A proper fix would use reflection to compare the Schema against
	// a zero-value Schema, similar to the isEmptySchema concern.
	t.Log("documentation-only: maintenance fragility similar to isEmptySchema")
}

func TestCollectStructFieldsAllOfShadowing(t *testing.T) {
	t.Parallel()

	// When needsAllOfComposition returns true for an embedded struct, its fields
	// are not collected into the byName map for shadowing resolution. Instead a
	// synthetic __allof__ entry is created. But encoding/json still applies
	// field-level promotion and shadowing. If the allOf-composed embed has a
	// field with the same JSON name as a parent field, both appear in the schema.
	//
	// Cannot test without a type implementing JSONSchemaProvider (to trigger
	// allOf composition) that also shares a field name with the parent struct.
	t.Log("documentation-only: requires allOf-composed embed with name collision")
}

func TestGenerateThenValidateRoundTrip(t *testing.T) {
	t.Parallel()

	type Address struct {
		Street string `json:"street"`
		City   string `json:"city"`
	}

	type Person struct {
		Name    string  `json:"name"`
		Age     int     `json:"age"`
		Address Address `json:"address"`
	}

	s, err := jsonschema.GenerateFor[Person]()
	require.NoError(t, err)

	// Valid instance should pass.
	valid := `{"name":"Alice","age":30,"address":{"street":"123 Main","city":"NY"}}`
	err = jsonschema.ValidateJSON(s, []byte(valid))
	require.NoError(t, err, "valid instance should pass generated schema")

	// Invalid instance (extra property) should fail.
	invalid := `{"name":"Alice","age":30,"address":{"street":"123 Main","city":"NY"},"extra":"bad"}`
	err = jsonschema.ValidateJSON(s, []byte(invalid))
	require.Error(t, err, "instance with extra properties should fail generated schema")

	// Invalid type should fail.
	wrongType := `{"name":42,"age":30,"address":{"street":"123 Main","city":"NY"}}`
	err = jsonschema.ValidateJSON(s, []byte(wrongType))
	require.Error(t, err, "instance with wrong type should fail generated schema")
}

func TestExtractToDefsOverwriteBeforeDisambiguation(t *testing.T) {
	t.Parallel()

	// When two types from different packages produce the same name via g.namer(t),
	// the second call to extractToDefs sets g.defs[name] = s, overwriting the
	// first type's schema. The first type's schema survives in g.typeToDefSchema
	// and is recovered during disambiguateDefs. The intermediate state is correct
	// in practice but fragile.
	//
	// Cannot test without types from distinct packages sharing a name.
	t.Log("documentation-only: requires same-named types from different packages")
}

func TestBuildStructSchemaClearsAdditionalPropertiesBeforeExtender(t *testing.T) {
	t.Parallel()

	// When allOf composition is present, buildStructSchema sets
	// AdditionalProperties to nil before calling callExtender. A
	// JSONSchemaExtender implementation that expects AdditionalProperties
	// to be set to its default value ({not: {}}) will instead see nil.
	//
	// Would require a JSONSchemaExtender on an embedded type (triggering
	// allOf) that inspects AdditionalProperties.
	t.Log("documentation-only: requires extender on allOf-composed embed inspecting additionalProperties")
}

func TestCallProviderPanicsOnZeroValues(t *testing.T) {
	t.Parallel()

	// CallProvider creates a zero value via reflect.New(t) and calls
	// JSONSchema() on it. If the type's method dereferences a nil pointer
	// field, this panics with no recovery mechanism.
	//
	// A recover() wrapper with a descriptive error would be more robust.
	// The test below would demonstrate the issue with a type whose
	// JSONSchema() method dereferences a nil pointer, but since we cannot
	// define such a type in test code without it being exercised by the
	// parallel test runner, this remains documentation-only.
	t.Log("documentation-only: requires JSONSchemaProvider that panics on zero value")
}

func TestHasDirectMethodFalseNegatives(t *testing.T) {
	t.Parallel()

	// If the outer type defines a method directly AND embeds a type that
	// also has it, Go shadows the embedded method. HasDirectMethod checks
	// whether any embedded field provides the method and returns false
	// (assumes promoted). This incorrectly reports the outer type's
	// JSONSchemaProvider/JSONSchemaExtender/TextMarshaler as promoted,
	// causing it to be ignored in favor of field promotion.
	//
	// Would require an outer type that implements JSONSchemaProvider and
	// embeds a type that also implements it. The outer type's method should
	// take precedence, but hasDirectMethod reports it as promoted.
	t.Log("documentation-only: requires outer+inner both implementing JSONSchemaProvider")
}

func TestEmbedTestSchemasNotValidated(t *testing.T) {
	t.Parallel()

	// Generated schemas with allOf and unevaluatedProperties are never
	// validated against test instances. This is a test coverage gap.
	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Inner
		Name string `json:"name"`
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	// Validate a conforming instance.
	valid := `{"value":"hello","name":"test"}`
	err = jsonschema.ValidateJSON(s, []byte(valid))
	require.NoError(t, err, "valid embedded struct instance should pass")

	// Validate a non-conforming instance.
	invalid := `{"value":"hello","name":"test","extra":"bad"}`
	err = jsonschema.ValidateJSON(s, []byte(invalid))
	require.Error(t, err, "extra properties on embedded struct should fail")
}

func TestNeedsAllOfCompositionMissingExtender(t *testing.T) {
	t.Parallel()

	// A type implementing only JSONSchemaExtender still has its fields promoted
	// rather than composed via allOf. The extender modifies the parent struct's
	// schema context rather than its own standalone schema.
	//
	// Metadata (from generate_test.go) implements JSONSchemaExtender. When
	// embedded, needsAllOfComposition returns false, so its fields are promoted
	// rather than allOf-composed. The extender runs in the parent context.
	type Outer struct {
		Metadata
		Name string `json:"name"`
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// If needsAllOfComposition checked JSONSchemaExtender, Metadata would be
	// composed via allOf. Currently it is promoted (fields inlined).
	assert.Contains(t, got, `"tags"`,
		"Metadata.Tags should be present (currently promoted, not allOf-composed)")
}

func TestShadowingTestDifferentTypesToDistinguishWinner(t *testing.T) {
	t.Parallel()

	// TestGenerateFor_FieldShadowing checks Properties length but doesn't
	// verify which type won because both Inner.Name and Outer.Name are string.
	// Using different types verifies shadowing precedence.
	type Inner struct {
		Name int `json:"name"` // int, not string
	}

	type Outer struct {
		Inner
		Name string `json:"name"` // string -- should shadow Inner.Name
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	prop := s.Properties["name"]
	require.NotNil(t, prop, "shadowed 'name' field should exist")

	// Outer.Name (string) should shadow Inner.Name (int).
	assert.Equal(t, "string", prop.Type,
		"Outer.Name (string) should shadow Inner.Name (int)")
}

func TestGenerateFor_CrossPackageNameDisambiguation(t *testing.T) {
	t.Parallel()

	// Alpha.Widget and beta.Widget share the bare name "Widget"; the colliding
	// $defs keys are disambiguated by each package's base directory name.
	type Root struct {
		A alpha.Widget `json:"a"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root]()
	require.NoError(t, err)

	require.Contains(t, s.Defs, "alphaWidget")
	require.Contains(t, s.Defs, "betaWidget")
	require.NotContains(t, s.Defs, "Widget", "colliding bare name must be replaced")

	// $ref targets are rewritten to the disambiguated names.
	require.Equal(t, "#/$defs/alphaWidget", s.Properties["a"].Ref)
	require.Equal(t, "#/$defs/betaWidget", s.Properties["b"].Ref)
}

type jsonMarshalerOnly struct {
	Value int `json:"value"`
}

func (jsonMarshalerOnly) MarshalJSON() ([]byte, error) { return []byte(`"opaque"`), nil }

func TestGenerateFor_JSONMarshalerNotConsulted(t *testing.T) {
	t.Parallel()

	// A type implementing only json.Marshaler (not encoding.TextMarshaler) falls
	// through to kind-based reflection; MarshalJSON does not influence the schema.
	s, err := jsonschema.GenerateFor[jsonMarshalerOnly]()
	require.NoError(t, err)

	assert.Equal(t, "object", s.Type)
	assert.Contains(t, s.Properties, "value")
}

type ptrProvider struct{}

func (*ptrProvider) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Format: "ptr-provider-marker"}
}

func TestGenerateFor_ProviderPointerReceiver(t *testing.T) {
	t.Parallel()

	// JSONSchemaProvider declared on a pointer receiver still applies.
	s, err := jsonschema.GenerateFor[ptrProvider]()
	require.NoError(t, err)

	raw, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ptr-provider-marker")
	assert.NotContains(t, string(raw), `"object"`, "provider must bypass reflection")
}

type ptrExtender struct {
	Name string `json:"name"`
}

func (*ptrExtender) JSONSchemaExtend(s *jsonschema.Schema) {
	s.Description = "ptr-extender-marker"
}

func TestGenerateFor_ExtenderPointerReceiver(t *testing.T) {
	t.Parallel()

	// JSONSchemaExtender declared on a pointer receiver still applies.
	s, err := jsonschema.GenerateFor[ptrExtender]()
	require.NoError(t, err)

	raw, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ptr-extender-marker")
	assert.Contains(t, string(raw), "name", "reflection-built properties must survive the extender")
}

type byteSliceNamed []byte

type byteElem byte

type byteSliceOfNamed []byteElem

type marshalByte byte

func (marshalByte) MarshalJSON() ([]byte, error) { return []byte(`"b"`), nil }

type byteSliceOfMarshaler []marshalByte

func TestGenerateFor_NamedByteSlice(t *testing.T) {
	t.Parallel()

	// encoding/json base64-encodes any byte slice — selected by the element kind
	// (uint8), not the exact type — so named byte slices and slices of named
	// uint8 elements are base64 strings too. The exception is an element type
	// implementing json.Marshaler/encoding.TextMarshaler, which is encoded via
	// that method rather than as base64.
	const base64Schema = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"type":["null","string"],"contentEncoding":"base64"}`

	t.Run("named []byte is base64 string", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceNamed]()
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, base64Schema, string(got))

		// The schema accepts the type's own serialized form (a base64 string).
		data, err := json.Marshal(byteSliceNamed("hello"))
		require.NoError(t, err)
		require.NoError(t, jsonschema.ValidateJSON(s, data))
	})

	t.Run("slice of named uint8 is base64 string", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceOfNamed]()
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, base64Schema, string(got))
	})

	t.Run("uint8 element implementing json.Marshaler is not base64", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceOfMarshaler]()
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.NotContains(t, string(got), "base64",
			"a json.Marshaler element must not trigger the base64 byte-slice path")
		assert.Contains(t, string(got), `"array"`)
	})
}
