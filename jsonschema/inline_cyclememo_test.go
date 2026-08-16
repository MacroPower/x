package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineCycleTruncatedCopyNotMemoized pins that a copy truncated by the
// cycle fallback is not reused for refs expanded under a different inflight
// stack. Expanding /properties/x reaches b while a is in flight, so b's inner
// ref to a is cycle-dropped; that truncated copy must stay local to x's
// expansion. Expanding /properties/y from a cycle-free position must produce
// the same full expansion the in-place $defs/b walk does, or a constraint
// (a's minimum) silently disappears from y and the output disagrees with
// itself about one source pointer.
func TestInlineCycleTruncatedCopyNotMemoized(t *testing.T) {
	t.Parallel()

	root, err := jsonschema.ParseSchema([]byte(stringtest.Input(`
		{
			"properties": {
				"x": {"$ref": "#/$defs/a"},
				"y": {"$ref": "#/$defs/b"}
			},
			"$defs": {
				"a": {"allOf": [{"$ref": "#/$defs/b"}], "minimum": 1},
				"b": {"allOf": [{"$ref": "#/$defs/a"}], "maximum": 5}
			}
		}
	`)))
	require.NoError(t, err)

	drop := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.DropRef()
	})

	inlined, err := jsonschema.Inline(t.Context(), root, jsonschema.WithRefFallback(drop))
	require.NoError(t, err)

	raw, err := json.Marshal(inlined)
	require.NoError(t, err)

	var doc map[string]any

	require.NoError(t, json.Unmarshal(raw, &doc))

	properties, ok := doc["properties"].(map[string]any)
	require.True(t, ok)

	// The expansion of b at y must carry a's minimum; before the fix y reused
	// x's truncated copy and the constraint vanished. (The truncation point
	// still differs from the in-place $defs/b expansion by one unrolling: a
	// single-point cycle policy is open follow-up work.)
	yJSON, err := json.Marshal(properties["y"])
	require.NoError(t, err)
	require.Contains(t, string(yJSON), `"minimum"`,
		"y must keep the minimum constraint from a")

	// The functional statement of the same property: the inlined schema must
	// still reject an instance a's minimum forbids.
	compiled, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err)
	require.Error(t, compiled.ValidateJSON(t.Context(), []byte(`{"y": 0}`)),
		"the inlined schema must keep rejecting what minimum forbids")
}
