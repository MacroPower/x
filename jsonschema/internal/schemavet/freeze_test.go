package schemavet_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

const rootURI = "https://example.test/root.json"

// bk mints the DocKey a base string names, the way the parent package's
// ParseBase does before it hands a base to Freeze.
func bk(t *testing.T, s string) uriref.DocKey {
	t.Helper()

	k, err := uriref.ParseBase(s)
	require.NoError(t, err)

	return k
}

// ak builds the anchor key a name registers under a base, the key the freeze
// walk stores through DocKey.Anchor.
func ak(t *testing.T, uri, name string) uriref.AnchorKey {
	t.Helper()

	return bk(t, uri).Anchor(name)
}

// TestFreezeDuplicatesAliasedNodes pins the tree property: a node the source
// reaches through two paths becomes two nodes, each with its own id and
// pointer, and neither is the source node.
func TestFreezeDuplicatesAliasedNodes(t *testing.T) {
	t.Parallel()

	shared := &schemavet.Schema{Type: "string"}
	src := &schemavet.Schema{Properties: map[string]*schemavet.Schema{"a": shared, "b": shared}}

	f, err := schemavet.Freeze(src, "the root document", bk(t, rootURI), schemavet.Profile{})
	require.NoError(t, err)

	a, ok := f.At("/properties/a")
	require.True(t, ok)

	b, ok := f.At("/properties/b")
	require.True(t, ok)

	assert.NotSame(t, a, b, "each path gets its own copy")
	assert.NotSame(t, shared, a, "the copy shares nothing with the source")
	assert.Equal(t, "string", a.Type)
	assert.Equal(t, "string", b.Type)
	assert.Len(t, f.Nodes(), 3)

	aID, ok := f.ID(a)
	require.True(t, ok)
	assert.Equal(t, "/properties/a", f.Path(aID))

	_, ok = f.ID(shared)
	assert.False(t, ok, "the source node is not a node of the tree")

	rootID, ok := f.ID(f.Root())
	require.True(t, ok)
	assert.Equal(t, 0, rootID)
	assert.Empty(t, f.Path(rootID))
}

// TestFreezeRefusesCycles pins the cycle refusal and its wording: the
// message names the subject, the pointer where the loop closes, and the
// pointer it returns to, whether the loop runs through a sub-schema keyword
// or a value field.
func TestFreezeRefusesCycles(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *schemavet.Schema
		want  string
	}{
		"self cycle": {
			build: func() *schemavet.Schema {
				s := &schemavet.Schema{Type: "object"}
				s.Items = s

				return s
			},
			want: `the root document holds a loop where "/items" crosses a schema and returns to ""`,
		},
		"cycle through an unknown keyword": {
			build: func() *schemavet.Schema {
				s := &schemavet.Schema{Type: "object"}
				s.Extra = map[string]any{"x-self": s}

				return s
			},
			want: `"/x-self" crosses a schema and returns to ""`,
		},
		"cycle through examples": {
			build: func() *schemavet.Schema {
				s := &schemavet.Schema{Type: "object"}
				s.Examples = []any{s}

				return s
			},
			want: `"/examples/0"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := schemavet.Freeze(tc.build(), "the root document", bk(t, rootURI), schemavet.Profile{})
			require.ErrorIs(t, err, schemavet.ErrSchemaCycle)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestFreezeRefusesAliasedIdentifiers pins that a duplicated node carrying an
// identifier the profile registers is refused, and that an identifier the
// profile ignores is not.
func TestFreezeRefusesAliasedIdentifiers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		shared  *schemavet.Schema
		profile schemavet.Profile
		err     bool
	}{
		"aliased $id":            {shared: &schemavet.Schema{ID: "https://example.test/x"}, err: true},
		"aliased $anchor":        {shared: &schemavet.Schema{Anchor: "x"}, err: true},
		"aliased $dynamicAnchor": {shared: &schemavet.Schema{DynamicAnchor: "x"}, err: true},
		"aliased $id under inert ids": {
			shared:  &schemavet.Schema{ID: "https://example.test/x"},
			profile: schemavet.Profile{InertIDs: true},
		},
		"aliased $anchor under draft-07": {
			shared:  &schemavet.Schema{Anchor: "x"},
			profile: schemavet.Profile{Draft7: true},
		},
		"aliased node with no identifier":   {shared: &schemavet.Schema{Type: "string"}},
		"distinct nodes sharing an $anchor": {shared: nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var src *schemavet.Schema

			if tc.shared != nil {
				src = &schemavet.Schema{Properties: map[string]*schemavet.Schema{"a": tc.shared, "b": tc.shared}}
			} else {
				src = &schemavet.Schema{Properties: map[string]*schemavet.Schema{
					"a": {Anchor: "dup", Type: "string"},
					"b": {Anchor: "dup", Type: "integer"},
				}}
			}

			f, err := schemavet.Freeze(src, "the root document", bk(t, rootURI), tc.profile)
			if tc.err {
				require.ErrorIs(t, err, schemavet.ErrIDCollision)
				assert.Contains(t, err.Error(), `"/properties/a" and "/properties/b"`)

				return
			}

			require.NoError(t, err)

			if tc.shared == nil {
				first, ok := f.At("/properties/a")
				require.True(t, ok)
				assert.Same(t, first, f.Anchors()[ak(t, rootURI, "dup")], "a repeated key keeps the first node")
			}
		})
	}
}

// TestFreezeRefusesAliasedIdentifiersInWalkOrder pins that a root holding
// several duplicated identified nodes is refused with one stable message,
// naming the node whose first copy the walk reaches first, since the aliased
// set is a map and a message that varied by its iteration order would make
// Compile and Inline disagree on one root.
func TestFreezeRefusesAliasedIdentifiersInWalkOrder(t *testing.T) {
	t.Parallel()

	identified := &schemavet.Schema{ID: "https://example.test/x"}
	anchored := &schemavet.Schema{Anchor: "y"}
	src := &schemavet.Schema{
		Properties: map[string]*schemavet.Schema{
			"c": anchored,
			"d": anchored,
			"a": identified,
			"b": identified,
		},
	}

	want := `the root document reaches one schema carrying $id "https://example.test/x" ` +
		`at "/properties/a" and "/properties/b"`

	for range 50 {
		_, err := schemavet.Freeze(src, "the root document", bk(t, rootURI), schemavet.Profile{})
		require.ErrorIs(t, err, schemavet.ErrIDCollision)
		require.ErrorContains(t, err, want)
	}
}

// TestFreezeTables pins the registrations and per-node bases the walk
// records, under both drafts and under inert ids.
func TestFreezeTables(t *testing.T) {
	t.Parallel()

	src := func() *schemavet.Schema {
		return &schemavet.Schema{
			Defs: map[string]*schemavet.Schema{
				"nested": {
					ID:     "https://example.test/nested.json",
					Anchor: "n",
					Properties: map[string]*schemavet.Schema{
						"deep": {DynamicAnchor: "d"},
					},
				},
				"sibling": {ID: "https://example.test/sibling.json", Ref: "#/x"},
				"frag":    {ID: "#frag"},
			},
		}
	}

	t.Run("draft 2020-12", func(t *testing.T) {
		t.Parallel()

		f, err := schemavet.Freeze(
			src(), "the root document", bk(t, rootURI), schemavet.Profile{RejectIDFragment: true},
		)
		require.NoError(t, err)

		nested, ok := f.At("/$defs/nested")
		require.True(t, ok)

		deep, ok := f.At("/$defs/nested/properties/deep")
		require.True(t, ok)

		sibling, ok := f.At("/$defs/sibling")
		require.True(t, ok)

		assert.Same(t, nested, f.URIs()[bk(t, "https://example.test/nested.json")])
		assert.Same(t, nested, f.Anchors()[ak(t, "https://example.test/nested.json", "n")])
		assert.Same(t, deep, f.Anchors()[ak(t, "https://example.test/nested.json", "d")])
		assert.Same(t, deep, f.DynamicAnchors()[ak(t, "https://example.test/nested.json", "d")])
		assert.NotContains(t, f.Anchors(), ak(t, rootURI, "frag"), "2020-12 registers nothing for a fragment-only $id")

		rootID, _ := f.ID(f.Root())
		deepID, _ := f.ID(deep)
		siblingID, _ := f.ID(sibling)

		assert.Equal(t, rootURI, f.NodeBase(rootID).String())
		assert.Equal(t, "https://example.test/nested.json", f.NodeBase(deepID).String())
		assert.Equal(t, "https://example.test/sibling.json", f.NodeBase(siblingID).String(),
			"2020-12 honors a $id beside $ref")
		assert.Equal(t, rootURI, f.Base().String())
	})

	t.Run("draft-07", func(t *testing.T) {
		t.Parallel()

		f, err := schemavet.Freeze(src(), "the root document", bk(t, rootURI), schemavet.Profile{Draft7: true})
		require.NoError(t, err)

		frag, ok := f.At("/$defs/frag")
		require.True(t, ok)

		sibling, ok := f.At("/$defs/sibling")
		require.True(t, ok)

		assert.Same(t, frag, f.Anchors()[ak(t, rootURI, "frag")], "draft-07 reads a fragment-only $id as an anchor")
		assert.NotContains(t, f.Anchors(), ak(t, "https://example.test/nested.json", "n"), "draft-07 has no $anchor")
		assert.Empty(t, f.DynamicAnchors())

		siblingID, _ := f.ID(sibling)
		assert.Equal(t, rootURI, f.NodeBase(siblingID).String(), "draft-07 ignores a $id beside $ref")
		assert.NotContains(t, f.URIs(), bk(t, "https://example.test/sibling.json"),
			"draft-07 registers no target for a $id beside $ref")
	})

	t.Run("draft-07 anchor beside ref", func(t *testing.T) {
		t.Parallel()

		src := &schemavet.Schema{Defs: map[string]*schemavet.Schema{
			"a": {ID: "#foo", Ref: "#/$defs/b"},
			"b": {Type: "string"},
		}}

		f, err := schemavet.Freeze(src, "the root document", bk(t, rootURI), schemavet.Profile{Draft7: true})
		require.NoError(t, err)
		assert.NotContains(t, f.Anchors(), ak(t, rootURI, "foo"),
			"draft-07 registers no anchor for a fragment-only $id beside $ref")
	})

	t.Run("inert ids", func(t *testing.T) {
		t.Parallel()

		f, err := schemavet.Freeze(src(), "the root document", bk(t, rootURI), schemavet.Profile{InertIDs: true})
		require.NoError(t, err)

		deep, ok := f.At("/$defs/nested/properties/deep")
		require.True(t, ok)

		assert.Empty(t, f.URIs())
		assert.Same(t, deep, f.Anchors()[ak(t, rootURI, "d")], "anchors hang off the retrieval base")

		deepID, _ := f.ID(deep)
		assert.Equal(t, rootURI, f.NodeBase(deepID).String())
	})

	// A fragment registers every $id as a key but rebases nothing, so its
	// anchors and children hang off the base in effect at its position.
	t.Run("fragment", func(t *testing.T) {
		t.Parallel()

		f, err := schemavet.Freeze(src(), "the fragment", bk(t, rootURI), schemavet.Profile{Fragment: true})
		require.NoError(t, err)

		nested, ok := f.At("/$defs/nested")
		require.True(t, ok)

		deep, ok := f.At("/$defs/nested/properties/deep")
		require.True(t, ok)

		assert.Same(t, nested, f.URIs()[bk(t, "https://example.test/nested.json")], "a fragment's $id registers")
		assert.Same(t, nested, f.Anchors()[ak(t, rootURI, "n")], "anchors hang off the fragment's base")
		assert.Same(t, deep, f.Anchors()[ak(t, rootURI, "d")])

		nestedID, _ := f.ID(nested)
		deepID, _ := f.ID(deep)
		assert.Equal(t, rootURI, f.ScopeBase(nestedID).String(), "a fragment's $id rebases no child")
		assert.Equal(t, rootURI, f.NodeBase(deepID).String())
	})
}

// TestFreezeAtFollowsSubschemaEdgesOnly pins that At answers typed positions
// only, so a pointer into a value or unknown keyword is the JSON-form
// fallback's to answer.
func TestFreezeAtFollowsSubschemaEdgesOnly(t *testing.T) {
	t.Parallel()

	src := &schemavet.Schema{
		Items:      &schemavet.Schema{Type: "string"},
		ItemsArray: nil,
		PrefixItems: []*schemavet.Schema{
			{Type: "integer"},
		},
		Extra: map[string]any{"x-custom": map[string]any{"type": "string"}},
	}

	f, err := schemavet.Freeze(src, "the root document", bk(t, ""), schemavet.Profile{})
	require.NoError(t, err)

	_, ok := f.At("/items")
	assert.True(t, ok)

	_, ok = f.At("/prefixItems/0")
	assert.True(t, ok)

	_, ok = f.At("/items/0")
	assert.False(t, ok)

	_, ok = f.At("/x-custom")
	assert.False(t, ok)
}

// TestFrozenVetMintsCurrency pins that Vet runs the identifier checks and
// VetNode does not, and that both mint a currency carrying the tree.
func TestFrozenVetMintsCurrency(t *testing.T) {
	t.Parallel()

	withFragment := &schemavet.Schema{ID: "#frag", Type: "string"}
	profile := schemavet.Profile{RejectIDFragment: true}

	f, err := schemavet.Freeze(withFragment, "the root document", bk(t, rootURI), profile)
	require.NoError(t, err)

	doc, err := f.Vet("")
	require.ErrorIs(t, err, schemavet.ErrInvalidID)
	assert.Nil(t, doc.Root())

	node, err := f.VetNode("")
	require.NoError(t, err, "the structural checks do not read $id")
	assert.Same(t, f.Root(), node.Root())
	assert.Same(t, f, node.Frozen())

	valid, err := schemavet.Freeze(&schemavet.Schema{Type: "string"}, "the root document", bk(t, rootURI), profile)
	require.NoError(t, err)

	doc, err = valid.Vet("")
	require.NoError(t, err)
	assert.Same(t, valid.Root(), doc.Root())
	assert.Same(t, valid, doc.Frozen())

	invalid, err := schemavet.Freeze(
		&schemavet.Schema{Type: "no-such-type"}, "the root document", bk(t, rootURI), profile,
	)
	require.NoError(t, err, "freezing runs no vetting check")

	_, err = invalid.Vet("https://example.test/a.json#")
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Contains(t, err.Error(), "https://example.test/a.json#/type")
}

// TestFreezeNode pins the fragment path: freeze plus the structural checks
// under the locator, with the fragment's own identifiers resolved against the
// base in effect at its position.
func TestFreezeNode(t *testing.T) {
	t.Parallel()

	node, err := schemavet.FreezeNode(
		&schemavet.Schema{Anchor: "a", Type: "string"},
		rootURI+"#/x-custom/sub", bk(t, "https://example.test/res.json"), schemavet.Profile{},
	)
	require.NoError(t, err)
	assert.Contains(t, node.Frozen().Anchors(), ak(t, "https://example.test/res.json", "a"))

	_, err = schemavet.FreezeNode(
		&schemavet.Schema{Type: "no-such-type"},
		rootURI+"#/x-custom/sub", bk(t, ""), schemavet.Profile{},
	)
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Contains(t, err.Error(), rootURI+"#/x-custom/sub/type")
}

// TestNodeNarrow pins that a minted Node re-roots at any node of its tree
// and at nothing else, keeping the whole tree behind the narrowed proof.
func TestNodeNarrow(t *testing.T) {
	t.Parallel()

	node, err := schemavet.FreezeNode(
		&schemavet.Schema{Properties: map[string]*schemavet.Schema{"p": {Type: "string"}}},
		rootURI+"#/x-custom/sub", bk(t, rootURI), schemavet.Profile{},
	)
	require.NoError(t, err)

	inner, ok := node.Frozen().At("/properties/p")
	require.True(t, ok)

	narrowed, ok := node.Narrow(inner)
	require.True(t, ok)
	assert.Same(t, inner, narrowed.Root())
	assert.Same(t, node.Frozen(), narrowed.Frozen())

	_, ok = node.Narrow(&schemavet.Schema{Type: "string"})
	assert.False(t, ok, "a schema outside the tree narrows nothing")

	_, ok = schemavet.Node{}.Narrow(inner)
	assert.False(t, ok, "the zero Node holds no tree")
}
