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
		"one node at two positions": {
			build: func() *jsonschema.Schema {
				shared := &jsonschema.Schema{Type: "string"}

				return &jsonschema.Schema{
					Properties: map[string]*jsonschema.Schema{"a": shared, "b": shared},
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.Inline(t.Context(), tc.build())
			require.ErrorIs(t, err, jsonschema.ErrSchemaNotTree)

			_, compileErr := jsonschema.Compile(t.Context(), tc.build())
			require.ErrorIs(t, compileErr, jsonschema.ErrSchemaNotTree,
				"Compile and Inline must reject the same roots")
		})
	}
}

// TestInlineAliasedRemoteDocument covers the graph a resolver can hand in, which
// neither engine tree-checks. The shared node is expanded once, in place, and
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

// TestCompileCyclicRemoteDocument pins that a cyclic document from a resolver
// reaches the validator without looping. The deep copy no longer rejects one,
// and it no longer needs to: every walk a fetched document reaches stops on a
// pointer it has already seen, and the validation walk keys its own guard on the
// schema and the instance position together.
func TestCompileCyclicRemoteDocument(t *testing.T) {
	t.Parallel()

	remote := &jsonschema.Schema{ID: "https://example.com/cyclic", Type: "array"}
	remote.Items = remote

	root := &jsonschema.Schema{Ref: "https://example.com/cyclic"}

	err := jsonschema.Validate(t.Context(), root, []any{[]any{[]any{}}},
		jsonschema.WithRefResolver(fixedResolver{schema: remote}))
	require.NoError(t, err)

	err = jsonschema.Validate(t.Context(), root, "not an array",
		jsonschema.WithRefResolver(fixedResolver{schema: remote}))
	require.Error(t, err)
}
