package refresolve_test

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema/internal/refresolve"
	"go.jacobcolvin.com/x/jsonschema/internal/schemafield"
	"go.jacobcolvin.com/x/jsonschema/internal/uriref"
)

// testDeps returns the Deps the registry walk needs: the sub-schema traversal
// the parent injects, spelled here with the same schemafield table the parent
// derives it from. The walk materializes no JSON-pointer target, so
// Materialize stays nil.
func testDeps() refresolve.Deps {
	return refresolve.Deps{Children: schemafield.Children}
}

// TestFragmentOnlyIDRegistersByDraft pins the walk's draft gate on the
// fragment-only $id: Draft-07 reads it as the anchor spelling and registers an
// anchor, while Draft 2020-12 forbids a fragment in $id (core section 8.2.1)
// and registers nothing, so a plain-name fragment naming it stays
// unresolvable. Under 2020-12 both engines refuse such a document at the
// identifier check before resolution runs, so the gate sits behind that
// refusal with no observable path through the public API; under Draft-07 a
// fragment reference resolves through the registration it makes.
func TestFragmentOnlyIDRegistersByDraft(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		draft refresolve.Draft
		want  bool
	}{
		"draft-07 reads a fragment-only $id as an anchor": {draft: refresolve.Draft7, want: true},
		"draft 2020-12 registers nothing for it":          {draft: refresolve.Draft2020, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			target := &jsonschema.Schema{ID: "#a", Type: "integer"}
			root := &jsonschema.Schema{Definitions: map[string]*jsonschema.Schema{"t": target}}

			reg := refresolve.NewRegistry(testDeps(), tc.draft, false)
			reg.Build(root, "https://example.test/root.json")

			got, ok := reg.NewSession(nil).
				LookupAnchor(uriref.AnchorKey("https://example.test/root.json", "a"))

			require.Equal(t, tc.want, ok, "the anchor registration follows the draft")

			if tc.want {
				assert.Same(t, target, got, "the anchor names the schema carrying the $id")
			}
		})
	}
}

// TestRegisterFetchedCollisions pins what a fetched document may claim against
// a registry that already holds the root document and one remote. The root
// carries the URI https://example.test/root.json and an anchor "r"; the remote
// is served from https://example.test/a.json. A claim on an identifier either
// already holds reports ErrIDCollision and registers nothing, while every other
// claim registers.
//
// Every row that collides does so on a URI. RegisterFetched's doc comment
// carries the argument for why that is the space a collision reaches.
func TestRegisterFetchedCollisions(t *testing.T) {
	t.Parallel()

	const (
		rootURI   = "https://example.test/root.json"
		loadedURI = "https://example.test/a.json"
		freshURI  = "https://example.test/b.json"
	)

	tests := map[string]struct {
		doc     *jsonschema.Schema
		baseURI string
		err     bool
	}{
		"a document claiming nothing registers": {
			doc:     &jsonschema.Schema{Type: "string"},
			baseURI: freshURI,
		},
		"a $id equal to the retrieval URI is the document's own claim": {
			doc:     &jsonschema.Schema{ID: freshURI, Type: "string"},
			baseURI: freshURI,
		},
		"a duplicate anchor within one document keeps the first": {
			doc: &jsonschema.Schema{Defs: map[string]*jsonschema.Schema{
				"one": {Anchor: "dup", Type: "string"},
				"two": {Anchor: "dup", Type: "integer"},
			}},
			baseURI: freshURI,
		},
		"a $id naming the root's URI collides": {
			doc:     &jsonschema.Schema{ID: rootURI, Type: "string"},
			baseURI: freshURI,
			err:     true,
		},
		"a nested $id naming a loaded document's URI collides": {
			doc: &jsonschema.Schema{Defs: map[string]*jsonschema.Schema{
				"evil": {ID: loadedURI, Type: "integer"},
			}},
			baseURI: freshURI,
			err:     true,
		},
		"a $id naming the retrieval URI of a loaded document collides": {
			doc:     &jsonschema.Schema{ID: loadedURI, Type: "string"},
			baseURI: freshURI,
			err:     true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sess := loadedSession(t, rootURI, loadedURI)

			err := sess.RegisterFetched(tc.doc, tc.baseURI)
			if tc.err {
				require.ErrorIs(t, err, refresolve.ErrIDCollision)

				_, ok := sess.LookupURI(tc.baseURI)
				assert.False(t, ok, "a refused document registers nothing, not even its retrieval URI")

				return
			}

			require.NoError(t, err)

			got, ok := sess.LookupURI(tc.baseURI)
			require.True(t, ok, "the document registers under its retrieval URI")
			assert.Same(t, tc.doc, got)
		})
	}
}

// TestRegisterFetchedTwiceIsNotACollision pins that registering one document
// again under its own retrieval URI is no claim against another document. The
// registry maps the URI to the same pointer both times, and only a different
// pointer is a collision.
func TestRegisterFetchedTwiceIsNotACollision(t *testing.T) {
	t.Parallel()

	sess := loadedSession(t, "https://example.test/root.json", "https://example.test/a.json")
	doc := &jsonschema.Schema{ID: "https://example.test/b.json", Type: "string"}

	require.NoError(t, sess.RegisterFetched(doc, "https://example.test/b.json"))
	require.NoError(t, sess.RegisterFetched(doc, "https://example.test/b.json"),
		"the same document under the same URI claims nothing new")
}

// TestRegisterFallbackDocumentChecksOnlyIDs pins the narrower rule a substitute
// follows. Its $id is a claim a real document could answer instead, so a $id
// naming a loaded URI is refused. Its anchors are not, because a substitute
// registers under the base of the reference it answers, so an anchor it
// carries lands in that document's anchor space by construction and no
// reference reaches it.
func TestRegisterFallbackDocumentChecksOnlyIDs(t *testing.T) {
	t.Parallel()

	const (
		rootURI   = "https://example.test/root.json"
		loadedURI = "https://example.test/a.json"
	)

	sess := loadedSession(t, rootURI, loadedURI)

	err := sess.RegisterFallbackDocument(
		&jsonschema.Schema{ID: loadedURI, Type: "string"}, rootURI, "the substitute",
	)
	require.ErrorIs(t, err, refresolve.ErrIDCollision,
		"a substitute may not claim a URI a real document holds")

	err = sess.RegisterFallbackDocument(
		&jsonschema.Schema{Anchor: "r", Type: "string"}, rootURI, "the substitute",
	)
	require.NoError(t, err, "a substitute's anchor is not a claim against the document holding it")
}

// loadedSession returns a session over a registry holding two documents: a root
// carrying rootURI and the anchor "r", and a remote served from loadedURI. It is
// the state a run reaches after fetching one document, which is what makes a
// later claim a collision.
func loadedSession(t *testing.T, rootURI, loadedURI string) *refresolve.Session {
	t.Helper()

	root := &jsonschema.Schema{ID: rootURI, Anchor: "r", Type: "object"}

	reg := refresolve.NewRegistry(testDeps(), refresolve.Draft2020, false)
	reg.Build(root, rootURI)

	sess := reg.NewSession(nil)
	require.NoError(t, sess.RegisterFetched(&jsonschema.Schema{Type: "string"}, loadedURI))

	return sess
}

// TestCollisionMessageNamesBothDocuments pins that the message names two
// distinct documents even when the incumbent's own base is the colliding URI.
// Naming the holder by its base alone prints that URI twice and leaves the
// other party out, so the message falls back to the URI the holder was
// retrieved from.
func TestCollisionMessageNamesBothDocuments(t *testing.T) {
	t.Parallel()

	const (
		rootURI   = "https://example.test/root.json"
		firstURI  = "https://example.test/a.json"
		secondURI = "https://example.test/b.json"
		sharedID  = "https://example.test/shared.json"
	)

	reg := refresolve.NewRegistry(testDeps(), refresolve.Draft2020, false)
	reg.Build(&jsonschema.Schema{ID: rootURI, Type: "object"}, rootURI)

	sess := reg.NewSession(nil)
	require.NoError(t, sess.RegisterFetched(&jsonschema.Schema{ID: sharedID, Type: "string"}, firstURI))

	err := sess.RegisterFetched(&jsonschema.Schema{ID: sharedID, Type: "integer"}, secondURI)
	require.ErrorIs(t, err, refresolve.ErrIDCollision)
	assert.Contains(t, err.Error(), secondURI, "the message names the document making the claim")
	assert.Contains(t, err.Error(), firstURI,
		"the message names the document already holding the URI, by the URI it was retrieved from")
}
