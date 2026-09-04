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

// TestCloneTreeDuplicatesAliasedNodes pins the tree property: a node reached
// through two paths is copied once per path, the report names the source
// with both copies, and a node reached once is not reported.
func TestCloneTreeDuplicatesAliasedNodes(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string"}
	single := &jsonschema.Schema{Type: "integer"}
	src := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared, "c": single},
	}

	tree, cyc := schemaclone.CloneTree(src)
	require.Nil(t, cyc)

	a, b := tree.Root.Properties["a"], tree.Root.Properties["b"]
	assert.NotSame(t, a, b, "each path holds its own copy")
	assert.NotSame(t, shared, a)
	assert.Equal(t, "string", a.Type)
	assert.Equal(t, "string", b.Type)

	require.Len(t, tree.Aliased, 1)
	assert.ElementsMatch(t, []*jsonschema.Schema{a, b}, tree.Aliased[shared])
}

// TestCloneTreeDuplicatesAliasedContainers pins that the value containers an
// any-typed field holds are copied per path too, so the copy shares no
// mutable interior with itself.
func TestCloneTreeDuplicatesAliasedContainers(t *testing.T) {
	t.Parallel()

	shared := map[string]any{"k": "v"}
	src := &jsonschema.Schema{Extra: map[string]any{"x": shared, "y": shared}}

	tree, cyc := schemaclone.CloneTree(src)
	require.Nil(t, cyc)

	x, ok := tree.Root.Extra["x"].(map[string]any)
	require.True(t, ok)

	y, ok := tree.Root.Extra["y"].(map[string]any)
	require.True(t, ok)

	assert.NotEqual(t, reflect.ValueOf(x).Pointer(), reflect.ValueOf(y).Pointer())
	assert.Equal(t, shared, x)
}

// TestCloneTreeReportsCycles pins that a loop crossing a schema is reported
// where CloneChecked reports it, through a sub-schema keyword or a value
// field, since a cycle has no tree form.
func TestCloneTreeReportsCycles(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema
		want  *schemaclone.Cycle
	}{
		"a self cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{}
				s.Items = s

				return s
			},
			want: &schemaclone.Cycle{Path: "/items"},
		},
		"a two-node cycle": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{}
				b := &jsonschema.Schema{Items: a}
				a.Items = b

				return a
			},
			want: &schemaclone.Cycle{Path: "/items/items"},
		},
		"a cycle through an unknown keyword": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{}
				s.Extra = map[string]any{"self": s}

				return s
			},
			want: &schemaclone.Cycle{Path: "/self"},
		},
		"a cycle closed through a container reached from outside it": {
			build: func() *jsonschema.Schema {
				shared := map[string]any{}
				inner := &jsonschema.Schema{Extra: map[string]any{"b": shared}}
				shared["s"] = inner

				return &jsonschema.Schema{Extra: map[string]any{"a": shared}}
			},
			want: &schemaclone.Cycle{Path: "/a/s/b", Target: "/a"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, cyc := schemaclone.CloneTree(tc.build())
			require.NotNil(t, cyc)
			assert.Equal(t, tc.want, cyc)
		})
	}
}

// TestCloneTreeKeepsContainerSelfReference pins the carve-out CloneChecked
// makes: a container holding itself with no schema in between copies as a
// cyclic container and goes unreported, since encoding/json meets that one
// inside a single encoder and returns an ordinary error.
func TestCloneTreeKeepsContainerSelfReference(t *testing.T) {
	t.Parallel()

	held := map[string]any{}
	held["self"] = held

	tree, cyc := schemaclone.CloneTree(&jsonschema.Schema{Extra: held})
	assert.Nil(t, cyc)

	copied, ok := tree.Root.Extra["self"].(map[string]any)
	require.True(t, ok)
	assert.NotEqual(t, reflect.ValueOf(held).Pointer(), reflect.ValueOf(copied).Pointer(),
		"the copy owns its own container")
	assert.Equal(t, reflect.ValueOf(copied).Pointer(), reflect.ValueOf(copied["self"]).Pointer(),
		"the copy holds itself as the source does")

	_, err := json.Marshal(tree.Root)
	require.Error(t, err, "encoding/json reports this cycle on its own")
}
