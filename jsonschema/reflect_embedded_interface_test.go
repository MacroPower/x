package jsonschema_test

import (
	"encoding"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

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
