package schemaclone_test

import (
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemaclone"
)

// wrapper is a typed container with an exported schema field, the shape the
// reflection walk must reach. Encoding a wrapper serializes that field, so the
// copy owes it the same treatment a sub-schema gets.
type wrapper struct {
	S *jsonschema.Schema `json:"s"`
}

// hidden holds a schema only in an unexported field. Reflection cannot write
// such a field, so the copy keeps the source's pointer there; this is the one
// documented exception to the no-shared-values contract.
type hidden struct {
	s *jsonschema.Schema //nolint:unused // Read through reflection in the test's assertion.
}

// TestCloneTypedContainers covers the reflection fallback: a schema reachable
// only through a typed container an any-typed field happens to hold. The copy
// must reach those the way encoding/json's reflection would, so the container is
// rebuilt and the schema inside it routes through the same memo the sub-schema
// walk uses.
//
// Each row also pins the cycle report, nil where the graph holds no loop. The
// pinned pointers show the reflection walk pushes a segment at every edge it
// follows. A typed slice addresses its element by index, a typed map by its
// printed key, and a struct by the name its json tag gives.
func TestCloneTypedContainers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema
		check func(t *testing.T, src, cp *jsonschema.Schema)
		want  *schemaclone.Cycle
	}{
		"cycle through typed schema slice in Extra": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []*jsonschema.Schema{s}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				list, ok := cp.Extra["x"].([]*jsonschema.Schema)
				require.True(t, ok, "the copy keeps the container's Go type")
				assert.Same(t, cp, list[0])
			},
			want: &schemaclone.Cycle{Path: "/x/0"},
		},
		"cycle through typed schema map in Extra": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": map[string]*jsonschema.Schema{"y": s}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				m, ok := cp.Extra["x"].(map[string]*jsonschema.Schema)
				require.True(t, ok, "the copy keeps the container's Go type")
				assert.Same(t, cp, m["y"])
			},
			want: &schemaclone.Cycle{Path: "/x/y"},
		},
		"two-node cycle split across typed slices": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{Type: "object"}
				b := &jsonschema.Schema{Type: "array"}
				a.Extra = map[string]any{"x": []*jsonschema.Schema{b}}
				b.Extra = map[string]any{"y": []*jsonschema.Schema{a}}

				return a
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				outer, ok := cp.Extra["x"].([]*jsonschema.Schema)
				require.True(t, ok)

				inner, ok := outer[0].Extra["y"].([]*jsonschema.Schema)
				require.True(t, ok)
				assert.Same(t, cp, inner[0])
			},
			want: &schemaclone.Cycle{Path: "/x/0/y/0"},
		},
		"cycle through schema value element in typed slice": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []jsonschema.Schema{{Items: s}}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				list, ok := cp.Extra["x"].([]jsonschema.Schema)
				require.True(t, ok, "a schema held by value stays a value")
				assert.Same(t, cp, list[0].Items, "its sub-schema pointer still routes through the memo")
			},
			want: &schemaclone.Cycle{Path: "/x/0/items"},
		},
		"cycle through exported struct field in Examples": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Examples = []any{wrapper{S: s}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				held, ok := cp.Examples[0].(wrapper)
				require.True(t, ok, "the copy keeps the struct's Go type")
				assert.Same(t, cp, held.S)
			},
			want: &schemaclone.Cycle{Path: "/examples/0/s"},
		},
		"cycle through typed slice nested under any containers": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Enum = []any{map[string]any{"deep": []*jsonschema.Schema{s}}}

				return s
			},
			check: func(t *testing.T, _, cp *jsonschema.Schema) {
				t.Helper()

				object, ok := cp.Enum[0].(map[string]any)
				require.True(t, ok)

				list, ok := object["deep"].([]*jsonschema.Schema)
				require.True(t, ok)
				assert.Same(t, cp, list[0])
			},
			want: &schemaclone.Cycle{Path: "/enum/0/deep/0"},
		},
		"acyclic typed schema slice in Extra is unaliased": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": []*jsonschema.Schema{{Type: "string"}}}

				return s
			},
			check: func(t *testing.T, src, cp *jsonschema.Schema) {
				t.Helper()

				before, ok := src.Extra["x"].([]*jsonschema.Schema)
				require.True(t, ok)

				after, ok := cp.Extra["x"].([]*jsonschema.Schema)
				require.True(t, ok)
				assert.NotSame(t, before[0], after[0], "the nested schema is copied, not shared")
			},
		},
		"schema behind an unexported struct field stays shared": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x": hidden{s: s}}

				return s
			},
			check: func(t *testing.T, src, cp *jsonschema.Schema) {
				t.Helper()

				held, ok := cp.Extra["x"].(hidden)
				require.True(t, ok)
				assert.Same(t, src, held.s,
					"reflection cannot write an unexported field, so the copy keeps the source's pointer")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := tc.build()

			cp, cyc := schemaclone.CloneChecked(src)
			assert.Equal(t, tc.want, cyc)

			require.NotNil(t, cp)
			assert.NotSame(t, src, cp)
			assert.True(t, reflect.DeepEqual(src, cp), "the copy is value-equal to the source")

			tc.check(t, src, cp)
		})
	}
}
