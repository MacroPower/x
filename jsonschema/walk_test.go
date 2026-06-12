package jsonschema_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// childSchemas projects SubschemaEntries onto the bare child schemas, for
// assertions about coverage and order that do not care about pointers.
func childSchemas(s *jsonschema.Schema) []*jsonschema.Schema {
	entries := jsonschema.SubschemaEntries(s)
	if len(entries) == 0 {
		return nil
	}

	children := make([]*jsonschema.Schema, len(entries))
	for i, entry := range entries {
		children[i] = entry.Schema
	}

	return children
}

func TestSubschemaEntriesChildren(t *testing.T) {
	t.Parallel()

	propA := &jsonschema.Schema{Type: "string"}
	propB := &jsonschema.Schema{Type: "integer"}
	defY := &jsonschema.Schema{Type: "object"}
	defZ := &jsonschema.Schema{Type: "array"}
	allOf0 := &jsonschema.Schema{MinLength: jsonschema.Ptr(1)}
	allOf1 := &jsonschema.Schema{MaxLength: jsonschema.Ptr(2)}
	items := &jsonschema.Schema{Type: "number"}
	not := &jsonschema.Schema{}

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   []*jsonschema.Schema
	}{
		"nil schema": {
			schema: nil,
			want:   nil,
		},
		"no sub-schemas": {
			schema: &jsonschema.Schema{Type: "string", Title: "leaf"},
			want:   nil,
		},
		"map children in sorted-key order": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"b": propB, "a": propA},
			},
			want: []*jsonschema.Schema{propA, propB},
		},
		"nil map entries skipped": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a": propA, "b": nil},
			},
			want: []*jsonschema.Schema{propA},
		},
		"nil slice entries skipped": {
			schema: &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{allOf0, nil, allOf1},
			},
			want: []*jsonschema.Schema{allOf0, allOf1},
		},
		"maps then slices then singles": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"b": propB, "a": propA},
				Defs:       map[string]*jsonschema.Schema{"z": defZ, "y": defY},
				AllOf:      []*jsonschema.Schema{allOf0, allOf1},
				Items:      items,
				Not:        not,
			},
			want: []*jsonschema.Schema{propA, propB, defY, defZ, allOf0, allOf1, items, not},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, childSchemas(tc.schema))

			// A second call must return the same order: map children are
			// emitted in sorted-key order, so traversal is deterministic.
			assert.Equal(t, tc.want, childSchemas(tc.schema))
		})
	}
}

// TestSubschemaEntriesDirectOnly pins that SubschemaEntries returns only
// direct children: a grandchild is reachable through Walk, not through one
// SubschemaEntries call.
func TestSubschemaEntriesDirectOnly(t *testing.T) {
	t.Parallel()

	grandchild := &jsonschema.Schema{Type: "string"}
	child := &jsonschema.Schema{Items: grandchild}
	root := &jsonschema.Schema{Not: child}

	assert.Equal(t, []*jsonschema.Schema{child}, childSchemas(root))
}

func TestSubschemaEntries(t *testing.T) {
	t.Parallel()

	propA := &jsonschema.Schema{Type: "string"}
	escaped := &jsonschema.Schema{Type: "boolean"}
	allOf0 := &jsonschema.Schema{MinLength: jsonschema.Ptr(1)}
	allOf1 := &jsonschema.Schema{MaxLength: jsonschema.Ptr(2)}
	items := &jsonschema.Schema{Type: "number"}

	tests := map[string]struct {
		schema *jsonschema.Schema
		want   []jsonschema.SubschemaEntry
	}{
		"nil schema": {
			schema: nil,
			want:   nil,
		},
		"no sub-schemas": {
			schema: &jsonschema.Schema{Type: "string", Title: "leaf"},
			want:   nil,
		},
		"map, list, and single keywords labeled": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a": propA},
				AllOf:      []*jsonschema.Schema{allOf0, allOf1},
				Items:      items,
			},
			want: []jsonschema.SubschemaEntry{
				{
					Pointer:  "/properties/a",
					Segments: []jsonschema.Segment{{Key: "properties"}, {Key: "a"}},
					Schema:   propA,
				},
				{
					Pointer:  "/allOf/0",
					Segments: []jsonschema.Segment{{Key: "allOf"}, {Index: 0, IsIndex: true}},
					Schema:   allOf0,
				},
				{
					Pointer:  "/allOf/1",
					Segments: []jsonschema.Segment{{Key: "allOf"}, {Index: 1, IsIndex: true}},
					Schema:   allOf1,
				},
				{
					Pointer:  "/items",
					Segments: []jsonschema.Segment{{Key: "items"}},
					Schema:   items,
				},
			},
		},
		"member keys escaped per RFC 6901, segments verbatim": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a/b~c": escaped},
			},
			want: []jsonschema.SubschemaEntry{
				{
					Pointer:  "/properties/a~1b~0c",
					Segments: []jsonschema.Segment{{Key: "properties"}, {Key: "a/b~c"}},
					Schema:   escaped,
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, jsonschema.SubschemaEntries(tt.schema))
		})
	}
}

func TestWalk(t *testing.T) {
	t.Parallel()

	t.Run("nil schema is a no-op", func(t *testing.T) {
		t.Parallel()

		called := false
		err := jsonschema.Walk(nil, func(string, []jsonschema.Segment, *jsonschema.Schema) error {
			called = true

			return nil
		})

		require.NoError(t, err)
		assert.False(t, called)
	})

	t.Run("pre-order over the transitive closure", func(t *testing.T) {
		t.Parallel()

		leaf := &jsonschema.Schema{Type: "string"}
		mid := &jsonschema.Schema{Items: leaf}
		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{"p": mid},
		}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			visited = append(visited, s)

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root, mid, leaf}, visited)
	})

	t.Run("aliased schema visited once", func(t *testing.T) {
		t.Parallel()

		shared := &jsonschema.Schema{Type: "string"}
		root := &jsonschema.Schema{Items: shared, Not: shared}

		count := 0

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			if s == shared {
				count++
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("cyclic graph terminates", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{Type: "object"}
		root.Properties = map[string]*jsonschema.Schema{"self": root}

		count := 0

		err := jsonschema.Walk(root, func(string, []jsonschema.Segment, *jsonschema.Schema) error {
			count++

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("stops at and returns the first error", func(t *testing.T) {
		t.Parallel()

		errStop := errors.New("stop")
		first := &jsonschema.Schema{Type: "string"}
		second := &jsonschema.Schema{Type: "integer"}
		root := &jsonschema.Schema{AllOf: []*jsonschema.Schema{first, second}}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			visited = append(visited, s)
			if s == first {
				return errStop
			}

			return nil
		})

		require.ErrorIs(t, err, errStop)
		assert.Equal(t, []*jsonschema.Schema{root, first}, visited)
	})

	t.Run("follows children replaced by fn", func(t *testing.T) {
		t.Parallel()

		original := &jsonschema.Schema{Type: "integer"}
		replacement := &jsonschema.Schema{Type: "string"}
		root := &jsonschema.Schema{Items: original}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			if s.Items == original {
				s.Items = replacement
			}

			visited = append(visited, s)

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root, replacement}, visited,
			"fn runs before children are gathered, so the walk follows the replacement")
	})

	t.Run("SkipChildren prunes the subtree and continues with siblings", func(t *testing.T) {
		t.Parallel()

		prunedChild := &jsonschema.Schema{Type: "string"}
		pruned := &jsonschema.Schema{Items: prunedChild}
		sibling := &jsonschema.Schema{Type: "integer"}
		root := &jsonschema.Schema{AllOf: []*jsonschema.Schema{pruned, sibling}}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			visited = append(visited, s)
			if s == pruned {
				return jsonschema.SkipChildren
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root, pruned, sibling}, visited,
			"the pruned schema's children are skipped; its siblings still walk")
	})

	t.Run("SkipChildren on the root visits only the root", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{Items: &jsonschema.Schema{Type: "string"}}

		var visited []*jsonschema.Schema

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			visited = append(visited, s)

			return jsonschema.SkipChildren
		})

		require.NoError(t, err)
		assert.Equal(t, []*jsonschema.Schema{root}, visited)
	})

	t.Run("wrapped SkipChildren prunes too", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{Items: &jsonschema.Schema{Type: "string"}}

		count := 0

		err := jsonschema.Walk(root, func(string, []jsonschema.Segment, *jsonschema.Schema) error {
			count++

			return fmt.Errorf("rewrite pass: %w", jsonschema.SkipChildren)
		})

		require.NoError(t, err, "Walk matches SkipChildren with errors.Is")
		assert.Equal(t, 1, count)
	})

	t.Run("schema pruned via one path stays visited on another", func(t *testing.T) {
		t.Parallel()

		shared := &jsonschema.Schema{Items: &jsonschema.Schema{Type: "string"}}
		root := &jsonschema.Schema{
			AllOf: []*jsonschema.Schema{shared},
			Not:   shared,
		}

		count := 0

		err := jsonschema.Walk(root, func(_ string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			if s == shared {
				count++

				return jsonschema.SkipChildren
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, count, "pruning marks the schema visited; the second path does not re-run fn")
	})

	t.Run("paths accumulate SubschemaEntry pointers from the root", func(t *testing.T) {
		t.Parallel()

		leaf := &jsonschema.Schema{Type: "string"}
		mid := &jsonschema.Schema{Items: leaf}
		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{"a/b": mid},
			AllOf:      []*jsonschema.Schema{{Type: "integer"}},
		}

		paths := map[*jsonschema.Schema]string{}

		err := jsonschema.Walk(root, func(path string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			paths[s] = path

			return nil
		})

		require.NoError(t, err)
		assert.Empty(t, paths[root], "the root is the empty pointer")
		assert.Equal(t, "/properties/a~1b", paths[mid], "map keys are RFC 6901-escaped")
		assert.Equal(t, "/properties/a~1b/items", paths[leaf])
		assert.Equal(t, "/allOf/0", paths[root.AllOf[0]])
	})

	t.Run("segments mirror the pointer in typed form", func(t *testing.T) {
		t.Parallel()

		leaf := &jsonschema.Schema{Type: "string"}
		mid := &jsonschema.Schema{Items: leaf}
		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{"a/b": mid},
			AllOf:      []*jsonschema.Schema{{Type: "integer"}},
		}

		segs := map[*jsonschema.Schema][]jsonschema.Segment{}

		err := jsonschema.Walk(root, func(_ string, segments []jsonschema.Segment, s *jsonschema.Schema) error {
			segs[s] = segments

			return nil
		})

		require.NoError(t, err)
		assert.Nil(t, segs[root], "the root is the nil segment slice")
		assert.Equal(t, []jsonschema.Segment{{Key: "properties"}, {Key: "a/b"}}, segs[mid],
			"member keys are carried verbatim, no RFC 6901 escaping to undo")
		assert.Equal(t,
			[]jsonschema.Segment{{Key: "properties"}, {Key: "a/b"}, {Key: "items"}},
			segs[leaf])
		assert.Equal(t,
			[]jsonschema.Segment{{Key: "allOf"}, {Index: 0, IsIndex: true}},
			segs[root.AllOf[0]],
			"list elements carry the index in typed form")
	})

	t.Run("sibling segment slices do not alias", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"a": {Items: &jsonschema.Schema{Type: "string"}},
				"b": {Items: &jsonschema.Schema{Type: "integer"}},
			},
		}

		segs := map[*jsonschema.Schema][]jsonschema.Segment{}

		err := jsonschema.Walk(root, func(_ string, segments []jsonschema.Segment, s *jsonschema.Schema) error {
			segs[s] = segments

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t,
			[]jsonschema.Segment{{Key: "properties"}, {Key: "a"}, {Key: "items"}},
			segs[root.Properties["a"].Items],
			"the b subtree's descent must not overwrite a's retained segments")
		assert.Equal(t,
			[]jsonschema.Segment{{Key: "properties"}, {Key: "b"}, {Key: "items"}},
			segs[root.Properties["b"].Items])
	})

	t.Run("shared schema keeps the first path encountered", func(t *testing.T) {
		t.Parallel()

		shared := &jsonschema.Schema{Type: "string"}
		root := &jsonschema.Schema{Items: shared, Not: shared}

		var paths []string

		err := jsonschema.Walk(root, func(path string, _ []jsonschema.Segment, s *jsonschema.Schema) error {
			if s == shared {
				paths = append(paths, path)
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"/items"}, paths,
			"one visit, with the path of the first traversal order arrival")
	})

	t.Run("SkipChildren prunes by path", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"skip": {Items: &jsonschema.Schema{Type: "string"}},
				"walk": {Items: &jsonschema.Schema{Type: "integer"}},
			},
		}

		var visited []string

		err := jsonschema.Walk(root, func(path string, _ []jsonschema.Segment, _ *jsonschema.Schema) error {
			visited = append(visited, path)
			if path == "/properties/skip" {
				return jsonschema.SkipChildren
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"", "/properties/skip", "/properties/walk", "/properties/walk/items"}, visited)
	})
}

// TestSubschemaEntriesFieldCoverage is a maintenance guard over
// [jsonschema.SubschemaEntries], the single source of truth for which Schema
// fields hold sub-schemas. It enumerates Schema's fields via reflection,
// populates every field of type *Schema, []*Schema, or map[string]*Schema on
// a probe schema with a distinct child, and asserts SubschemaEntries returns
// each child exactly once. When upstream adds a new sub-schema-bearing field,
// the probe gains a child SubschemaEntries does not return and this test
// fails, forcing the field list to be extended -- so every traversal built on
// SubschemaEntries (Walk, Inline, and the internal walks) picks the new
// keyword up in one place.
func TestSubschemaEntriesFieldCoverage(t *testing.T) {
	t.Parallel()

	var (
		singleType = reflect.TypeFor[*jsonschema.Schema]()
		sliceType  = reflect.TypeFor[[]*jsonschema.Schema]()
		mapType    = reflect.TypeFor[map[string]*jsonschema.Schema]()
	)

	probe := &jsonschema.Schema{}
	probeValue := reflect.ValueOf(probe).Elem()
	schemaType := probeValue.Type()

	// Map each planted child to the field it was planted in, so a missing
	// child names the uncovered field.
	want := map[*jsonschema.Schema]string{}

	for i := range schemaType.NumField() {
		field := schemaType.Field(i)
		if !field.IsExported() {
			continue
		}

		child := &jsonschema.Schema{}

		switch field.Type {
		case singleType:
			probeValue.Field(i).Set(reflect.ValueOf(child))
		case sliceType:
			probeValue.Field(i).Set(reflect.ValueOf([]*jsonschema.Schema{child}))
		case mapType:
			probeValue.Field(i).Set(reflect.ValueOf(map[string]*jsonschema.Schema{"k": child}))
		default:
			continue
		}

		want[child] = field.Name
	}

	// The upstream Schema currently carries 23 sub-schema-bearing fields; the
	// exact count is not pinned, but an implausibly low one means the
	// reflection above stopped matching the field types.
	require.GreaterOrEqual(t, len(want), 23,
		"reflection found fewer sub-schema-bearing fields than the known upstream set")

	got := map[*jsonschema.Schema]int{}
	for _, entry := range jsonschema.SubschemaEntries(probe) {
		got[entry.Schema]++
	}

	for child, fieldName := range want {
		assert.Equal(t, 1, got[child],
			"Schema field %q holds a sub-schema that SubschemaEntries must return exactly once", fieldName)
	}

	assert.Len(t, got, len(want),
		"SubschemaEntries returned schemas that were not planted on the probe")
}
