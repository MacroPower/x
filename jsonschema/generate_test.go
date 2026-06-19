package jsonschema_test

import (
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
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
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[string](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`,
		},
		"bool": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[bool](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"boolean"}`,
		},
		"int": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[int](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer"}`,
		},
		"int8": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[int8](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer","minimum":-128,"maximum":127}`,
		},
		"uint16": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[uint16](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer","minimum":0,"maximum":65535}`,
		},
		"float64": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[float64](t.Context()) },
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

	s, err := jsonschema.GenerateFor[*string](t.Context())
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

	s, err := jsonschema.GenerateFor[[]int](t.Context())
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

	s, err := jsonschema.GenerateFor[[3]string](t.Context())
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

	s, err := jsonschema.GenerateFor[map[string]int](t.Context())
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

	s, err := jsonschema.GenerateFor[any](t.Context())
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

	s, err := jsonschema.GenerateFor[SimpleStruct](t.Context())
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
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[time.Time](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","format":"date-time"}`,
		},
		"json.RawMessage": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[json.RawMessage](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`,
		},
		"json.Number": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[json.Number](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"number"}`,
		},
		"[]byte": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[[]byte](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":["null","string"],"contentEncoding":"base64"}`,
		},
		"big.Int": {
			// Big.Int.MarshalJSON emits a bare JSON number, so the schema is an
			// unbounded integer rather than a string.
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Int](t.Context()) },
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

			_, err := jsonschema.Generate(t.Context(), tc.t)
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

		want, err := jsonschema.GenerateFor[Config](t.Context())
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

func TestMustGenerate(t *testing.T) {
	t.Parallel()

	t.Run("returns the Generate schema", func(t *testing.T) {
		t.Parallel()

		type Config struct {
			Name string `json:"name"`
		}

		want, err := jsonschema.Generate(t.Context(), reflect.TypeFor[Config]())
		require.NoError(t, err)

		got := jsonschema.MustGenerate(reflect.TypeFor[Config]())
		assert.Equal(t, marshalSchema(t, want), marshalSchema(t, got))
	})

	t.Run("panics on generation error", func(t *testing.T) {
		t.Parallel()

		assert.Panics(t, func() {
			jsonschema.MustGenerate(reflect.TypeFor[func()]())
		})
	})
}

func TestGenerateFor_UnsupportedMapKey(t *testing.T) {
	t.Parallel()

	type BadMap map[float64]string

	_, err := jsonschema.GenerateFor[BadMap](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedMapKey)
}

func TestGenerateFor_Draft7(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[SimpleStruct](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
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

	s, err := jsonschema.GenerateFor[UserWithAddress](t.Context())
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

	s, err := jsonschema.GenerateFor[UserWithAddress](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
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

	s, err := jsonschema.GenerateFor[UserWithAddress](t.Context(), jsonschema.WithDefinitions(false))
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

	s, err := jsonschema.GenerateFor[SimpleStruct](t.Context(), jsonschema.WithAdditionalProperties(true))
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

	s, err := jsonschema.GenerateFor[MyEnum](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`, string(got))
}

// JSONSchemaProvider type.
type Status string

func (Status) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type: "string",
		Enum: []any{"active", "inactive", "suspended"},
	}, nil
}

func TestGenerateFor_JSONSchemaProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[Status](t.Context())
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

func (Metadata) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Description = "Arbitrary key-value metadata"
	s.MinProperties = new(1)

	return nil
}

func TestGenerateFor_JSONSchemaExtender(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[Metadata](t.Context())
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

// failingExtender returns an error from JSONSchemaExtend, proving the error
// aborts generation and reaches the caller wrapped with the type and method.
type failingExtender struct {
	Name string `json:"name"`
}

func (failingExtender) JSONSchemaExtend(context.Context, jsonschema.TypeContext, *jsonschema.Schema) error {
	return errExtendUnavailable
}

func TestGenerateFor_JSONSchemaExtenderError(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.GenerateFor[failingExtender](t.Context())
	require.ErrorIs(t, err, errExtendUnavailable)
	assert.ErrorContains(t, err, "failingExtender.JSONSchemaExtend")
}

// nullableDefStatus is a named scalar implementing JSONSchemaExtender, so it is
// extracted to $defs and shared by every reference. The shared definition must
// stay nullability-free: nullability belongs on each reference (a pointer field
// wraps the $ref in an anyOf null branch), not on the single shared entry.
type nullableDefStatus string

func (nullableDefStatus) JSONSchemaExtend(context.Context, jsonschema.TypeContext, *jsonschema.Schema) error {
	return nil
}

type ptrFirstHolder struct {
	Optional *nullableDefStatus `json:"optional"`
	Required nullableDefStatus  `json:"required"`
}

type valueFirstHolder struct {
	Required nullableDefStatus  `json:"required"`
	Optional *nullableDefStatus `json:"optional"`
}

func TestGenerateFor_SharedDefNullabilityIndependentOfFieldOrder(t *testing.T) {
	t.Parallel()

	// The shared $defs entry must be identical whichever reference is generated
	// first, and must never bake in a null branch: baking nullability into the
	// shared definition would let field declaration order decide it for every
	// reference.
	ptrFirst, err := jsonschema.GenerateFor[ptrFirstHolder](t.Context())
	require.NoError(t, err)

	valueFirst, err := jsonschema.GenerateFor[valueFirstHolder](t.Context())
	require.NoError(t, err)

	defPtrFirst := ptrFirst.Defs["nullableDefStatus"]
	require.NotNil(t, defPtrFirst)

	defValueFirst := valueFirst.Defs["nullableDefStatus"]
	require.NotNil(t, defValueFirst)

	assert.Equal(t, "string", defPtrFirst.Type, "def: %s", marshalSchema(t, defPtrFirst))
	assert.Empty(t, defPtrFirst.AnyOf)
	assert.JSONEq(t, marshalSchema(t, defValueFirst), marshalSchema(t, defPtrFirst))

	// A non-pointer field is never nullable, regardless of field order.
	for name, s := range map[string]*jsonschema.Schema{"ptrFirst": ptrFirst, "valueFirst": valueFirst} {
		v, cerr := jsonschema.Compile(t.Context(), s)
		require.NoError(t, cerr, name)

		verr := v.Validate(t.Context(), map[string]any{"required": nil, "optional": "x"})
		require.Error(t, verr, "%s: a null non-pointer field must be rejected", name)
	}
}

func TestGenerateFor_WithTypeSchema(t *testing.T) {
	t.Parallel()

	override := &jsonschema.Schema{
		Type:   "string",
		Format: "date",
	}
	s, err := jsonschema.GenerateFor[time.Time](t.Context(),
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

func TestGenerateFor_WithTypeSchemaFor(t *testing.T) {
	t.Parallel()

	// The generic form matches WithTypeSchema with reflect.TypeFor spelled
	// out, including the highest-priority position in the resolution chain.
	override := &jsonschema.Schema{
		Type:   "string",
		Format: "date",
	}
	s, err := jsonschema.GenerateFor[time.Time](t.Context(),
		jsonschema.WithTypeSchemaFor[time.Time](override),
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	s, err := jsonschema.GenerateFor[Empty](t.Context())
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

	s, err := jsonschema.GenerateFor[RecursiveNode](t.Context())
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

	s, err := jsonschema.GenerateFor[recursiveSlice](t.Context())
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

	s, err := jsonschema.GenerateFor[recursiveMap](t.Context())
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

	s, err := jsonschema.GenerateFor[recursiveArray](t.Context())
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

	s, err := jsonschema.GenerateFor[selfEmbeddingStruct](t.Context())
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
	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), encoded))
}

func TestGenerateFor_IntegerMapKeys(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[map[int]string](t.Context())
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
	s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[*any]())
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

	s, err := jsonschema.GenerateFor[Msg](t.Context())
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

	s, err := jsonschema.GenerateFor[UserWithAddress](t.Context(),
		jsonschema.WithNamer(jsonschema.NamerFunc(func(tc jsonschema.TypeContext) string {
			return "custom_" + tc.Type.Name()
		})),
	)
	require.NoError(t, err)
	// Check that the custom namer was used.
	assert.NotNil(t, s.Defs["custom_Address"])
}

// TestGenerateFor_WithNamerEmptyDefersToDefault pins the partial-namer
// contract: an empty answer defers to the built-in namer instead of
// producing an empty $defs key and a broken "#/$defs/" ref.
func TestGenerateFor_WithNamerEmptyDefersToDefault(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[UserWithAddress](t.Context(),
		jsonschema.WithNamer(jsonschema.NamerFunc(func(jsonschema.TypeContext) string {
			return ""
		})),
	)
	require.NoError(t, err)
	assert.NotNil(t, s.Defs["Address"], "empty namer answers should fall back to the default name")

	_, exists := s.Defs[""]
	assert.False(t, exists, "no empty definitions key should be created")
}

// TestGenerator pins the reusable form: one option set applied at
// construction serves every call, runs never share state, and the zero
// option case behaves like the package-level entry points.
func TestGenerator(t *testing.T) {
	t.Parallel()

	type plain struct {
		Name string `json:"name"`
	}

	t.Run("options apply to every call", func(t *testing.T) {
		t.Parallel()

		gen := jsonschema.NewGenerator(
			jsonschema.WithNamer(jsonschema.NamerFunc(func(tc jsonschema.TypeContext) string {
				return "custom_" + tc.Type.Name()
			})),
		)

		s, err := gen.Generate(t.Context(), reflect.TypeFor[UserWithAddress]())
		require.NoError(t, err)
		assert.NotNil(t, s.Defs["custom_Address"])

		s, err = gen.Generate(t.Context(), reflect.TypeFor[Address]())
		require.NoError(t, err)
		assert.Equal(t, "object", s.Type)
	})

	t.Run("runs do not share state", func(t *testing.T) {
		t.Parallel()

		gen := jsonschema.NewGenerator()

		s, err := gen.Generate(t.Context(), reflect.TypeFor[UserWithAddress]())
		require.NoError(t, err)
		require.NotNil(t, s.Defs["Address"], "the first run extracts Address into $defs")

		s, err = gen.Generate(t.Context(), reflect.TypeFor[plain]())
		require.NoError(t, err)
		assert.Empty(t, s.Defs, "a later run must not carry the first run's $defs")
	})

	t.Run("concurrent use", func(t *testing.T) {
		t.Parallel()

		gen := jsonschema.NewGenerator()

		const runs = 8

		var (
			wg      sync.WaitGroup
			schemas [runs]*jsonschema.Schema
			errs    [runs]error
		)

		for i := range runs {
			wg.Go(func() {
				schemas[i], errs[i] = gen.Generate(t.Context(), reflect.TypeFor[UserWithAddress]())
			})
		}

		wg.Wait()

		for i := range runs {
			require.NoError(t, errs[i])
			assert.NotNil(t, schemas[i].Defs["Address"])
		}
	})

	t.Run("nil option is skipped", func(t *testing.T) {
		t.Parallel()

		assert.NotPanics(t, func() {
			_, err := jsonschema.NewGenerator(nil).Generate(t.Context(), reflect.TypeFor[plain]())
			assert.NoError(t, err)
		})
	})

	t.Run("GenerateWith is the generic form", func(t *testing.T) {
		t.Parallel()

		gen := jsonschema.NewGenerator(
			jsonschema.WithNamer(jsonschema.NamerFunc(func(tc jsonschema.TypeContext) string {
				return "custom_" + tc.Type.Name()
			})),
		)

		s, err := jsonschema.GenerateWith[UserWithAddress](t.Context(), gen)
		require.NoError(t, err)
		assert.NotNil(t, s.Defs["custom_Address"], "the Generator's options apply")

		want, err := gen.Generate(t.Context(), reflect.TypeFor[UserWithAddress]())
		require.NoError(t, err)
		assert.Equal(t, want, s, "GenerateWith matches Generator.Generate for the same type")
	})
}

func TestGenerateFor_ValidateInterpreter(t *testing.T) {
	t.Parallel()

	type CreateUser struct {
		Name  string `json:"name"  validate:"required,min=1,max=100"`
		Email string `json:"email" validate:"required,email"`
		Age   int    `json:"age"   validate:"gte=0,lte=150"`
	}

	s, err := jsonschema.GenerateFor[CreateUser](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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

	_, err := jsonschema.GenerateFor[Config](t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key")
}

func TestGenerateFor_MultiplePointerIndirection(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[**string]())
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

	s, err := jsonschema.GenerateFor[map[string]string](t.Context())
	require.NoError(t, err)
	// Maps should be nullable.
	assert.Equal(t, []string{"null", "object"}, s.Types)
}

func TestGenerateFor_SliceNullable(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[]string](t.Context())
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
					return jsonschema.GenerateFor[[]string](t.Context(), jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"array",
					"items":{"type":"string"}
				}`,
			},
			"map": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[map[string]int](t.Context(), jsonschema.WithNullable(false))
				},
				want: `{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"additionalProperties":{"type":"integer"}
				}`,
			},
			"byteSlice": {
				generate: func() (*jsonschema.Schema, error) {
					return jsonschema.GenerateFor[[]byte](t.Context(), jsonschema.WithNullable(false))
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
					return jsonschema.GenerateFor[recursiveSlice](t.Context(), jsonschema.WithNullable(false))
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

	// Pointer fields produce bare value schemas, none wrapped in anyOf: *int
	// inlines, pointer-to-struct is a bare $ref, and json:",string" yields a
	// plain string.
	t.Run("pointers", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[NullableOptOut](t.Context(), jsonschema.WithNullable(false))
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

		s, err := jsonschema.GenerateFor[[]string](t.Context(), jsonschema.WithNullable(true))
		require.NoError(t, err)
		assert.Equal(t, []string{"null", "array"}, s.Types)
	})
}

func TestGenerateFor_Draft7_RefWithAnnotation(t *testing.T) {
	t.Parallel()

	type Container struct {
		Home Address `json:"home" jsonschema:"description=Home address"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.GenerateFor[Ordered](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
	require.NoError(t, err)

	assert.JSONEq(t, `8080`, string(s.Properties["port"].Default))
	assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
}

func TestGenerateFor_JSONSchemaTag_Const(t *testing.T) {
	t.Parallel()

	type Config struct {
		Version string `json:"version" jsonschema:"const=v1"`
	}

	s, err := jsonschema.GenerateFor[Config](t.Context())
	require.NoError(t, err)

	constVal := *s.Properties["version"].Const
	assert.Equal(t, "v1", constVal)
}

// NonStructExtender is a named non-struct type implementing JSONSchemaExtender.
type NonStructExtender []string

func (NonStructExtender) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Description = "A list of tags"

	return nil
}

func TestGenerateFor_NonStructExtender(t *testing.T) {
	t.Parallel()

	// Non-struct types implementing JSONSchemaExtender should have
	// the extender called and be extracted to $defs.
	type Container struct {
		Tags NonStructExtender `json:"tags"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context())
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
	s, err := jsonschema.GenerateFor[NonStructExtender](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "A list of tags", s.Description)
	assert.Equal(t, []string{"null", "array"}, s.Types)
}

// NonStructProvider is a named non-struct type implementing JSONSchemaProvider.
type NonStructProvider int

func (NonStructProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type: "integer",
		Enum: []any{1, 2, 3},
	}, nil
}

func TestGenerateFor_NonStructProvider_ExtractedToDefs(t *testing.T) {
	t.Parallel()

	type Container struct {
		Level NonStructProvider `json:"level"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
	require.NoError(t, err)

	// No $defs: named primitive types are inlined.
	assert.Nil(t, s.Defs)
	assert.Equal(t, "integer", s.Properties["p1"].Type)
	assert.Equal(t, "integer", s.Properties["p2"].Type)
}

func TestGenerateFor_RecursiveType_WithDefinitionsFalse(t *testing.T) {
	t.Parallel()

	// Cyclic types must still emit $defs/$ref even when WithDefinitions(false).
	s, err := jsonschema.GenerateFor[RecursiveNode](t.Context(), jsonschema.WithDefinitions(false))
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

	// Draft has a doc comment; the full text is extracted, so pin its
	// opening sentence rather than the whole comment.
	s, err := jsonschema.GenerateFor[jsonschema.Draft](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	assert.True(t,
		strings.HasPrefix(s.Description, "Draft represents a JSON Schema draft version."),
		"description should start with the doc comment's opening sentence, got: %s", s.Description)
}

func TestGenerateFor_WithComments_StructDescription(t *testing.T) {
	t.Parallel()

	// FieldContext has a type-level doc comment; the full text is extracted,
	// so pin its opening words rather than the whole comment.
	s, err := jsonschema.GenerateFor[jsonschema.FieldContext](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	assert.True(t,
		strings.HasPrefix(s.Description, "FieldContext provides context about a struct field"),
		"description should start with the doc comment's opening words, got: %s", s.Description)
}

func TestGenerateFor_BigRatAndBigFloat(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		want     string
	}{
		"big.Rat": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Rat](t.Context()) },
			want:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string","pattern":"^-?[0-9]+(/[0-9]+)?$"}`,
		},
		"big.Float": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Float](t.Context()) },
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

	_, err := jsonschema.Generate(t.Context(), reflect.TypeFor[unsafe.Pointer]())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

func TestGenerate_NilType(t *testing.T) {
	t.Parallel()

	// A nil reflect.Type carries no kind to reflect on; it is reported through
	// the error contract rather than panicking inside reflection.
	_, err := jsonschema.Generate(t.Context(), nil)
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

func TestGenerateFor_Complex64(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.GenerateFor[complex64](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

// NamedTime is a named type wrapping time.Time. It should NOT get the
// time.Time built-in override (format: "date-time").
type NamedTime time.Time

func TestGenerateFor_NamedTypeWrappingBuiltinNoOverride(t *testing.T) {
	t.Parallel()

	// NamedTime wraps time.Time but is a distinct type; it should NOT get
	// the time.Time override. Since time.Time is a struct, NamedTime will
	// also be reflected as a struct.
	s, err := jsonschema.GenerateFor[NamedTime](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// json:",string" on a slice should be silently ignored. The schema is
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
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
// Provider should take priority; Extender should NOT be called.
type BothProviderAndExtender struct {
	Value string `json:"value"`
}

func (BothProviderAndExtender) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "from provider",
	}, nil
}

func (BothProviderAndExtender) JSONSchemaExtend(
	_ context.Context,
	_ jsonschema.TypeContext,
	s *jsonschema.Schema,
) error {
	s.Description = "from extender"

	return nil
}

func TestGenerateFor_ProviderTakesPriorityOverExtender(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[BothProviderAndExtender](t.Context())
	require.NoError(t, err)

	// Provider takes priority: description should be "from provider", not "from extender".
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
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
	s, err := jsonschema.GenerateFor[alpha.Widget](
		t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
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

	s, err := jsonschema.GenerateFor[Config](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	assert.Equal(t, new(5), s.Properties["strict"].MinLength)
	assert.Equal(t, new(3), s.Properties["loose"].MinLength)
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[slog.Level]())
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

//nolint:nilnil // A nil schema with a nil error is the documented unrestricted-schema answer.
func (NilProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return nil, nil
}

func TestGenerateFor_JSONSchemaProviderReturnsNil(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[NilProvider](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	// Nil return → unrestricted schema.
	assert.JSONEq(t, `{"$schema":"https://json-schema.org/draft/2020-12/schema"}`, string(got))
}

func TestGenerateFor_PointerToJSONRawMessage(t *testing.T) {
	t.Parallel()

	// *json.RawMessage → {} (unrestricted, not nullable-wrapped).
	s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[*json.RawMessage]())
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
	s, err := jsonschema.GenerateFor[PointerReceiverMarshaler](t.Context())
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
	require.NoError(t, err)

	// No $defs. Named composite types are inlined.
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

	s, err := jsonschema.GenerateFor[map[TextMarshalerKey]string](t.Context())
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

	s, err := jsonschema.GenerateFor[Config](t.Context())
	require.NoError(t, err)

	// Integer examples should be parsed as integers (precision-preserving).
	assert.Equal(t, []any{int64(8080), int64(3000), int64(443)}, s.Properties["port"].Examples)
	// String examples should be strings.
	assert.Equal(t, []any{"debug", "release"}, s.Properties["mode"].Examples)
}

func TestGenerateFor_ExtenderDescriptionPreservedWithComments(t *testing.T) {
	t.Parallel()

	// JSONSchemaExtend runs after comment extraction, so the extender's
	// description takes precedence over the AST doc comment.
	s, err := jsonschema.GenerateFor[NonStructExtender](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)

	// The extender sets Description to "A list of tags"; this must not be
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
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

	s, err := jsonschema.GenerateFor[Container](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
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

func TestGenerateFor_Draft7_RefWithInterpreterComment(t *testing.T) {
	t.Parallel()

	// A bare $ref field that a tag interpreter decorates with $comment (a
	// non-constraint annotation isEmptySchema ignores) must still be wrapped in
	// allOf under Draft-07. Otherwise the comment is a sibling of $ref, which a
	// Draft-07 consumer ignores; wrapping moves $ref into allOf and keeps the
	// comment on the outer schema.
	type Container struct {
		Level NonStructProvider `json:"level" note:"x"`
	}

	interp := jsonschema.TagInterpreterFunc(
		func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
			field.Schema.Comment = "a note"

			return nil
		},
	)

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("note", interp),
	)
	require.NoError(t, err)

	field := s.Properties["level"]
	require.Empty(t, field.Ref, "the $ref must be wrapped, not left as a bare $comment sibling")
	require.Len(t, field.AllOf, 1)
	assert.Equal(t, "#/definitions/NonStructProvider", field.AllOf[0].Ref)
	assert.Equal(t, "a note", field.Comment, "the $comment survives on the outer schema")
}

func TestGenerateFor_Draft7_NullableRefWithInterpreterConst(t *testing.T) {
	t.Parallel()

	// A nullable ($ref) field whose value branch carries an interpreter-set
	// const must wrap that inner branch in allOf under Draft-07. Otherwise the
	// const is a $ref sibling the Draft-07 validator ignores, and a value that
	// violates it is wrongly accepted.
	type Container struct {
		Level *NonStructProvider `json:"level" validate:"eq=2"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	// The const landed on the inner value branch, which must be allOf-wrapped so
	// the $ref does not shadow it.
	field := s.Properties["level"]
	require.Len(t, field.AnyOf, 2)

	inner := field.AnyOf[0]
	require.Empty(t, inner.Ref, "inner $ref must be wrapped in allOf, not left as a const sibling")
	require.Len(t, inner.AllOf, 1)
	assert.Equal(t, "#/definitions/NonStructProvider", inner.AllOf[0].Ref)
	require.NotNil(t, inner.Const)

	// The generated schema, validated by this package, must reject a value that
	// violates the const and accept the one that satisfies it.
	err = jsonschema.Validate(t.Context(), s, map[string]any{"level": 1.0})
	require.Error(t, err, "level=1 violates the eq=2 const and must be rejected")

	err = jsonschema.Validate(t.Context(), s, map[string]any{"level": 2.0})
	require.NoError(t, err, "level=2 satisfies the const")
}

// selfPointer is a self-referential pointer type: its element type is itself,
// so reflect reports t.Elem() == t.
type selfPointer *selfPointer

func TestGenerateFor_SelfReferentialPointerTypeDoesNotHang(t *testing.T) {
	t.Parallel()

	// Following pointers on selfPointer must terminate (t.Elem() == t) rather
	// than spinning forever. Generation ends with an unsupported-type error.
	_, err := jsonschema.Generate(t.Context(), reflect.TypeFor[selfPointer]())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

// cyclicPointerA and cyclicPointerB form a multi-step pointer cycle: neither
// type's Elem is itself, so the single-step elem == t guard never fires.
type (
	cyclicPointerA *cyclicPointerB
	cyclicPointerB *cyclicPointerA
)

func TestGenerateFor_MultiStepPointerCycleDoesNotHang(t *testing.T) {
	t.Parallel()

	// Following pointers through a mutually recursive pair must terminate on the
	// repeated type rather than spinning forever, ending in an unsupported-type
	// error like the single-step case.
	_, err := jsonschema.Generate(t.Context(), reflect.TypeFor[cyclicPointerA]())
	require.ErrorIs(t, err, jsonschema.ErrUnsupportedType)
}

// WithTypeSchemaProvider implements JSONSchemaProvider but will be overridden
// by WithTypeSchema.
type WithTypeSchemaProvider struct {
	Value string `json:"value"`
}

func (WithTypeSchemaProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "from provider",
	}, nil
}

func TestGenerateFor_WithTypeSchemaOverridesProvider(t *testing.T) {
	t.Parallel()

	// WithTypeSchema has the highest priority, overriding even JSONSchemaProvider.
	override := &jsonschema.Schema{
		Type:        "object",
		Description: "from override",
	}
	s, err := jsonschema.GenerateFor[WithTypeSchemaProvider](t.Context(),
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
	// {type: string} directly, not a $ref, and the type's own schema is not
	// generated at all, so no orphan $defs entry is left behind.
	type Container struct {
		S Status `json:"s,string"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context())
	require.NoError(t, err)

	// The field schema should be {type: string}, NOT a $ref to Status.
	assert.Equal(t, "string", s.Properties["s"].Type)
	assert.Empty(t, s.Properties["s"].Ref)
	assert.NotContains(t, s.Defs, "Status",
		"the overridden type must not leave an orphan $defs entry")
}

func TestGenerateFor_NullablePointerJSONStringConstAcceptsNull(t *testing.T) {
	t.Parallel()

	// A nullable pointer carrying json:",string" generates the type-list
	// {"type":["null","string"]} shape. A const/enum tag on it must land on the
	// non-null branch so the permitted null is not rejected, mirroring the anyOf
	// nullable shape.
	type Container struct {
		F *int `json:"f,string" jsonschema:"const=5"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context())
	require.NoError(t, err)

	field := s.Properties["f"]
	require.NotNil(t, field)
	require.Len(t, field.AnyOf, 2)
	assert.Nil(t, field.Const, "const must not sit on the nullable wrapper")

	validator, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)

	assert.NoError(t, validator.ValidateJSON(t.Context(), []byte(`{"f": null}`)),
		"a null pointer is permitted")
	assert.NoError(t, validator.ValidateJSON(t.Context(), []byte(`{"f": "5"}`)),
		"the string-encoded const is permitted")
	assert.Error(t, validator.ValidateJSON(t.Context(), []byte(`{"f": "6"}`)),
		"a different value is rejected")
}

func TestGenerateFor_AllofPrefixFieldNotMisclassified(t *testing.T) {
	t.Parallel()

	// A user field whose JSON name happens to start with the synthetic allOf
	// composition marker must be treated as an ordinary property, not as an
	// embedded composition.
	type T struct {
		Normal string `json:"normal"`
		Weird  string `json:"__allof__Foo__0"`
	}

	s, err := jsonschema.GenerateFor[T](t.Context())
	require.NoError(t, err)

	require.Contains(t, s.Properties, "__allof__Foo__0",
		"a field named like the synthetic marker stays a normal property")
	assert.Equal(t, "string", s.Properties["__allof__Foo__0"].Type)
	assert.Empty(t, s.AllOf, "no spurious allOf composition is created")
}

func TestGenerateFor_AllofKeyCollisionKeepsComposition(t *testing.T) {
	t.Parallel()

	// A user field whose JSON name equals the synthetic key of an allOf-composed
	// embed at the same depth must not shadow the composition. The composition's
	// key lives in a namespace disjoint from real JSON names, so both survive:
	// the embed composes via allOf and the field stays a normal property.
	type T struct {
		allOfEmbed        // composes via allOf -> synthetic key __allof__allOfEmbed__0
		Weird      string `json:"__allof__allOfEmbed__0"`
	}

	s, err := jsonschema.GenerateFor[T](t.Context())
	require.NoError(t, err)

	require.Len(t, s.AllOf, 1, "the allOf composition survives the name collision")
	assert.Equal(t, "#/$defs/allOfEmbed", s.AllOf[0].Ref)
	require.Contains(t, s.Properties, "__allof__allOfEmbed__0",
		"the colliding user field stays a normal property")
	assert.Equal(t, "string", s.Properties["__allof__allOfEmbed__0"].Type)
}

// allOfEmbed implements JSONSchemaProvider, so it is composed via allOf rather
// than having its fields promoted.
//
//nolint:unused // Embedded into allOfDupOuter; exercised via reflection.
type allOfEmbed struct {
	X int `json:"x"`
}

//nolint:unused // Provider method invoked via reflection during generation.
func (allOfEmbed) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:       "object",
		Properties: map[string]*jsonschema.Schema{"x": {Type: "integer"}},
		Required:   []string{"x"},
	}, nil
}

// allOfWrapM and allOfWrapN each embed allOfEmbed so it reaches allOfDupOuter
// twice at the same depth.
//
//nolint:unused // Routes allOfEmbed to allOfDupOuter; exercised via reflection.
type allOfWrapM struct{ allOfEmbed }

//nolint:unused // Routes allOfEmbed to allOfDupOuter; exercised via reflection.
type allOfWrapN struct{ allOfEmbed }

type allOfDupOuter struct {
	allOfWrapM //nolint:unused // Embedded only; exercised via reflection.
	allOfWrapN //nolint:unused // Embedded only; exercised via reflection.
}

func TestGenerateFor_SameDepthDuplicateAllOfEmbedAnnihilated(t *testing.T) {
	t.Parallel()

	// The allOfEmbed type reaches allOfDupOuter twice at the same depth (through
	// wrapM and wrapN). Encoding/json annihilates its promoted field, so the
	// type marshals to {}. The schema must annihilate the composition too rather
	// than emit two allOf branches requiring x, which would reject the type's
	// own output.
	s, err := jsonschema.GenerateFor[allOfDupOuter](t.Context())
	require.NoError(t, err)

	assert.Empty(t, s.AllOf,
		"a provider type embedded twice at one depth is annihilated, like encoding/json")
	assert.NotContains(t, s.Defs, "allOfEmbed",
		"the annihilated composition leaves no $defs entry")

	marshaled, err := json.Marshal(allOfDupOuter{
		allOfWrapM{allOfEmbed{X: 10}},
		allOfWrapN{allOfEmbed{X: 20}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, "{}", string(marshaled),
		"encoding/json annihilates the duplicated field")

	v := jsonschema.MustCompile(s)
	assert.NoError(t, v.ValidateJSON(t.Context(), marshaled),
		"the schema accepts the type's own marshaled output")
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithTypeSchema(reflect.TypeFor[Priority](), override),
	)
	require.NoError(t, err)

	// Named non-struct type with WithTypeSchema but no Provider/Extender
	// → still inlined, no $defs.
	assert.Nil(t, s.Defs)
	assert.Equal(t, []any{1, 2, 3}, s.Properties["p1"].Enum)
	assert.Equal(t, []any{1, 2, 3}, s.Properties["p2"].Enum)
}

func TestGenerateFor_WithTypeSchemaNullBearingPointerNotDoubleWrapped(t *testing.T) {
	t.Parallel()

	// A WithTypeSchema override that already accepts null (its type list includes
	// "null") on a pointer field must not be wrapped in a redundant
	// anyOf[override, {"type":"null"}]. The override is emitted as-is, matching
	// the []byte builtin form, since applyNullable leaves a null-bearing schema
	// alone.
	type MyID string

	type Container struct {
		ID *MyID `json:"id"`
	}

	override := &jsonschema.Schema{
		Types: []string{"null", "string"},
	}

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithTypeSchema(reflect.TypeFor[MyID](), override),
	)
	require.NoError(t, err)

	field := s.Properties["id"]
	assert.Nil(t, field.AnyOf, "a null-bearing override must not be wrapped in anyOf")
	assert.Equal(t, []string{"null", "string"}, field.Types)
}

func TestGenerateFor_Draft7_NullableRefWithAnnotation(t *testing.T) {
	t.Parallel()

	// In Draft-07, nullable $ref uses anyOf wrapping. Since the $ref is nested
	// inside anyOf (not at the top level), no allOf wrapping is needed for
	// sibling annotations.
	type Container struct {
		Work *Address `json:"work" jsonschema:"description=Work address"`
	}

	s, err := jsonschema.GenerateFor[Container](t.Context(),
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
	s, err := jsonschema.GenerateFor[jsonschema.FieldContext](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
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

	s, err := jsonschema.GenerateFor[Container](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
	require.NoError(t, err)
	assert.NotNil(t, s)
}

// TextMarshalerWithExtender implements both TextMarshaler (producing "string")
// and JSONSchemaExtender (adding enum constraints). The extender runs after
// base type reflection, so it sees and augments the "string" schema.
type TextMarshalerWithExtender int

func (TextMarshalerWithExtender) MarshalText() ([]byte, error) { return nil, nil }

func (TextMarshalerWithExtender) JSONSchemaExtend(
	_ context.Context,
	_ jsonschema.TypeContext,
	s *jsonschema.Schema,
) error {
	s.Enum = []any{"active", "inactive", "pending"}

	return nil
}

func TestGenerateFor_TextMarshalerWithExtender(t *testing.T) {
	t.Parallel()

	// TextMarshaler produces {"type": "string"}, then JSONSchemaExtend
	// should add enum values. The type also implements JSONSchemaExtender,
	// so it should be extracted to $defs when used in a struct field.
	s, err := jsonschema.GenerateFor[TextMarshalerWithExtender](t.Context())
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
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

	s, err := jsonschema.GenerateFor[Event](t.Context())
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

	s, err := jsonschema.GenerateFor[uint64](t.Context())
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
	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), []byte("18446744073709551615")),
		"MaxUint64 must satisfy the uint64 schema")
}

// TestInt64BoundExclusiveMaximum covers the int64 bounds. MinInt64 (-2^63) is
// representable as float64, so the minimum stays inclusive; MaxInt64 (2^63-1) is
// not, so the upper bound uses an exclusive maximum of 2^63 to admit the
// boundary value exactly.
func TestInt64BoundExclusiveMaximum(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[int64](t.Context())
	require.NoError(t, err)

	require.NotNil(t, s.Minimum, "int64 should have an inclusive minimum bound")
	assert.InDelta(t, float64(math.MinInt64), *s.Minimum, 0, "minimum should be MinInt64")

	require.Nil(t, s.Maximum, "int64 should not use an inclusive maximum")
	require.NotNil(t, s.ExclusiveMaximum, "int64 should express an exclusive maximum bound")
	assert.InDelta(t, float64(1<<63), *s.ExclusiveMaximum, 0, "exclusive maximum should be 2^63")

	// Both boundary values must validate against their own schema.
	v, err := jsonschema.Compile(t.Context(), s)
	require.NoError(t, err)
	assert.NoError(t, v.ValidateJSON(t.Context(), []byte("9223372036854775807")),
		"MaxInt64 must satisfy the int64 schema")
	assert.NoError(t, v.ValidateJSON(t.Context(), []byte("-9223372036854775808")),
		"MinInt64 must satisfy the int64 schema")
}

func TestArrayGenerationUsesPrefixItems(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[[3]string](t.Context())
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
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

	s, err := jsonschema.Generate(t.Context(), reflect.TypeFor[Outer](), jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)

	got := marshalSchema(t, s)

	// A plain embedded struct has its fields promoted rather than allOf-composed,
	// so additionalProperties stays on the schema and the closed-schema guarantee
	// holds in Draft-07.
	assert.Contains(t, got, `"additionalProperties"`,
		"Draft7 with promoted embed should retain additionalProperties")
}

type providerMutationTestType struct{}

func (providerMutationTestType) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &sharedProviderSchema, nil
}

var (
	sharedProviderSchema = jsonschema.Schema{
		Type:        "string",
		Description: "original",
	}

	errExtendUnavailable = errors.New("constraint source unavailable")

	errProviderUnavailable = errors.New("schema document unavailable")

	// The aliasingContainerTypes slice lists the non-sub-schema reference types a
	// registered [jsonschema.WithTypeSchema] override carries. Generation must
	// hand out schemas whose containers do not alias the registered override, or
	// an in-place append/assign by an extender, interpreter, or caller would
	// corrupt the override across Generate calls.
	aliasingContainerTypes = []reflect.Type{
		reflect.TypeFor[[]any](),
		reflect.TypeFor[[]string](),
		reflect.TypeFor[map[string]bool](),
		reflect.TypeFor[map[string][]string](),
		reflect.TypeFor[json.RawMessage](),
		reflect.TypeFor[*any](),
		reflect.TypeFor[map[string]any](),
	}
)

func TestProviderSchemaIsolatedAcrossCalls(t *testing.T) {
	t.Parallel()

	// First generation call.
	s1, err := jsonschema.GenerateFor[providerMutationTestType](t.Context())
	require.NoError(t, err)

	desc1 := s1.Description

	// Second generation call -- should not see mutations from first.
	s2, err := jsonschema.GenerateFor[providerMutationTestType](t.Context())
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

	s, err := jsonschema.GenerateFor[alpha.ProviderSingleton](t.Context(),
		jsonschema.WithDescriptionProvider(jsonschema.NewGoCommentProvider()),
	)
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
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Rat](t.Context()) },
		},
		"big.Float": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[big.Float](t.Context()) },
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
	require.NoError(t, err)

	_ = marshalSchema(t, s)

	// A nil *Embedded causes encoding/json to omit embedded fields.
	// The schema should reflect this optionality.
	data := `{"name": "test"}`
	err = validateJSON(t.Context(), s, []byte(data))
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

	s, err := jsonschema.GenerateFor[Container](t.Context())
	require.NoError(t, err)

	// Check that no $defs key contains a space.
	for name := range s.Defs {
		assert.NotContains(t, name, " ",
			"$defs key %q should not contain spaces", name)
	}
}

func TestWithNamerNilRestoresDefault(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Name string `json:"name"`
	}

	type Outer struct {
		A Inner `json:"a"`
		B Inner `json:"b"`
	}

	// A nil namer after a custom one restores the default short-name namer.
	s, err := jsonschema.GenerateFor[Outer](t.Context(),
		jsonschema.WithNamer(jsonschema.NamerFunc(func(jsonschema.TypeContext) string { return "Custom" })),
		jsonschema.WithNamer(nil),
	)
	require.NoError(t, err)
	assert.Contains(t, s.Defs, "Inner")
	assert.NotContains(t, s.Defs, "Custom")
}

func TestWithTagInterpreterNilDoesNotPanic(t *testing.T) {
	t.Parallel()

	type Simple struct {
		Name string `json:"name"`
	}

	// A nil interpreter or an empty key is ignored, not a panic.
	assert.NotPanics(t, func() {
		//nolint:errcheck // Asserting only that the call does not panic.
		_, _ = jsonschema.GenerateFor[Simple](t.Context(),
			jsonschema.WithTagInterpreter("inspect", nil),
			jsonschema.WithTagInterpreter("", jsonschema.TagInterpreterFunc(
				func(context.Context, jsonschema.FieldContext, jsonschema.Tag) error { return nil })))
	}, "WithTagInterpreter with a nil interpreter or empty key should not panic")
}

func TestJSONStringOnStructLeavesNoOrphanedDefs(t *testing.T) {
	t.Parallel()

	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Data Inner `json:"data,string"` //nolint:staticcheck // Intentional: exercises ",string" on a non-applicable type.
	}

	s, err := jsonschema.GenerateFor[Outer](t.Context())
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
	s, err := jsonschema.GenerateFor[mutualA](t.Context())
	require.NoError(t, err)

	require.NotNil(t, s.Defs)
	assert.Contains(t, s.Defs, "mutualA", "mutualA must remain in $defs (referenced by mutualB)")
	assert.Contains(t, s.Defs, "mutualB", "mutualB must remain in $defs")

	// A dangling $ref would surface as a resolve error during validation.
	err = jsonschema.Validate(t.Context(), s, map[string]any{"b": map[string]any{"a": nil}})
	require.NoError(t, err, "generated schema must resolve with no dangling $ref")
}

func TestSliceAndPointerSliceProduceIdenticalSchemas(t *testing.T) {
	t.Parallel()

	// Slices always emit ["null","array"], so []T and *[]T produce identical
	// schemas.
	sliceSchema, err := jsonschema.GenerateFor[[]string](t.Context())
	require.NoError(t, err)

	ptrSliceSchema, err := jsonschema.GenerateFor[*[]string](t.Context())
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
	mapSchema, err := jsonschema.GenerateFor[map[string]string](t.Context())
	require.NoError(t, err)

	ptrMapSchema, err := jsonschema.GenerateFor[*map[string]string](t.Context())
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

	s, err := jsonschema.GenerateFor[Person](t.Context())
	require.NoError(t, err)

	// Valid instance should pass.
	valid := `{"name":"Alice","age":30,"address":{"street":"123 Main","city":"NY"}}`
	err = validateJSON(t.Context(), s, []byte(valid))
	require.NoError(t, err, "valid instance should pass generated schema")

	// Invalid instance (extra property) should fail.
	invalid := `{"name":"Alice","age":30,"address":{"street":"123 Main","city":"NY"},"extra":"bad"}`
	err = validateJSON(t.Context(), s, []byte(invalid))
	require.Error(t, err, "instance with extra properties should fail generated schema")

	// Invalid type should fail.
	wrongType := `{"name":42,"age":30,"address":{"street":"123 Main","city":"NY"}}`
	err = validateJSON(t.Context(), s, []byte(wrongType))
	require.Error(t, err, "instance with wrong type should fail generated schema")
}

// panickingProvider has an empty slice field that its JSONSchema method
// indexes, so calling it on the zero value panics with an out-of-range access.
type panickingProvider struct {
	names []string
}

func (p panickingProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Description: p.names[0]}, nil // out of range on zero value
}

// embeddedProvider supplies a JSONSchema method that an outer type can both
// promote and shadow with its own direct method.
//
//nolint:unused // Embedded into outerWithDirectMethods to set up method shadowing, exercised via reflection.
type embeddedProvider struct{}

//nolint:unused // Shadowed by outerWithDirectMethods.JSONSchema; present only to create the shadowing case.
func (embeddedProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "embedded"}, nil
}

// outerWithDirectMethods declares JSONSchema directly and also embeds a type
// that provides it; the direct method shadows the promoted one.
type outerWithDirectMethods struct {
	embeddedProvider //nolint:unused // Embedded to exercise direct-method-wins-over-promoted shadowing.
}

func (outerWithDirectMethods) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "outer"}, nil
}

func TestProviderPanicOnZeroValueWrapsErrProviderPanic(t *testing.T) {
	t.Parallel()

	// A JSONSchema() method that dereferences a nil pointer field panics when
	// called on the zero value during generation. The generator recovers the
	// panic and returns an error wrapping ErrProviderPanic rather than crashing.
	_, err := jsonschema.GenerateFor[panickingProvider](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrProviderPanic)
}

// failingProvider returns an error from JSONSchema, the ordinary failure
// channel for a provider that cannot produce its schema.
type failingProvider struct{}

func (failingProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return nil, errProviderUnavailable
}

// TestProviderErrorAbortsGeneration proves a provider error aborts
// generation and reaches the caller wrapped with the failing type and
// method, matching the JSONSchemaExtend error path.
func TestProviderErrorAbortsGeneration(t *testing.T) {
	t.Parallel()

	_, err := jsonschema.GenerateFor[failingProvider](t.Context())
	require.ErrorIs(t, err, errProviderUnavailable)
	assert.Contains(t, err.Error(), "JSONSchema")
	assert.Contains(t, err.Error(), "failingProvider")
}

func TestDirectMethodWinsOverEmbeddedProvider(t *testing.T) {
	t.Parallel()

	// When an outer type declares JSONSchema() directly and also embeds a type
	// that provides it, Go's method resolution gives the outer type's directly-
	// declared method precedence. The generator honors that: the outer schema
	// wins over the embedded provider's schema.
	s, err := jsonschema.GenerateFor[outerWithDirectMethods](t.Context())
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
	require.NoError(t, err)

	// Validate a conforming instance.
	valid := `{"value":"hello","name":"test"}`
	err = validateJSON(t.Context(), s, []byte(valid))
	require.NoError(t, err, "valid embedded struct instance should pass")

	// Validate a non-conforming instance.
	invalid := `{"value":"hello","name":"test","extra":"bad"}`
	err = validateJSON(t.Context(), s, []byte(invalid))
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
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

	s, err := jsonschema.GenerateFor[Outer](t.Context())
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

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	require.Contains(t, s.Defs, "alpha_Widget")
	require.Contains(t, s.Defs, "beta_Widget")
	require.NotContains(t, s.Defs, "Widget", "colliding bare name must be replaced")

	// $ref targets are rewritten to the disambiguated names.
	require.Equal(t, "#/$defs/alpha_Widget", s.Properties["a"].Ref)
	require.Equal(t, "#/$defs/beta_Widget", s.Properties["b"].Ref)
}

func TestGenerateFor_GenericAnonymousArgRefIsURISafe(t *testing.T) {
	t.Parallel()

	// A generic instantiated over an anonymous struct yields a reflect name
	// carrying spaces, braces, and tag quotes. The generated $defs key and $ref
	// must contain only RFC 3986 fragment-safe characters so external validators
	// can resolve them, not just this package's own resolver.
	type Root struct {
		B alpha.Box[struct {
			A int `json:"a"`
		}] `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	ref := s.Properties["b"].Ref
	require.NotEmpty(t, ref)

	// The pointer token after "#/$defs/" is the def key; it must be free of
	// characters that are unsafe in an RFC 3986 URI fragment or RFC 6901 token.
	const unsafeChars = " {}\"[],~*();:"

	token := strings.TrimPrefix(ref, "#/$defs/")
	assert.False(t, strings.ContainsAny(token, unsafeChars+"/"),
		"generated $ref token %q must not contain URI-unsafe characters", token)

	for key := range s.Defs {
		assert.False(t, strings.ContainsAny(key, unsafeChars),
			"generated $defs key %q must not contain URI-unsafe characters", key)
	}
}

func TestGenerateFor_TypeOverrideDropsRefUnderCollision(t *testing.T) {
	t.Parallel()

	// Field C is a $defs-extracted type whose bare $ref is dropped by a type=
	// override. A sibling Widget collision forces disambiguateDefs to run; it
	// must not re-point C's now-cleared refRecord, which would re-emit a stale
	// $ref next to the override and yield an unsatisfiable {"type","$ref"}.
	type Root struct {
		A alpha.Widget `json:"a"`
		B beta.Widget  `json:"b"`
		C alpha.Widget `json:"c" jsonschema:"type=integer"`
	}

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	require.Contains(t, s.Defs, "alpha_Widget")
	require.Contains(t, s.Defs, "beta_Widget")

	assert.Equal(t, "integer", s.Properties["c"].Type)
	assert.Empty(t, s.Properties["c"].Ref,
		"a type= override must not leave a stale $ref after disambiguation")
}

type jsonMarshalerOnly struct {
	Value int `json:"value"`
}

func (jsonMarshalerOnly) MarshalJSON() ([]byte, error) { return []byte(`"opaque"`), nil }

func TestGenerateFor_JSONMarshalerNotConsulted(t *testing.T) {
	t.Parallel()

	// A type implementing only json.Marshaler (not encoding.TextMarshaler) falls
	// through to kind-based reflection; MarshalJSON does not influence the schema.
	s, err := jsonschema.GenerateFor[jsonMarshalerOnly](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "object", s.Type)
	assert.Contains(t, s.Properties, "value")
}

type ptrProvider struct{}

func (*ptrProvider) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string", Format: "ptr-provider-marker"}, nil
}

func TestGenerateFor_ProviderPointerReceiver(t *testing.T) {
	t.Parallel()

	// JSONSchemaProvider declared on a pointer receiver still applies.
	s, err := jsonschema.GenerateFor[ptrProvider](t.Context())
	require.NoError(t, err)

	raw, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ptr-provider-marker")
	assert.NotContains(t, string(raw), `"object"`, "provider must bypass reflection")
}

type ptrExtender struct {
	Name string `json:"name"`
}

func (*ptrExtender) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Description = "ptr-extender-marker"

	return nil
}

func TestGenerateFor_ExtenderPointerReceiver(t *testing.T) {
	t.Parallel()

	// JSONSchemaExtender declared on a pointer receiver still applies.
	s, err := jsonschema.GenerateFor[ptrExtender](t.Context())
	require.NoError(t, err)

	raw, err := json.Marshal(s)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ptr-extender-marker")
	assert.Contains(t, string(raw), "name", "reflection-built properties must survive the extender")
}

// draftAwareProvider supplies a draft-appropriate schema from the TypeContext
// its JSONSchema method receives.
type draftAwareProvider struct{}

func (draftAwareProvider) JSONSchema(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
	if tc.Draft == jsonschema.Draft7 {
		return &jsonschema.Schema{Type: "string", Description: "draft-07"}, nil
	}

	return &jsonschema.Schema{Type: "string", Description: "draft-2020"}, nil
}

func TestJSONSchemaProviderReceivesTypeContext(t *testing.T) {
	t.Parallel()

	// The provider sees the target draft of the generation run, the same
	// TypeContext its registered counterpart receives.
	s7, err := jsonschema.GenerateFor[draftAwareProvider](t.Context(), jsonschema.WithDraft(jsonschema.Draft7))
	require.NoError(t, err)
	assert.Equal(t, "draft-07", s7.Description)

	s20, err := jsonschema.GenerateFor[draftAwareProvider](t.Context())
	require.NoError(t, err)
	assert.Equal(t, "draft-2020", s20.Description)
}

// extenderCtxKey keys the context value ctxAwareExtender reads.
type extenderCtxKey struct{}

// ctxAwareExtender copies a context value into its schema, observing that
// JSONSchemaExtend runs under the Generate call's context.
type ctxAwareExtender struct {
	Value string `json:"value"`
}

func (ctxAwareExtender) JSONSchemaExtend(ctx context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	if v, ok := ctx.Value(extenderCtxKey{}).(string); ok {
		s.Description = v
	}

	return nil
}

func TestJSONSchemaExtenderReceivesContext(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(t.Context(), extenderCtxKey{}, "from the Generate context")

	s, err := jsonschema.GenerateFor[ctxAwareExtender](ctx)
	require.NoError(t, err)
	assert.Equal(t, "from the Generate context", s.Description)
}

type byteSliceNamed []byte

type byteElem byte

type byteSliceOfNamed []byteElem

type marshalByte byte

func (marshalByte) MarshalJSON() ([]byte, error) { return []byte(`"b"`), nil }

type byteSliceOfMarshaler []marshalByte

func TestGenerateFor_NamedByteSlice(t *testing.T) {
	t.Parallel()

	// Encoding/json base64-encodes any byte slice. It chooses that path by the
	// element kind (uint8) rather than the exact type, so named byte slices and
	// slices of named uint8 elements are base64 strings too. The exception is an
	// element type implementing json.Marshaler/encoding.TextMarshaler, which is
	// encoded via that method rather than as base64.
	const base64Schema = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"type":["null","string"],"contentEncoding":"base64"}`

	t.Run("named []byte is base64 string", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceNamed](t.Context())
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, base64Schema, string(got))

		// The schema accepts the type's own serialized form (a base64 string).
		data, err := json.Marshal(byteSliceNamed("hello"))
		require.NoError(t, err)
		require.NoError(t, validateJSON(t.Context(), s, data))
	})

	t.Run("slice of named uint8 is base64 string", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceOfNamed](t.Context())
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, base64Schema, string(got))
	})

	t.Run("uint8 element implementing json.Marshaler is not base64", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[byteSliceOfMarshaler](t.Context())
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

	s, err := jsonschema.GenerateFor[url.URL](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "object", s.Type)
	assert.Contains(t, s.Properties, "Scheme")
	assert.NoError(t, validateJSON(t.Context(), s, doc),
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

	s, err := jsonschema.GenerateFor[bigIntDoc](t.Context())
	require.NoError(t, err)
	assert.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected big.Int's actual serialization: %s", data)
}

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

// Tests for WithDefaultsFrom: seeding root property defaults from an instance
// of the generated type.

// defaultsNested is a nested struct whose marshaled value becomes a
// whole-value default on its top-level property.
type defaultsNested struct {
	Path string `json:"path"`
}

// defaultsConfig exercises the presence rules: a plain field, a field with a
// tag default, omitempty fields, and a nested struct.
type defaultsConfig struct {
	Host   string         `json:"host"`
	Port   int            `json:"port"            jsonschema:"default=80"`
	Debug  bool           `json:"debug,omitempty"`
	Tags   []string       `json:"tags,omitempty"`
	Nested defaultsNested `json:"nested"`
}

// defaultsRecursive references itself, so its root schema stays a $defs entry
// and the defaults land on that definition.
type defaultsRecursive struct {
	Name string             `json:"name"`
	Next *defaultsRecursive `json:"next,omitempty"`
}

// defaultsString marshals to a JSON string, not an object.
type defaultsString string

func TestWithDefaultsFrom(t *testing.T) {
	t.Parallel()

	t.Run("instance values become property defaults", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Host:   "localhost",
				Port:   8080,
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("omitempty zero value leaves default unset", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		// Debug and Tags are zero and omitempty, so encoding/json omits them
		// and their properties carry no default.
		assert.Nil(t, s.Properties["debug"].Default,
			"a key omitted by omitempty contributes no default")
		assert.Nil(t, s.Properties["tags"].Default,
			"a key omitted by omitempty contributes no default")

		// A present omitempty key still contributes its value.
		s, err = jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Debug: true}),
		)
		require.NoError(t, err)
		assert.JSONEq(t, `true`, string(s.Properties["debug"].Default))
	})

	t.Run("instance overwrites tag default", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Port: 8080}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `8080`, string(s.Properties["port"].Default),
			"the instance value wins over the jsonschema tag default")
	})

	t.Run("tag default survives without the option", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context())
		require.NoError(t, err)

		assert.JSONEq(t, `80`, string(s.Properties["port"].Default))
	})

	t.Run("nested struct becomes whole-value default", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		// The nested property is a $ref to its $defs entry; the default sits
		// beside the $ref as the whole marshaled object.
		assert.JSONEq(t, `{"path":"/var/data"}`, string(s.Properties["nested"].Default))
	})

	t.Run("pointer instance", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(&defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("pointer root applies through nullable wrapper", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost", Port: 8080}),
		)
		require.NoError(t, err)

		// A pointer root generates anyOf[{$ref: #/$defs/...}, {type: null}];
		// the defaults resolve through the wrapper and its $ref to the $defs
		// entry the value branch targets.
		require.Len(t, s.AnyOf, 2)
		require.Equal(t, "#/$defs/defaultsConfig", s.AnyOf[0].Ref)

		def := s.Defs["defaultsConfig"]
		require.NotNil(t, def)
		assert.JSONEq(t, `"localhost"`, string(def.Properties["host"].Default))
		assert.JSONEq(t, `8080`, string(def.Properties["port"].Default),
			"the instance value wins over the jsonschema tag default")
	})

	t.Run("pointer root with nullability disabled", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*defaultsConfig](t.Context(),
			jsonschema.WithNullable(false),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost"}),
		)
		require.NoError(t, err)

		// Without the null branch the pointer root inlines like a value root,
		// so the defaults land directly on the root properties.
		assert.JSONEq(t, `"localhost"`, string(s.Properties["host"].Default))
	})

	t.Run("type mismatch", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsNested{Path: "/var/data"}),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance)
	})

	t.Run("nil instance restores the default", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsConfig{Host: "localhost"}),
			jsonschema.WithDefaultsFrom(nil),
		)
		require.NoError(t, err)
		assert.Nil(t, s.Properties["host"].Default,
			"a nil instance clears an earlier registration, seeding no defaults")
	})

	t.Run("non-object marshal", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsString](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsString("hello")),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance,
			"an instance marshaling to a JSON string is not an object")
	})

	t.Run("nil pointer instance marshals to null", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDefaultsFrom((*defaultsConfig)(nil)),
		)
		require.ErrorIs(t, err, jsonschema.ErrInvalidDefaultsInstance,
			"a nil pointer marshals to JSON null, not an object")
	})

	t.Run("Draft-07 wraps a defaulted ref property in allOf", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsConfig](t.Context(),
			jsonschema.WithDraft(jsonschema.Draft7),
			jsonschema.WithDefaultsFrom(defaultsConfig{
				Nested: defaultsNested{Path: "/var/data"},
			}),
		)
		require.NoError(t, err)

		// Draft-07 readers ignore keywords beside $ref, so the default on the
		// definitions-extracted nested property forces the $ref into allOf,
		// the same shape a tag default produces.
		nested := s.Properties["nested"]
		require.NotNil(t, nested)
		assert.Empty(t, nested.Ref)
		require.Len(t, nested.AllOf, 1)
		assert.Equal(t, "#/definitions/defaultsNested", nested.AllOf[0].Ref)
		assert.JSONEq(t, `{"path":"/var/data"}`, string(nested.Default))
	})

	t.Run("self-referential root applies to definition", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[defaultsRecursive](t.Context(),
			jsonschema.WithDefaultsFrom(defaultsRecursive{Name: "head"}),
		)
		require.NoError(t, err)

		// The root stays a $ref because the type references itself; the
		// defaults land on the $defs entry, shared by every occurrence.
		require.NotEmpty(t, s.Ref)

		def := s.Defs["defaultsRecursive"]
		require.NotNil(t, def)
		assert.JSONEq(t, `"head"`, string(def.Properties["name"].Default))
	})
}

// isAliasingContainerType reports whether t is one of the shared-container
// types the guard tracks.
func isAliasingContainerType(t reflect.Type) bool {
	return slices.Contains(aliasingContainerTypes, t)
}

// TestTypeSchemaOverrideContainersUnaliased is a maintenance guard ensuring
// every exported Schema field of a container type is unaliased by the
// [jsonschema.WithTypeSchema] generation path. For each such field the test
// registers an override with fresh backing storage, generates a schema from
// it, and asserts the field's backing pointer changed -- proving a write
// through the generated schema cannot reach the registered override. When
// upstream adds a new field of one of these types and the override copy does
// not cover it, the test fails rather than silently aliasing.
func TestTypeSchemaOverrideContainersUnaliased(t *testing.T) {
	t.Parallel()

	type overrideProbe struct{}

	probeType := reflect.TypeFor[overrideProbe]()
	schemaType := reflect.TypeFor[jsonschema.Schema]()

	var (
		covered   []string
		uncovered []string
	)

	for field := range schemaType.Fields() {
		if !field.IsExported() || !isAliasingContainerType(field.Type) {
			continue
		}

		override := populatedSchema(t, field)

		// Definitions are disabled so the generated root is the override copy
		// itself rather than a $ref into $defs.
		got, err := jsonschema.Generate(t.Context(), probeType,
			jsonschema.WithTypeSchema(probeType, override),
			jsonschema.WithDefinitions(false),
		)
		require.NoError(t, err, "generate with override for field %s", field.Name)

		if containerPointer(t, fieldValue(override, field)) != containerPointer(t, fieldValue(got, field)) {
			covered = append(covered, field.Name)

			continue
		}

		uncovered = append(uncovered, field.Name)
	}

	sort.Strings(uncovered)
	require.Empty(t, uncovered,
		"exported Schema field(s) %v of a container type stay aliased between a WithTypeSchema "+
			"override and the generated schema; a write through the generated schema would corrupt "+
			"the override across Generate calls",
		uncovered)

	// Sanity: the guard actually inspected the fields it is meant to protect.
	require.NotEmpty(t, covered, "guard found no container-typed Schema fields to check; the type set is stale")
	assert.Contains(t, covered, "Examples", "Examples is the empirically reproduced aliasing case and must be covered")
}

// populatedSchema returns a *Schema with the given field set to a fresh,
// non-empty value of the field's type, so containerPointer can read a stable
// backing pointer. Slices and maps gain one element; the *any pointer
// addresses a fresh value.
func populatedSchema(t *testing.T, field reflect.StructField) *jsonschema.Schema {
	t.Helper()

	s := &jsonschema.Schema{}
	fv := reflect.ValueOf(s).Elem().FieldByIndex(field.Index)

	switch field.Type.Kind() {
	case reflect.Slice:
		elem := field.Type.Elem()
		slice := reflect.MakeSlice(field.Type, 1, 1)
		slice.Index(0).Set(sampleElem(t, elem))
		fv.Set(slice)

	case reflect.Map:
		m := reflect.MakeMapWithSize(field.Type, 1)
		m.SetMapIndex(sampleElem(t, field.Type.Key()), sampleElem(t, field.Type.Elem()))
		fv.Set(m)

	case reflect.Pointer:
		p := reflect.New(field.Type.Elem())
		fv.Set(p)

	default:
		t.Fatalf("unexpected container kind %s for field %s", field.Type.Kind(), field.Name)
	}

	return s
}

// sampleElem returns a non-zero value for a slice/map element or key type used
// to populate a guard schema.
func sampleElem(t *testing.T, et reflect.Type) reflect.Value {
	t.Helper()

	switch et.Kind() {
	case reflect.String:
		return reflect.ValueOf("x").Convert(et)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(et)
	case reflect.Uint8: // json.RawMessage element
		return reflect.ValueOf(byte('x')).Convert(et)
	case reflect.Interface: // []any element
		return reflect.ValueOf(any("x"))
	case reflect.Slice: // map[string][]string value
		s := reflect.MakeSlice(et, 1, 1)
		s.Index(0).Set(sampleElem(t, et.Elem()))

		return s

	default:
		t.Fatalf("unexpected element kind %s", et.Kind())

		return reflect.Value{}
	}
}

// fieldValue reads the named field off a *Schema.
func fieldValue(s *jsonschema.Schema, field reflect.StructField) reflect.Value {
	return reflect.ValueOf(s).Elem().FieldByIndex(field.Index)
}

// containerPointer returns the backing pointer that identifies a slice, map,
// or pointer value, so two values can be compared for shared storage.
func containerPointer(t *testing.T, v reflect.Value) uintptr {
	t.Helper()

	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
		return v.Pointer()
	default:
		t.Fatalf("containerPointer: unexpected kind %s", v.Kind())

		return 0
	}
}

// Tests for WithRootTitle: deriving the root schema's title from the
// generated root type's name.

// rootTitleStruct is a named root type for title derivation.
type rootTitleStruct struct {
	Name string `json:"name"`
}

// rootTitleRecursive references itself, so its root schema stays a $defs
// (definitions for Draft-07) entry referenced from the root.
type rootTitleRecursive struct {
	Name string              `json:"name"`
	Next *rootTitleRecursive `json:"next,omitempty"`
}

// rootTitleExtender sets its own title via JSONSchemaExtend.
type rootTitleExtender struct {
	Name string `json:"name"`
}

func (rootTitleExtender) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Title = "Extended"

	return nil
}

func TestWithRootTitle(t *testing.T) {
	t.Parallel()

	t.Run("named struct root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Equal(t, "rootTitleStruct", s.Title)
	})

	t.Run("defaults to off", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context())
		require.NoError(t, err)

		assert.Empty(t, s.Title)
	})

	t.Run("pointer root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[*rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// A pointer root generates a nullable anyOf wrapper; the title is
		// derived from the pointer-dereferenced type and sits on the root.
		assert.Equal(t, "rootTitleStruct", s.Title)
	})

	t.Run("self-referential Draft-07 root titles the definitions entry", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleRecursive](t.Context(),
			jsonschema.WithDraft(jsonschema.Draft7),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// Draft-07 readers ignore keywords beside $ref, so a title on the
		// bare $ref root would be invisible; it lands on the definitions
		// entry instead, shared by every occurrence of the type.
		require.Equal(t, "#/definitions/rootTitleRecursive", s.Ref)
		assert.Empty(t, s.Title, "the bare $ref root carries no sibling title")

		def := s.Definitions["rootTitleRecursive"]
		require.NotNil(t, def)
		assert.Equal(t, "rootTitleRecursive", def.Title)
	})

	t.Run("self-referential Draft 2020-12 root titles the root", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleRecursive](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		// Draft 2020-12 honors $ref siblings, so the title sits on the root
		// $ref node and the $defs entry stays untitled.
		require.Equal(t, "#/$defs/rootTitleRecursive", s.Ref)
		assert.Equal(t, "rootTitleRecursive", s.Title)

		def := s.Defs["rootTitleRecursive"]
		require.NotNil(t, def)
		assert.Empty(t, def.Title)
	})

	t.Run("anonymous struct root has no title", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[struct {
			Name string `json:"name"`
		}](t.Context(), jsonschema.WithRootTitle(true))
		require.NoError(t, err)

		assert.Empty(t, s.Title, "an unnamed root type yields no name to title")
	})

	t.Run("unnamed map root has no title", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[map[string]int](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Empty(t, s.Title)
	})

	t.Run("existing title preserved", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithTypeSchema(
				reflect.TypeFor[rootTitleStruct](),
				&jsonschema.Schema{Type: "object", Title: "Custom"},
			),
		)
		require.NoError(t, err)

		assert.Equal(t, "Custom", s.Title, "WithTypeSchema title is never overwritten")
	})

	t.Run("extender title preserved", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleExtender](t.Context(),
			jsonschema.WithRootTitle(true),
		)
		require.NoError(t, err)

		assert.Equal(t, "Extended", s.Title, "JSONSchemaExtend title is never overwritten")
	})

	t.Run("custom namer honored", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithNamer(jsonschema.NamerFunc(func(tc jsonschema.TypeContext) string {
				return "My" + tc.Type.Name()
			})),
		)
		require.NoError(t, err)

		assert.Equal(t, "MyrootTitleStruct", s.Title)
	})

	t.Run("definitions disabled", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[rootTitleStruct](t.Context(),
			jsonschema.WithRootTitle(true),
			jsonschema.WithDefinitions(false),
		)
		require.NoError(t, err)

		// With WithDefinitions(false) the root carries no $id or $defs name,
		// so the derived title is the only place the type name appears.
		assert.Equal(t, "rootTitleStruct", s.Title)
		assert.Empty(t, s.Defs)
	})
}

// extendedKind is a named type whose author sets a description via
// JSONSchemaExtend, for ordering tests against registered extenders.
type extendedKind int

func (extendedKind) JSONSchemaExtend(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
	s.Description = "by author"

	return nil
}

// describePlainKind extends plainKind with a description and leaves every
// other type untouched.
func describePlainKind() jsonschema.TypeSchemaExtender {
	return jsonschema.TypeSchemaExtenderFunc(
		func(_ context.Context, tc jsonschema.TypeContext, s *jsonschema.Schema) error {
			if tc.Type == reflect.TypeFor[plainKind]() {
				s.Description = "extended"
			}

			return nil
		},
	)
}

func TestWithTypeSchemaExtender(t *testing.T) {
	t.Parallel()

	type doc struct {
		Plain plainKind `json:"plain"`
	}

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"extender adjusts matching reflected types only": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaExtender(describePlainKind())},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer", "description": "extended"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"extenders apply in registration order": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaExtender(describePlainKind()),
				jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
					func(_ context.Context, tc jsonschema.TypeContext, s *jsonschema.Schema) error {
						if tc.Type == reflect.TypeFor[plainKind]() {
							s.Description += ", then refined"
						}

						return nil
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer", "description": "extended, then refined"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"not called for resolver-supplied schemas": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
				jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
					func(_ context.Context, tc jsonschema.TypeContext, _ *jsonschema.Schema) error {
						if tc.Type == reflect.TypeFor[plainKind]() {
							return errors.New("extender reached a replaced type")
						}

						return nil
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "string"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
		"nil extender is ignored": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaExtender(nil)},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"plain": {"type": "integer"}
				},
				"required": ["plain"],
				"additionalProperties": false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(), tc.opts...)
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// TestWithTypeSchemaExtenderFor pins the generic form: f runs only for T,
// other types pass through untouched, an error from f aborts generation,
// and a nil f is ignored.
func TestWithTypeSchemaExtenderFor(t *testing.T) {
	t.Parallel()

	type doc struct {
		Plain plainKind `json:"plain"`
		Other int       `json:"other"`
	}

	t.Run("extends only the named type", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[plainKind](
				func(_ context.Context, _ jsonschema.TypeContext, s *jsonschema.Schema) error {
					s.Description = "extended"
					return nil
				}),
		)
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "object",
			"properties": {
				"plain": {"type": "integer", "description": "extended"},
				"other": {"type": "integer"}
			},
			"required": ["plain", "other"],
			"additionalProperties": false
		}`, string(got))
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		errBoom := errors.New("boom")

		_, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[plainKind](
				func(context.Context, jsonschema.TypeContext, *jsonschema.Schema) error { return errBoom }),
		)
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("nil function is ignored", func(t *testing.T) {
		t.Parallel()

		_, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTypeSchemaExtenderFor[plainKind](nil))
		require.NoError(t, err)
	})
}

// TestWithTypeSchemaExtender_AfterJSONSchemaExtend proves the ordering
// contract: a registered extender sees the schema after the type's own
// JSONSchemaExtend has run, so it can adjust what the author produced.
func TestWithTypeSchemaExtender_AfterJSONSchemaExtend(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[extendedKind](t.Context(),
		jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
			func(_ context.Context, tc jsonschema.TypeContext, s *jsonschema.Schema) error {
				if tc.Type == reflect.TypeFor[extendedKind]() {
					s.Description += ", then extended"
				}

				return nil
			},
		)),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "integer",
		"description": "by author, then extended"
	}`, string(got))
}

// TestWithTypeSchemaExtender_ReceivesDraft proves the TypeContext carries the
// generation run's target draft, matching the resolver contract.
func TestWithTypeSchemaExtender_ReceivesDraft(t *testing.T) {
	t.Parallel()

	var got []jsonschema.Draft

	_, err := jsonschema.GenerateFor[plainKind](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
		jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
			func(_ context.Context, tc jsonschema.TypeContext, _ *jsonschema.Schema) error {
				got = append(got, tc.Draft)
				return nil
			},
		)),
	)
	require.NoError(t, err)

	require.NotEmpty(t, got)

	for _, d := range got {
		assert.Equal(t, jsonschema.Draft7, d)
	}
}

// TestWithTypeSchemaExtender_Error proves an extender error aborts generation
// and surfaces with the failing type named.
func TestWithTypeSchemaExtender_Error(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	_, err := jsonschema.GenerateFor[plainKind](t.Context(),
		jsonschema.WithTypeSchemaExtender(jsonschema.TypeSchemaExtenderFunc(
			func(context.Context, jsonschema.TypeContext, *jsonschema.Schema) error { return errBoom },
		)),
	)
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "extend type")
	assert.Contains(t, err.Error(), "plainKind")
}

// stringerKind is a named type implementing fmt.Stringer for provider
// predicate tests.
type stringerKind int

func (stringerKind) String() string { return "kind" }

// plainKind is a named type that implements nothing, so it falls through
// every provider predicate to kind-based reflection.
type plainKind int

// stringerProvider resolves every fmt.Stringer to a plain string schema.
func stringerProvider() jsonschema.TypeSchemaProvider {
	return jsonschema.TypeSchemaProviderFunc(
		func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
			if !tc.Type.Implements(reflect.TypeFor[fmt.Stringer]()) {
				return nil, jsonschema.ErrTypeNotHandled
			}

			return &jsonschema.Schema{Type: "string"}, nil
		},
	)
}

func TestWithTypeSchemaProvider(t *testing.T) {
	t.Parallel()

	type doc struct {
		Kind  stringerKind `json:"kind"`
		Plain plainKind    `json:"plain"`
	}

	tests := map[string]struct {
		opts []jsonschema.GenerateOption
		want string
	}{
		"predicate provider overrides matching types only": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaProvider(stringerProvider())},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"later WithTypeSchema wins over earlier provider": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaProvider(stringerProvider()),
				jsonschema.WithTypeSchemaFor[stringerKind](&jsonschema.Schema{Type: "string", Format: "uri"}),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string", "format": "uri"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"later provider wins over earlier WithTypeSchema": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaFor[stringerKind](&jsonschema.Schema{Type: "string", Format: "uri"}),
				jsonschema.WithTypeSchemaProvider(stringerProvider()),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "string"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"nil schema with nil error is unrestricted": {
			opts: []jsonschema.GenerateOption{
				jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
					func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
						if tc.Type != reflect.TypeFor[stringerKind]() {
							return nil, jsonschema.ErrTypeNotHandled
						}

						return nil, nil //nolint:nilnil // The unrestricted answer.
					},
				)),
			},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": true,
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
		"nil provider is ignored": {
			opts: []jsonschema.GenerateOption{jsonschema.WithTypeSchemaProvider(nil)},
			want: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"kind": {"type": "integer"},
					"plain": {"type": "integer"}
				},
				"required": ["kind", "plain"],
				"additionalProperties": false
			}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[doc](t.Context(), tc.opts...)
			require.NoError(t, err)

			got, err := json.Marshal(s)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestWithTypeSchema_LastRegistrationWins(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[plainKind](t.Context(),
		jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
		jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "number"}),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "number"
	}`, string(got))
}

// TestWithTypeSchema_NilUnregisters proves a nil schema restores the type's
// default resolution: earlier exact registrations for the type are removed,
// while predicate providers still apply.
func TestWithTypeSchema_NilUnregisters(t *testing.T) {
	t.Parallel()

	t.Run("removes earlier exact registration", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[plainKind](t.Context(),
			jsonschema.WithTypeSchemaFor[plainKind](&jsonschema.Schema{Type: "string"}),
			jsonschema.WithTypeSchemaFor[plainKind](nil),
		)
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "integer"
		}`, string(got))
	})

	t.Run("leaves predicate providers in place", func(t *testing.T) {
		t.Parallel()

		s, err := jsonschema.GenerateFor[stringerKind](t.Context(),
			jsonschema.WithTypeSchemaProvider(stringerProvider()),
			jsonschema.WithTypeSchemaFor[stringerKind](nil),
		)
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "string"
		}`, string(got))
	})
}

// TestWithTypeSchemaProvider_ReceivesDraft proves the TypeContext carries the
// generation run's target draft, so a provider can emit draft-appropriate
// keywords.
func TestWithTypeSchemaProvider_ReceivesDraft(t *testing.T) {
	t.Parallel()

	for name, draft := range map[string]jsonschema.Draft{
		"draft7":    jsonschema.Draft7,
		"draft2020": jsonschema.Draft2020,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got []jsonschema.Draft

			_, err := jsonschema.GenerateFor[plainKind](t.Context(),
				jsonschema.WithDraft(draft),
				jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
					func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
						got = append(got, tc.Draft)
						return nil, jsonschema.ErrTypeNotHandled
					},
				)),
			)
			require.NoError(t, err)

			require.NotEmpty(t, got)

			for _, d := range got {
				assert.Equal(t, draft, d)
			}
		})
	}
}

// TestWithTypeSchemaProvider_EmbeddedComposition mirrors the WithTypeSchema embed
// behavior: an embedded struct intercepted by a provider composes via allOf
// rather than having its fields promoted.
func TestWithTypeSchemaProvider_EmbeddedComposition(t *testing.T) {
	t.Parallel()

	type base struct {
		Name string `json:"name"`
	}

	type doc struct {
		base //nolint:unused // Exercised via reflection.

		Extra int `json:"extra"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
			func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
				if tc.Type != reflect.TypeFor[base]() {
					return nil, jsonschema.ErrTypeNotHandled
				}

				return &jsonschema.Schema{Type: "object"}, nil
			},
		)),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"$defs": {"base": {"type": "object"}},
		"allOf": [{"$ref": "#/$defs/base"}],
		"properties": {
			"extra": {"type": "integer"}
		},
		"required": ["extra"],
		"unevaluatedProperties": false
	}`, string(got))
}

// TestWithTypeSchemaProvider_Error proves a provider error aborts generation
// and reaches the caller wrapped, whether the provider is consulted for the
// root type or for a type reached through a field or an embed.
func TestWithTypeSchemaProvider_Error(t *testing.T) {
	t.Parallel()

	errLoad := errors.New("schema document unavailable")

	failFor := func(target reflect.Type) jsonschema.GenerateOption {
		return jsonschema.WithTypeSchemaProvider(jsonschema.TypeSchemaProviderFunc(
			func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
				if tc.Type == target {
					return nil, errLoad
				}

				return nil, jsonschema.ErrTypeNotHandled
			},
		))
	}

	type inner struct {
		Kind stringerKind `json:"kind"`
	}

	type withField struct {
		Kind stringerKind `json:"kind"`
	}

	type withEmbed struct {
		inner //nolint:unused // Exercised via reflection.

		Extra int `json:"extra"`
	}

	tests := map[string]struct {
		generate func(opt jsonschema.GenerateOption) error
		target   reflect.Type
	}{
		"root type": {
			target: reflect.TypeFor[stringerKind](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[stringerKind](t.Context(), opt)
				return err
			},
		},
		"field type": {
			target: reflect.TypeFor[stringerKind](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[withField](t.Context(), opt)
				return err
			},
		},
		"embedded type": {
			target: reflect.TypeFor[inner](),
			generate: func(opt jsonschema.GenerateOption) error {
				_, err := jsonschema.GenerateFor[withEmbed](t.Context(), opt)
				return err
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.generate(failFor(tc.target))
			require.ErrorIs(t, err, errLoad)
		})
	}
}

// TestWithTypeSchemaProvider_SchemaUnaliased proves a provider-supplied schema is
// copied before use: mutating the generated output cannot reach back into the
// schema value the resolver returns across calls.
func TestWithTypeSchemaProvider_SchemaUnaliased(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string", Enum: []any{"a"}}
	provider := jsonschema.TypeSchemaProviderFunc(
		func(_ context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error) {
			if tc.Type != reflect.TypeFor[plainKind]() {
				return nil, jsonschema.ErrTypeNotHandled
			}

			return shared, nil
		},
	)

	s, err := jsonschema.GenerateFor[plainKind](t.Context(), jsonschema.WithTypeSchemaProvider(provider))
	require.NoError(t, err)

	s.Enum = append(s.Enum, "b")

	assert.Equal(t, []any{"a"}, shared.Enum)
}

type Base struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type WithEmbedded struct {
	Base
	Email string `json:"email"`
}

func TestGenerateFor_EmbeddedStructPromoted(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[WithEmbedded](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Base fields should be promoted (flattened) into the parent.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"id":{"type":"integer"},
			"name":{"type":"string"},
			"email":{"type":"string"}
		},
		"required":["id","name","email"],
		"additionalProperties":false
	}`, string(got))
}

type EmbeddedWithTag struct {
	Base  `json:"base"`
	Email string `json:"email"`
}

func TestGenerateFor_EmbeddedStructWithTag(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[EmbeddedWithTag](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Embedded with json tag → treated as regular named field.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"base":{"$ref":"#/$defs/Base"},
			"email":{"type":"string"}
		},
		"required":["base","email"],
		"additionalProperties":false,
		"$defs":{
			"Base":{
				"type":"object",
				"properties":{
					"id":{"type":"integer"},
					"name":{"type":"string"}
				},
				"required":["id","name"],
				"additionalProperties":false
			}
		}
	}`, string(got))
}

type EmbeddedPointer struct {
	*Base
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedPointerToStruct(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[EmbeddedPointer](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Embedded pointer-to-struct: fields are promoted but optional, because a
	// nil embedded pointer causes encoding/json to omit them entirely.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"id":{"type":"integer"},
			"name":{"type":"string"},
			"extra":{"type":"string"}
		},
		"required":["extra"],
		"additionalProperties":false
	}`, string(got))
}

// Shadowing tests.
type Inner struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type Outer struct {
	Inner
	Name string `json:"name"` // shadows Inner.Name
}

func TestGenerateFor_FieldShadowing(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[Outer](t.Context())
	require.NoError(t, err)

	// Outer.Name should shadow Inner.Name.
	assert.Contains(t, s.Required, "name")
	assert.Contains(t, s.Required, "age")
	assert.Len(t, s.Properties, 2) // name and age
}

// Ambiguity test.
type AmbigA struct {
	X string
}
type AmbigB struct {
	X string
}
type AmbigParent struct {
	AmbigA
	AmbigB
	Y string `json:"y"`
}

func TestGenerateFor_FieldAmbiguity(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[AmbigParent](t.Context())
	require.NoError(t, err)

	// X is ambiguous (same depth, different embedded types) → dropped.
	assert.NotContains(t, s.Properties, "X")
	assert.Contains(t, s.Properties, "y")
}

// Same-depth collision tie-break: one embed contributes the JSON name via an
// explicit json tag, the other via the bare Go field name.
type taggedShared struct {
	V string `json:"Shared"`
}

type untaggedShared struct {
	Shared string
}

type tieBreakParent struct {
	taggedShared   //nolint:unused // Exercised via reflection.
	untaggedShared //nolint:unused // Exercised via reflection.
}

// Same-depth collision where both contributors carry an explicit json tag → no
// single winner, so encoding/json drops the field. The duplicate tag is reached
// through one extra embed level on each side, which keeps the collision at the
// same promotion depth while staying out of go vet's single-struct structtag
// check (it would otherwise flag the deliberately duplicated json tag).
type taggedDupA struct {
	V string `json:"Dup"`
}

type taggedDupB struct {
	W string `json:"Dup"`
}

type taggedDupMidA struct {
	taggedDupA //nolint:unused // Exercised via reflection.
}

type taggedDupMidB struct {
	taggedDupB //nolint:unused // Exercised via reflection.
}

type bothTaggedParent struct {
	taggedDupMidA //nolint:unused // Exercised via reflection.
	taggedDupMidB //nolint:unused // Exercised via reflection.
}

func TestGenerateFor_SameDepthTagTieBreak(t *testing.T) {
	t.Parallel()

	// Encoding/json's rule for fields colliding on a JSON name at the same
	// shallowest depth: if exactly one has an explicit json tag name, it wins;
	// if none or two-plus are tagged, the field is dropped. The generated schema
	// must match whatever encoding/json actually emits, asserted here directly.

	// Exactly one tagged → the tagged field wins and appears as a property.
	mixed, err := json.Marshal(tieBreakParent{
		taggedShared:   taggedShared{V: "from-tag"},
		untaggedShared: untaggedShared{Shared: "from-field"},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"Shared":"from-tag"}`, string(mixed),
		"encoding/json keeps the explicitly tagged field on a same-depth collision")

	s, err := jsonschema.GenerateFor[tieBreakParent](t.Context())
	require.NoError(t, err)
	assert.Contains(t, s.Properties, "Shared",
		"schema must include the property encoding/json marshals")
	assert.Equal(t, "string", s.Properties["Shared"].Type)

	// Both tagged → no single winner, so the field is dropped.
	both, err := json.Marshal(bothTaggedParent{
		taggedDupMidA: taggedDupMidA{taggedDupA{V: "a"}},
		taggedDupMidB: taggedDupMidB{taggedDupB{W: "b"}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(both),
		"encoding/json drops the field when two same-depth fields are tagged")

	bs, err := jsonschema.GenerateFor[bothTaggedParent](t.Context())
	require.NoError(t, err)
	assert.NotContains(t, bs.Properties, "Dup",
		"schema must drop the property encoding/json omits")
}

// Embedded non-struct type.
type MyString string

type HasEmbeddedNonStruct struct {
	MyString
	Other string `json:"other"`
}

func TestGenerateFor_EmbeddedNonStructType(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasEmbeddedNonStruct](t.Context())
	require.NoError(t, err)

	// MyString becomes a regular field named "MyString".
	assert.Contains(t, s.Properties, "MyString")
	assert.Contains(t, s.Properties, "other")
	assert.Equal(t, "string", s.Properties["MyString"].Type)
}

// Embedded struct with JSONSchemaProvider → allOf composition.
type ProviderEmbed struct {
	Field1 string `json:"field1"`
}

func (ProviderEmbed) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"field1": {Type: "string"},
		},
	}, nil
}

type HasProviderEmbed struct {
	ProviderEmbed
	Field2 string `json:"field2"`
}

func TestGenerateFor_EmbeddedStructWithProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderEmbed](t.Context())
	require.NoError(t, err)

	// ProviderEmbed should be composed via allOf.
	assert.NotNil(t, s.AllOf, "schema: %s", marshalSchema(t, s))
	assert.Contains(t, s.Properties, "field2")
}

// Unexported embedded struct with exported fields.
type unexportedBase struct { //nolint:unused // Used as embedded field in HasUnexportedEmbed.
	Visible string `json:"visible"` //nolint:unused // Promoted via embedding.
}

type HasUnexportedEmbed struct {
	unexportedBase        //nolint:unused // Embeds unexportedBase to promote its exported fields.
	Other          string `json:"other"`
}

func TestGenerateFor_UnexportedEmbeddedStruct(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasUnexportedEmbed](t.Context())
	require.NoError(t, err)

	// Unexported embedded struct's exported fields should be promoted.
	assert.Contains(t, s.Properties, "visible")
	assert.Contains(t, s.Properties, "other")
}

// Embedded interface tests.
type HasEmbeddedInterface struct {
	fmt.Stringer
	Name string `json:"name"`
}

func TestGenerateFor_EmbeddedInterfaceSkipped(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasEmbeddedInterface](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Embedded interface without JSONSchemaProvider is skipped.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"}
		},
		"required":["name"],
		"additionalProperties":false
	}`, string(got))
}

// SchemaInterface is an interface that implements JSONSchemaProvider.
type SchemaInterface interface {
	fmt.Stringer
	JSONSchema(ctx context.Context, tc jsonschema.TypeContext) (*jsonschema.Schema, error)
}

type HasProviderInterface struct {
	SchemaInterface
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedInterfaceWithProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderInterface](t.Context())
	require.NoError(t, err)

	// SchemaInterface declares JSONSchemaProvider, but an interface cannot be
	// instantiated to call it (callProvider returns nil), so the embed is skipped
	// rather than composed into a vacuous allOf:[{}] branch that constrains
	// nothing.
	assert.Empty(t, s.AllOf, "schema: %s", marshalSchema(t, s))

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"extra":{"type":"string"}},
		"required":["extra"],
		"additionalProperties":false
	}`, string(got))
}

// textMarshalerIface is an interface whose method set includes
// encoding.TextMarshaler. An interface cannot serialize as a string the way a
// concrete TextMarshaler does, so an embed of it must be skipped, not composed
// into an unsatisfiable allOf:[{"type":"string"}] branch.
type textMarshalerIface interface {
	encoding.TextMarshaler
}

// embedsTextMarshalerIface declares a direct MarshalJSON so the outer-level
// promoted-TextMarshaler short-circuit does not fire (it would emit
// {"type":"string"} and mask how the embedded interface is handled), forcing
// the struct to reflect its fields.
type embedsTextMarshalerIface struct {
	textMarshalerIface

	Name string `json:"name"`
}

func (embedsTextMarshalerIface) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func TestGenerateFor_EmbeddedTextMarshalerInterfaceSkipped(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[embedsTextMarshalerIface](t.Context())
	require.NoError(t, err)

	// The embed matches TextMarshaler only through the interface method set, so
	// it is skipped rather than composed into an allOf branch that would make the
	// schema unsatisfiable.
	assert.Empty(t, s.AllOf, "schema: %s", marshalSchema(t, s))

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"name":{"type":"string"}},
		"required":["name"],
		"additionalProperties":false
	}`, string(got))
}

// Embedded struct implementing TextMarshaler → the promoted MarshalText
// serializes the whole outer struct as a string.
type TextMarshalerEmbed struct {
	Field1 string
}

func (TextMarshalerEmbed) MarshalText() ([]byte, error) { return nil, nil }

type HasTextMarshalerEmbed struct {
	TextMarshalerEmbed
	Field2 string `json:"field2"`
}

func TestGenerateFor_EmbeddedTextMarshalerStruct(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed](t.Context())
	require.NoError(t, err)

	// HasTextMarshalerEmbed's method set includes the promoted MarshalText,
	// so encoding/json serializes the whole struct as a string; reflecting
	// its fields would describe a shape that never appears.
	assert.Equal(t, "string", s.Type, "schema: %s", marshalSchema(t, s))
	assert.Empty(t, s.Properties)
}

func TestGenerateFor_EmbeddedTextMarshalerStruct_Draft2020(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft2020),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The promoted MarshalText drives serialization: the value is a string.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"string"
	}`, string(got))
}

func TestGenerateFor_EmbeddedTextMarshalerStruct_Draft7(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed](t.Context(),
		jsonschema.WithDraft(jsonschema.Draft7),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The promoted MarshalText drives serialization: the value is a string.
	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"string"
	}`, string(got))
}

// Embedded pointer-to-non-struct type.
type MyInt int

type HasEmbeddedPointerNonStruct struct {
	*MyInt
	Other string `json:"other"`
}

func TestGenerateFor_EmbeddedPointerToNonStruct(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasEmbeddedPointerNonStruct](t.Context())
	require.NoError(t, err)

	// *MyInt becomes a regular field named "MyInt" with a nullable schema.
	assert.Contains(t, s.Properties, "MyInt")
	assert.Contains(t, s.Properties, "other")

	// Nullable pointers are expressed via anyOf with a null alternative.
	myInt := s.Properties["MyInt"]
	require.Len(t, myInt.AnyOf, 2)
	assert.Equal(t, "integer", myInt.AnyOf[0].Type)
	assert.Equal(t, "null", myInt.AnyOf[1].Type)
}

// unexportedString is an unexported embedded non-struct type. Per
// encoding/json, it is excluded from the generated schema.
type unexportedString string //nolint:unused // Used as embedded field.

type HasUnexportedEmbeddedNonStruct struct {
	unexportedString        //nolint:unused // Unexported embedded non-struct type, should be excluded.
	Visible          string `json:"visible"`
}

func TestGenerateFor_UnexportedEmbeddedNonStructExcluded(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasUnexportedEmbeddedNonStruct](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Unexported embedded non-struct type should be excluded (encoding/json behavior).
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"visible":{"type":"string"}
		},
		"required":["visible"],
		"additionalProperties":false
	}`, string(got))
}

// unexportedIface is an unexported embedded interface. Per encoding/json, it
// is excluded from the generated schema.
type unexportedIface interface { //nolint:unused // Used as embedded field.
	doSomething()
}

type HasUnexportedEmbeddedInterface struct {
	unexportedIface        //nolint:unused // Unexported embedded interface, should be excluded.
	Name            string `json:"name"`
}

func TestGenerateFor_UnexportedEmbeddedInterfaceExcluded(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasUnexportedEmbeddedInterface](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// Unexported embedded interface should be excluded (encoding/json behavior).
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"name":{"type":"string"}
		},
		"required":["name"],
		"additionalProperties":false
	}`, string(got))
}

// WithTypeSchema on embedded struct → allOf composition.
type OverriddenEmbed struct {
	Value string `json:"value"`
}

type HasOverriddenEmbed struct {
	OverriddenEmbed
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedStructWithTypeSchemaOverride(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasOverriddenEmbed](t.Context(),
		jsonschema.WithTypeSchema(
			reflect.TypeFor[OverriddenEmbed](),
			&jsonschema.Schema{Type: "string", Format: "custom"},
		),
	)
	require.NoError(t, err)

	// OverriddenEmbed has a WithTypeSchema override → composed via allOf.
	assert.NotNil(t, s.AllOf, "schema: %s", marshalSchema(t, s))
	assert.Contains(t, s.Properties, "extra")
}

// WithAdditionalProperties(true) + allOf composition.
func TestGenerateFor_AllOfWithAdditionalPropertiesTrue(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderEmbed](t.Context(),
		jsonschema.WithAdditionalProperties(true),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// With WithAdditionalProperties(true), both additionalProperties
	// and unevaluatedProperties should be omitted.
	// ProviderEmbed is a named struct type, so it's extracted to $defs.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"allOf":[{"$ref":"#/$defs/ProviderEmbed"}],
		"properties":{
			"field2":{"type":"string"}
		},
		"required":["field2"],
		"$defs":{
			"ProviderEmbed":{
				"type":"object",
				"properties":{
					"field1":{"type":"string"}
				}
			}
		}
	}`, string(got))
}

// Embedded time.Time → the promoted MarshalJSON serializes the whole outer
// struct, so its JSON shape is opaque to reflection.
type HasEmbeddedTime struct {
	time.Time
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedBuiltinOverrideType(t *testing.T) {
	t.Parallel()

	// HasEmbeddedTime's method set includes time.Time's promoted MarshalJSON,
	// so encoding/json serializes the whole struct via that method, and the
	// schema is unrestricted. The output here is a bare date-time string, but
	// a promoted MarshalJSON can emit any JSON value in general. Reflecting an
	// object with an "extra" property composed with the date-time string via
	// allOf would be unsatisfiable and reject every actual serialization.
	s, err := jsonschema.GenerateFor[HasEmbeddedTime](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema"
	}`, string(got))
}

// Embedded WithTypeSchema override → allOf composition.
type OverrideTarget struct {
	Inner string `json:"inner"`
}

type HasWithTypeSchemaEmbed struct {
	OverrideTarget
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedWithTypeSchemaOverride(t *testing.T) {
	t.Parallel()

	// Embedded struct with WithTypeSchema override should be composed via allOf.
	s, err := jsonschema.GenerateFor[HasWithTypeSchemaEmbed](t.Context(),
		jsonschema.WithTypeSchema(
			reflect.TypeFor[OverrideTarget](),
			&jsonschema.Schema{Type: "string", Format: "custom"},
		),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// OverrideTarget has a WithTypeSchema override → composed via allOf.
	// Named struct type, so the override goes into $defs.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"allOf":[{"$ref":"#/$defs/OverrideTarget"}],
		"properties":{
			"extra":{"type":"string"}
		},
		"required":["extra"],
		"unevaluatedProperties":false,
		"$defs":{
			"OverrideTarget":{
				"type":"string",
				"format":"custom"
			}
		}
	}`, string(got))
}

type embedPromoteInner struct {
	A int `json:"a"`
}

type embedOptionsOnly struct {
	embedPromoteInner `json:",omitempty"` //nolint:unused,modernize // Tag under test: ",omitempty" on an embedded struct; promoted via reflection.
}

type embedEmptyName struct {
	embedPromoteInner `json:","` //nolint:unused,staticcheck // Tag under test: "," carries no name, so the embed is promoted via reflection.
}

type embedExplicitName struct {
	embedPromoteInner `json:"inner"` //nolint:unused // Named field via reflection in the generated schema.
}

func TestGenerateFor_EmbeddedOptionsOnlyTagPromotesFields(t *testing.T) {
	t.Parallel()

	// Encoding/json promotes an embedded struct whose json tag carries options
	// but no name (e.g. json:",omitempty"), exactly like an untagged embed. Only
	// an explicit name turns it into a named field. The generated schema must
	// accept the value the type actually serializes to.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		marshal  func() ([]byte, error)
		promoted bool
	}{
		"options-only tag is promoted": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedOptionsOnly](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(embedOptionsOnly{embedPromoteInner{A: 5}}) },
			promoted: true,
		},
		"empty name is promoted": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedEmptyName](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(embedEmptyName{embedPromoteInner{A: 5}}) },
			promoted: true,
		},
		"explicit name is a named field": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedExplicitName](t.Context()) },
			marshal:  func() ([]byte, error) { return json.Marshal(embedExplicitName{embedPromoteInner{A: 5}}) },
			promoted: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := tc.generate()
			require.NoError(t, err)

			data, err := tc.marshal()
			require.NoError(t, err)

			// The schema must accept the type's own serialized form.
			require.NoError(t, validateJSON(t.Context(), s, data),
				"generated schema rejected the value the type serializes to: %s", data)

			got, err := json.Marshal(s)
			require.NoError(t, err)

			if tc.promoted {
				assert.Contains(t, string(got), `"a"`, "promoted field should appear inline")
				assert.NotContains(t, string(got), `"$ref"`, "promoted embed should not be a $ref")
			} else {
				assert.Contains(t, string(got), `"$ref"`, "named embed should be a $ref field")
			}
		})
	}
}

// Shadowing across embed levels: SharedEmbed appears at depth 1 (direct) and
// depth 2 (via LevelOne), and OtherEmbed collides on the same JSON name at
// depth 2. Encoding/json keeps the shallowest occurrence, so the direct
// SharedEmbed.X wins. The colliding fields are deliberately untagged (the Go
// field name is the JSON name) so go vet's structtag check, which only
// inspects tags, stays quiet about the intentional collision.
type SharedEmbed struct {
	X int
}

type OtherEmbed struct {
	X string
}

type LevelOne struct {
	SharedEmbed
}

type LevelOneOther struct {
	OtherEmbed
}

type ShallowWins struct {
	LevelOne
	LevelOneOther
	SharedEmbed
}

func TestGenerateFor_EmbeddedShallowestTypeWins(t *testing.T) {
	t.Parallel()

	// Encoding/json ground truth: the direct (depth-1) SharedEmbed.X shadows
	// both depth-2 candidates, so the document carries an integer X.
	v := ShallowWins{SharedEmbed: SharedEmbed{X: 99}}
	doc, err := json.Marshal(v) //nolint:musttag // Untagged on purpose; see type comment.
	require.NoError(t, err)
	require.JSONEq(t, `{"X":99}`, string(doc))

	s, err := jsonschema.GenerateFor[ShallowWins](t.Context())
	require.NoError(t, err)

	require.Contains(t, s.Properties, "X")
	assert.Equal(t, "integer", s.Properties["X"].Type, "schema: %s", marshalSchema(t, s))
	require.NoError(t, validateJSON(t.Context(), s, doc))
}

// The same type embedded twice at the same depth via distinct paths: its
// fields are ambiguous and encoding/json drops them from the output entirely.
type AnnihilateA struct {
	SharedEmbed
}

type AnnihilateB struct {
	SharedEmbed
}

type HasAnnihilatedEmbeds struct {
	AnnihilateA
	AnnihilateB
	Name string `json:"name"`
}

func TestGenerateFor_EmbeddedRepeatedTypeAnnihilates(t *testing.T) {
	t.Parallel()

	// Encoding/json ground truth: X is ambiguous (SharedEmbed twice at depth 2)
	// and omitted from the output.
	v := HasAnnihilatedEmbeds{Name: "n"}
	doc, err := json.Marshal(v) //nolint:musttag // Untagged on purpose; see SharedEmbed comment.
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"n"}`, string(doc))

	s, err := jsonschema.GenerateFor[HasAnnihilatedEmbeds](t.Context())
	require.NoError(t, err)

	assert.NotContains(t, s.Properties, "X", "schema: %s", marshalSchema(t, s))
	require.NoError(t, validateJSON(t.Context(), s, doc))
}

// Pointer-embedded provider: a nil embed contributes nothing to the marshaled
// object, so the provider's schema must not be an unconditional allOf branch.
type OptionalProviderEmbed struct {
	Req string `json:"req"`
}

func (OptionalProviderEmbed) JSONSchema(context.Context, jsonschema.TypeContext) (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:       "object",
		Properties: map[string]*jsonschema.Schema{"req": {Type: "string"}},
		Required:   []string{"req"},
	}, nil
}

type HasOptionalProviderEmbed struct {
	*OptionalProviderEmbed
	Name string `json:"name"`
}

func TestGenerateFor_EmbeddedPointerProviderOptional(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasOptionalProviderEmbed](t.Context())
	require.NoError(t, err)

	// Nil embed: encoding/json omits the provider's properties entirely, and
	// the anyOf[$ref, {}] composition accepts the document.
	nilDoc, err := json.Marshal(HasOptionalProviderEmbed{Name: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"x"}`, string(nilDoc))
	require.NoError(t, validateJSON(t.Context(), s, nilDoc), "schema: %s", marshalSchema(t, s))

	// Non-nil embed: the provider's branch matches and its annotations keep
	// the embedded properties evaluated.
	fullDoc, err := json.Marshal(HasOptionalProviderEmbed{
		OptionalProviderEmbed: &OptionalProviderEmbed{Req: "r"},
		Name:                  "x",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"req":"r","name":"x"}`, string(fullDoc))
	require.NoError(t, validateJSON(t.Context(), s, fullDoc), "schema: %s", marshalSchema(t, s))
}
