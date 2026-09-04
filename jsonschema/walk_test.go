package jsonschema_test

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
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
	allOf0 := &jsonschema.Schema{MinLength: new(1)}
	allOf1 := &jsonschema.Schema{MaxLength: new(2)}
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

// TestSchemafieldChildrenMatchesSubschemaEntries pins that
// schemafield.Children yields exactly the SubschemaEntries schemas in the same
// order. The two walks share the Subschemas field list but assemble children
// independently (Children skips the Location build), and consumers like the
// clone pairing walk require lockstep order between them.
func TestSchemafieldChildrenMatchesSubschemaEntries(t *testing.T) {
	t.Parallel()

	s := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"b": {Type: "string"},
			"a": {Type: "integer"},
		},
		Defs: map[string]*jsonschema.Schema{
			"z": {Type: "number"},
			"y": {Type: "boolean"},
		},
		AllOf:      []*jsonschema.Schema{{Type: "object"}, nil, {Type: "array"}},
		Items:      &jsonschema.Schema{Type: "string"},
		ItemsArray: []*jsonschema.Schema{{Type: "integer"}},
		Not:        &jsonschema.Schema{Type: "null"},
	}

	assert.Equal(t, childSchemas(s), schemafield.Children(s))
	assert.Nil(t, schemafield.Children(nil))
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

func TestSubschemaEntriesItemsAndItemsArray(t *testing.T) {
	t.Parallel()

	// A hand-built schema can set both forms of the items keyword (JSON sets at
	// most one). The array form wins; the single Items form is omitted so the
	// keyword never yields a /items pointer contradicting the /items/N ones.
	single := &jsonschema.Schema{Type: "string"}
	first := &jsonschema.Schema{Type: "integer"}

	entries := jsonschema.SubschemaEntries(&jsonschema.Schema{
		Items:      single,
		ItemsArray: []*jsonschema.Schema{first},
	})

	require.Len(t, entries, 1)
	assert.Equal(t, jsontext.Pointer("/items/0"), entries[0].Pointer)
	assert.Same(t, first, entries[0].Schema)
}

func TestSubschemaEntries(t *testing.T) {
	t.Parallel()

	propA := &jsonschema.Schema{Type: "string"}
	escaped := &jsonschema.Schema{Type: "boolean"}
	allOf0 := &jsonschema.Schema{MinLength: new(1)}
	allOf1 := &jsonschema.Schema{MaxLength: new(2)}
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
					Location: jsonschema.Location{
						Pointer:  "/properties/a",
						Segments: []jsonschema.Segment{{Key: "properties"}, {Key: "a"}},
					},
					Schema: propA,
				},
				{
					Location: jsonschema.Location{
						Pointer:  "/allOf/0",
						Segments: []jsonschema.Segment{{Key: "allOf"}, {Index: 0, IsIndex: true}},
					},
					Schema: allOf0,
				},
				{
					Location: jsonschema.Location{
						Pointer:  "/allOf/1",
						Segments: []jsonschema.Segment{{Key: "allOf"}, {Index: 1, IsIndex: true}},
					},
					Schema: allOf1,
				},
				{
					Location: jsonschema.Location{
						Pointer:  "/items",
						Segments: []jsonschema.Segment{{Key: "items"}},
					},
					Schema: items,
				},
			},
		},
		"member keys escaped per RFC 6901, segments verbatim": {
			schema: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"a/b~c": escaped},
			},
			want: []jsonschema.SubschemaEntry{
				{
					Location: jsonschema.Location{
						Pointer:  "/properties/a~1b~0c",
						Segments: []jsonschema.Segment{{Key: "properties"}, {Key: "a/b~c"}},
					},
					Schema: escaped,
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
		err := jsonschema.Walk(nil, func(jsonschema.Location, *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(jsonschema.Location, *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(jsonschema.Location, *jsonschema.Schema) error {
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

		err := jsonschema.Walk(root, func(_ jsonschema.Location, s *jsonschema.Schema) error {
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

		paths := map[*jsonschema.Schema]jsontext.Pointer{}

		err := jsonschema.Walk(root, func(loc jsonschema.Location, s *jsonschema.Schema) error {
			paths[s] = loc.Pointer

			return nil
		})

		require.NoError(t, err)
		assert.Empty(t, paths[root], "the root is the empty pointer")
		assert.Equal(t, jsontext.Pointer("/properties/a~1b"), paths[mid], "map keys are RFC 6901-escaped")
		assert.Equal(t, jsontext.Pointer("/properties/a~1b/items"), paths[leaf])
		assert.Equal(t, jsontext.Pointer("/allOf/0"), paths[root.AllOf[0]])
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

		err := jsonschema.Walk(root, func(loc jsonschema.Location, s *jsonschema.Schema) error {
			segs[s] = loc.Segments

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

		err := jsonschema.Walk(root, func(loc jsonschema.Location, s *jsonschema.Schema) error {
			segs[s] = loc.Segments

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

		var paths []jsontext.Pointer

		err := jsonschema.Walk(root, func(loc jsonschema.Location, s *jsonschema.Schema) error {
			if s == shared {
				paths = append(paths, loc.Pointer)
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, []jsontext.Pointer{"/items"}, paths,
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

		var visited []jsontext.Pointer

		err := jsonschema.Walk(root, func(loc jsonschema.Location, _ *jsonschema.Schema) error {
			visited = append(visited, loc.Pointer)
			if loc.Pointer == "/properties/skip" {
				return jsonschema.SkipChildren
			}

			return nil
		})

		require.NoError(t, err)
		assert.Equal(
			t,
			[]jsontext.Pointer{"", "/properties/skip", "/properties/walk", "/properties/walk/items"},
			visited,
		)
	})
}

// TestSchemas pins the iterator form of Walk: the same pre-order visit over
// the transitive closure with the same locations, cycle termination, and an
// early break that simply stops the iteration.
func TestSchemas(t *testing.T) {
	t.Parallel()

	t.Run("matches Walk's visit order and locations", func(t *testing.T) {
		t.Parallel()

		leaf := &jsonschema.Schema{Type: "string"}
		mid := &jsonschema.Schema{Items: leaf}
		root := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{"p": mid},
		}

		var (
			walkedSchemas, rangedSchemas []*jsonschema.Schema
			walkedPaths, rangedPaths     []jsontext.Pointer
		)

		err := jsonschema.Walk(root, func(loc jsonschema.Location, s *jsonschema.Schema) error {
			walkedSchemas = append(walkedSchemas, s)
			walkedPaths = append(walkedPaths, loc.Pointer)

			return nil
		})
		require.NoError(t, err)

		for loc, s := range jsonschema.Schemas(root) {
			rangedSchemas = append(rangedSchemas, s)
			rangedPaths = append(rangedPaths, loc.Pointer)
		}

		assert.Equal(t, walkedSchemas, rangedSchemas)
		assert.Equal(t, walkedPaths, rangedPaths)
	})

	t.Run("break stops the iteration", func(t *testing.T) {
		t.Parallel()

		first := &jsonschema.Schema{Type: "string"}
		second := &jsonschema.Schema{Type: "integer"}
		root := &jsonschema.Schema{AllOf: []*jsonschema.Schema{first, second}}

		var visited []*jsonschema.Schema

		for _, s := range jsonschema.Schemas(root) {
			visited = append(visited, s)
			if s == first {
				break
			}
		}

		assert.Equal(t, []*jsonschema.Schema{root, first}, visited)
	})

	t.Run("cyclic graph terminates", func(t *testing.T) {
		t.Parallel()

		root := &jsonschema.Schema{Type: "object"}
		root.Properties = map[string]*jsonschema.Schema{"self": root}

		count := 0
		for range jsonschema.Schemas(root) {
			count++
		}

		assert.Equal(t, 1, count)
	})

	t.Run("nil schema yields nothing", func(t *testing.T) {
		t.Parallel()

		for range jsonschema.Schemas(nil) {
			t.Fatal("a nil schema must yield nothing")
		}
	})
}

// TestSchemavetEntriesMatchSubschemaEntries pins that schemavet.Entries yields
// exactly the SubschemaEntries schemas and pointers in the same order. The two
// walks share the schemafield.Subschemas field list but assemble pointers
// independently (schemavet cannot import the main package), and the vetting
// error paths embed schemavet's pointers, so a divergence would change
// compile-error text.
func TestSchemavetEntriesMatchSubschemaEntries(t *testing.T) {
	t.Parallel()

	s := &jsonschema.Schema{
		Properties: map[string]*jsonschema.Schema{
			"plain": {Type: "string"},
			"a/b":   {Type: "integer"},
			"c~d":   {Type: "number"},
			// Invalid UTF-8 must survive byte-verbatim in both walks; an
			// escaper that substitutes U+FFFD would spell the two pointers
			// differently while every other key still agreed.
			"e\xffg": {Type: "boolean"},
		},
		Defs: map[string]*jsonschema.Schema{
			"z": {Type: "number"},
			"y": {Type: "boolean"},
		},
		DependentSchemas: map[string]*jsonschema.Schema{
			"dep": {Type: "object"},
		},
		AllOf:       []*jsonschema.Schema{{Type: "object"}, nil, {Type: "array"}},
		PrefixItems: []*jsonschema.Schema{{Type: "string"}},
		// Both forms of the items keyword set: the array form must win in
		// both walks, and the single Items form must be omitted.
		Items:      &jsonschema.Schema{Type: "string"},
		ItemsArray: []*jsonschema.Schema{{Type: "integer"}},
		Not:        &jsonschema.Schema{Type: "null"},
		If:         &jsonschema.Schema{Type: "string"},
	}

	for _, root := range []*jsonschema.Schema{s, nil, {Type: "string"}} {
		want := jsonschema.SubschemaEntries(root)
		got := schemavet.Entries(root)

		require.Len(t, got, len(want))

		for i, entry := range want {
			assert.Same(t, entry.Schema, got[i].Schema)
			assert.Equal(t, entry.Pointer, got[i].Pointer)
		}
	}
}
