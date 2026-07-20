package validate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// forbiddenString is a named string type so a WithTypeSchemaFor override can
// target the element type of the field under test.
type forbiddenString string

// TestValidateInterpreter_NeWithForeignNotSiblings pins that ne composes with
// an override-authored not that carries keywords beyond a lone const or enum.
// A not holding {const: x, minLength: 5} is a conjunction: merging the new
// forbidden value into its enum (or deduplicating against its const) would
// only forbid the value when the sibling keyword also matches, silently
// weakening or dropping the exclusion. The existing not must instead move
// under allOf with a separate not for the ne value, the same composition the
// default branch already applies to other foreign not shapes.
func TestValidateInterpreter_NeWithForeignNotSiblings(t *testing.T) {
	t.Parallel()

	type Form struct {
		F forbiddenString `json:"f" validate:"ne=y"`
	}

	cases := map[string]struct {
		override *jsonschema.Schema
	}{
		"const with sibling keyword": {
			override: &jsonschema.Schema{
				Type: "string",
				Not:  &jsonschema.Schema{Const: new(any("x")), MinLength: new(5)},
			},
		},
		"same const with sibling keyword": {
			// The dedup shortcut ("y" is already forbidden) is equally unsound
			// with siblings present: not{const: y, minLength: 5} only rejects
			// a "y" of length >= 5, which is no "y" at all.
			override: &jsonschema.Schema{
				Type: "string",
				Not:  &jsonschema.Schema{Const: new(any("y")), MinLength: new(5)},
			},
		},
		"enum with sibling keyword": {
			override: &jsonschema.Schema{
				Type: "string",
				Not:  &jsonschema.Schema{Enum: []any{"x"}, MinLength: new(5)},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[Form](t.Context(),
				jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
				jsonschema.WithTypeSchemaFor[forbiddenString](tc.override),
			)
			require.NoError(t, err)

			v, err := jsonschema.Compile(t.Context(), s)
			require.NoError(t, err)

			require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"f":"y"}`)),
				"ne=y must reject the forbidden value regardless of the override's not siblings")
			require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"f":"z"}`)),
				"a value neither forbidden by ne nor by the override stays valid")
		})
	}
}

// TestValidateInterpreter_NeStillMergesBareNot pins that the const and enum
// accumulation paths are untouched when the existing not carries nothing else:
// required and ne on a numeric field still fold their forbidden values into a
// single not.enum rather than an allOf.
func TestValidateInterpreter_NeStillMergesBareNot(t *testing.T) {
	t.Parallel()

	type Form struct {
		N int `json:"n" validate:"required,ne=5"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(),
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()),
	)
	require.NoError(t, err)

	prop := s.Properties["n"]
	require.NotNil(t, prop)
	require.NotNil(t, prop.Not)
	require.Nil(t, prop.AllOf, "bare not.const still promotes to not.enum, not allOf")
	require.Len(t, prop.Not.Enum, 2)
}
