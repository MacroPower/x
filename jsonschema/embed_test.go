package jsonschema_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
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

func (ProviderEmbed) JSONSchema() (*jsonschema.Schema, error) {
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
	JSONSchema() (*jsonschema.Schema, error)
}

type HasProviderInterface struct {
	SchemaInterface
	Extra string `json:"extra"`
}

func TestGenerateFor_EmbeddedInterfaceWithProvider(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[HasProviderInterface](t.Context())
	require.NoError(t, err)

	// SchemaInterface implements JSONSchemaProvider → composed via allOf.
	assert.NotNil(t, s.AllOf, "schema: %s", marshalSchema(t, s))
	assert.Contains(t, s.Properties, "extra")
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

// Unexported embedded non-struct type — should be excluded per encoding/json.
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
	// so encoding/json serializes the whole struct via that method (a bare
	// date-time string here — but a promoted MarshalJSON can emit any JSON
	// value in general), and the schema is unrestricted. Reflecting an object
	// with an "extra" property composed with the date-time string via allOf
	// would be unsatisfiable and reject every actual serialization.
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
			require.NoError(t, jsonschema.ValidateJSON(t.Context(), s, data),
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
	require.NoError(t, jsonschema.ValidateJSON(t.Context(), s, doc))
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
	require.NoError(t, jsonschema.ValidateJSON(t.Context(), s, doc))
}

// Pointer-embedded provider: a nil embed contributes nothing to the marshaled
// object, so the provider's schema must not be an unconditional allOf branch.
type OptionalProviderEmbed struct {
	Req string `json:"req"`
}

func (OptionalProviderEmbed) JSONSchema() (*jsonschema.Schema, error) {
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
	require.NoError(t, jsonschema.ValidateJSON(t.Context(), s, nilDoc), "schema: %s", marshalSchema(t, s))

	// Non-nil embed: the provider's branch matches and its annotations keep
	// the embedded properties evaluated.
	fullDoc, err := json.Marshal(HasOptionalProviderEmbed{
		OptionalProviderEmbed: &OptionalProviderEmbed{Req: "r"},
		Name:                  "x",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"req":"r","name":"x"}`, string(fullDoc))
	require.NoError(t, jsonschema.ValidateJSON(t.Context(), s, fullDoc), "schema: %s", marshalSchema(t, s))
}
