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
