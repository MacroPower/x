package jsonschema_test

import (
	"encoding/json/v2"
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
