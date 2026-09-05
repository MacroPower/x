package jsonschema_test

import (
	"encoding"
	"encoding/json/v2"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// GenList is a generic named slice type. Embedded as GenList[int], its
// [reflect.StructField.Name] is the bare identifier "GenList", while
// [reflect.Type.Name] is "GenList[int]"; encoding/json keys the field by the
// former.
type GenList[T any] []T

type hasGenericEmbed struct {
	GenList[int]

	B string `json:"b"`
}

func TestGenerateFor_EmbeddedGenericNonStructType(t *testing.T) {
	t.Parallel()

	// An embedded non-struct without an explicit name is an error under
	// encoding/json/v2, generic instantiations included.
	_, err := json.Marshal(hasGenericEmbed{GenList: GenList[int]{1}, B: "x"})
	require.ErrorContains(t, err, "must be explicitly given a JSON name")

	_, err = jsonschema.GenerateFor[hasGenericEmbed](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "GenList")
}

// Encoding/json/v2 refuses an embedded interface outright: an embedded
// non-struct must be explicitly given a JSON name. Generation refuses the
// same declarations; an interface as a regular named field stays supported.

// LeafIface is an exported interface embedded as a leaf field.
type LeafIface interface{ LeafValue() string }

// leafIfaceObject is a concrete LeafIface that marshals as a JSON object
// matching the WithTypeSchema override used in the tests below.
type leafIfaceObject struct {
	Kind string `json:"kind"`
}

func (o leafIfaceObject) LeafValue() string { return o.Kind }

type hasLeafIface struct {
	LeafIface

	Name string `json:"name"`
}

func TestGenerateFor_EmbeddedInterfaceRefused(t *testing.T) {
	t.Parallel()

	// Encoding/json/v2 refuses the embedded interface, whatever the value.
	_, err := json.Marshal(hasLeafIface{Name: "x"})
	require.ErrorContains(t, err, "must be explicitly given a JSON name")

	// Generation refuses the same declaration. A concrete implementation
	// stays usable as a regular field (asserted by the field tests below).
	_, err = jsonschema.GenerateFor[hasLeafIface](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

// leafIfaceOverride is the WithTypeSchema override describing what every
// concrete LeafIface implementation marshals as in these tests.
func leafIfaceOverride() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"kind": {Type: "string"},
		},
		Required: []string{"kind"},
	}
}

func TestGenerateFor_EmbeddedInterfaceWithTypeSchemaOverride(t *testing.T) {
	t.Parallel()

	// A WithTypeSchema override cannot rescue an embedded interface: the
	// refusal is encoding/json/v2's, about the declaration rather than the
	// schema, so generation reports it before the override is consulted.
	_, err := jsonschema.GenerateFor[hasLeafIface](
		t.Context(),
		jsonschema.WithTypeSchemaFor[LeafIface](jsonschema.TypeSchema{Value: leafIfaceOverride()}),
	)
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

func TestGenerateFor_InterfaceFieldTypeSchemaAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		Val LeafIface `json:"val"`
	}

	nilData, err := json.Marshal(doc{})
	require.NoError(t, err)
	require.JSONEq(t, `{"val":null}`, string(nilData))

	setData, err := json.Marshal(doc{Val: leafIfaceObject{Kind: "a"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"val":{"kind":"a"}}`, string(setData))

	s, err := jsonschema.GenerateFor[doc](
		t.Context(),
		jsonschema.WithTypeSchemaFor[LeafIface](jsonschema.TypeSchema{Value: leafIfaceOverride()}),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// An intercepted interface schema admits null on a regular field too.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"val":{
				"anyOf":[
					{
						"type":"object",
						"properties":{"kind":{"type":"string"}},
						"required":["kind"]
					},
					{"type":"null"}
				]
			}
		},
		"required":["val"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-field serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-field serialization: %s", setData)
}

// TextIface is an exported interface whose method set includes
// [encoding.TextMarshaler]. A non-nil value marshals as a string via
// MarshalText; a nil interface marshals as null.
type TextIface interface {
	encoding.TextMarshaler

	TextValue() string
}

// textVal is a concrete TextMarshaler stored in TextIface fields.
type textVal string

func (v textVal) MarshalText() ([]byte, error) { return []byte(v), nil }

func (v textVal) TextValue() string { return string(v) }

func TestGenerateFor_TextMarshalerInterfaceFieldAdmitsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		T TextIface `json:"t"`
	}

	nilData, err := json.Marshal(doc{})
	require.NoError(t, err)
	require.JSONEq(t, `{"t":null}`, string(nilData))

	setData, err := json.Marshal(doc{T: textVal("hi")})
	require.NoError(t, err)
	require.JSONEq(t, `{"t":"hi"}`, string(setData))

	s, err := jsonschema.GenerateFor[doc](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// A non-nil value marshals via MarshalText as a string; a nil interface
	// marshals as null, so the string schema admits null alongside.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"t":{"anyOf":[{"type":"string"},{"type":"null"}]}
		},
		"required":["t"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-field serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-field serialization: %s", setData)
}

// embedsTextIface embeds the exported TextMarshaler interface. Its direct
// MarshalJSON keeps the promoted-TextMarshaler interception from firing on the
// outer type, so the struct reflects its fields and reaches the embedded
// interface's refusal.
type embedsTextIface struct {
	TextIface

	Name string `json:"name"`
}

func (embedsTextIface) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func TestGenerateFor_EmbeddedTextMarshalerInterfaceRefused(t *testing.T) {
	t.Parallel()

	// Encoding/json/v2 marshals the outer value through its direct
	// MarshalJSON and never walks the fields, but the generator's documented
	// policy reflects a direct marshaler's fields as its best guess, and the
	// reflected declaration carries an embedded interface v2 refuses to walk.
	// The conservative answer is the refusal.
	_, err := jsonschema.GenerateFor[embedsTextIface](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "must be explicitly given a JSON name")
}

// shadowedProvided is an embedded type intercepted by a WithTypeSchema
// override, so its composition rides an allOf branch instead of promoting
// fields. Its schema asserts X is a required integer.
type shadowedProvided struct {
	X int `json:"X"`
}

// shadowedProvidedWide adds a second field so only part of the embed's
// contribution can be shadowed.
type shadowedProvidedWide struct {
	X int `json:"X"`
	Y int `json:"Y"`
}

// shadowedProvidedB duplicates shadowedProvided as a distinct type, so two
// composed embeds can collide with each other on the same promoted name.
type shadowedProvidedB struct {
	X int `json:"X"`
}

// shadowedUntagged promotes X without a json tag, so a tagged real field at
// the same depth wins encoding/json's tie-break against it.
type shadowedUntagged struct {
	X int
}

// TestGenerateShadowedComposedEmbed pins the module's core accept property
// for allOf-composed embeds under field shadowing: encoding/json resolves a
// composed embed's promoted fields normally, so a shallower outer field wins
// the JSON name and the marshaled object carries the outer value where the
// composed schema asserts the embed's constraints. The generated schema must
// still accept everything the type marshals: the shadowed branch becomes
// conditional, and a partially shadowed composition leaves the object open.
func TestGenerateShadowedComposedEmbed(t *testing.T) {
	t.Parallel()

	provided := jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"X": {Type: "integer"},
		},
		Required: []string{"X"},
	}}

	providedWide := jsonschema.TypeSchema{Value: &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"X": {Type: "integer"},
			"Y": {Type: "integer"},
		},
		Required: []string{"X"},
	}}

	t.Run("fully shadowed embed accepts marshaled output", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.

			X string `json:"X"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{X: "hi"})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi"}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the generated schema must accept the type's own marshaled JSON")
	})

	t.Run("partially shadowed embed accepts marshaled output", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvidedWide //nolint:unused // Embedded only; exercised via reflection.

			X string `json:"X"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedWide](), providedWide))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{X: "hi"})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi","Y":0}`, string(data))

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the unshadowed field must not be rejected as unevaluated")
	})

	t.Run("embed name shadows a deeper real field", func(t *testing.T) {
		t.Parallel()

		type deep2 struct { //nolint:unused // Embedded only; exercised via reflection.
			X string `json:"X"`
		}

		type deep1 struct { //nolint:unused // Embedded only; exercised via reflection.
			deep2 //nolint:unused // Embedded only; exercised via reflection.
		}

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.
			deep1            //nolint:unused // Embedded only; exercised via reflection.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{shadowedProvided: shadowedProvided{X: 7}})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":7}`, string(data),
			"encoding/json promotes the embed's shallower field")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the schema must assert the embed's winning field, not the deeper loser")
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"X":"hi"}`)),
			"the deeper real field lost the name and must not type it as a string")
	})

	t.Run("same-depth tie with a real field annihilates both", func(t *testing.T) {
		t.Parallel()

		type embReal struct {
			X string `json:"X"`
		}

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.
			embReal          //nolint:unused,govet // The duplicate json name is the annihilation under test.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{shadowedProvided: shadowedProvided{X: 7}, embReal: embReal{X: "hi"}})
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(data),
			"encoding/json annihilates the same-depth tie")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"neither the branch nor the real field may require the annihilated name")
	})

	t.Run("same-depth tie won by the tagged real field", func(t *testing.T) {
		t.Parallel()

		type embTagged struct {
			X2 string `json:"X"`
		}

		type outer struct {
			shadowedUntagged //nolint:unused // Embedded only; exercised via reflection.
			embTagged        //nolint:unused // Embedded only; exercised via reflection.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedUntagged](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{embTagged: embTagged{X2: "hi"}})
		require.NoError(t, err)
		require.JSONEq(t, `{"X":"hi"}`, string(data),
			"encoding/json's tag tie-break keeps the tagged real field")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"the tagged winner's schema must accept its own marshaled value")
	})

	t.Run("colliding composed embeds both become conditional", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided  //nolint:unused // Embedded only; exercised via reflection.
			shadowedProvidedB //nolint:unused,govet // The duplicate json name is the collision under test.
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvidedB](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		data, err := json.Marshal(outer{
			shadowedProvided:  shadowedProvided{X: 1},
			shadowedProvidedB: shadowedProvidedB{X: 2},
		})
		require.NoError(t, err)
		require.JSONEq(t, `{}`, string(data),
			"encoding/json annihilates the cross-embed collision")

		require.NoError(t, v.ValidateJSON(t.Context(), data),
			"neither branch may stay unconditionally required")
	})

	t.Run("unshadowed embed keeps the unconditional branch", func(t *testing.T) {
		t.Parallel()

		type outer struct {
			shadowedProvided //nolint:unused // Embedded only; exercised via reflection.

			Other string `json:"other"`
		}

		s, err := jsonschema.GenerateFor[outer](t.Context(),
			jsonschema.WithTypeSchema(reflect.TypeFor[shadowedProvided](), provided))
		require.NoError(t, err)

		v, err := jsonschema.Compile(t.Context(), s)
		require.NoError(t, err)

		require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"X":1,"other":"a"}`)))
		require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"X":"nope","other":"a"}`)),
			"a collision-free composition must stay unconditional")
	})
}
