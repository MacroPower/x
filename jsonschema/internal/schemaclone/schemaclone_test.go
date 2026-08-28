package schemaclone_test

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

func TestClone(t *testing.T) {
	t.Parallel()

	t.Run("produces an independent deep copy", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			Type: "object",
			Enum: []any{"x", "y"},
			Properties: map[string]*jsonschema.Schema{
				"a": {Type: "string"},
			},
		}

		cp := schemaclone.Clone(src)
		require.NotNil(t, cp)

		assert.Equal(t, src.Type, cp.Type)
		assert.NotSame(t, src, cp)
		assert.NotSame(t, src.Properties["a"], cp.Properties["a"])

		// Mutating the copy's shared-by-value fields must not reach src.
		cp.Enum[0] = "mutated"
		assert.Equal(t, "x", src.Enum[0])
	})

	t.Run("copies PropertyOrder at every depth", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			PropertyOrder: []string{"a", "b"},
			Items: &jsonschema.Schema{
				PropertyOrder: []string{"deep", "nested"},
			},
			Properties: map[string]*jsonschema.Schema{
				"a": {PropertyOrder: []string{"inner"}},
			},
		}

		cp := schemaclone.Clone(src)

		assert.Equal(t, []string{"a", "b"}, cp.PropertyOrder)
		assert.Equal(t, []string{"deep", "nested"}, cp.Items.PropertyOrder)
		assert.Equal(t, []string{"inner"}, cp.Properties["a"].PropertyOrder)
	})

	t.Run("cloned PropertyOrder slice is unaliased from src", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{PropertyOrder: []string{"a", "b"}}

		cp := schemaclone.Clone(src)

		cp.PropertyOrder[0] = "mutated"
		assert.Equal(t, "a", src.PropertyOrder[0])
	})

	t.Run("unaliases the interior of a map of string lists", func(t *testing.T) {
		t.Parallel()

		src := &jsonschema.Schema{
			DependencyStrings: map[string][]string{"a": {"b"}},
			DependentRequired: map[string][]string{"c": {"d"}},
		}

		cp := schemaclone.Clone(src)

		cp.DependencyStrings["a"][0] = "mutated"
		cp.DependentRequired["c"][0] = "mutated"

		assert.Equal(t, []string{"b"}, src.DependencyStrings["a"])
		assert.Equal(t, []string{"d"}, src.DependentRequired["c"])
	})
}

// TestClonePreservesNumberValues covers the any-typed value fields. A copy
// routed through JSON decodes their numbers as float64, silently rounding a
// json.Number beyond float64 precision and changing what a cloned const or enum
// accepts. A structural copy carries the literal across untouched, since
// json.Number is a string type.
func TestClonePreservesNumberValues(t *testing.T) {
	t.Parallel()

	const big = "12345678901234567890"

	src := &jsonschema.Schema{
		Const:    new(any(json.Number(big))),
		Enum:     []any{json.Number(big), "x"},
		Examples: []any{json.Number(big)},
		Extra:    map[string]any{"x-custom": json.Number(big)},
		Items:    &jsonschema.Schema{Const: new(any(json.Number(big)))},
	}

	cp := schemaclone.Clone(src)

	assert.Equal(t, json.Number(big), *cp.Const)
	assert.Equal(t, []any{json.Number(big), "x"}, cp.Enum)
	assert.Equal(t, []any{json.Number(big)}, cp.Examples)
	assert.Equal(t, json.Number(big), cp.Extra["x-custom"])
	assert.Equal(t, json.Number(big), *cp.Items.Const, "nested nodes carry their literals too")

	assert.NotSame(t, src.Const, cp.Const, "the const box is reallocated")

	*src.Const = any(json.Number("1"))
	src.Enum[1] = "mutated"

	assert.Equal(t, json.Number(big), *cp.Const)
	assert.Equal(t, "x", cp.Enum[1])
}

// TestCloneFidelity pins the copy's faithfulness on the shapes a JSON-mediated
// copy normalizes away. Such a copy re-encodes hand-built Go values, hoists an
// unknown keyword whose name collides with a real one, and refuses outright to
// marshal a schema upstream's own checks reject. The remaining cases pin the
// contract those shapes share: what the source holds is what the copy holds.
func TestCloneFidelity(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		src   *jsonschema.Schema
		check func(t *testing.T, cp *jsonschema.Schema)
	}{
		"a Go-typed const keeps its Go type": {
			src:   &jsonschema.Schema{Const: new(any(42))},
			check: func(t *testing.T, cp *jsonschema.Schema) { t.Helper(); assert.Equal(t, 42, *cp.Const) },
		},
		"an unknown keyword colliding with a real one stays unknown": {
			src: &jsonschema.Schema{Extra: map[string]any{"type": "string"}},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				assert.Empty(t, cp.Type, "an Extra key colliding with a keyword must stay in Extra")
				assert.Equal(t, "string", cp.Extra["type"])
			},
		},
		"a nil sub-schema element stays nil": {
			src: &jsonschema.Schema{AllOf: []*jsonschema.Schema{nil}},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				require.Len(t, cp.AllOf, 1)
				assert.Nil(t, cp.AllOf[0], "a nil sub-schema element must stay nil")
			},
		},
		"a schema upstream refuses to marshal still copies": {
			src: &jsonschema.Schema{Type: "object", Types: []string{"string"}},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				assert.Equal(t, "object", cp.Type)
				assert.Equal(t, []string{"string"}, cp.Types)
			},
		},
		"both items forms survive together": {
			src: &jsonschema.Schema{
				Items:      &jsonschema.Schema{Type: "string"},
				ItemsArray: []*jsonschema.Schema{{Type: "integer"}},
			},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				require.NotNil(t, cp.Items, "the traversal skips Items here; the copy must not")
				assert.Equal(t, "string", cp.Items.Type)
				require.Len(t, cp.ItemsArray, 1)
				assert.Equal(t, "integer", cp.ItemsArray[0].Type)
			},
		},
		"an empty container stays present": {
			src: &jsonschema.Schema{
				Enum:       []any{},
				Properties: map[string]*jsonschema.Schema{},
				AllOf:      []*jsonschema.Schema{},
			},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				assert.NotNil(t, cp.Enum, "a present empty enum rejects every instance")
				assert.NotNil(t, cp.Properties)
				assert.NotNil(t, cp.AllOf)
				assert.Empty(t, cp.Enum)
			},
		},
		"an absent container stays absent": {
			src: &jsonschema.Schema{Type: "string"},
			check: func(t *testing.T, cp *jsonschema.Schema) {
				t.Helper()
				assert.Nil(t, cp.Enum)
				assert.Nil(t, cp.Properties)
				assert.Nil(t, cp.AllOf)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cp := schemaclone.Clone(tc.src)
			require.NotNil(t, cp)

			tc.check(t, cp)
		})
	}
}
