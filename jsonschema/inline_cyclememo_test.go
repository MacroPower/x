package jsonschema_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
)

// TestInlineCycleTruncatedCopyNotMemoized pins two properties of the cycle
// fallback. A copy truncated by the fallback is not reused for refs expanded
// from a different position. Expanding /properties/x reaches b while a is in
// flight, so b's inner ref to a is cycle-dropped, and that truncated copy must
// stay local to x's expansion or a's minimum silently disappears from y. The
// truncation point itself does not depend on the entry point. The walk marks
// every node it is inside, so the in-place /$defs/a expansion truncates where
// the /properties/x expansion of the same node does.
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

	defs, ok := doc["$defs"].(map[string]any)
	require.True(t, ok)

	// Each def unrolls once and truncates at the ref that returns to the node the
	// walk entered on. Asserting the exact shape pins that depth, not just the
	// agreement between the two entry points.
	wantA := `{"minimum": 1, "allOf": [{"maximum": 5, "allOf": [true]}]}`
	wantB := `{"maximum": 5, "allOf": [{"minimum": 1, "allOf": [true]}]}`

	assert.JSONEq(t, wantA, marshalValue(t, defs["a"]))
	assert.JSONEq(t, wantB, marshalValue(t, defs["b"]))

	// The two ways into each def must agree. /properties/x and /properties/y
	// enter through a ref while the in-place $defs walk descends in, and both
	// truncate on the first return to a node the walk is already inside.
	assert.JSONEq(t, wantA, marshalValue(t, properties["x"]),
		"the ref expansion of a and the in-place expansion of a must agree")
	assert.JSONEq(t, wantB, marshalValue(t, properties["y"]),
		"the expansion of b at y must keep the minimum constraint from a")

	// The functional statement of the same property: the inlined schema must
	// still reject an instance a's minimum forbids.
	compiled, err := jsonschema.Compile(t.Context(), inlined)
	require.NoError(t, err)
	require.Error(t, compiled.ValidateJSON(t.Context(), []byte(`{"y": 0}`)),
		"the inlined schema must keep rejecting what minimum forbids")
}

// TestInlineCycleGuardHoldsAcrossAliasedTarget pins that a node the walk is
// inside stays in flight while the walk re-enters it further down. The document
// comes from a resolver, which is held to the no-cycle rule rather than the
// tree rule, so /$defs/p is also /$defs/t/properties/inner. Expanding p reaches
// t, whose child is p again; that inner visit must leave p's in-flight mark
// alone. Otherwise p's own second ref to itself expands instead of closing the
// cycle, and the walk expands the aliased graph without bound; losing the guard
// kills the test binary with a stack overflow rather than failing this test.
func TestInlineCycleGuardHoldsAcrossAliasedTarget(t *testing.T) {
	t.Parallel()

	// The refs sit in an allOf rather than in properties, so the walk reads them
	// in the authored order rather than in a map-key sort.
	shared := &jsonschema.Schema{AllOf: []*jsonschema.Schema{
		{Ref: "#/$defs/t"},
		{Ref: "#/$defs/p"},
	}}

	document := &jsonschema.Schema{
		ID: "https://example.com/aliased.json",
		Defs: map[string]*jsonschema.Schema{
			"p": shared,
			"t": {Properties: map[string]*jsonschema.Schema{"inner": shared}},
		},
	}

	drop := jsonschema.RefFallbackFunc(func(context.Context, jsonschema.RefFailure) jsonschema.RefAction {
		return jsonschema.DropRef()
	})

	root := &jsonschema.Schema{Ref: "https://example.com/aliased.json#/$defs/p"}

	inlined, err := jsonschema.Inline(t.Context(), root,
		jsonschema.WithRefResolver(fixedResolver{schema: document}),
		jsonschema.WithRefFallback(drop))
	require.NoError(t, err)

	assert.JSONEq(t, stringtest.Input(`
		{
			"allOf": [
				{"properties": {"inner": {"allOf": [true, true]}}},
				true
			]
		}
	`), marshalValue(t, inlined))
}

// marshalValue renders an inlined document, or a value in one, as JSON text.
func marshalValue(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	return string(raw)
}
