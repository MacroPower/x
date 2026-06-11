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

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/beta"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
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
			// Big.Int.MarshalJSON emits a bare JSON number, so the schema is an
			// unbounded integer rather than a string.
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Int]() },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer"}`,
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

func TestMustGenerateFor(t *testing.T) {
	t.Parallel()

	t.Run("returns the GenerateFor schema", func(t *testing.T) {
		t.Parallel()

		type Config struct {
			Name string `json:"name"`
		}

		want, err := jsonschema.GenerateFor[Config]()
		require.NoError(t, err)

		got := jsonschema.MustGenerateFor[Config]()
		assert.Equal(t, marshalSchema(t, want), marshalSchema(t, got))
	})

	t.Run("panics on generation error", func(t *testing.T) {
		t.Parallel()

		assert.Panics(t, func() {
			jsonschema.MustGenerateFor[func()]()
		})
	})
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
		DashID string `json:"-,"` //nolint:staticcheck // Tag under test: "-," names the field "-" in encoding/json v1.
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

// recursiveSlice, recursiveMap, and recursiveArray are named non-struct types
// that contain themselves. They are valid Go types that encoding/json handles,
// and the generator must break their cycles via $defs/$ref rather than recursing
// without bound. A by-value recursive array (type A [2]A) has infinite size and
// the Go compiler rejects it, so the array case recurses through a pointer.
type recursiveSlice []recursiveSlice

type recursiveMap map[string]recursiveMap

type recursiveArray [2]*recursiveArray

// TestGenerateFor_RecursiveSlice covers a named slice type whose element is the
// slice type itself. Without cycle detection on the slice path the generator
// recurses without bound; the result must reference itself through $defs.
func TestGenerateFor_RecursiveSlice(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[recursiveSlice]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$ref":"#/$defs/recursiveSlice",
		"$defs":{
			"recursiveSlice":{
				"type":["null","array"],
				"items":{"$ref":"#/$defs/recursiveSlice"}
			}
		}
	}`, string(got))
}

// TestGenerateFor_RecursiveMap covers a named map type whose value is the map
// type itself, exercising cycle detection on the map path.
func TestGenerateFor_RecursiveMap(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[recursiveMap]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$ref":"#/$defs/recursiveMap",
		"$defs":{
			"recursiveMap":{
				"type":["null","object"],
				"additionalProperties":{"$ref":"#/$defs/recursiveMap"}
			}
		}
	}`, string(got))
}

// TestGenerateFor_RecursiveArray covers a named array type whose elements are
// pointers to the array type itself, exercising cycle detection on the array
// path (the only recursive array form Go permits).
func TestGenerateFor_RecursiveArray(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[recursiveArray]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$ref":"#/$defs/recursiveArray",
		"$defs":{
			"recursiveArray":{
				"type":"array",
				"minItems":2,
				"maxItems":2,
				"prefixItems":[
					{"anyOf":[{"$ref":"#/$defs/recursiveArray"},{"type":"null"}]},
					{"anyOf":[{"$ref":"#/$defs/recursiveArray"},{"type":"null"}]}
				]
			}
		}
	}`, string(got))
}

// selfEmbeddingStruct embeds a pointer to itself. The standard library marshals
// selfEmbeddingStruct{X: 5} as {"x":5}: the embedded pointer promotes its own X
// at a deeper level, where the outer X shadows it. The field collector must
// track visited embedded types so it does not recurse without bound.
type selfEmbeddingStruct struct {
	*selfEmbeddingStruct //nolint:unused // Self-embed under test; exercised via reflection.

	X int `json:"x"`
}

// TestGenerateFor_SelfEmbeddingStruct covers a struct that embeds a pointer to
// itself. Field collection must terminate, and the schema must match what
// encoding/json serializes: a single "x" property.
func TestGenerateFor_SelfEmbeddingStruct(t *testing.T) {
	t.Parallel()

	// Confirm the reference behavior: encoding/json marshals to {"x":5}.
	encoded, err := json.Marshal(selfEmbeddingStruct{X: 5})
	require.NoError(t, err)
	assert.JSONEq(t, `{"x":5}`, string(encoded))

	s, err := jsonschema.GenerateFor[selfEmbeddingStruct]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The embedded pointer's only field, X, is shadowed by the outer X, so the
	// final schema carries no self-reference and the root is inlined: a single
	// "x" property, matching encoding/json's {"x":5}.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"x":{"type":"integer"}},
		"required":["x"],
		"additionalProperties":false
	}`, string(got))

	// The schema must accept exactly what encoding/json produces.
	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(encoded))
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

// NullableOptOut exercises WithNullable(false) on a struct: a pointer to a
// scalar, a pointer to a $ref'd struct, and a json:",string" pointer field all
// lose their null branch.
type NullableOptOut struct {
	Count *int     `json:"count"`
	Work  *Address `json:"work"`
	Port  *int     `json:"port,string"`
}

func TestGenerateFor_WithNullable(t *testing.T) {
	t.Parallel()

	// With WithNullable(false), nil-able container types drop the "null" branch
	// and emit a singular type keyword.
	t.Run("containers", func(t *testing.T) {
		t.Parallel()

		tests := map[string]struct {
			generate func() (*jsonschema.Schema, error)
			want     string
		}{
			"slice": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[[]string](jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"array",
					"items":{"type":"string"}
				}`,
			},
			"map": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[map[string]int](jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"additionalProperties":{"type":"integer"}
				}`,
			},
			"byteSlice": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[[]byte](jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"string",
					"contentEncoding":"base64"
				}`,
			},
			// A named, recursive slice forces the extractToDefs path; the $defs
			// entry must also drop the null branch and the root ref stays bare.
			"recursiveSliceDefs": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[recursiveSlice](jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"$ref":"#/$defs/recursiveSlice",
					"$defs":{
						"recursiveSlice":{
							"type":"array",
							"items":{"$ref":"#/$defs/recursiveSlice"}
						}
					}
				}`,
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
	})

	// Pointer fields produce bare value schemas: *int inlines, pointer-to-struct
	// is a bare $ref, and json:",string" yields a plain string — none wrapped in
	// anyOf.
	t.Run("pointers", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[NullableOptOut](jsonschema.WithNullable(false))
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema":"https://json-schema.org/draft/2020-12/schema",
			"type":"object",
			"properties":{
				"count":{"type":"integer"},
				"work":{"$ref":"#/$defs/Address"},
				"port":{"type":"string"}
			},
			"required":["count","work","port"],
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
	})

	// The default (and explicit WithNullable(true)) keeps the null branch.
	t.Run("defaultStillNullable", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[[]string](jsonschema.WithNullable(true))
		require.NoError(t, err)
		assert.Equal(t, []string{"null", "array"}, s.Types)
	})
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

func TestGenerateFor_TagInterpreterIntersectsJSONSchemaTagBounds(t *testing.T) {
	t.Parallel()

	// When both the jsonschema tag and the validate interpreter bound the same
	// property, the bounds intersect: floors only rise and ceilings only fall,
	// so the stricter constraint wins regardless of processing order.
	type Config struct {
		// The jsonschema tag's floor is stricter and is kept.
		Strict string `json:"strict" jsonschema:"minLength=5" validate:"min=3"`
		// The interpreter's floor is stricter and raises the tag's floor.
		Loose string `json:"loose" jsonschema:"minLength=2" validate:"min=3"`
	}

	s, err := jsonschema.GenerateFor[Config](
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, jsonschema.Ptr(5), s.Properties["strict"].MinLength)
	assert.Equal(t, jsonschema.Ptr(3), s.Properties["loose"].MinLength)
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

	// JSONSchemaExtend runs after comment extraction, so the extender's
	// description takes precedence over the AST doc comment.
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

func TestGenerateFor_Draft7_RefWithInterpreterNot(t *testing.T) {
	t.Parallel()

	// A bare $ref field that a tag interpreter decorates with a logic keyword
	// (here validate:"ne=0" sets not:{const:0} on the integer-typed
	// NonStructProvider) must be wrapped in allOf under Draft-07, or the
	// validator silently drops the not sibling alongside $ref. The not lands as a
	// sibling of allOf, never of $ref.
	type Container struct {
		Level NonStructProvider `json:"level" validate:"ne=0"`
	}

	s, err := jsonschema.GenerateFor[Container](
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter(validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// The $ref moved into allOf; not is a sibling of allOf, not of $ref.
	field := s.Properties["level"]
	require.Empty(t, field.Ref, "the bare $ref must be wrapped, not left as a sibling of not")
	require.Len(t, field.AllOf, 1)
	assert.Equal(t, "#/definitions/NonStructProvider", field.AllOf[0].Ref)
	require.NotNil(t, field.Not, "not must remain a sibling of allOf")

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{
			"level":{
				"allOf":[{"$ref":"#/definitions/NonStructProvider"}],
				"not":{"const":0}
			}
		},
		"required":["level"],
		"additionalProperties":false,
		"definitions":{
			"NonStructProvider":{
				"type":"integer",
				"enum":[1,2,3]
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

func TestGenerateFor_WithComments_HandlesTypesWithoutSource(t *testing.T) {
	t.Parallel()

	// WithComments does not error when a field's type comes from an external
	// package. Time.Time uses a built-in override, so no comment is extracted
	// for it, and generation succeeds.
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
// and JSONSchemaExtender (adding enum constraints). The extender runs after
// base type reflection, so it sees and augments the "string" schema.
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

	// Named struct types with built-in overrides (time.Time, big.Int, etc.)
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

// TestUint64BoundExclusiveMaximum covers the uint64 upper bound. Float64 cannot
// represent MaxUint64 (2^64-1) exactly, so an inclusive maximum would have to
// round down and reject the field's own boundary value. The schema instead uses
// an exclusive maximum of 2^64 (exactly representable), which admits every valid
// uint64 including the boundary while still rejecting out-of-range values.
func TestUint64BoundExclusiveMaximum(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[uint64]()
	require.NoError(t, err)

	// Float64 cannot represent MaxUint64 exactly: it rounds up to 2^64, which is
	// then indistinguishable from MaxUint64-1, so no inclusive maximum names the
	// boundary.
	maxUint64 := uint64(math.MaxUint64)
	assert.InDelta(t, float64(maxUint64), float64(maxUint64-1), 0,
		"demonstrates float64 cannot represent MaxUint64 exactly")

	require.Nil(t, s.Maximum, "uint64 should not use an inclusive maximum")
	require.NotNil(t, s.ExclusiveMaximum, "uint64 should express an exclusive maximum bound")
	assert.InDelta(t, float64(1<<64), *s.ExclusiveMaximum, 0, "exclusive maximum should be 2^64")

	// The boundary value MaxUint64 must validate against its own schema.
	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON([]byte("18446744073709551615")),
		"MaxUint64 must satisfy the uint64 schema")
}

// TestInt64BoundExclusiveMaximum covers the int64 bounds. MinInt64 (-2^63) is
// representable as float64, so the minimum stays inclusive; MaxInt64 (2^63-1) is
// not, so the upper bound uses an exclusive maximum of 2^63 to admit the
// boundary value exactly.
func TestInt64BoundExclusiveMaximum(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[int64]()
	require.NoError(t, err)

	require.NotNil(t, s.Minimum, "int64 should have an inclusive minimum bound")
	assert.InDelta(t, float64(math.MinInt64), *s.Minimum, 0, "minimum should be MinInt64")

	require.Nil(t, s.Maximum, "int64 should not use an inclusive maximum")
	require.NotNil(t, s.ExclusiveMaximum, "int64 should express an exclusive maximum bound")
	assert.InDelta(t, float64(1<<63), *s.ExclusiveMaximum, 0, "exclusive maximum should be 2^63")

	// Both boundary values must validate against their own schema.
	v, err := jsonschema.Compile(s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON([]byte("9223372036854775807")),
		"MaxInt64 must satisfy the int64 schema")
	assert.NoError(t, v.ValidateJSON([]byte("-9223372036854775808")),
		"MinInt64 must satisfy the int64 schema")
}

func TestArrayGenerationUsesPrefixItems(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[3]string]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// For 2020-12, fixed-length arrays use prefixItems.
	assert.Contains(t, got, `"prefixItems"`,
		"2020-12 fixed array should use prefixItems")
}

func TestCollectStructFieldsTaggedFieldWinsAtSameDepth(t *testing.T) {
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

func TestDraft07AdditionalPropertiesRetainedWithPromotedEmbed(t *testing.T) {
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

	// A plain embedded struct has its fields promoted rather than allOf-composed,
	// so additionalProperties stays on the schema and the closed-schema guarantee
	// holds in Draft-07.
	assert.Contains(t, got, `"additionalProperties"`,
		"Draft7 with promoted embed should retain additionalProperties")
}

type providerMutationTestType struct{}

func (providerMutationTestType) JSONSchema() *jsonschema.Schema {
	return &sharedProviderSchema
}

var sharedProviderSchema = jsonschema.Schema{
	Type:        "string",
	Description: "original",
}

func TestProviderSchemaIsolatedAcrossCalls(t *testing.T) {
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
	assert.Equal(t, "original", sharedProviderSchema.Description,
		"original provider schema should not be modified")
}

// TestProviderSchemaIsolatedWithComments covers the provider clone under
// comment extraction. Comment extraction writes Description in place, so without
// cloning the provider's returned pointer it would overwrite the shared
// singleton's Description and leak across fields and Generate calls. The earlier
// isolation test passes even without the clone because it runs without comments.
// The provider type lives in a real source package so its doc comment loads,
// making comment extraction (and thus the would-be mutation) actually fire.
func TestProviderSchemaIsolatedWithComments(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.ProviderSingleton](jsonschema.WithComments(true))
	require.NoError(t, err)

	// The generated schema picks up the type's doc comment, confirming comment
	// extraction ran and would have mutated the singleton absent the clone.
	assert.Contains(t, s.Description, "documents a provider")

	// The shared singleton the provider returns must remain untouched.
	assert.Empty(t, alpha.SharedProviderSchema.Description,
		"provider singleton must not be mutated by comment extraction")
}

func TestBigNumericSchemaHasFormatOrPattern(t *testing.T) {
	t.Parallel()

	// Big.Int marshals as a bare JSON number (covered by the built-in override
	// test); big.Rat and big.Float marshal via MarshalText as strings, which
	// need a pattern hint to distinguish them from arbitrary strings.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
	}{
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

func TestNullablePointerFieldsUseConsistentPattern(t *testing.T) {
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

	// Nullable pointer fields express nullability the same way regardless of
	// whether the value is a $ref or an inline type: both wrap in anyOf with a
	// null branch.
	refProp := s.Properties["ref"]
	plainProp := s.Properties["plain"]

	require.NotNil(t, refProp)
	require.NotNil(t, plainProp)

	refUsesAnyOf := len(refProp.AnyOf) > 0
	plainUsesAnyOf := len(plainProp.AnyOf) > 0

	assert.Equal(t, refUsesAnyOf, plainUsesAnyOf,
		"both nullable fields should use the same nullability pattern, got schema: %s", got)
}

func TestPointerEmbeddedStructFieldsAreOptional(t *testing.T) {
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

func TestWithNamerNilDoesNotPanic(t *testing.T) {
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

func TestWithTagInterpreterNilDoesNotPanic(t *testing.T) {
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

func TestJSONStringOnStructLeavesNoOrphanedDefs(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Data Inner `json:"data,string"` //nolint:staticcheck // Intentional: exercises ",string" on a non-applicable type.
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	// json:",string" does not apply to a struct field, so Inner keeps its normal
	// $ref and every $defs entry stays referenced.
	b, err := json.Marshal(s)
	require.NoError(t, err)

	if s.Defs != nil {
		for name := range s.Defs {
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

func TestSliceAndPointerSliceProduceIdenticalSchemas(t *testing.T) {
	t.Parallel()

	// Slices always emit ["null","array"], so []T and *[]T produce identical
	// schemas.
	sliceSchema, err := jsonschema.GenerateFor[[]string]()
	require.NoError(t, err)

	ptrSliceSchema, err := jsonschema.GenerateFor[*[]string]()
	require.NoError(t, err)

	sliceJSON, err := json.Marshal(sliceSchema)
	require.NoError(t, err)

	ptrSliceJSON, err := json.Marshal(ptrSliceSchema)
	require.NoError(t, err)

	assert.JSONEq(t, string(sliceJSON), string(ptrSliceJSON),
		"[]T and *[]T produce identical schemas")
}

func TestMapAndPointerMapProduceIdenticalSchemas(t *testing.T) {
	t.Parallel()

	// Maps always emit ["null","object"], so map and *map produce identical
	// schemas.
	mapSchema, err := jsonschema.GenerateFor[map[string]string]()
	require.NoError(t, err)

	ptrMapSchema, err := jsonschema.GenerateFor[*map[string]string]()
	require.NoError(t, err)

	mapJSON, err := json.Marshal(mapSchema)
	require.NoError(t, err)

	ptrMapJSON, err := json.Marshal(ptrMapSchema)
	require.NoError(t, err)

	assert.JSONEq(t, string(mapJSON), string(ptrMapJSON),
		"map and *map produce identical schemas")
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

// panickingProvider has an empty slice field that its JSONSchema method
// indexes, so calling it on the zero value panics with an out-of-range access.
type panickingProvider struct {
	names []string
}

func (p panickingProvider) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Description: p.names[0]} // out of range on zero value
}

// embeddedProvider supplies a JSONSchema method that an outer type can both
// promote and shadow with its own direct method.
//
//nolint:unused // Embedded into outerWithDirectMethods to set up method shadowing, exercised via reflection.
type embeddedProvider struct{}

//nolint:unused // Shadowed by outerWithDirectMethods.JSONSchema; present only to create the shadowing case.
func (embeddedProvider) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "embedded"}
}

// outerWithDirectMethods declares JSONSchema directly and also embeds a type
// that provides it; the direct method shadows the promoted one.
type outerWithDirectMethods struct {
	embeddedProvider //nolint:unused // Embedded to exercise direct-method-wins-over-promoted shadowing.
}

func (outerWithDirectMethods) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "outer"}
}

func TestProviderPanicOnZeroValueWrapsErrProviderPanic(t *testing.T) {
	t.Parallel()

	// A JSONSchema() method that dereferences a nil pointer field panics when
	// called on the zero value during generation. The generator recovers the
	// panic and returns an error wrapping ErrProviderPanic rather than crashing.
	_, err := jsonschema.GenerateFor[panickingProvider]()
	require.ErrorIs(t, err, jsonschema.ErrProviderPanic)
}

func TestDirectMethodWinsOverEmbeddedProvider(t *testing.T) {
	t.Parallel()

	// When an outer type declares JSONSchema() directly and also embeds a type
	// that provides it, Go's method resolution gives the outer type's directly-
	// declared method precedence. The generator honors that: the outer schema
	// wins over the embedded provider's schema.
	s, err := jsonschema.GenerateFor[outerWithDirectMethods]()
	require.NoError(t, err)

	assert.Equal(t, "outer", s.Type, "outer's direct JSONSchema must win over the embedded provider")
}

func TestEmbeddedStructSchemaValidatesInstances(t *testing.T) {
	t.Parallel()

	// A schema generated from an embedded struct accepts a conforming instance
	// and rejects one with extra properties.
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

func TestExtenderOnlyEmbedHasFieldsPromoted(t *testing.T) {
	t.Parallel()

	// An embedded type that implements only JSONSchemaExtender (not a provider
	// or marshaler) has its fields promoted rather than composed via allOf.
	// Metadata implements JSONSchemaExtender, so needsAllOfComposition returns
	// false and Metadata.Tags is inlined into the parent's properties.
	type Outer struct {
		Metadata
		Name string `json:"name"`
	}

	s, err := jsonschema.GenerateFor[Outer]()
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// Metadata.Tags is promoted into the parent, so it appears directly in the
	// schema rather than inside an allOf branch.
	assert.Contains(t, got, `"tags"`,
		"Metadata.Tags should be promoted into the parent schema")
}

func TestFieldShadowingOuterWinsOverEmbedded(t *testing.T) {
	t.Parallel()

	// An outer field shadows an embedded field with the same JSON name. Inner
	// and Outer use distinct types so the winning field is unambiguous.
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
	// $defs keys are disambiguated by each package's base directory name joined
	// to the type name with an underscore.
	type Root struct {
		A alpha.Widget `json:"a"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root]()
	require.NoError(t, err)

	require.Contains(t, s.Defs, "alpha_Widget")
	require.Contains(t, s.Defs, "beta_Widget")
	require.NotContains(t, s.Defs, "Widget", "colliding bare name must be replaced")

	// $ref targets are rewritten to the disambiguated names.
	require.Equal(t, "#/$defs/alpha_Widget", s.Properties["a"].Ref)
	require.Equal(t, "#/$defs/beta_Widget", s.Properties["b"].Ref)
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

	// Encoding/json base64-encodes any byte slice — selected by the element kind
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

func TestGenerateFor_URLReflectsAsObject(t *testing.T) {
	t.Parallel()

	// Url.URL implements neither json.Marshaler nor encoding.TextMarshaler, so
	// encoding/json serializes it as a plain struct object; a string/uri schema
	// would reject every actual serialization.
	u, err := url.Parse("https://example.com/path")
	require.NoError(t, err)

	doc, err := json.Marshal(*u)
	require.NoError(t, err)

	s, err := jsonschema.GenerateFor[url.URL]()
	require.NoError(t, err)

	assert.Equal(t, "object", s.Type)
	assert.Contains(t, s.Properties, "Scheme")
	assert.NoError(t, jsonschema.ValidateJSON(s, doc),
		"generated schema rejected url.URL's actual serialization: %s", doc)
}

func TestGenerateFor_BigIntMatchesMarshalOutput(t *testing.T) {
	t.Parallel()

	type bigIntDoc struct {
		I *big.Int `json:"i"`
	}

	// Big.Int.MarshalJSON emits a bare JSON number.
	data, err := json.Marshal(bigIntDoc{I: big.NewInt(123)})
	require.NoError(t, err)
	require.JSONEq(t, `{"i":123}`, string(data))

	s, err := jsonschema.GenerateFor[bigIntDoc]()
	require.NoError(t, err)
	assert.NoError(t, jsonschema.ValidateJSON(s, data),
		"generated schema rejected big.Int's actual serialization: %s", data)
}
