package schemaclone_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// TestCloneCyclicGraph pins that a cyclic or aliased graph survives the copy
// with its shape intact. A JSON-mediated copy can express neither. A cycle
// recurses until the stack gives out, and two paths to one node come back as
// two independent subtrees. The structural copy reproduces both, so these cases
// exercise the memo.
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
				require.True(t, ok, "a *Schema in Extra must stay a *Schema")
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

// TestCloneCheckedReportsCycles pins the report a caller reads before handing
// the copy to anything that marshals it. A loop counts when the path crosses a
// schema, whether it closes on a schema or on a container. Aliasing is not a
// loop, and a loop through containers alone is one encoding/json catches within
// its own encoder. Every row also holds [schemaclone.HasCycle] to the same
// answer, so the report-only entry point cannot drift from
// [schemaclone.CloneChecked].
func TestCloneCheckedReportsCycles(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema
		want  bool
	}{
		"a self cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{}
				s.Items = s

				return s
			},
			want: true,
		},
		"a cycle through an unknown keyword": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{}
				s.Extra = map[string]any{"self": s}

				return s
			},
			want: true,
		},
		"a cycle through a typed container": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{}
				s.Extra = map[string]any{"x": []*jsonschema.Schema{s}}

				return s
			},
			want: true,
		},
		"a cycle closed through a container reached from outside it": {
			build: func() *jsonschema.Schema {
				// The shared map is reached first from the root and again from
				// inner, which the root reaches through it. A walk that stops at
				// the second sighting never re-enters inner and never sees the
				// loop, though a marshal still recurses into it.
				shared := map[string]any{}
				inner := &jsonschema.Schema{Extra: map[string]any{"b": shared}}
				shared["s"] = inner

				return &jsonschema.Schema{Extra: map[string]any{"a": shared}}
			},
			want: true,
		},
		"a cycle closed around a schema value": {
			build: func() *jsonschema.Schema {
				// Upstream's MarshalJSON takes a value receiver, so a schema
				// held by value resets the encoder just as a pointer does and
				// the loop through it recurses just as far.
				held := map[string]any{}
				held["self"] = jsonschema.Schema{Extra: held}

				return &jsonschema.Schema{Extra: map[string]any{"x-thing": held}}
			},
			want: true,
		},
		"a cycle closed through a const box": {
			build: func() *jsonschema.Schema {
				// The *any box is the only pointer a value field holds
				// directly, so it needs the same identity treatment a
				// container gets.
				var held any

				inner := jsonschema.Schema{Const: &held}
				held = any(inner)

				return &jsonschema.Schema{Const: &held}
			},
			want: true,
		},
		"a diamond": {
			build: func() *jsonschema.Schema {
				shared := &jsonschema.Schema{Type: "string"}

				return &jsonschema.Schema{
					Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
				}
			},
			want: false,
		},
		"a chain reaching one node twice in sequence": {
			build: func() *jsonschema.Schema {
				shared := &jsonschema.Schema{Type: "string"}

				return &jsonschema.Schema{
					AllOf: []*jsonschema.Schema{{Items: shared}, {Items: shared}},
				}
			},
			want: false,
		},
		"a nil schema": {
			build: func() *jsonschema.Schema { return nil },
			want:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cp, cyclic := schemaclone.CloneChecked(tc.build())
			assert.Equal(t, tc.want, cyclic)

			assert.Equal(t, tc.want, schemaclone.HasCycle(tc.build()),
				"HasCycle reports what CloneChecked reports")

			if !tc.want && cp != nil {
				_, err := json.Marshal(cp)
				require.NoError(t, err, "an acyclic copy marshals")
			}
		})
	}
}

// TestCloneCheckedIgnoresContainerSelfReference pins the carve-out. A container
// holding itself with no schema in between goes unreported, because a marshal
// meets that one inside a single encoder and returns an ordinary error rather
// than recursing.
func TestCloneCheckedIgnoresContainerSelfReference(t *testing.T) {
	t.Parallel()

	held := map[string]any{}
	held["self"] = held

	cp, cyclic := schemaclone.CloneChecked(&jsonschema.Schema{Extra: held})
	assert.False(t, cyclic)

	copied, ok := cp.Extra["self"].(map[string]any)
	require.True(t, ok)
	assert.NotEqual(t, reflect.ValueOf(held).Pointer(), reflect.ValueOf(copied).Pointer(),
		"the copy owns its own container")

	_, err := json.Marshal(cp)
	require.Error(t, err, "encoding/json reports this cycle on its own")
}
