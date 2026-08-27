package schemavet_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/schemavet"
)

func TestZeroCurrencyIsInert(t *testing.T) {
	t.Parallel()

	assert.Nil(t, schemavet.Doc{}.Schema())
	assert.Nil(t, schemavet.Node{}.Schema())
}

func TestVetMintsOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	vt := schemavet.NewVetter(schemavet.Profile{RejectItemsArray: true, RejectIDFragment: true, Vocabularies: true})

	valid := &schemavet.Schema{Type: "string"}

	node, err := vt.Vet(valid, "")
	require.NoError(t, err)
	assert.Same(t, valid, node.Schema())

	doc, err := schemavet.NewVetter(schemavet.Profile{}).VetDoc(valid, "", "https://example.com/s")
	require.NoError(t, err)
	assert.Same(t, valid, doc.Schema())

	invalid := &schemavet.Schema{Type: "no-such-type"}

	node, err = schemavet.NewVetter(schemavet.Profile{}).Vet(invalid, "")
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Nil(t, node.Schema())

	doc, err = schemavet.NewVetter(schemavet.Profile{}).VetDoc(invalid, "", "https://example.com/s")
	require.ErrorIs(t, err, schemavet.ErrInvalidType)
	assert.Nil(t, doc.Schema())
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

			_, err := schemavet.NewVetter(tc.profile).Vet(tc.schema, "")
			require.ErrorIs(t, err, tc.err)
			assert.Contains(t, err.Error(), tc.path)
		})
	}
}

func TestVetDocIdentifierChecks(t *testing.T) {
	t.Parallel()

	// A fragment-carrying $id is rejected under RejectIDFragment (2020-12)
	// and tolerated as the anchor spelling without it (Draft-07).
	withFragment := &schemavet.Schema{ID: "#frag"}

	_, err := schemavet.NewVetter(schemavet.Profile{RejectIDFragment: true}).
		VetDoc(withFragment, "", "https://example.com/s")
	require.ErrorIs(t, err, schemavet.ErrInvalidID)

	_, err = schemavet.NewVetter(schemavet.Profile{}).VetDoc(withFragment, "", "https://example.com/s")
	require.NoError(t, err)

	// $vocabulary under an empty $schema follows the Vocabularies flag.
	vocab := &schemavet.Schema{Vocabulary: map[string]bool{"https://example.com/vocab": true}}

	_, err = schemavet.NewVetter(schemavet.Profile{Vocabularies: true}).VetDoc(vocab, "", "https://example.com/s")
	require.NoError(t, err)

	_, err = schemavet.NewVetter(schemavet.Profile{}).VetDoc(vocab, "", "https://example.com/s")
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
