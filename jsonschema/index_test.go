package jsonschema_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

// A $defs entry referenced from two properties is one node in the identity
// index: it gets a single id, so its precomputed pattern and numeric-bound
// caches are built once and validation reaching it through either $ref indexes
// the same slot. (Upstream Schema.Resolve requires the pointer graph itself to
// be a tree, so shared reuse is expressed through $ref rather than by aliasing
// one *Schema pointer into two fields.) This guards that the id-indexed caches
// apply wherever a shared node is reached.
func TestValidateSharedDefSharesIndexedCache(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.CompileJSON(t.Context(), []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"a": {"$ref": "#/$defs/str"},
			"b": {"$ref": "#/$defs/str"}
		},
		"$defs": {
			"str": {"type": "string", "pattern": "^a+$", "minLength": 2}
		}
	}`))
	require.NoError(t, err)

	// Both positions resolve to the shared node and enforce its pattern and
	// minLength.
	require.NoError(t, v.Validate(t.Context(), map[string]any{"a": "aa", "b": "aaa"}))

	// "b" fails both the pattern and minLength at property a.
	require.Error(t, v.Validate(t.Context(), map[string]any{"a": "b"}))

	// "a" satisfies the pattern but fails minLength at property b, proving the
	// cache applies at the second referencing position too.
	require.Error(t, v.Validate(t.Context(), map[string]any{"b": "a"}))
}

// A remote document fetched at compile time and referenced from two properties
// exercises the identity index's remote-fold path: Compile assigns the remote's
// nodes ids (extend), grows and precomputes their caches (precomputeRange over
// the freshly indexed range), and validation at either referencing position hits
// the cached remote node by id. The two refs to one URI resolve to a single
// registered node, so the extend dedup keeps it indexed once.
func TestValidateCompileTimeRemoteSharedByTwoRefs(t *testing.T) {
	t.Parallel()

	remote, err := jsonschema.ParseSchema([]byte(`{"type": "integer", "minimum": 10}`))
	require.NoError(t, err)

	resolver := &lateResolver{doc: remote}
	resolver.armed.Store(true) // Resolve at compile time.

	root, err := jsonschema.ParseSchema([]byte(`{
		"type": "object",
		"properties": {
			"a": {"$ref": "https://example.test/shared.json"},
			"b": {"$ref": "https://example.test/shared.json"}
		}
	}`))
	require.NoError(t, err)

	v, err := jsonschema.Compile(t.Context(), root, jsonschema.WithRefResolver(resolver))
	require.NoError(t, err)

	// The remote's minimum bound applies at both positions.
	require.NoError(t, v.ValidateJSON(t.Context(), []byte(`{"a": 10, "b": 25}`)))
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"a": 5}`)))
	require.Error(t, v.ValidateJSON(t.Context(), []byte(`{"b": 3}`)))
}

// A recursive schema (a $defs entry that references itself) is finite in the
// pointer graph -- the self-reference is a $ref string, not a pointer cycle -- so
// the identity index assigns each node one id and validation of a nested instance
// terminates through the ref-resolution cycle guard.
func TestValidateRecursiveRefCompilesAndValidates(t *testing.T) {
	t.Parallel()

	v, err := jsonschema.CompileJSON(t.Context(), []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$ref": "#/$defs/node",
		"$defs": {
			"node": {
				"type": "object",
				"properties": {
					"value": {"type": "integer", "minimum": 0},
					"next": {"$ref": "#/$defs/node"}
				}
			}
		}
	}`))
	require.NoError(t, err)

	require.NoError(t, v.Validate(t.Context(), map[string]any{
		"value": 1,
		"next":  map[string]any{"value": 2, "next": map[string]any{"value": 3}},
	}))

	// A negative value deep in the recursion is still caught, proving the
	// minimum bound cache is reached through the recursive positions.
	require.Error(t, v.Validate(t.Context(), map[string]any{
		"value": 1,
		"next":  map[string]any{"value": -1},
	}))
}
