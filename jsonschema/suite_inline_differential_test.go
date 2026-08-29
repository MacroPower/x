package jsonschema_test

import (
	"context"
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

// TestRefEnginesAgreeOnTransitiveVetting pins the reference closure both
// engines compute. The root's anchor reference reaches b.json, whose own allOf
// reference reaches a.json, and a.json carries an invalid type name. Neither
// engine copies anything out of a.json, and Inline's output would be identical
// without it. A document inside the closure is still a document the run holds,
// so both engines fetch it, vet it, and refuse the graph for the same cause.
func TestRefEnginesAgreeOnTransitiveVetting(t *testing.T) {
	t.Parallel()

	root := `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`
	documents := map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
	}

	schema, resolver := parseRefGraph(t, root, documents)

	_, compileErr := jsonschema.Compile(t.Context(), schema, jsonschema.WithRefResolver(resolver))
	_, inlineErr := jsonschema.Inline(t.Context(), schema, jsonschema.WithRefResolver(resolver))

	for name, err := range map[string]error{"Compile": compileErr, "Inline": inlineErr} {
		require.ErrorIsf(t, err, jsonschema.ErrInvalidType,
			"%s reaches a.json through b.json's own reference and vets it", name)
		require.ErrorContainsf(t, err, "a.json",
			"%s names the transitively fetched document holding the violation", name)
	}
}

// TestInlineFallbackSuspendsTransitiveVetting pins what a fallback narrows. A
// [jsonschema.RefFallback] answers one failing reference at a time, and a
// document reachable only through another document's reference has no reference
// in the expansion for a policy to answer, so with a fallback configured the walk
// refuses nothing and every failure waits in the negative cache for the
// references walkPair does reach. TestRefEnginesAgreeOnTransitiveVetting pins
// that Inline refuses the same graph with no fallback.
func TestInlineFallbackSuspendsTransitiveVetting(t *testing.T) {
	t.Parallel()

	root := `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`
	documents := map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"type":"strnig"}}}`,
	}

	schema, resolver := parseRefGraph(t, root, documents)

	var consulted []jsonschema.RefFailure

	fallback := jsonschema.RefFallbackFunc(
		func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
			consulted = append(consulted, f)

			return jsonschema.PropagateRef()
		})

	out, err := jsonschema.Inline(t.Context(), schema,
		jsonschema.WithRefResolver(resolver), jsonschema.WithRefFallback(fallback))
	require.NoError(t, err, "a fallback suspends the walk's refusals, so the graph inlines")
	assert.Empty(t, consulted, "a document outside the expansion consults nobody")

	// The anchor still inlined, so the run did the work rather than stopping early.
	standalone, err := jsonschema.Compile(t.Context(), out)
	require.NoError(t, err)
	require.NoError(t, standalone.ValidateJSON(t.Context(), []byte(`"x"`)))
	require.Error(t, standalone.ValidateJSON(t.Context(), []byte(`1`)))
}

// TestRefEnginesDisagreeOnCollidingIDs records a divergence the rig found, not
// a behavior the package promises. Pinned so a fix at either engine has to
// acknowledge it.
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
// unresolvable references on purpose. Three reasons are not skips,
// reasonSubstituteBaseURI, reasonSubstituteNoAnchors, and
// reasonSubstituteTransitiveMalformed, which chooseWithheld applies when it
// picks a document to withhold. TestSubstituteDoesNotRebaseNestedRefs pins
// reasonSubstituteBaseURI.
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
