package validate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestValidateInterpreter_UniqueFieldParamRejected pins that the unique=<field>
// form is a generation error rather than silently degrading to whole-element
// uniqueItems. In go-playground, unique=Name asserts uniqueness of one named
// field across struct elements, strictly stronger than uniqueItems: two
// elements with equal Name but different Age are distinct wholes, so the
// degraded schema would accept an instance the tag rejects.
func TestValidateInterpreter_UniqueFieldParamRejected(t *testing.T) {
	t.Parallel()

	type User struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	cases := map[string]func(context.Context) (*jsonschema.Schema, error){
		"struct slice": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				U []User `json:"u" validate:"unique=Name"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
		"struct map": func(ctx context.Context) (*jsonschema.Schema, error) {
			type F struct {
				U map[string]User `json:"u" validate:"unique=Name"`
			}

			return jsonschema.GenerateFor[F](ctx, validateInterp())
		},
	}

	for name, gen := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := gen(t.Context())
			require.Error(t, err, "unique with a field parameter must be rejected")
			assert.Contains(t, err.Error(), "unique=Name")
			assert.Contains(t, err.Error(), "no JSON Schema equivalent")
			assert.Nil(t, s)
		})
	}
}

// TestValidateInterpreter_UniqueBareFormUnchanged pins that the bare unique
// form keeps its documented uniqueItems mapping alongside the param rejection.
func TestValidateInterpreter_UniqueBareFormUnchanged(t *testing.T) {
	t.Parallel()

	type Form struct {
		Tags []string `json:"tags" validate:"unique"`
	}

	s, err := jsonschema.GenerateFor[Form](t.Context(), validateInterp())
	require.NoError(t, err)
	assert.True(t, s.Properties["tags"].UniqueItems, "bare unique still maps to uniqueItems")
}
