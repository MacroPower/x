package validate_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// diveLevel is a numeric element type that marshals itself as text, so its
// element schema is a string. It is the element shape a sequence-wide rule used
// to get wrong: the field-level path applied a coercion gate that the
// sequence-wide path skipped, so oneof and dive,oneof disagreed about the same
// element type.
type diveLevel int

// MarshalText writes the level as L<n>.
func (l diveLevel) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "L%d", int(l)), nil
}

// generateDiveShape builds a one-field struct of the given type carrying tag.
func generateDiveShape(t *testing.T, typ reflect.Type, tag string) (*jsonschema.Schema, error) {
	t.Helper()

	structType := reflect.StructOf([]reflect.StructField{{
		Name: "V",
		Type: typ,
		Tag:  reflect.StructTag(fmt.Sprintf(`json:"v" validate:%q`, tag)),
	}})

	//nolint:wrapcheck // The test asserts on the generation error itself.
	return jsonschema.Generate(t.Context(), structType,
		jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))
}

// TestDiveEquivalenceOnSequences pins the property the shared element path
// buys: on a slice or array, a rule written on the sequence and the same rule
// written under a dive produce identical schemas.
//
// Both spellings mean "constrain the elements", and both now reach the elements
// through one implementation that re-dispatches on each element's own shape.
// Before that, the sequence-wide path had its own element handling, which is
// exactly where it could forget something the dive path remembered.
func TestDiveEquivalenceOnSequences(t *testing.T) {
	t.Parallel()

	for name, typ := range map[string]reflect.Type{
		"slice of string":                  reflect.TypeFor[[]string](),
		"slice of int8":                    reflect.TypeFor[[]int8](),
		"fixed array of string":            reflect.TypeFor[[3]string](),
		"fixed array of int8":              reflect.TypeFor[[2]int8](),
		"slice of pointer to string":       reflect.TypeFor[[]*string](),
		"slice of text-marshaling numeric": reflect.TypeFor[[]diveLevel](),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			values := "1 2"
			if typ.Elem().Kind() == reflect.String ||
				(typ.Elem().Kind() == reflect.Pointer && typ.Elem().Elem().Kind() == reflect.String) {
				values = "a b"
			}

			direct, directErr := generateDiveShape(t, typ, "oneof="+values)
			dived, divedErr := generateDiveShape(t, typ, "dive,oneof="+values)

			require.NoError(t, directErr, "the sequence-wide spelling")
			require.NoError(t, divedErr, "the dive spelling")

			wantJSON, err := json.Marshal(direct)
			require.NoError(t, err)

			gotJSON, err := json.Marshal(dived)
			require.NoError(t, err)

			assert.JSONEq(t, string(wantJSON), string(gotJSON),
				"oneof and dive,oneof constrain the elements identically")
		})
	}
}

// TestDiveEquivalenceExceptions pins the two shapes where the sequence-wide and
// dive spellings deliberately differ, so the asymmetry stays a decision rather
// than drifting back into an accident.
func TestDiveEquivalenceExceptions(t *testing.T) {
	t.Parallel()

	t.Run("a map descends only under an explicit dive", func(t *testing.T) {
		t.Parallel()

		// The dive says "descend" outright, so it constrains the values. A bare
		// oneof on a map has no go-playground element meaning at all, so there is
		// nothing for it to mean and it is rejected rather than guessed at.
		typ := reflect.TypeFor[map[string]string]()

		dived, err := generateDiveShape(t, typ, "dive,oneof=a b")
		require.NoError(t, err, "a dive into a map constrains its values")
		require.NotNil(t, dived.Properties["v"].AdditionalProperties)
		assert.Equal(t, []any{"a", "b"}, dived.Properties["v"].AdditionalProperties.Enum)

		_, err = generateDiveShape(t, typ, "oneof=a b")
		require.Error(t, err, "a bare oneof on a map has no element meaning")
		assert.Contains(t, err.Error(), "no element meaning")
	})

	t.Run("a byte slice rejects both spellings", func(t *testing.T) {
		t.Parallel()

		// The field encodes as one base64 string, so neither spelling has an
		// element schema to reach. Both report, rather than one reporting and the
		// other silently accepting a constraint it cannot apply.
		typ := reflect.TypeFor[[]byte]()

		for _, tag := range []string{"oneof=a b", "dive,oneof=a b"} {
			_, err := generateDiveShape(t, typ, tag)
			require.Error(t, err, "validate:%q on a []byte", tag)
			assert.Contains(t, err.Error(), "no item schema to constrain")
		}
	})
}
