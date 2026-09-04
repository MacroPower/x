package schemavet_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

func TestZeroCurrencyIsInert(t *testing.T) {
	t.Parallel()

	assert.Nil(t, schemavet.Doc{}.Root())
	assert.Nil(t, schemavet.Node{}.Root())
	assert.Nil(t, schemavet.Doc{}.Frozen())
	assert.Nil(t, schemavet.Node{}.Frozen())
}

func TestVetMintsOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	strict := schemavet.Profile{RejectItemsArray: true, RejectIDFragment: true, Vocabularies: true}
	valid := &schemavet.Schema{Type: "string"}

	node, err := schemavet.FreezeNode(valid, "", "https://example.com/s", strict)
	require.NoError(t, err)
	require.NotNil(t, node.Root())
	assert.NotSame(t, valid, node.Root(), "the currency holds the frozen copy")
	assert.Same(t, node.Frozen().Root(), node.Root())

	frozen, err := schemavet.Freeze(valid, "the document", "https://example.com/s", schemavet.Profile{})
	require.NoError(t, err)

	doc, err := frozen.Vet("")
	require.NoError(t, err)
	assert.Same(t, frozen.Root(), doc.Root())
	assert.Same(t, frozen, doc.Frozen())

	invalid := &schemavet.Schema{Type: "no-such-type"}

	node, err = schemavet.FreezeNode(invalid, "", "https://example.com/s", schemavet.Profile{})
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Nil(t, node.Root())

	frozen, err = schemavet.Freeze(invalid, "the document", "https://example.com/s", schemavet.Profile{})
	require.NoError(t, err, "the freeze reads structure, not the checks")

	doc, err = frozen.Vet("")
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Nil(t, doc.Root())
}

func TestVetViolationPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema  *schemavet.Schema
		profile schemavet.Profile
		err     error
		path    string
	}{
		"invalid type name in property": {
			schema: &schemavet.Schema{Properties: map[string]*schemavet.Schema{
				"a/b": {Type: "no-such-type"},
			}},
			err:  schemavet.ErrInvalidType,
			path: "/properties/a~1b/type",
		},
		"negative bound": {
			schema: &schemavet.Schema{MinLength: new(-1)},
			err:    schemavet.ErrNegativeBound,
			path:   "/minLength",
		},
		"items array under 2020-12": {
			schema:  &schemavet.Schema{ItemsArray: []*schemavet.Schema{{}}},
			profile: schemavet.Profile{RejectItemsArray: true},
			err:     schemavet.ErrItemsArrayUnderDraft2020,
			path:    "/items",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := schemavet.FreezeNode(tc.schema, "", "https://example.com/s", tc.profile)
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path)
		})
	}
}

func TestVetDocIdentifierChecks(t *testing.T) {
	t.Parallel()

	vetDoc := func(s *schemavet.Schema, profile schemavet.Profile) error {
		frozen, err := schemavet.Freeze(s, "the document", "https://example.com/s", profile)
		require.NoError(t, err)

		_, err = frozen.Vet("")

		return err
	}

	// A fragment-carrying $id is rejected under RejectIDFragment (2020-12)
	// and tolerated as the anchor spelling without it (Draft-07).
	withFragment := &schemavet.Schema{ID: "#frag"}

	err := vetDoc(withFragment, schemavet.Profile{RejectIDFragment: true})
	require.ErrorIs(t, err, schemavet.ErrInvalidID)

	err = vetDoc(withFragment, schemavet.Profile{})
	require.NoError(t, err)

	// $vocabulary under an empty $schema follows the Vocabularies flag.
	vocab := &schemavet.Schema{Vocabulary: map[string]bool{"https://example.com/vocab": true}}

	err = vetDoc(vocab, schemavet.Profile{Vocabularies: true})
	require.NoError(t, err)

	err = vetDoc(vocab, schemavet.Profile{})
	require.ErrorIs(t, err, schemavet.ErrMisplacedVocabulary)
}

func TestCheckTypeNamesRunsOnlyTypeWalk(t *testing.T) {
	t.Parallel()

	// A schema carrying both a structural conflict and a bad type name must
	// report the type name; the standalone check runs no structure pass,
	// matching the public wrapper's contract.
	both := &schemavet.Schema{
		Type:  "no-such-type",
		Defs:  map[string]*schemavet.Schema{"a": {}},
		AllOf: []*schemavet.Schema{nil},
	}

	err := schemavet.CheckTypeNames(both)
	require.ErrorIs(t, err, schemavet.ErrInvalidType)

	assert.NoError(t, schemavet.CheckTypeNames(nil))
	assert.NoError(t, schemavet.CheckTypeNames(&schemavet.Schema{AllOf: []*schemavet.Schema{nil}}))
}
