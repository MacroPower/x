package jsonschema_test

import (
	"encoding"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// Encoding/json never flattens an embedded interface: the field is a regular
// leaf keyed by the field name, emitted as null when the interface is nil. The
// generated schema must model it the same way, or the default
// additionalProperties: false rejects every marshaled instance.

// LeafIface is an exported interface embedded as a leaf field.
type LeafIface interface{ LeafValue() string }

// leafIfaceString is a concrete LeafIface that marshals as a JSON string.
type leafIfaceString string

func (s leafIfaceString) LeafValue() string { return string(s) }

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

func TestGenerateFor_EmbeddedInterfaceLeafProperty(t *testing.T) {
	t.Parallel()

	// A nil interface marshals as null; a non-nil one as its concrete value.
	// Either way the key is always present.
	nilData, err := json.Marshal(hasLeafIface{Name: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"LeafIface":null,"name":"x"}`, string(nilData))

	setData, err := json.Marshal(hasLeafIface{LeafIface: leafIfaceString("hi"), Name: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"LeafIface":"hi","name":"x"}`, string(setData))

	s, err := jsonschema.GenerateFor[hasLeafIface](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"LeafIface":true,
			"name":{"type":"string"}
		},
		"required":["LeafIface","name"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-embed serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-embed serialization: %s", setData)
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

	nilData, err := json.Marshal(hasLeafIface{Name: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"LeafIface":null,"name":"x"}`, string(nilData))

	setData, err := json.Marshal(hasLeafIface{LeafIface: leafIfaceObject{Kind: "a"}, Name: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"LeafIface":{"kind":"a"},"name":"x"}`, string(setData))

	s, err := jsonschema.GenerateFor[hasLeafIface](
		t.Context(),
		jsonschema.WithTypeSchemaFor[LeafIface](jsonschema.TypeSchema{Value: leafIfaceOverride()}),
	)
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The intercepted embed is a leaf property under the field name; the
	// override admits null alongside, since a nil interface marshals as null.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"LeafIface":{
				"anyOf":[
					{
						"type":"object",
						"properties":{"kind":{"type":"string"}},
						"required":["kind"]
					},
					{"type":"null"}
				]
			},
			"name":{"type":"string"}
		},
		"required":["LeafIface","name"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, nilData),
		"generated schema rejected the nil-embed serialization: %s", nilData)
	require.NoError(t, validateJSON(t.Context(), s, setData),
		"generated schema rejected the non-nil-embed serialization: %s", setData)
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
// outer type, so the struct reflects its fields and exposes how the embedded
// interface itself is handled.
type embedsTextIface struct {
	TextIface

	Name string `json:"name"`
}

func (embedsTextIface) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

func TestGenerateFor_EmbeddedTextMarshalerInterfaceLeafProperty(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[embedsTextIface](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	// The exported embed is a leaf property under the field name: a string when
	// non-nil (MarshalText), null when nil.
	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"TextIface":{"anyOf":[{"type":"string"},{"type":"null"}]},
			"name":{"type":"string"}
		},
		"required":["TextIface","name"],
		"additionalProperties":false
	}`, string(got))
}
