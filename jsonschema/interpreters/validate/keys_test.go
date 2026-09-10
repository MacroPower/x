package validate_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/interpreters/validate"
)

// TestKeysNameEveryValidator pins Keys to the key table: every name it
// returns is one the interpreter recognizes on a string field, the list is
// sorted, and it holds the validators the package documents, so a table
// entry cannot go unlisted and a listed name cannot be a stranger.
func TestKeysNameEveryValidator(t *testing.T) {
	t.Parallel()

	keys := validate.Keys()
	require.True(t, slices.IsSorted(keys), "Keys must be sorted")

	for _, key := range []string{"required", "min", "max", "len", "eq", "ne", "oneof", "unique", "email", "alpha", "json", "base64"} {
		assert.Contains(t, keys, key)
	}

	for _, key := range keys {
		typ := reflect.StructOf([]reflect.StructField{{
			Name: "V",
			Type: reflect.TypeFor[string](),
			Tag:  reflect.StructTag(`json:"v" validate:"` + key + `=1"`),
		}})

		_, err := jsonschema.Generate(t.Context(), typ,
			jsonschema.WithTagInterpreter("validate", validate.NewInterpreter()))

		assert.NotErrorIs(t, err, validate.ErrUnrecognizedValidator,
			"Keys names %q, which the interpreter does not know", key)
	}
}
