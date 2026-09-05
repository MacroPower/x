package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRefIntoFallbackTree pins that a $ref reaching a node below the
// root of a JSON-pointer fallback target inlines whichever ref the walk
// expands first. The pointer ref materializes the whole x-defs/sub tree and
// registers the $id its inner node declares, so a ref naming that $id
// resolves to a node the walk has not recorded. The two rows swap the
// property names, which swaps the expansion order, and both inline to the
// same shapes.
func TestInlineRefIntoFallbackTree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		schema string
		want   string
	}{
		"the $id ref expands before the tree root": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"$ref": "urn:t"},
						"b": {"$ref": "#/x-defs/sub"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"type": "string"},
						"b": {
							"type": "object",
							"properties": {"p": {"type": "string"}}
						}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
		},
		"the tree root expands before the $id ref": {
			schema: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {"$ref": "#/x-defs/sub"},
						"b": {"$ref": "urn:t"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
			want: stringtest.Input(`
				{
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"properties": {
						"a": {
							"type": "object",
							"properties": {"p": {"type": "string"}}
						},
						"b": {"type": "string"}
					},
					"x-defs": {
						"sub": {
							"type": "object",
							"properties": {"p": {"$id": "urn:t", "type": "string"}}
						}
					}
				}
			`),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var schema jsonschema.Schema

			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))

			got, err := jsonschema.Inline(t.Context(), &schema)
			require.NoError(t, err)

			data, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

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

// missCountingResolver counts ResolveRef invocations per URI and answers every
// fetch with the configured error (or a plain miss when the error is nil).
type missCountingResolver struct {
	calls map[string]int
	err   error
}

func (r *missCountingResolver) ResolveRef(_ context.Context, uri string) (*jsonschema.Schema, error) {
	r.calls[uri]++

	return nil, r.err
}

func TestInlineFallbackFetchesUnresolvableDocumentOnce(t *testing.T) {
	t.Parallel()

	// With a fallback that continues past fetch failures, many nodes can
	// reference the same unresolvable URI; the per-run negative cache must keep
	// the resolver at one consultation per distinct URI, upholding the
	// documented at-most-once-per-call fetch contract, while every referencing
	// node still gets its own fallback consultation with the recorded failure.
	resolverErr := errors.New("upstream unreachable")

	tests := map[string]struct {
		resolverErr error
	}{
		"plain miss":     {},
		"resolver error": {resolverErr: resolverErr},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resolver := &missCountingResolver{calls: map[string]int{}, err: tt.resolverErr}

			var failures []jsonschema.RefFailure

			drop := jsonschema.RefFallbackFunc(func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
				failures = append(failures, f)

				return jsonschema.DropRef()
			})

			root, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
				{
					"properties": {
						"a": {"$ref": "https://example.com/missing.json"},
						"b": {"$ref": "https://example.com/missing.json"},
						"c": {"$ref": "https://example.com/missing.json"}
					}
				}
			`)))
			require.NoError(t, err)

			_, err = jsonschema.Inline(t.Context(), root,
				jsonschema.WithRefResolver(resolver),
				jsonschema.WithRefFallback(drop))
			require.NoError(t, err)

			assert.Equal(t, map[string]int{"https://example.com/missing.json": 1}, resolver.calls,
				"an unresolvable document is fetched at most once per Inline call")

			require.Len(t, failures, 3, "every referencing node is consulted")

			for _, f := range failures {
				require.ErrorIs(t, f.Err, jsonschema.ErrRefResolve)

				if tt.resolverErr != nil {
					require.ErrorIs(t, f.Err, tt.resolverErr,
						"a recorded resolver error is replayed on later consultations")
				}
			}
		})
	}
}

// TestInlineFetchedRemoteStructuralChecks locks in that a remote document
// fetched during Inline is vetted with the same [jsonschema.Compile]-style
// structural checks the validator applies to fetched documents, so a remote
// carrying an invalid type name, a negative bound, or the array form of items
// under a draft that rejects it fails Inline with an error wrapping
// [jsonschema.ErrRefResolve] instead of being inlined into a malformed output
// schema. A Draft-07 array-form items remote stays valid under a Draft-07 run,
// since fetched documents follow the root document's draft.
func TestInlineFetchedRemoteStructuralChecks(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	tests := map[string]struct {
		root  string
		doc   string
		err   error
		valid bool
	}{
		"negative bound in fetched document": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"maxItems": -1}`,
			err:  jsonschema.ErrNegativeBound,
		},
		"items array in fetched document under 2020-12": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"items": [{"type": "string"}]}`,
			err:  jsonschema.ErrItemsArrayUnderDraft2020,
		},
		"invalid type name in fetched document": {
			root: `{"$ref": "https://example.test/doc.json"}`,
			doc:  `{"type": "strng"}`,
			err:  jsonschema.ErrInvalidType,
		},
		"items array in fetched document under draft-07 keeps tuple semantics": {
			root:  `{"$schema": "http://json-schema.org/draft-07/schema#", "$ref": "https://example.test/doc.json"}`,
			doc:   `{"items": [{"type": "string"}]}`,
			valid: true,
		},
		"well-formed fetched document inlines": {
			root:  `{"$ref": "https://example.test/doc.json"}`,
			doc:   `{"type": "string"}`,
			valid: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(tc.root))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(tc.doc))
			require.NoError(t, err)

			resolver := jsonschema.SchemaMap{uri: doc}

			out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(resolver))
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, out)

				return
			}

			require.Error(t, err)
			require.ErrorIs(t, err, jsonschema.ErrRefResolve,
				"a structurally invalid fetched document must fail Inline loudly")
			require.ErrorIs(t, err, tc.err,
				"the structural sentinel must stay reachable through the inline error")
		})
	}
}

// TestInlineRefFallbackOnVetFailedRemote pins the [jsonschema.WithRefFallback]
// interaction with a fetched remote that fails structural vetting: the failure
// is an ordinary reference-expansion failure, so the fallback is consulted at
// the referencing node with an error wrapping [jsonschema.ErrRefResolve] and
// the structural sentinel, and its answer decides between propagating that
// error, dropping the reference keyword while keeping the node's siblings, and
// expanding a substitute schema with the usual draft sibling semantics.
func TestInlineRefFallbackOnVetFailedRemote(t *testing.T) {
	t.Parallel()

	const uri = "https://example.test/doc.json"

	tests := map[string]struct {
		action jsonschema.RefAction
		want   string
		err    error
	}{
		"propagate keeps the vet failure fatal": {
			action: jsonschema.PropagateRef(),
			err:    jsonschema.ErrNegativeBound,
		},
		"drop clears the ref and keeps siblings": {
			action: jsonschema.DropRef(),
			want:   `{"title": "root"}`,
		},
		"substitute joins the node's allOf": {
			action: jsonschema.SubstituteRef(&jsonschema.Schema{Type: "string"}),
			want:   `{"title": "root", "allOf": [{"type": "string"}]}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root, err := jsonschema.ParseSchema([]byte(`{"title": "root", "$ref": "https://example.test/doc.json"}`))
			require.NoError(t, err)

			doc, err := jsonschema.ParseSchema([]byte(`{"maxItems": -1}`))
			require.NoError(t, err)

			var failures []jsonschema.RefFailure

			fallback := jsonschema.RefFallbackFunc(
				func(_ context.Context, f jsonschema.RefFailure) jsonschema.RefAction {
					failures = append(failures, f)

					return tc.action
				},
			)

			out, err := jsonschema.Inline(t.Context(), root,
				jsonschema.WithRefResolver(jsonschema.SchemaMap{uri: doc}),
				jsonschema.WithRefFallback(fallback))

			require.Len(t, failures, 1, "the failing ref is consulted exactly once")
			assert.Equal(t, uri, failures[0].Ref)
			require.ErrorIs(t, failures[0].Err, jsonschema.ErrRefResolve,
				"the consultation carries the resolve failure")
			require.ErrorIs(t, failures[0].Err, jsonschema.ErrNegativeBound,
				"the consultation keeps the structural sentinel reachable")

			if tc.err != nil {
				require.ErrorIs(t, err, jsonschema.ErrRefResolve)
				require.ErrorIs(t, err, tc.err)

				return
			}

			require.NoError(t, err)

			data, err := json.Marshal(out)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(data))
		})
	}
}

// TestInlineClosureRejectsBeforeSplicing pins that a violation only the
// closure walk reaches yields no document at all, rather than one spliced
// around the offending branch. It runs the same three-document shape as
// TestRefEnginesAgreeOnTransitiveVetting, which compares the two engines'
// causes; this one asserts what an Inline caller receives, and it
// carries a different violation so the two do not pin one sentinel twice.
func TestInlineClosureRejectsBeforeSplicing(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$schema":"http://json-schema.org/draft-07/schema#",` +
			`"$id":"https://ex.test/root.json","$ref":"https://ex.test/b.json#anc"}`,
	))
	require.NoError(t, err)

	documents := map[string]*jsonschema.Schema{}

	for uri, body := range map[string]string{
		"https://ex.test/b.json": `{"$id":"https://ex.test/b.json","definitions":{"anc":{"$id":"#anc","type":"string"}},"allOf":[{"$ref":"https://ex.test/a.json"}]}`,
		"https://ex.test/a.json": `{"definitions":{"bad":{"maxItems": -1}}}`,
	} {
		doc, parseErr := jsonschema.ParseSchema([]byte(body))
		require.NoError(t, parseErr)

		documents[uri] = doc
	}

	out, err := jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(jsonschema.SchemaMap(documents)))

	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, jsonschema.ErrNegativeBound)
	require.ErrorContains(t, err, "a.json", "the message names the document the walk refused")
	assert.Nil(t, out, "a refused closure yields no document")
}

// TestInlineFetchedDocCollisionPrecedesVet pins which fault the engines report
// for a remote carrying two, one claiming the root's URI and misspelling a type
// name. Both report the collision, because the shared fetch checks the frozen
// document's claims before it vets. A fetch that vetted first would name the
// other cause, and a fault ordered differently in one engine would refuse one
// graph for two reasons. TestInlineFallbackCollisionPrecedesVet covers the
// same graph over the path a configured fallback takes.
func TestInlineFetchedDocCollisionPrecedesVet(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "properties": {"p": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	resolver := mapResolver{"https://ex.test/a.json": doc}

	_, err = jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the collision is reported ahead of the structural vet")
	require.NotErrorIs(t, err, jsonschema.ErrInvalidType,
		"the vet does not run on a document the registration refused")

	_, err = jsonschema.Compile(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision, "Compile names the same cause")
}

// TestInlineFallbackDoesNotSuspendCollision pins the one closure-walk refusal a
// configured fallback leaves standing, the identifier collision doc.go's
// WithRefFallback section carves out. The colliding document sits in a $defs
// branch no expansion visits, so only the walk reaches it.
func TestInlineFallbackDoesNotSuspendCollision(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "$defs": {"unused": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "integer"}`,
	))
	require.NoError(t, err)

	consulted := 0
	fallback := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		consulted++

		return jsonschema.DropRef()
	})

	_, err = jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(mapResolver{"https://ex.test/a.json": doc}),
		jsonschema.WithRefFallback(fallback))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"a configured fallback does not suspend the collision refusal")
	assert.Zero(t, consulted, "the walk refuses before any reference reaches the fallback")
}

// TestInlineFallbackCollisionPrecedesVet guards the collision check the fetch
// runs before its vet under a configured fallback. The fallback suspends a
// structural refusal but not a collision, so a vet running first would hand
// the fallback a structural failure to drop. The run would then succeed with
// a substitute in place of a graph Compile refuses.
func TestInlineFallbackCollisionPrecedesVet(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "properties": {"p": {"$ref": "https://ex.test/a.json"}}}`,
	))
	require.NoError(t, err)

	// The remote carries both faults. It claims the root's URI and misspells a
	// type name.
	doc, err := jsonschema.ParseSchema([]byte(
		`{"$id": "https://ex.test/root.json", "type": "strnig"}`,
	))
	require.NoError(t, err)

	fallback := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.DropRef()
	})

	_, err = jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(mapResolver{"https://ex.test/a.json": doc}),
		jsonschema.WithRefFallback(fallback))
	require.ErrorIs(t, err, jsonschema.ErrIDCollision,
		"the collision is settled before the vet, so the fallback never sees a structural failure to drop")
}

// TestInlineRetrievalBasePointerFallback asserts that the JSON-pointer
// fallback honors [jsonschema.WithRetrievalBase]: a pointer routed through an
// unknown keyword must not rebase at an intermediate $id it crosses, so a ref
// inside the located schema absolutizes against the document's retrieval base
// and resolves from disk, the same way the typed traversal path treats the
// $id as inert.
func TestInlineRetrievalBasePointerFallback(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"leaf.json": `{"type": "string"}`,
	}

	doc := stringtest.Input(`
		{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$ref": "#/x-wrap/mid/inner",
			"x-wrap": {
				"mid": {
					"$id": "https://other.example/pub/",
					"inner": {"$ref": "leaf.json"}
				}
			}
		}
	`)

	var schema jsonschema.Schema

	require.NoError(t, json.Unmarshal([]byte(doc), &schema))

	got, err := jsonschema.Inline(
		t.Context(),
		&schema,
		jsonschema.WithRefResolver(jsonschema.NewFileResolver(mapFS(files))),
		jsonschema.WithBaseURI("main.json"),
		jsonschema.WithRetrievalBase(true),
	)
	require.NoError(t, err)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"string"`)
}
