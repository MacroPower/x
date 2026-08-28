package schemaclone_test

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// TestCloneCyclicGraph pins that a cyclic or aliased graph survives the copy
// with its shape intact. A JSON round-trip could express neither: a cycle
// recursed until the stack gave out, and two paths to one node came back as two
// independent subtrees. The structural copy reproduces both, so the memo's edge
// cases are what these cases exercise.
func TestCloneCyclicGraph(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema
		check func(t *testing.T, src, cp *jsonschema.Schema)
	}{
		"self cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Items = s

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()
				assert.Same(t, cp, cp.Items, "the copy closes the loop on itself")
			},
		},
		"two-node cycle": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{Type: "object"}
				b := &jsonschema.Schema{Type: "array"}
				a.Items = b
				b.Items = a

				return a
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()
				assert.Same(t, cp, cp.Items.Items, "the copy closes the loop after two edges")
			},
		},
		"cycle through Extra": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"self": s}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()
				assert.Same(t, cp, cp.Extra["self"], "an unknown keyword holding the node stays that node")
			},
		},
		"cycle through nested Examples": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Examples = []any{map[string]any{"schema": s}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				nested, ok := cp.Examples[0].(map[string]any)
				require.True(t, ok, "the copy keeps the nested object's shape")
				assert.Same(t, cp, nested["schema"])
			},
		},
		"cycle through Const": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				v := any(s)
				s.Const = &v

				return s
			},
			check: func(t *testing.T, src, cp *jsonschema.Schema) {
				t.Helper()
				assert.NotSame(t, src.Const, cp.Const, "the const box is reallocated")
				assert.Same(t, cp, *cp.Const)
			},
		},
		"schema pointer in Extra stays a schema": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"meta": &jsonschema.Schema{Type: "string"}}

				return s
			},
			check: func(t *testing.T, src, cp *jsonschema.Schema) {
				t.Helper()

				meta, ok := cp.Extra["meta"].(*jsonschema.Schema)
				require.True(t, ok, "the round-trip degraded this to map[string]any; the structural copy does not")
				assert.Equal(t, "string", meta.Type)
				assert.NotSame(t, src.Extra["meta"], meta)
			},
		},
		"diamond keeps one node": {
			build: func() *jsonschema.Schema {
				shared := &jsonschema.Schema{Type: "string"}

				return &jsonschema.Schema{
					Type: "object",
					Properties: map[string]*jsonschema.Schema{
						"a": shared,
						"b": shared,
					},
				}
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()
				assert.Same(t, cp.Properties["a"], cp.Properties["b"],
					"a node reached twice is copied once and reached twice")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := tc.build()
			cp := schemaclone.Clone(src)

			require.NotNil(t, cp)
			assert.NotSame(t, src, cp)
			assert.True(t, reflect.DeepEqual(src, cp), "the copy is value-equal to the source")

			tc.check(t, src, cp)
		})
	}
}
