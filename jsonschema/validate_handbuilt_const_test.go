package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A hand-built const or enum value can carry container shapes the schema
// parser never produces, most commonly the map[any]any gopkg.in/yaml.v2
// decodes documents into. Comparing such a value against a decoded object
// instance must report the values unequal instead of panicking inside the
// upstream reflect-based comparison (reflect.Value.MapIndex rejects an
// interface{} key against a string-keyed map).
func TestValidateHandBuiltMapAnyConst(t *testing.T) {
	t.Parallel()

	constVal := any(map[any]any{"a": 1})

	tests := map[string]struct {
		schema *jsonschema.Schema
		err    string
	}{
		"const": {
			schema: &jsonschema.Schema{Const: &constVal},
			err:    "const",
		},
		"enum": {
			schema: &jsonschema.Schema{Enum: []any{map[any]any{"a": 1}}},
			err:    "enum",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tc.schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), map[string]any{"a": 1.0})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}
