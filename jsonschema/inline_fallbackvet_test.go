package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineFallbackTargetStructuralChecks locks in that a $ref target
// materialized from an unknown-keyword (Extra) position, reached only through
// the resolution core's JSON-pointer fallback, is vetted with the same
// structural checks a fetched document gets, so an ill-formed target fails
// Inline with an error wrapping [jsonschema.ErrRefResolve] instead of being
// spliced into a malformed output schema [jsonschema.Compile] rejects. Both
// the remote-document and same-document spellings route through the fallback.
//
// Three rows run one graph under both walk modes and both fallback policies,
// which is where the two paths split. A strict walk fails at the target the
// vet refused, wherever the session materializes it, the same point
// [jsonschema.Compile] fails. A [jsonschema.RefFallback] makes the walk
// tolerant, so the refusal instead reaches the policy at the ref that expands
// the target, and the policy decides.
func TestInlineFallbackTargetStructuralChecks(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	// The one graph three rows share. Property p refers to a well-formed
	// target whose own $ref reaches a malformed one, so the rejection lands a
	// reference deeper than the first target. Those rows differ only in the
	// walk mode and the policy's answer, so a shared spelling keeps them from
	// drifting into different graphs.
	const deeperRejection = `{"x-shared": {"$ref": "#/x-bad"}, "x-bad": {"type": "nteger"},` +
		` "properties": {"p": {"$ref": "#/x-shared"}}}`

	tests := map[string]struct {
		root string
		doc  string
		want string
		err  error

		// Whether the row configures a [jsonschema.RefFallback], which turns
		// the closure walk tolerant. The refusal then waits for the ref that
		// expands the target instead of failing the walk, so the row names the
		// refs the fallback must see, and reads err only when the policy
		// propagates.
		fallback bool

		// Whether the row's fallback propagates the failure rather than
		// dropping the reference. Only read when fallback is set.
		propagate bool

		// The ref each fallback consultation must name, in order.
		consulted []string
	}{
		"invalid type name in a remote unknown-keyword target": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"type": "nteger"}}`,
			err:  jsonschema.ErrInvalidType,
		},
		"negative bound in a remote unknown-keyword target": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"maxItems": -1}}`,
			err:  jsonschema.ErrNegativeBound,
		},
		"invalid type name in a same-document unknown-keyword target": {
			root: `{"x-shared": {"type": "nteger"}, "properties": {"p": {"$ref": "#/x-shared"}}}`,
			err:  jsonschema.ErrInvalidType,
		},
		"well-formed remote unknown-keyword target inlines": {
			root: `{"properties": {"p": {"$ref": "https://example.test/doc.json#/x-shared"}}}`,
			doc:  `{"x-shared": {"type": "integer"}}`,
			want: `{"properties": {"p": {"type": "integer"}}}`,
		},
		"well-formed same-document unknown-keyword target inlines": {
			root: `{"x-shared": {"type": "integer"}, "properties": {"p": {"$ref": "#/x-shared"}}}`,
			want: `{"x-shared": {"type": "integer"}, "properties": {"p": {"type": "integer"}}}`,
		},
		"violation one target deeper than the first": {
			// #/x-shared resolves directly and #/x-bad only through the first
			// target's own $ref, so the walk must refuse a rejected target
			// wherever it materializes one, not only at the first level.
			root: deeperRejection,
			err:  jsonschema.ErrInvalidType,
		},
		"a propagating fallback reproduces the refusal": {
			// The row installs a fallback, the expanding ref consults it, and
			// it returns the failure to the walk, so the run refuses with the
			// same sentinel the strict row reports. The consultation is what
			// separates the two, since a strict walk refuses the target
			// itself.
			root:      deeperRejection,
			fallback:  true,
			propagate: true,
			consulted: []string{"#/x-bad"},
			err:       jsonschema.ErrInvalidType,
		},
		"a fallback answers a rejected target one deeper": {
			// The same graph with a fallback that drops the reference. The
			// fallback suspends the walk's refusals, so the rejection reaches
			// the policy at the ref that expands the target. Dropping the
			// reference makes the run succeed, where both refusing rows stop.
			root:      deeperRejection,
			fallback:  true,
			consulted: []string{"#/x-bad"},
			want: `{"x-shared": {"$ref": "#/x-bad"}, "x-bad": {"type": "nteger"},` +
				` "properties": {"p": true}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(tc.root))
			require.NoError(t, err)

			opts := []jsonschema.InlineOption{}

			var consulted []string

			if tc.fallback {
				opts = append(opts, jsonschema.WithRefFallback(jsonschema.RefFallbackFunc(
					func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
						consulted = append(consulted, f.Ref)

						if tc.propagate {
							return jsonschema.PropagateRef()
						}

						return jsonschema.DropRef()
					},
				)))
			}

			if tc.doc != "" {
				doc, docErr := jsonschema.ParseSchema([]byte(tc.doc))
				require.NoError(t, docErr)

				opts = append(opts, jsonschema.WithRefResolver(jsonschema.SchemaMap{uri: doc}))
			}

			out, err := jsonschema.Inline(t.Context(), root, opts...)
			if tc.err != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, jsonschema.ErrRefResolve,
					"a structurally invalid fallback target must fail Inline loudly")
				require.ErrorIs(t, err, tc.err,
					"the structural sentinel must stay reachable through the inline error")
				assert.Equal(t, tc.consulted, consulted,
					"a strict walk refuses the target itself, while a tolerant walk consults the fallback")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.consulted, consulted,
				"the fallback answers each rejected target at the ref that expands it")

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

// TestDynamicRefRejectedPointerTargetSplitsTheEngines pins the one structural
// rejection the two engines answer differently. Inline resolves a $dynamicRef
// non-strictly in both walk modes, so a rejected target never refuses the walk,
// while [jsonschema.Compile] resolves it strictly and refuses with the sentinel
// of the check that rejected the target, [jsonschema.ErrInvalidType] for this
// fixture.
//
// The divergence is deliberate, and doc.go and README.md state it. Inline has
// no static expansion for the keyword and answers [jsonschema.ErrRefInline]
// wherever the expansion meets one, so a strict walk would displace that answer
// with a resolution error. The two rows record the cost. Inline without a
// fallback reports the keyword rather than the rejection, and a fallback that
// drops the reference inlines the input [jsonschema.Compile] refuses.
func TestDynamicRefRejectedPointerTargetSplitsTheEngines(t *testing.T) {
	t.Parallel()

	// The one graph this test hands both engines. Property p carries a
	// $dynamicRef whose JSON-pointer fragment names a schema inside an unknown
	// keyword, and that schema misspells its type name. The resolution core
	// materializes the target out of x-custom and the structural vet rejects
	// it. The two engines answer that rejection differently.
	const rejectedDynamicTarget = `{"x-custom": {"sub": {"type": "strnig"}},` +
		` "properties": {"p": {"$dynamicRef": "#/x-custom/sub"}}}`

	tests := map[string]struct {
		// Whether the row installs a [jsonschema.RefFallback] that drops the
		// reference. Inline resolves the $dynamicRef non-strictly either way,
		// so the row records what Inline tells the policy rather than a second
		// refusal.
		fallback bool

		// The ref each fallback consultation must name, in order.
		consulted []string

		// The output a row that inlines must produce. Read only when err is
		// nil.
		want string

		// The sentinel Inline must report, and the sentinels it must not. The
		// negative half is the load-bearing one. It states that the structural
		// vet's rejection is the fault Inline does not report. The Compile
		// sub-test below establishes that the vet does reject this graph.
		err    error
		notErr []error
	}{
		"a strict walk answers the keyword and not the vet": {
			err: jsonschema.ErrRefInline,
			notErr: []error{
				jsonschema.ErrInvalidType,
				jsonschema.ErrRefResolve,
			},
		},
		"a dropping fallback inlines the graph Compile refuses": {
			fallback:  true,
			consulted: []string{"#/x-custom/sub"},
			want: `{"x-custom": {"sub": {"type": "strnig"}},` +
				` "properties": {"p": true}}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(rejectedDynamicTarget))
			require.NoError(t, err)

			opts := []jsonschema.InlineOption{}

			var failures []jsonschema.RefFailure

			if tc.fallback {
				opts = append(opts, jsonschema.WithRefFallback(jsonschema.RefFallbackFunc(
					func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
						failures = append(failures, f)

						return jsonschema.DropRef()
					},
				)))
			}

			out, err := jsonschema.Inline(t.Context(), root, opts...)

			var refs []string

			for _, failure := range failures {
				refs = append(refs, failure.Ref)

				// Inline tells the policy the keyword has no static expansion,
				// not that the vet refused the target. That is the other row's
				// divergence read from the side that gets to answer it.
				require.ErrorIs(t, failure.Err, jsonschema.ErrRefInline,
					"the fallback answers Inline's own refusal to expand the keyword")
				require.NotErrorIs(t, failure.Err, jsonschema.ErrInvalidType,
					"the vet's rejection must not reach the policy")
			}

			assert.Equal(t, tc.consulted, refs,
				"the fallback answers the $dynamicRef the walk passed over")

			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Contains(t, err.Error(), dynamicRefInlinePhrase,
					"the refusal must name the keyword, "+
						"not one of ErrRefInline's other producers")

				for _, sentinel := range tc.notErr {
					require.NotErrorIs(t, err, sentinel,
						"a non-strict $dynamicRef walk reports no resolution fault")
				}

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))

			// The dropped reference takes the $dynamicRef with it, leaving the
			// rejected target unreachable inside x-custom, so Compile accepts
			// what it refused as input.
			_, err = jsonschema.Compile(t.Context(), out)
			require.NoError(t, err, "Compile must accept the inlined document")
		})
	}

	// The rows above rest on the vet rejecting this graph, and this sub-test
	// establishes that rejection. TestInlineFallbackTargetStructuralChecks
	// cannot absorb them, since it requires [jsonschema.ErrRefResolve]. Inline
	// adds that wrapper itself in walkClosure, over whatever its closure walk
	// refuses, and with strictDyn false no rejected $dynamicRef target reaches
	// it. The refWalkError helper wraps the cause alone, so Compile reports
	// ErrInvalidType unwrapped, whatever keyword carries this target.
	t.Run("Compile refuses the target Inline passes over", func(t *testing.T) {
		t.Parallel()

		root, err := jsonschema.ParseSchema([]byte(rejectedDynamicTarget))
		require.NoError(t, err)

		_, err = jsonschema.Compile(t.Context(), root)
		require.ErrorIs(t, err, jsonschema.ErrInvalidType,
			"a strict $dynamicRef walk reports ErrInvalidType, the sentinel "+
				"of the check that rejected the target, "+
				"a sentinel Inline never reports")
		require.NotErrorIs(t, err, jsonschema.ErrRefResolve,
			"Compile reports ErrInvalidType unwrapped, "+
				"so the $ref path's wrapper is Inline's")
		assert.Contains(t, err.Error(), `cannot resolve $dynamicRef "#/x-custom/sub"`,
			"the refusal names the keyword whose target the vet rejected")
	})
}
