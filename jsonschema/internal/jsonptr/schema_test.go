package jsonptr_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/jsonptr"
)

// materializeSchema is a plain JSON round-trip [jsonptr.Materialize] for tests;
// the production materializer (the parent's ParseSchemaValue) additionally
// keeps const/enum numbers exact, which these structural tests do not exercise.
func materializeSchema(node any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(node)
	if err != nil {
		return nil, err
	}

	var s jsonschema.Schema

	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, err
	}

	return &s, nil
}

func TestSchemaAtJSONPointer(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{
		ID:   "https://example.com/root",
		Type: "object",
		Defs: map[string]*jsonschema.Schema{
			"Foo": {Type: "string"},
		},
		PrefixItems: []*jsonschema.Schema{
			{Type: "integer"},
			{Type: "boolean"},
		},
	}

	t.Run("navigates into $defs", func(t *testing.T) {
		t.Parallel()

		got, base := jsonptr.SchemaAtJSONPointer(
			root, []string{"$defs", "Foo"}, "https://example.com/root", true, materializeSchema,
		)
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Equal(t, "https://example.com/root", base)
	})

	t.Run("navigates an array index", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"prefixItems", "1"}, "", true, materializeSchema)
		require.NotNil(t, got)
		assert.Equal(t, "boolean", got.Type)
	})

	t.Run("missing segment returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"$defs", "Missing"}, "", true, materializeSchema)
		assert.Nil(t, got)
	})

	t.Run("non-schema target returns nil", func(t *testing.T) {
		t.Parallel()

		got, _ := jsonptr.SchemaAtJSONPointer(root, []string{"type"}, "", true, materializeSchema)
		assert.Nil(t, got)
	})

	t.Run("intermediate $id rebases", func(t *testing.T) {
		t.Parallel()

		nested := &jsonschema.Schema{
			Properties: map[string]*jsonschema.Schema{
				"a": {
					ID: "sub/",
					Properties: map[string]*jsonschema.Schema{
						"b": {Type: "string"},
					},
				},
			},
		}

		got, base := jsonptr.SchemaAtJSONPointer(
			nested,
			[]string{"properties", "a", "properties", "b"},
			"https://example.com/root",
			true,
			materializeSchema,
		)
		require.NotNil(t, got)
		assert.Equal(t, "string", got.Type)
		assert.Contains(t, base, "sub")
	})
}

// TestTypedPrefix pins the typed walk both [jsonptr.TraverseSchema] and
// [jsonptr.SchemaAtJSONPointer] run: where it stops, what it leaves for the
// JSON-form walk, and which crossed $id rebases the base URI.
func TestTypedPrefix(t *testing.T) {
	t.Parallel()

	var (
		defLeaf   = &jsonschema.Schema{Type: "string"}
		sliceLeaf = &jsonschema.Schema{Type: "integer"}
		dataNode  = &jsonschema.Schema{
			Extra: map[string]any{"x-vendor": map[string]any{"k": true}},
		}
	)

	root := &jsonschema.Schema{
		ID:          "https://example.com/root",
		Defs:        map[string]*jsonschema.Schema{"Foo": defLeaf, "Data": dataNode},
		AllOf:       []*jsonschema.Schema{sliceLeaf},
		PrefixItems: []*jsonschema.Schema{sliceLeaf},
	}

	var (
		idLeaf   = &jsonschema.Schema{Type: "string"}
		fragLeaf = &jsonschema.Schema{Type: "number"}
		fragNode = &jsonschema.Schema{
			ID:         "#anchored",
			Properties: map[string]*jsonschema.Schema{"p": fragLeaf},
		}
	)

	idRoot := &jsonschema.Schema{
		ID: "https://example.com/root",
		Defs: map[string]*jsonschema.Schema{
			"Sub": {
				ID:         "https://example.com/sub",
				Properties: map[string]*jsonschema.Schema{"p": idLeaf},
			},
			"Frag": fragNode,
		},
	}

	tests := map[string]struct {
		root     *jsonschema.Schema
		segs     []string
		base     string
		trackIDs bool
		want     *jsonschema.Schema
		wantRest []string
		wantBase string
	}{
		"empty segments stop at the root": {
			root: root, segs: nil, base: "b",
			want: root, wantRest: nil, wantBase: "b",
		},
		"nil root consumes nothing": {
			root: nil, segs: []string{"$defs", "Foo"}, base: "b",
			want: nil, wantRest: []string{"$defs", "Foo"}, wantBase: "b",
		},
		"map edge consumes keyword and member": {
			root: root, segs: []string{"$defs", "Foo"}, base: "b",
			want: defLeaf, wantRest: nil, wantBase: "b",
		},
		"slice edge consumes keyword and index": {
			root: root, segs: []string{"allOf", "0"}, base: "b",
			want: sliceLeaf, wantRest: nil, wantBase: "b",
		},
		"unknown keyword stops at the root": {
			root: root, segs: []string{"x-vendor", "k"}, base: "b",
			want: root, wantRest: []string{"x-vendor", "k"}, wantBase: "b",
		},
		"data keyword below a typed node leaves the remainder": {
			root: root, segs: []string{"$defs", "Data", "x-vendor", "k"}, base: "b",
			want: dataNode, wantRest: []string{"x-vendor", "k"}, wantBase: "b",
		},
		"absent map member stops before the keyword": {
			root: root, segs: []string{"$defs", "Missing"}, base: "b",
			want: root, wantRest: []string{"$defs", "Missing"}, wantBase: "b",
		},
		"map keyword with no member stops before the keyword": {
			root: root, segs: []string{"$defs"}, base: "b",
			want: root, wantRest: []string{"$defs"}, wantBase: "b",
		},
		"out-of-range index stops before the keyword": {
			root: root, segs: []string{"prefixItems", "9"}, base: "b",
			want: root, wantRest: []string{"prefixItems", "9"}, wantBase: "b",
		},
		"crossed $id rebases the base": {
			root: idRoot, segs: []string{"$defs", "Sub", "properties", "p"},
			base: "https://example.com/root", trackIDs: true,
			want: idLeaf, wantRest: nil, wantBase: "https://example.com/sub",
		},
		"crossed $id is inert without tracking": {
			root: idRoot, segs: []string{"$defs", "Sub", "properties", "p"},
			base: "https://example.com/root",
			want: idLeaf, wantRest: nil, wantBase: "https://example.com/root",
		},
		"fragment-only $id leaves the base": {
			root: idRoot, segs: []string{"$defs", "Frag", "properties", "p"},
			base: "https://example.com/root", trackIDs: true,
			want: fragLeaf, wantRest: nil, wantBase: "https://example.com/root",
		},
		"the root's own $id never rebases": {
			root: idRoot, segs: []string{"$defs", "Frag"},
			base: "https://example.com/other", trackIDs: true,
			want: fragNode, wantRest: nil, wantBase: "https://example.com/other",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			node, rest, base := jsonptr.TypedPrefix(tt.root, tt.segs, tt.base, tt.trackIDs)

			if tt.want == nil {
				assert.Nil(t, node)
			} else {
				assert.Same(t, tt.want, node)
			}

			// A fully consumed pointer leaves an empty remainder; whether it
			// is nil or a zero-length slice is not a distinction any caller
			// makes.
			if len(tt.wantRest) == 0 {
				assert.Empty(t, rest)
			} else {
				assert.Equal(t, tt.wantRest, rest)
			}

			assert.Equal(t, tt.wantBase, base)

			// TraverseSchema is the all-or-nothing form of the same walk, so
			// it answers exactly the pointers this one fully consumes.
			traversed := jsonptr.TraverseSchema(tt.root, tt.segs)
			if len(rest) == 0 {
				assert.Same(t, node, traversed)
			} else {
				assert.Nil(t, traversed)
			}
		})
	}
}
