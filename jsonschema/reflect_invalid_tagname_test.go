package jsonschema_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A json tag name is the run of runes before the first comma, backslash, or
// quote character, whatever those runes are. A name cut short by a reserved
// rune other than the comma keeps its leading identifier instead, and is
// discarded in favor of the Go field name when it has none.

func TestGenerateFor_TagNameGrammar(t *testing.T) {
	t.Parallel()

	// An unreserved-rune name is kept as written and carries no error.
	type clean struct {
		A string `json:"a😀b"`
	}

	data, err := json.Marshal(clean{A: "1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"a😀b":"1"}`, string(data))

	s, err := jsonschema.GenerateFor[clean](t.Context())
	require.NoError(t, err)

	got, err := json.Marshal(s)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"a😀b":{"type":"string"}
		},
		"required":["a😀b"],
		"additionalProperties":false
	}`, string(got))

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)

	// A name cut short by a reserved rune is a malformed tag under
	// encoding/json/v2, so both marshaling and generation refuse it.
	type cut struct {
		C string `json:"x\"y"` //nolint:staticcheck // Intentional: reserved rune cuts the name.
	}

	_, err = json.Marshal(cut{C: "3"})
	require.ErrorContains(t, err, "malformed `json` tag")

	_, err = jsonschema.GenerateFor[cut](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "malformed `json` tag")
}

type taggedEmbedInner struct {
	X string `json:"x"`
}

type taggedEmbedOuter struct {
	taggedEmbedInner `json:"a😀b"` //nolint:unused // Exercised via reflection.

	Y string `json:"y"`
}

func TestGenerateFor_TagNameOnEmbeddedStructNamesIt(t *testing.T) {
	t.Parallel()

	// The name is honored, so the embed is a regular named field rather than a
	// promotion source.
	data, err := json.Marshal(taggedEmbedOuter{taggedEmbedInner: taggedEmbedInner{X: "1"}, Y: "2"})
	require.NoError(t, err)
	require.JSONEq(t, `{"a😀b":{"x":"1"},"y":"2"}`, string(data))

	s, err := jsonschema.GenerateFor[taggedEmbedOuter](t.Context())
	require.NoError(t, err)

	require.NoError(t, validateJSON(t.Context(), s, data),
		"generated schema rejected the struct's own serialization: %s", data)
}

type namelessTagInner struct {
	X string `json:"x"`
}

type namelessTagOuter struct {
	namelessTagInner `json:"\"q"` //nolint:unused,staticcheck // Exercised via reflection; fields promoted.

	Y string `json:"y"`
}

func TestGenerateFor_NamelessTagOnEmbeddedStructPromotes(t *testing.T) {
	t.Parallel()

	// The tag's name chunk opens with a reserved rune and yields no name, so
	// the embed stays anonymous and its fields are promoted -- and the
	// malformed tag is an error under encoding/json/v2, which generation
	// mirrors.
	_, err := json.Marshal(namelessTagOuter{namelessTagInner: namelessTagInner{X: "1"}, Y: "2"})
	require.ErrorContains(t, err, "malformed `json` tag")

	_, err = jsonschema.GenerateFor[namelessTagOuter](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, "malformed `json` tag")
}

// TestGenerateFor_DuplicateNameInOneDeclaration pins the same-declaration
// conflict: two fields of one struct claiming a JSON name is a refusal under
// encoding/json/v2 (v1 silently dropped both), and generation mirrors it
// with v2's verbatim text.
func TestGenerateFor_DuplicateNameInOneDeclaration(t *testing.T) {
	t.Parallel()

	type dup struct {
		A int `json:"dup"`
		B int `json:"dup"` //nolint:govet // The duplicate json name is the refusal under test.
	}

	_, err := json.Marshal(dup{})
	require.ErrorContains(t, err, `Go struct fields A and B conflict over JSON object name "dup"`)

	_, err = jsonschema.GenerateFor[dup](t.Context())
	require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
	require.ErrorContains(t, err, `Go struct fields A and B conflict over JSON object name "dup"`)
}

// unexportedZeroer carries the IsZero method encoding/json/v2 would have to
// call through an unexported named field under omitzero.
type unexportedZeroer struct{ A int }

func (unexportedZeroer) IsZero() bool { return false }

// TestGenerateFor_UnexportedAnonymousNamedField pins encoding/json/v2's
// export rule on the named-field path: an unexported anonymous field with a
// tag name is walked only as a struct v2 can read without calling a method.
// A non-struct type is refused outright, a struct whose marshal or omitzero
// IsZero method would be called through the unexported field is refused for
// method calls, and a plain struct is accepted as an ordinary named field.
func TestGenerateFor_UnexportedAnonymousNamedField(t *testing.T) {
	t.Parallel()

	type myInt int //nolint:unused // Embedded only; exercised via reflection.

	type nonStruct struct {
		myInt `json:"n"` //nolint:govet,unused // The tag on the unexported field is the refusal under test; exercised via reflection.
		B     int        `json:"b"`
	}

	type withMethodCall struct {
		unexportedZeroer `json:"in,omitzero"` //nolint:govet,unused // The tag on the unexported field is the refusal under test; exercised via reflection.
	}

	type plain struct{ A int } //nolint:unused // Embedded only; exercised via reflection.

	type accepted struct {
		plain `json:"in"` //nolint:govet,unused // The tag on the unexported field is the case under test; exercised via reflection.
	}

	t.Run("non-struct type is refused", func(t *testing.T) {
		t.Parallel()

		_, err := json.Marshal(nonStruct{})
		require.ErrorContains(t, err, "Go struct field myInt is not exported")

		_, err = jsonschema.GenerateFor[nonStruct](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
		require.ErrorContains(t, err, "Go struct field myInt is not exported")
	})

	t.Run("struct needing a method call is refused", func(t *testing.T) {
		t.Parallel()

		_, err := json.Marshal(withMethodCall{})
		require.ErrorContains(t, err, "Go struct field unexportedZeroer is not exported for method calls")

		_, err = jsonschema.GenerateFor[withMethodCall](t.Context())
		require.ErrorIs(t, err, jsonschema.ErrInvalidJSONField)
		require.ErrorContains(t, err, "Go struct field unexportedZeroer is not exported for method calls")
	})

	t.Run("plain struct is a named field", func(t *testing.T) {
		t.Parallel()

		data, err := json.Marshal(accepted{})
		require.NoError(t, err)
		assert.JSONEq(t, `{"in":{"A":0}}`, string(data))

		s, err := jsonschema.GenerateFor[accepted](t.Context())
		require.NoError(t, err)
		assert.Contains(t, s.Properties, "in")
	})
}
