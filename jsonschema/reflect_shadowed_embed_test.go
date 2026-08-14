package jsonschema_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

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
