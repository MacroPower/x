package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRejectsNonTreeRoot pins that Inline demands the same root shape
// Compile does. Inlining copies the input and expands each reference in place,
// so a node reached from two positions would take one position's expansion at
// both, and a pointer cycle has no finite expansion.
func TestInlineRejectsNonTreeRoot(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build func() *jsonschema.Schema

		// False marks a root Compile accepts and Inline does not. Compile never
		// copies its root, so a loop closing through a value field escapes its
		// tree check.
		compileRejects bool
	}{
		"self cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Items = s

				return s
			},
			compileRejects: true,
		},
		"two-node cycle": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{Type: "object"}
				b := &jsonschema.Schema{Type: "array"}
				a.Items = b
				b.Items = a

				return a
			},
			compileRejects: true,
		},
		"a cycle through an unknown keyword": {
			build: func() *jsonschema.Schema {
				// The tree check reads the sub-schema graph only, so the
				// pristine clone is what reports this one.
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x-self": s}

				return s
			},
			compileRejects: false,
		},
		"one node at two positions": {
			build: func() *jsonschema.Schema {
				shared := &jsonschema.Schema{Type: "string"}

				return &jsonschema.Schema{
					Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
				}
			},
			compileRejects: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Inline(t.Context(), tc.build())
			require.ErrorIs(t, err, jsonschema.ErrSchemaNotTree)

			if !tc.compileRejects {
				return
			}

			_, compileErr := jsonschema.Compile(t.Context(), tc.build())
			require.ErrorIs(t, compileErr, jsonschema.ErrSchemaNotTree,
				"Compile and Inline must reject the same sub-schema graphs")
		})
	}
}

// TestInlineAliasedRemoteDocument covers the graph a resolver can hand in, which
// no engine tree-checks. The shared node is expanded once, in place, and
// both positions read that one expansion; expanding it twice would join the
// target to the node's allOf twice over.
func TestInlineAliasedRemoteDocument(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Ref: "#/$defs/leaf", Title: "shared"}
	remote := &jsonschema.Schema{
		ID:   "https://example.com/aliased",
		Defs: map[string]*jsonschema.Schema{"leaf": {Type: "string"}},
		Properties: map[string]*jsonschema.Schema{
			"a": shared,
			"b": shared,
		},
	}

	root := &jsonschema.Schema{Ref: "https://example.com/aliased"}

	out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefResolver(fixedResolver{schema: remote}))
	require.NoError(t, err)

	a := out.Properties["a"]
	b := out.Properties["b"]

	require.NotNil(t, a)
	assert.Same(t, a, b, "the copy of an aliased document keeps the node shared")
	assert.Empty(t, a.Ref, "the reference is expanded")

	_, err = jsonschema.Compile(t.Context(), out)
	require.ErrorIs(t, err, jsonschema.ErrSchemaNotTree,
		"the sharing survives into the output, which Compile then rejects")
	require.Len(t, a.AllOf, 1, "the target joins the node's allOf once, not once per position")
	assert.Equal(t, "string", a.AllOf[0].Type)
}

// TestInlineAliasedSubstitute covers the second graph no engine tree-checks: a
// WithRefFallback substitute, which the caller hands in at fallback time.
func TestInlineAliasedSubstitute(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Ref: "#/$defs/leaf", Title: "shared"}
	substitute := &jsonschema.Schema{
		ID:   "https://example.com/substitute",
		Defs: map[string]*jsonschema.Schema{"leaf": {Type: "integer"}},
		Properties: map[string]*jsonschema.Schema{
			"a": shared,
			"b": shared,
		},
	}

	fallback := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.SubstituteRef(substitute)
	})

	root := &jsonschema.Schema{Ref: "https://example.com/missing"}

	out, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefFallback(fallback))
	require.NoError(t, err)

	a := out.Properties["a"]
	b := out.Properties["b"]

	require.NotNil(t, a)
	assert.Same(t, a, b, "the copy of an aliased substitute keeps the node shared")
	require.Len(t, a.AllOf, 1, "the target joins the node's allOf once, not once per position")
	assert.Equal(t, "integer", a.AllOf[0].Type)
}

// cyclicRemote returns a document whose Items points back at itself, plus an
// unknown keyword holding a schema the typed traversal cannot reach. A $ref
// into that keyword drives the JSON-pointer fallback, which marshals the
// document it searches.
func cyclicRemote() *jsonschema.Schema {
	remote := &jsonschema.Schema{
		ID:    "https://example.com/cyclic",
		Type:  "array",
		Extra: map[string]any{"x-hidden": map[string]any{"type": "string"}},
	}
	remote.Items = remote

	return remote
}

// TestCyclicGraphFromOutsideRejected pins that the fetch and substitution
// boundaries refuse a schema graph holding a pointer cycle, whether it arrives
// from a resolver or a fallback. Such a graph enters resolution space, where
// the JSON-pointer fallback marshals the document it searches, and marshaling a
// cyclic schema graph overflows the stack fatally rather than returning an
// error. The boundary returns an ordinary ErrSchemaNotTree instead, wrapped in
// ErrRefResolve for a fetched document and bare for a substitute.
func TestCyclicGraphFromOutsideRejected(t *testing.T) {
	t.Parallel()

	cyclicSubstitute := func() *jsonschema.Schema {
		s := &jsonschema.Schema{Type: "object"}
		s.Items = s

		return s
	}

	tests := map[string]struct {
		ref  string
		opts []jsonschema.InlineOption
	}{
		"validating against a cyclic remote": {
			ref:  "https://example.com/cyclic",
			opts: []jsonschema.InlineOption{jsonschema.WithRefResolver(fixedResolver{schema: cyclicRemote()})},
		},
		"a pointer fragment into an unknown keyword of a cyclic remote": {
			ref:  "https://example.com/cyclic#/x-hidden",
			opts: []jsonschema.InlineOption{jsonschema.WithRefResolver(fixedResolver{schema: cyclicRemote()})},
		},
		"a remote whose cycle closes through a shared container": {
			ref: "https://example.com/shared",
			opts: []jsonschema.InlineOption{jsonschema.WithRefResolver(fixedResolver{schema: func() *jsonschema.Schema {
				shared := map[string]any{}
				inner := &jsonschema.Schema{Extra: map[string]any{"b": shared}}
				shared["s"] = inner

				return &jsonschema.Schema{
					ID:    "https://example.com/shared",
					Extra: map[string]any{"a": shared},
				}
			}()})},
		},
		"a remote whose cycle closes around a schema value": {
			ref: "https://example.com/value",
			opts: []jsonschema.InlineOption{jsonschema.WithRefResolver(fixedResolver{schema: func() *jsonschema.Schema {
				held := map[string]any{}
				held["self"] = jsonschema.Schema{Extra: held}

				return &jsonschema.Schema{
					ID:    "https://example.com/value",
					Extra: map[string]any{"x-thing": held},
				}
			}()})},
		},
		"a cyclic fallback substitute": {
			ref: "https://example.com/missing",
			opts: []jsonschema.InlineOption{jsonschema.WithRefFallback(
				jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
					return jsonschema.SubstituteRef(cyclicSubstitute())
				}),
			)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := &jsonschema.Schema{Ref: tc.ref}

			_, err := jsonschema.Inline(t.Context(), root, tc.opts...)
			require.ErrorIs(t, err, jsonschema.ErrSchemaNotTree)
		})
	}
}

// TestValidateCyclicRemoteDocument pins the validator's half of the same
// boundary. The fetch refuses the cyclic document, so no cache holds a graph a
// later marshal would recurse into.
func TestValidateCyclicRemoteDocument(t *testing.T) {
	t.Parallel()

	root := &jsonschema.Schema{Ref: "https://example.com/cyclic"}

	err := jsonschema.Validate(t.Context(), root, []any{},
		jsonschema.WithRefResolver(fixedResolver{schema: cyclicRemote()}))
	require.ErrorIs(t, err, jsonschema.ErrRefResolve)
	require.ErrorIs(t, err, jsonschema.ErrSchemaNotTree)
}

// TestValidateAliasedRemoteDocument pins that the boundary check reads cycles
// only. A remote reaching one node from two positions is legal, because every
// walk that reaches a registered document dedupes pointers, and the validator
// gives the shared node one index id and one cache slot.
func TestValidateAliasedRemoteDocument(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Type: "string"}
	remote := &jsonschema.Schema{
		ID:         "https://example.com/aliased-remote",
		Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
	}

	root := &jsonschema.Schema{Ref: "https://example.com/aliased-remote"}
	opt := jsonschema.WithRefResolver(fixedResolver{schema: remote})

	require.NoError(t, jsonschema.Validate(t.Context(), root, map[string]any{"a": "x", "b": "y"}, opt))
	require.Error(t, jsonschema.Validate(t.Context(), root, map[string]any{"a": 1}, opt))
}
