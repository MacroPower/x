package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A hand-built const or enum value can carry container shapes the schema
// parser never produces, most commonly the map[any]any gopkg.in/yaml.v2
// decodes documents into. The validator compares such a value the way its
// document renders it, so a string-keyed map[any]any equals the object
// encoding/json v1 writes for it. A map v1 refuses to marshal (one with a
// bool key) has no document form, and comparing it against a decoded object
// must report the values unequal instead of panicking inside the upstream
// reflect-based comparison (reflect.Value.MapIndex rejects an interface{}
// key against a string-keyed map).
func TestValidateHandBuiltMapAnyConst(t *testing.T) {
	t.Parallel()

	stringKeyed := any(map[any]any{"a": 1})
	boolKeyed := any(map[any]any{true: 1})

	tests := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		err      string
	}{
		"const matches its document": {
			schema:   &jsonschema.Schema{Const: &stringKeyed},
			instance: map[string]any{"a": 1.0},
		},
		"const rejects another object": {
			schema:   &jsonschema.Schema{Const: &stringKeyed},
			instance: map[string]any{"a": 2.0},
			err:      "const",
		},
		"const without a document form": {
			schema:   &jsonschema.Schema{Const: &boolKeyed},
			instance: map[string]any{"a": 1.0},
			err:      "const",
		},
		"enum matches its document": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{"a": 1}}},
			instance: map[string]any{"a": 1.0},
		},
		"enum rejects another object": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{"a": 1}}},
			instance: map[string]any{"a": 2.0},
			err:      "enum",
		},
		"enum without a document form": {
			schema:   &jsonschema.Schema{Enum: []any{map[any]any{true: 1}}},
			instance: map[string]any{"a": 1.0},
			err:      "enum",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := jsonschema.Compile(t.Context(), tc.schema)
			require.NoError(t, err)

			err = v.Validate(t.Context(), tc.instance)
			if tc.err == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}
