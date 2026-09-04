package jsonschema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineRejectsCyclicRoot pins that Inline and Compile refuse the same
// cyclic roots and say the same thing. Both freeze the root into a tree
// before reading it, and a pointer cycle has no tree form, whether the loop
// closes through a sub-schema keyword or through a value field. Each
// value-field row states the pointer the refusal must name, since agreement
// on a message the two engines both word wrongly would still pass the
// byte-equal assertion.
func TestInlineRejectsCyclicRoot(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		build   func() *jsonschema.Schema
		pointer string
	}{
		"self cycle": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Items = s

				return s
			},
		},
		"two-node cycle": {
			build: func() *jsonschema.Schema {
				a := &jsonschema.Schema{Type: "object"}
				b := &jsonschema.Schema{Type: "array"}
				a.Items = b
				b.Items = a

				return a
			},
		},
		"a cycle through an unknown keyword": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Extra = map[string]any{"x-self": s}

				return s
			},
			pointer: `"/x-self"`,
		},
		"a cycle through examples": {
			build: func() *jsonschema.Schema {
				s := &jsonschema.Schema{Type: "object"}
				s.Examples = []any{s}

				return s
			},
			pointer: `"/examples/0"`,
		},
		"a root holding several loops": {
			build: func() *jsonschema.Schema {
				// Three loops close on this root. The field table walks
				// examples before the unknown keywords, and each container
				// orders its own members, so both engines close the same loop
				// first.
				s := &jsonschema.Schema{Type: "object"}
				s.Examples = []any{"first", s}
				s.Extra = map[string]any{"x-late": s, "a-early": s}

				return s
			},
			pointer: `"/examples/1"`,
		},
		"a cycle through const": {
			build: func() *jsonschema.Schema {
				// Const is *any, so the box is the pointer the loop closes
				// around.
				var held any

				s := &jsonschema.Schema{Type: "object", Const: &held}
				held = any(s)

				return s
			},
			pointer: `"/const"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, inlineErr := jsonschema.Inline(t.Context(), tc.build())
			require.ErrorIs(t, inlineErr, jsonschema.ErrSchemaCycle)

			_, compileErr := jsonschema.Compile(t.Context(), tc.build())
			require.ErrorIs(t, compileErr, jsonschema.ErrSchemaCycle,
				"Compile and Inline must reject the same root graphs")

			assert.Equal(t, inlineErr.Error(), compileErr.Error(),
				"the two engines must refuse one root with one message")

			if tc.pointer != "" {
				assert.Contains(t, inlineErr.Error(), tc.pointer,
					"the refusal must name the pointer where the loop closes")
			}
		})
	}
}

// TestInlineAliasedRoot pins that both engines accept a root reaching one
// node through two paths: the freeze copies the node once per path, so the
// output holds two independent nodes and Compile accepts what Inline
// produced.
func TestInlineAliasedRoot(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.Schema{Ref: "#/$defs/leaf"}
	root := &jsonschema.Schema{
		Defs:       map[string]*jsonschema.Schema{"leaf": {Type: "string"}},
		Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
	}

	out, err := jsonschema.Inline(t.Context(), root)
	require.NoError(t, err)

	a := out.Properties["a"]
	b := out.Properties["b"]

	require.NotNil(t, a)
	assert.NotSame(t, a, b, "each position holds its own copy")
	assert.Equal(t, "string", a.Type)
	assert.Equal(t, "string", b.Type)
	assert.Same(t, shared, root.Properties["a"], "the input is left as it was")

	_, err = jsonschema.Compile(t.Context(), root)
	require.NoError(t, err, "Compile freezes the same root")
}

// TestInlineAliasedRemoteDocument covers the graph a resolver can hand in. The
// freeze copies the shared node once per path, so each position expands its
// own copy and the output shares nothing.
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
	require.NotNil(t, b)
	assert.NotSame(t, a, b, "the frozen copy holds one node per position")
	assert.Empty(t, a.Ref, "the reference is expanded")
	assert.Empty(t, b.Ref, "the reference is expanded at both positions")

	_, err = jsonschema.Compile(t.Context(), out)
	require.NoError(t, err, "the output is a tree Compile accepts")
	require.Len(t, a.AllOf, 1, "the target joins each node's allOf once")
	require.Len(t, b.AllOf, 1, "the target joins each node's allOf once")
	assert.Equal(t, "string", a.AllOf[0].Type)
	assert.Equal(t, "string", b.AllOf[0].Type)
}

// TestInlineAliasedSubstitute covers the second graph a caller hands in
// aliased: a WithRefFallback substitute, frozen at fallback time.
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
	require.NotNil(t, b)
	assert.NotSame(t, a, b, "the frozen substitute holds one node per position")
	require.Len(t, a.AllOf, 1, "the target joins each node's allOf once")
	require.Len(t, b.AllOf, 1, "the target joins each node's allOf once")
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
// from a resolver or a fallback. Such a graph has no tree form, so the freeze
// at the boundary returns an ordinary ErrSchemaCycle, wrapped in
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
			require.ErrorIs(t, err, jsonschema.ErrSchemaCycle)
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
	require.ErrorIs(t, err, jsonschema.ErrSchemaCycle)
}

// TestValidateAliasedRemoteDocument pins that the boundary refuses cycles
// only. A remote reaching one node from two positions is legal, because the
// freeze copies the node once per position and the validator indexes each
// copy on its own.
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
