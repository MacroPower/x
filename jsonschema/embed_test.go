package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/jsonschema"
)

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

	s, err := jsonschema.GenerateFor[WithEmbedded]()
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

	s, err := jsonschema.GenerateFor[EmbeddedWithTag]()
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

	s, err := jsonschema.GenerateFor[EmbeddedPointer]()
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

	s, err := jsonschema.GenerateFor[Outer]()
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

	s, err := jsonschema.GenerateFor[AmbigParent]()
	require.NoError(t, err)

	// X is ambiguous (same depth, different embedded types) → dropped.
	assert.NotContains(t, s.Properties, "X")
	assert.Contains(t, s.Properties, "y")
}

// Embedded non-struct type.
type MyString string

type HasEmbeddedNonStruct struct {
	MyString
	Other string `json:"other"`
}

func TestGenerateFor_EmbeddedNonStructType(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasEmbeddedNonStruct]()
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

func (ProviderEmbed) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"field1": {Type: "string"},
		},
	}
}

type HasProviderEmbed struct {
	ProviderEmbed
	Field2 string `json:"field2"`
}

func TestGenerateFor_EmbeddedStructWithProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderEmbed]()
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

	s, err := jsonschema.GenerateFor[HasUnexportedEmbed]()
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

	s, err := jsonschema.GenerateFor[HasEmbeddedInterface]()
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
	JSONSchema() *jsonschema.Schema
}

type HasProviderInterface struct {
	SchemaInterface
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedInterfaceWithProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderInterface]()
	require.NoError(t, err)

	// SchemaInterface implements JSONSchemaProvider → composed via allOf.
	assert.NotNil(t, s.AllOf, "schema: %s", marshalSchema(t, s))
	assert.Contains(t, s.Properties, "extra")
}

// Embedded struct implementing TextMarshaler → allOf composition.
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

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed]()
	require.NoError(t, err)

	// TextMarshalerEmbed implements TextMarshaler, so it should be
	// composed via allOf rather than having its fields promoted.
	assert.NotNil(t, s.AllOf, "schema: %s", marshalSchema(t, s))
	assert.Contains(t, s.Properties, "field2")
}

func TestGenerateFor_EmbeddedTextMarshalerStruct_Draft2020(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// In Draft 2020-12, allOf composition uses unevaluatedProperties: false.
	// TextMarshalerEmbed is a named struct type, so it's extracted to $defs.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"allOf":[{"$ref":"#/$defs/TextMarshalerEmbed"}],
		"properties":{
			"field2":{"type":"string"}
		},
		"required":["field2"],
		"unevaluatedProperties":false,
		"$defs":{
			"TextMarshalerEmbed":{"type":"string"}
		}
	}`, string(got))
}

func TestGenerateFor_EmbeddedTextMarshalerStruct_Draft7(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed](
		jsonschema.WithDraft(jsonschema.Draft7),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// In Draft-07, additionalProperties: false is omitted when allOf is in use.
	// TextMarshalerEmbed is a named struct type, so it's extracted to definitions.
	assert.JSONEq(t, `{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"allOf":[{"$ref":"#/definitions/TextMarshalerEmbed"}],
		"properties":{
			"field2":{"type":"string"}
		},
		"required":["field2"],
		"definitions":{
			"TextMarshalerEmbed":{"type":"string"}
		}
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

	s, err := jsonschema.GenerateFor[HasEmbeddedPointerNonStruct]()
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

// Unexported embedded non-struct type — should be excluded per encoding/json.
type unexportedString string //nolint:unused // Used as embedded field.

type HasUnexportedEmbeddedNonStruct struct {
	unexportedString        //nolint:unused // Unexported embedded non-struct type, should be excluded.
	Visible          string `json:"visible"`
}

func TestGenerateFor_UnexportedEmbeddedNonStructExcluded(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasUnexportedEmbeddedNonStruct]()
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

// Unexported embedded interface — should be excluded per encoding/json.
type unexportedIface interface { //nolint:unused // Used as embedded field.
	doSomething()
}

type HasUnexportedEmbeddedInterface struct {
	unexportedIface        //nolint:unused // Unexported embedded interface, should be excluded.
	Name            string `json:"name"`
}

func TestGenerateFor_UnexportedEmbeddedInterfaceExcluded(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasUnexportedEmbeddedInterface]()
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

	s, err := jsonschema.GenerateFor[HasOverriddenEmbed](
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

	s, err := jsonschema.GenerateFor[HasTextMarshalerEmbed](
		jsonschema.WithAdditionalProperties(true),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// With WithAdditionalProperties(true), both additionalProperties
	// and unevaluatedProperties should be omitted.
	// TextMarshalerEmbed is a named struct type, so it's extracted to $defs.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"allOf":[{"$ref":"#/$defs/TextMarshalerEmbed"}],
		"properties":{
			"field2":{"type":"string"}
		},
		"required":["field2"],
		"$defs":{
			"TextMarshalerEmbed":{"type":"string"}
		}
	}`, string(got))
}

// Embedded built-in override type (time.Time) → allOf composition.
type HasEmbeddedTime struct {
	time.Time
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedBuiltinOverrideType(t *testing.T) {
	t.Parallel()

	// Time.Time has a built-in override (format: date-time). When embedded
	// without a json tag, it should be composed via allOf, not have its
	// fields promoted. As a named struct type, it is extracted to $defs.
	s, err := jsonschema.GenerateFor[HasEmbeddedTime]()
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"allOf":[{"$ref":"#/$defs/Time"}],
		"properties":{
			"extra":{"type":"string"}
		},
		"required":["extra"],
		"unevaluatedProperties":false,
		"$defs":{
			"Time":{"type":"string","format":"date-time"}
		}
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
	s, err := jsonschema.GenerateFor[HasWithTypeSchemaEmbed](
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
	embedPromoteInner `json:",omitempty"`
}

type embedEmptyName struct {
	embedPromoteInner `json:","`
}

type embedExplicitName struct {
	embedPromoteInner `json:"inner"`
}

func TestGenerateFor_EmbeddedOptionsOnlyTagPromotesFields(t *testing.T) {
	t.Parallel()

	// encoding/json promotes an embedded struct whose json tag carries options
	// but no name (e.g. json:",omitempty"), exactly like an untagged embed. Only
	// an explicit name turns it into a named field. The generated schema must
	// accept the value the type actually serializes to.
	tests := map[string]struct {
		generate func() (*jsonschema.Schema, error)
		marshal  func() ([]byte, error)
		promoted bool
	}{
		"options-only tag is promoted": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedOptionsOnly]() },
			marshal:  func() ([]byte, error) { return json.Marshal(embedOptionsOnly{embedPromoteInner{A: 5}}) },
			promoted: true,
		},
		"empty name is promoted": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedEmptyName]() },
			marshal:  func() ([]byte, error) { return json.Marshal(embedEmptyName{embedPromoteInner{A: 5}}) },
			promoted: true,
		},
		"explicit name is a named field": {
			generate: func() (*jsonschema.Schema, error) { return jsonschema.GenerateFor[embedExplicitName]() },
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
			require.NoError(t, jsonschema.ValidateJSON(s, data),
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
