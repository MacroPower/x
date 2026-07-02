package magicschema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

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

// markerStubAnnotator parses a custom "# @myschema type:<type>" comment line,
// standing in for a third-party annotator with its own marker syntax. It
// implements [magicschema.MarkerAnnotator] so the fallback description
// extractor keeps its marker lines out of descriptions.
type markerStubAnnotator struct{}

func (markerStubAnnotator) Name() string { return "myschema" }

func (m markerStubAnnotator) ForContent(_ []byte) (magicschema.Annotator, error) { return m, nil }

func (markerStubAnnotator) Annotate(node ast.Node, _ string) *magicschema.AnnotationResult {
	run, _, _ := magicschema.HeadCommentRun(node)

	for _, line := range run {
		cleaned := strings.TrimSpace(magicschema.StripCommentMarker(line))

		after, ok := strings.CutPrefix(cleaned, "@myschema type:")
		if !ok {
			continue
		}

		return &magicschema.AnnotationResult{
			Schema: &jsonschema.Schema{Type: strings.TrimSpace(after)},
		}
	}

	return nil
}

func (markerStubAnnotator) IsAnnotationLine(line string) bool {
	return strings.HasPrefix(line, "@myschema")
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

func TestGeneratorAnnotatorFlagsWithoutSchema(t *testing.T) {
	t.Parallel()

	// SkipProperties and MergeProperties are documented to take effect
	// whenever any annotator sets them; the AnnotationResult contract never
	// requires a Schema alongside. An annotation carrying only a flag must
	// transform the structural schema the same way one carrying an empty
	// Schema does -- under WithStrict, ignoring MergeProperties would emit
	// additionalProperties:false on a node the annotation declares dynamic
	// and reject the source file's own dynamic keys (fail closed).
	tcs := map[string]struct {
		result *magicschema.AnnotationResult
		check  func(*testing.T, map[string]any)
	}{
		"mergeProperties folds children into additionalProperties": {
			result: &magicschema.AnnotationResult{MergeProperties: true},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.NotContains(t, got, "properties")

				ap, ok := got["additionalProperties"].(map[string]any)
				require.True(t, ok,
					"expected a folded additionalProperties schema, got %v",
					got["additionalProperties"])
				assert.Equal(t, "integer", ap["type"])
			},
		},
		"skipProperties strips children and stays permissive": {
			result: &magicschema.AnnotationResult{SkipProperties: true},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.NotContains(t, got, "properties")
				assert.Equal(t, true, got["additionalProperties"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(stubAnnotator{name: "flags", result: tc.result}),
				magicschema.WithStrict(true),
			)

			schema, err := gen.Generate([]byte("env:\n  a: 1\n  b: 2\n"))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			env, ok := props["env"].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, "object", env["type"])
			tc.check(t, env)
		})
	}
}

func TestGeneratorAnnotatorPropertiesGetStrictAdditionalProperties(t *testing.T) {
	t.Parallel()

	// An annotator that authors its own Properties skips the structural
	// fill, but WithStrict's additionalProperties:false must still apply
	// when the annotation leaves the field unset, the same as every
	// structurally walked object.
	ann := stubAnnotator{
		name: "props",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"x": {Type: "string"},
			},
		}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(ann), magicschema.WithStrict(true))

	schema, err := gen.Generate([]byte("obj:\n  x: hi\n  y: 2\n"))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	obj, ok := props["obj"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, false, obj["additionalProperties"],
		"strict mode must apply to annotator-authored object schemas too")
}

func TestGeneratorAnnotatorRefNotGraftedBesideConstraints(t *testing.T) {
	t.Parallel()

	// Under Draft 7 every validation sibling of $ref is ignored, so grafting
	// a lower-priority $ref beside higher-priority constraints would let the
	// reference govern entirely and invert the documented precedence. The
	// $ref fill must be skipped when the winner carries a type or value
	// constraint.
	high := stubAnnotator{
		name: "high",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			Type:    "string",
			Pattern: "^[a-z]+$",
		}},
	}
	low := stubAnnotator{
		name: "low",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			Ref: "https://example.com/name.schema.json",
		}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

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

	assert.Equal(t, "string", prop["type"])
	assert.Equal(t, "^[a-z]+$", prop["pattern"])
	assert.NotContains(t, prop, "$ref",
		"a lower-priority $ref must not nullify the winner's constraints")
}

func TestGeneratorAnnotatorConditionalFilledAsUnit(t *testing.T) {
	t.Parallel()

	// The if/then/else conditional only has meaning as a unit. A then
	// without an if is inert (accepts everything), so grafting a
	// lower-priority annotator's if under it would activate a conditional
	// neither annotator wrote, pairing the winner's then with the loser's
	// trigger.
	high := stubAnnotator{
		name: "high",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			Then: &jsonschema.Schema{Type: "number"},
		}},
	}
	low := stubAnnotator{
		name: "low",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{
			If:   &jsonschema.Schema{Type: "integer"},
			Then: &jsonschema.Schema{Type: "string"},
		}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(high, low))

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

	assert.NotContains(t, prop, "if",
		"a lower-priority if must not activate the winner's inert then")

	then, ok := prop["then"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "number", then["type"])
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

func TestGeneratorAnnotatorHighPriorityEmptyTypesNormalized(t *testing.T) {
	t.Parallel()

	// The highest-priority annotator returns a non-nil but empty Types slice.
	// It copies straight through copySchema (no lower-priority result reaches
	// the mergeSchemaFields guard), so structural inference fills Type and
	// leaves both Type and Types set -- which the jsonschema marshaler rejects,
	// failing the whole document. Normalizing the empty slice to nil in
	// copySchema keeps the marshal working.
	high := stubAnnotator{
		name:   "high",
		result: &magicschema.AnnotationResult{Schema: &jsonschema.Schema{Types: []string{}, Description: "desc"}},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(high))

	schema, err := gen.Generate([]byte("key: value\n"))
	require.NoError(t, err)

	// The marshal must not error on a schema carrying both Type and Types.
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

func TestGeneratorMarkerAnnotatorKeepsMarkersOutOfDescriptions(t *testing.T) {
	t.Parallel()

	// The built-in [IsAnnotationComment] list does not know a custom
	// annotator's marker grammar, so without the annotator's MarkerAnnotator
	// recognizer the fallback comment extractor would emit its marker lines
	// as prose descriptions.
	tcs := map[string]struct {
		input    string
		wantType string
		wantDesc string // empty means the property must carry no description
	}{
		"marker alone yields no description": {
			input: stringtest.Input(`
				# @myschema type:string
				name: foo
			`),
			wantType: "string",
		},
		"prose beside the marker becomes the description": {
			input: stringtest.Input(`
				# The name.
				# @myschema type:string
				name: foo
			`),
			wantType: "string",
			wantDesc: "The name.",
		},
		"inline marker yields no description": {
			input:    "name: foo # @myschema type:string\n",
			wantType: "string",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(markerStubAnnotator{}))

			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			prop := propertyAt(t, got, "name")
			assert.Equal(t, tc.wantType, prop["type"])

			if tc.wantDesc == "" {
				assert.NotContains(t, prop, "description",
					"annotation marker must not leak into the description")
			} else {
				assert.Equal(t, tc.wantDesc, prop["description"])
			}
		})
	}
}
