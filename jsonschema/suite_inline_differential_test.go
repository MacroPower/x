package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.jacobcolvin.com/x/jsonschema"
)

// suiteInlineOpts returns the inline options matching suiteBaseOpts. Only the
// ref resolver carries over: WithRefResolver returns a RefOption serving both
// engines, while the metaschema resolver and the format and content gates are
// validation concerns the inliner has no use for.
func suiteInlineOpts() []jsonschema.InlineOption {
	return []jsonschema.InlineOption{jsonschema.WithRefResolver(suiteRemoteResolver{})}
}

// TestSuiteInlineAgrees runs every group schema in the vendored suite through
// the validator and through the inliner, and asserts the two agree on every
// case. It is the differential oracle over the three sites that materialize a
// $ref target: a fix at one site that misses another fails here rather than
// reaching a release.
//
// It deliberately ignores suiteSkips. Those entries record where this package's
// answer differs from the suite's expected answer, which is orthogonal to
// whether the two engines answer alike, and both engines share the regex engine
// and draft support the skips are about. Running the skipped cases is extra
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

// TestInlineDifferentialSkipsAreLive asserts every reason the suite
// differential can reach still classifies at least one vendored group, so a
// constant cannot go dead unnoticed. Were Inline to gain a static expansion for
// $dynamicRef, or the upstream suite to drop its recursive schemas, the
// matching constant would stop earning its place and this fails.
//
// Two reasons are not checked here, reasonDeferredRefMiss and
// reasonSubstituteBaseURI. The suite serves every remote it references, so no
// case reaches them; they belong to FuzzRefEnginesAgree, which draws
// unresolvable references on purpose.
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
