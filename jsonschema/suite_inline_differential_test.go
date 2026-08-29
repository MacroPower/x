package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// suiteInlineOpts returns the inline options matching suiteBaseOpts. Only the
// ref resolver carries over. WithRefResolver returns a RefOption serving both
// engines, while the metaschema resolver and the format and content gates are
// validation concerns the inliner has no use for.
func suiteInlineOpts() []jsonschema.InlineOption {
	return []jsonschema.InlineOption{jsonschema.WithRefResolver(suiteRemoteResolver{})}
}

// TestSuiteInlineAgrees runs every group schema in the vendored suite through
// the validator and through the inliner, and asserts the two agree on every
// case. It covers the three sites that materialize a $ref target. A fix at one
// site that misses another fails here rather than reaching a release.
//
// It deliberately ignores suiteSkips. Those entries record where this package's
// answer differs from the suite's expected answer. That is unrelated to whether
// the two engines answer alike, since both share the Go RE2 matcher and the
// draft support those skips are about. Running the skipped cases is extra
// coverage the conformance tests cannot give.
func TestSuiteInlineAgrees(t *testing.T) {
	t.Parallel()

	for _, file := range suiteFiles(t) {
		t.Run(file.pathKey, func(t *testing.T) {
			t.Parallel()

			for _, group := range loadSuiteGroups(t, file.path) {
				t.Run(group.Description, func(t *testing.T) {
					t.Parallel()

					schema := unmarshalTestSchema(t, group.Schema, file.schemaURI)

					pipelines, reason := refEngines(
						t.Context(), t, schema, file.opts, suiteInlineOpts(),
					)
					// The suite serves every remote it references, so a miss
					// here is an Inline resolution bug, never a deferred fetch.
					require.NotEqual(t, reasonDeferredRefMiss, reason,
						"Inline failed to resolve a reference the suite serves")

					if reason != "" {
						t.Skip(string(reason))
					}

					for _, tc := range group.Tests {
						t.Run(tc.Description, func(t *testing.T) {
							t.Parallel()

							assertRefEnginesAgree(t.Context(), t, tc.Data, pipelines...)
						})
					}
				})
			}
		})
	}
}

// TestCompileVetsTransitivelyInlineDoesNot records a divergence the rig found,
// not a behavior the package promises. Pinned so a fix at either engine has to
// acknowledge it.
//
// Compile walks the reference graph transitively. It fetches b.json for the
// root's anchor reference, then walks b.json's own references, fetches a.json,
// vets it, and refuses the schema. Inline expands only what a reference needs.
// The anchor resolves to a leaf inside b.json, so Inline never expands b.json's
// allOf reference, never fetches a.json, and never vets it.
//
// The observable consequence is that Compile refuses a schema whose inlined
// form it accepts, so Inline turns an uncompilable document into a compilable
// one. Both behaviors are defensible on their own. The doc.go contract promises
// that Inline vets every document it holds, its own root, its substitutes, and
// the remotes it fetches. Inline never fetches this one, so neither engine
// breaks that contract. Resolving the divergence means choosing whether a
// reference graph is vetted as a whole or only where it is walked.
func TestCompileVetsTransitivelyInlineDoesNot(t *testing.T) {
	t.Parallel()

	root := `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`
	documents := map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
	}

	schema, resolver := parseRefGraph(t, root, documents)

	_, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrInvalidType,
		"Compile reaches a.json through b.json's own reference and vets it")
	require.ErrorContains(t, err, "a.json",
		"the violation Compile reports is the one inside the transitively fetched document")

	inlined, err := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err, "Inline expands only the anchor, so it never fetches a.json")

	standalone, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err, "the inlined output carries nothing from a.json")

	// The anchor really inlined, rather than collapsing to a schema that
	// accepts everything.
	require.NoError(t, standalone.ValidateJSON(t.Context(), []byte(`"x"`)))
	require.Error(t, standalone.ValidateJSON(t.Context(), []byte(`1`)))
}

// TestRefEnginesDisagreeOnCollidingIDs records a second divergence the rig
// found, not a behavior the package promises. Pinned so a fix at either engine
// has to acknowledge it.
//
// A fetched document claims a URI another document already holds, and the two
// engines then resolve one anchor reference to two different targets. Compile
// registers the fetched document's anchor under the $id that document claims
// and reaches it. Inline keeps the document already loaded under that URI and
// reaches its anchor instead. Both engines carry a fix for a fetched $id
// overwriting a loaded registry entry, 52b5110 for the validator and da61121
// for the inliner, and the two fixes disagree here.
//
// The collision has two spellings and both diverge, so this is a class rather
// than one graph. A document can claim the root's URI, or it can claim another
// document's retrieval URI. In each row one engine accepts exactly what the
// other refuses.
//
// Neither engine breaks a stated contract, since doc.go fixes no precedence for
// a fetched document claiming a URI another document already holds. Resolving
// it means stating that precedence once and applying it at both sites.
func TestRefEnginesDisagreeOnCollidingIDs(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root      string
		documents map[string]string
	}{
		"a fetched document claims the root's URI": {
			root: `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"properties":{"p0":{"$ref":"https://ex.test/b.json#anc"}}}`,
			documents: map[string]string{
				"https://ex.test/a.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"array"}}}`,
				"https://ex.test/b.json": `{"$id":"https://ex.test/root.json","allOf":[{"$ref":"https://ex.test/a.json"}]}`,
			},
		},
		"a fetched document claims another's retrieval URI": {
			root: `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","properties":{"p0":{"$ref":"https://ex.test/a.json#anc"}}}`,
			documents: map[string]string{
				"https://ex.test/a.json": `{"$id":"https://ex.test/other.json","definitions":{"anc":{"$id":"#anc","type":"array"}},"allOf":[{"$ref":"https://ex.test/b.json"}]}`,
				"https://ex.test/b.json": `{"$id":"https://ex.test/a.json","definitions":{"anc":{"$id":"#anc","type":"string"}}}`,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, resolver := parseRefGraph(t, tc.root, tc.documents)

			compiled, err := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err)

			inlined, err := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))
			require.NoError(t, err)

			standalone, err := jsonschema.Compile(t.Context(), inlined)
			require.NoError(t, err)

			// The disagreement runs both ways, which rules out one side simply
			// being broken: each engine accepts exactly what the other refuses.
			array, str := []byte(`{"p0": [1]}`), []byte(`{"p0": "x"}`)

			assert.NotEqual(t,
				compiled.ValidateJSON(t.Context(), array) == nil,
				standalone.ValidateJSON(t.Context(), array) == nil,
				"the engines resolve the same anchor reference to different targets")

			assert.NotEqual(t,
				compiled.ValidateJSON(t.Context(), str) == nil,
				standalone.ValidateJSON(t.Context(), str) == nil,
				"the disagreement runs in both directions")
		})
	}
}

// TestInlineDifferentialSkipsAreLive asserts every reason the suite
// differential can reach still classifies at least one vendored group, so a
// constant cannot go dead unnoticed. Were Inline to gain a static expansion for
// $dynamicRef, or the upstream suite to drop its recursive schemas, the
// matching constant would stop earning its place and this test would fail.
//
// One reason is not checked here, reasonDeferredRefMiss. The suite serves every
// remote it references, so no case reaches it, and FuzzRefEnginesAgree draws
// unresolvable references on purpose. Neither reasonSubstituteBaseURI nor
// reasonSubstituteNoAnchors is a skip reason.
// TestSubstituteDoesNotRebaseNestedRefs pins reasonSubstituteBaseURI, and
// chooseWithheld applies both.
func TestInlineDifferentialSkipsAreLive(t *testing.T) {
	t.Parallel()

	seen := map[skipReason]int{}
	compared := 0

	for _, file := range suiteFiles(t) {
		for _, group := range loadSuiteGroups(t, file.path) {
			schema := unmarshalTestSchema(t, group.Schema, file.schemaURI)

			_, reason := refEngines(t.Context(), t, schema, file.opts, suiteInlineOpts())
			if reason != "" {
				seen[reason]++

				continue
			}

			compared++
		}
	}

	assert.NotZero(t, compared, "the differential compares no suite group at all")

	for _, reason := range []skipReason{reasonInlineCycle, reasonInlineDynamicRef} {
		assert.NotZerof(t, seen[reason],
			"no vendored suite group reaches this reason any more, so it is dead: %s", reason)
	}
}
