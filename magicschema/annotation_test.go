package magicschema_test

import (
	"encoding/json"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
)

// stubAnnotator returns a fixed AnnotationResult for every node, standing in
// for a third-party annotator that emits keywords the bundled ones never do.
type stubAnnotator struct {
	name   string
	result *magicschema.AnnotationResult
}

func (s stubAnnotator) Name() string { return s.name }

func (s stubAnnotator) ForContent(_ []byte) (magicschema.Annotator, error) { return s, nil }

func (s stubAnnotator) Annotate(_ ast.Node, _ string) *magicschema.AnnotationResult {
	return s.result
}

func TestGeneratorAnnotatorTypeFillRespectsValueSet(t *testing.T) {
	t.Parallel()

	// A higher-priority annotator constrains the value set (const or enum) but
	// sets no type; a lower-priority annotator sets a type the value set does
	// not satisfy. Filling the type would yield a schema no value matches (fail
	// closed), so the contradicting type is dropped and the value set stands.
	tcs := map[string]struct {
		high *jsonschema.Schema
	}{
		"const versus contradicting type": {
			high: &jsonschema.Schema{Const: magicschema.ConstValue(5)},
		},
		"enum versus contradicting type": {
			high: &jsonschema.Schema{Enum: []any{1, 2, 3}},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			high := stubAnnotator{name: "high", result: &magicschema.AnnotationResult{Schema: tc.high}}
			low := stubAnnotator{
				name:   "low",
				result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{Type: "string"}},
			}

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

			// A null value contributes no structural type, so the annotator
			// merge is the only type source under test.
			schema, err := gen.Generate([]byte("key:\n"))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			prop, ok := props["key"].(map[string]any)
			require.True(t, ok)

			assert.NotContains(t, prop, "type", "contradicting lower-priority type must not be grafted on")
		})
	}
}

func TestGeneratorAnnotatorEmptyTypesNotGrafted(t *testing.T) {
	t.Parallel()

	// A lower-priority annotator returns a non-nil but empty Types slice.
	// Grafting it onto a higher-priority schema with no type would set Types to
	// the empty slice; structural inference then fills Type, leaving both Type
	// and Types set, which the jsonschema marshaler rejects. The empty slice
	// must not be grafted, so structural inference alone supplies the type.
	high := stubAnnotator{
		name:   "high",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{Description: "desc"}},
	}
	low := stubAnnotator{
		name:   "low",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{Types: []string{}}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

	schema, err := gen.Generate([]byte("key: value\n"))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	prop, ok := props["key"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "string", prop["type"])
	assert.Equal(t, "desc", prop["description"])
}

func TestGeneratorAnnotatorMutuallyExclusiveKeywords(t *testing.T) {
	t.Parallel()

	// Two annotators emit the two shapes of one keyword on the same node: items
	// single-schema versus tuple, $defs versus definitions, and a dependencies
	// key as a schema versus a string array. The jsonschema basicChecks reject a
	// schema carrying both shapes, which would fail the whole document's marshal
	// (fail closed). The higher-priority shape must win and the conflicting one
	// must be dropped so the output still marshals. The bundled annotators never
	// emit these, so this guards third-party extensions.
	tcs := map[string]struct {
		high  *jsonschema.Schema
		low   *jsonschema.Schema
		check func(*testing.T, map[string]any)
	}{
		"items single-schema wins over tuple": {
			high: &jsonschema.Schema{Items: &jsonschema.Schema{Type: "string"}},
			low:  &jsonschema.Schema{ItemsArray: []*jsonschema.Schema{{Type: "integer"}}},
			check: func(t *testing.T, prop map[string]any) {
				t.Helper()

				items, ok := prop["items"].(map[string]any)
				require.True(t, ok, "higher-priority single-schema items must win")
				assert.Equal(t, "string", items["type"])
			},
		},
		"$defs wins over definitions": {
			high: &jsonschema.Schema{Defs: map[string]*jsonschema.Schema{"a": {Type: "string"}}},
			low:  &jsonschema.Schema{Definitions: map[string]*jsonschema.Schema{"b": {Type: "integer"}}},
			check: func(t *testing.T, prop map[string]any) {
				t.Helper()

				assert.Contains(t, prop, "$defs")
				assert.NotContains(t, prop, "definitions", "lower-priority sibling shape must drop")
			},
		},
		"colliding dependency key keeps one shape": {
			high: &jsonschema.Schema{DependencySchemas: map[string]*jsonschema.Schema{"foo": {Type: "string"}}},
			low:  &jsonschema.Schema{DependencyStrings: map[string][]string{"foo": {"bar"}}},
			check: func(t *testing.T, prop map[string]any) {
				t.Helper()

				deps, ok := prop["dependencies"].(map[string]any)
				require.True(t, ok)

				_, isArray := deps["foo"].([]any)
				assert.False(t, isArray, "the higher-priority schema shape must win for the colliding key")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			high := stubAnnotator{name: "high", result: &magicschema.AnnotationResult{Schema: tc.high}}
			low := stubAnnotator{name: "low", result: &magicschema.AnnotationResult{Schema: tc.low}}

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

			schema, err := gen.Generate([]byte("key:\n"))
			require.NoError(t, err)

			// The marshal must succeed: a schema carrying both shapes of the
			// keyword would error here (fail closed).
			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			prop, ok := props["key"].(map[string]any)
			require.True(t, ok)

			tc.check(t, prop)
		})
	}
}

func TestGeneratorAnnotatorRootKeywordsMerge(t *testing.T) {
	t.Parallel()

	// A lower-priority annotator's 2019-09/2020-12 root keywords must survive
	// the merge with a higher-priority annotator that leaves them unset, per
	// the documented "highest-priority annotator that sets a non-zero value
	// wins" rule. The bundled annotators never emit these, so this guards
	// third-party extensions against a silent drop in mergeSchemaFields.
	high := stubAnnotator{
		name:   "high",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{Type: "string"}},
	}
	low := stubAnnotator{
		name: "low",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			Schema:        "https://json-schema.org/draft/2020-12/schema",
			Anchor:        "anc",
			DynamicAnchor: "danc",
			DynamicRef:    "#dref",
			Vocabulary:    map[string]bool{"https://example.test/vocab": true},
		}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

	schema, err := gen.Generate([]byte("key: value\n"))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	prop, ok := props["key"].(map[string]any)
	require.True(t, ok)

	// The higher-priority type stays, and every lower-priority root keyword
	// fills the gap rather than being dropped.
	assert.Equal(t, "string", prop["type"])
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", prop["$schema"])
	assert.Equal(t, "anc", prop["$anchor"])
	assert.Equal(t, "danc", prop["$dynamicAnchor"])
	assert.Equal(t, "#dref", prop["$dynamicRef"])
	assert.Equal(t, map[string]any{"https://example.test/vocab": true}, prop["$vocabulary"])
}
