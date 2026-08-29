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
// refusal with no observable path through the public API; under Draft-07 the
// registration it makes is what a fragment reference resolves through.
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
