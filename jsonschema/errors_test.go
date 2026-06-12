package jsonschema_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/jsonschema"
)

func TestValidationError_Leaves(t *testing.T) {
	t.Parallel()

	leaf := func(path, keyword string) *jsonschema.ValidationError {
		return &jsonschema.ValidationError{InstancePath: path, Keyword: keyword}
	}

	typeName := leaf("/name", "type")
	typeAge := leaf("/age", "type")

	// The propertyNames node is a leaf despite carrying the inner name-check.
	propNames := &jsonschema.ValidationError{
		InstancePath: "/BadKey",
		Keyword:      "propertyNames",
		Causes:       []*jsonschema.ValidationError{leaf("/BadKey", "pattern")},
	}

	tcs := map[string]struct {
		err  *jsonschema.ValidationError
		want []*jsonschema.ValidationError
	}{
		"single leaf returns itself": {
			err:  typeName,
			want: []*jsonschema.ValidationError{typeName},
		},
		"synthetic root flattens to its leaves": {
			err:  &jsonschema.ValidationError{Causes: []*jsonschema.ValidationError{typeName, typeAge}},
			want: []*jsonschema.ValidationError{typeName, typeAge},
		},
		"nested wrappers are descended": {
			err: &jsonschema.ValidationError{
				Keyword: "anyOf",
				Message: "did not validate against any subschema",
				Causes: []*jsonschema.ValidationError{
					{Keyword: "allOf", Causes: []*jsonschema.ValidationError{typeName}},
					typeAge,
				},
			},
			want: []*jsonschema.ValidationError{typeName, typeAge},
		},
		"propertyNames is a leaf, its cause is not descended": {
			err:  propNames,
			want: []*jsonschema.ValidationError{propNames},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.err.Leaves())
		})
	}
}

func TestValidationError_Leaves_SharedNodeReturnedOnce(t *testing.T) {
	t.Parallel()

	shared := &jsonschema.ValidationError{InstancePath: "/x", Keyword: "type"}
	root := &jsonschema.ValidationError{
		Causes: []*jsonschema.ValidationError{
			{Keyword: "anyOf", Causes: []*jsonschema.ValidationError{shared}},
			{Keyword: "oneOf", Causes: []*jsonschema.ValidationError{shared}},
		},
	}

	assert.Equal(t, []*jsonschema.ValidationError{shared}, root.Leaves())
}

func TestValidationError_Leaves_EndToEnd(t *testing.T) {
	t.Parallel()

	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"name": {Type: "string"},
			"age":  {Type: "number"},
		},
	}

	err := jsonschema.Validate(t.Context(), schema, map[string]any{"name": 1, "age": "x"})

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	leaves := ve.Leaves()
	require.Len(t, leaves, 2)

	paths := []string{leaves[0].InstancePath, leaves[1].InstancePath}
	assert.ElementsMatch(t, []string{"/name", "/age"}, paths)

	for _, l := range leaves {
		assert.Equal(t, "type", l.Keyword)
	}
}

func TestValidationError_InstanceSegments(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		wantPath string
		want     []jsonschema.Segment
	}{
		"object property named 0": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"0": {Type: "string"}},
			},
			instance: map[string]any{"0": 1},
			wantPath: "/0",
			want:     []jsonschema.Segment{{Key: "0"}},
		},
		"array index 0": {
			schema: &jsonschema.Schema{
				Type:  "array",
				Items: &jsonschema.Schema{Type: "string"},
			},
			instance: []any{1},
			wantPath: "/0",
			want:     []jsonschema.Segment{{Index: 0, IsIndex: true}},
		},
		"key containing tilde is escaped in path but not in segment": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"a~b": {Type: "string"}},
			},
			instance: map[string]any{"a~b": 1},
			wantPath: "/a~0b",
			want:     []jsonschema.Segment{{Key: "a~b"}},
		},
		"key containing slash is escaped in path but not in segment": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"a/b": {Type: "string"}},
			},
			instance: map[string]any{"a/b": 1},
			wantPath: "/a~1b",
			want:     []jsonschema.Segment{{Key: "a/b"}},
		},
		"nested object, array, object": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"items": {
						Type: "array",
						Items: &jsonschema.Schema{
							Type: "object",
							Properties: map[string]*jsonschema.Schema{
								"name": {Type: "string"},
							},
						},
					},
				},
			},
			instance: map[string]any{"items": []any{map[string]any{"name": 1}}},
			wantPath: "/items/0/name",
			want: []jsonschema.Segment{
				{Key: "items"},
				{Index: 0, IsIndex: true},
				{Key: "name"},
			},
		},
		"root-level failure has nil segments": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: 1,
			wantPath: "",
			want:     nil,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tc.schema, tc.instance)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)

			leaves := ve.Leaves()
			require.Len(t, leaves, 1)

			assert.Equal(t, tc.wantPath, leaves[0].InstancePath)
			assert.Equal(t, tc.want, leaves[0].InstanceSegments())
		})
	}
}

func TestValidationError_InstanceSegments_SiblingsDoNotAlias(t *testing.T) {
	t.Parallel()

	// Both leaves descend from the same parent location, so this catches an
	// append into a shared backing array overwriting a sibling's segment.
	// The siblings branch at depth 4 because that is where a plain append
	// would first alias: append's doubling growth (1, 2, 4) gives the shared
	// three-segment prefix spare capacity (len 3, cap 4), so without the full
	// slice expression both descents would write their fourth segment into
	// the same backing array slot. At shallower depths cap equals len and
	// every append reallocates, hiding the bug.
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"l1": {
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"l2": {
						Type: "object",
						Properties: map[string]*jsonschema.Schema{
							"l3": {
								Type: "object",
								Properties: map[string]*jsonschema.Schema{
									"a": {Type: "string"},
									"b": {Type: "string"},
								},
							},
						},
					},
				},
			},
		},
	}

	err := jsonschema.Validate(t.Context(), schema, map[string]any{
		"l1": map[string]any{
			"l2": map[string]any{
				"l3": map[string]any{"a": 1, "b": 2},
			},
		},
	})

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	got := map[string][]jsonschema.Segment{}
	for _, leaf := range ve.Leaves() {
		got[leaf.InstancePath] = leaf.InstanceSegments()
	}

	assert.Equal(t, map[string][]jsonschema.Segment{
		"/l1/l2/l3/a": {{Key: "l1"}, {Key: "l2"}, {Key: "l3"}, {Key: "a"}},
		"/l1/l2/l3/b": {{Key: "l1"}, {Key: "l2"}, {Key: "l3"}, {Key: "b"}},
	}, got)
}

// TestValidationError_InstanceSegments_RenderEqualsInstancePath validates one
// schema/instance pair with failing sibling pairs at every depth from 1 to 6
// (object keys at 1-6, array indexes at 2-7) and asserts that re-rendering
// each leaf's InstanceSegments as an RFC 6901 pointer reproduces its
// InstancePath byte for byte. Sibling pairs at every depth keep the test
// independent of exactly where append growth first leaves spare capacity in a
// shared prefix's backing array, so any aliasing between sibling descents
// shows up as a segments/path mismatch at some depth.
func TestValidationError_InstanceSegments_RenderEqualsInstancePath(t *testing.T) {
	t.Parallel()

	// The renderer mirrors the package's pointer construction: keys escaped
	// per RFC 6901 ("~" -> "~0" before "/" -> "~1"), indexes via
	// strconv.Itoa.
	render := func(segs []jsonschema.Segment) string {
		var b strings.Builder

		for _, seg := range segs {
			b.WriteString("/")

			if seg.IsIndex {
				b.WriteString(strconv.Itoa(seg.Index))
			} else {
				key := strings.ReplaceAll(seg.Key, "~", "~0")
				b.WriteString(strings.ReplaceAll(key, "/", "~1"))
			}
		}

		return b.String()
	}

	// Each nesting level fails its "a~x" and "b/y" keys (exercising key
	// escaping at every depth) and both "arr" elements (exercising index
	// segments), then recurses through "n".
	const depth = 6

	var (
		schema   *jsonschema.Schema
		instance map[string]any
	)

	for range depth {
		props := map[string]*jsonschema.Schema{
			"a~x": {Type: "string"},
			"b/y": {Type: "string"},
			"arr": {Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		}
		values := map[string]any{
			"a~x": 1,
			"b/y": 2,
			"arr": []any{3, 4},
		}

		if schema != nil {
			props["n"] = schema
			values["n"] = instance
		}

		schema = &jsonschema.Schema{Type: "object", Properties: props}
		instance = values
	}

	err := jsonschema.Validate(t.Context(), schema, instance)

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	leaves := ve.Leaves()

	wantPaths := make([]string, 0, 4*depth)

	prefix := ""
	for range depth {
		wantPaths = append(wantPaths,
			prefix+"/a~0x", prefix+"/b~1y", prefix+"/arr/0", prefix+"/arr/1")
		prefix += "/n"
	}

	gotPaths := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		gotPaths = append(gotPaths, leaf.InstancePath)
	}

	require.ElementsMatch(t, wantPaths, gotPaths)

	for _, leaf := range leaves {
		assert.Equal(t, leaf.InstancePath, render(leaf.InstanceSegments()),
			"re-rendered segments must reproduce the pointer %q", leaf.InstancePath)
	}
}

func TestValidationError_InstanceSegments_HandConstructed(t *testing.T) {
	t.Parallel()

	ve := &jsonschema.ValidationError{InstancePath: "/a/0"}

	assert.Nil(t, ve.InstanceSegments())
}

func TestValidationError_SchemaSegments(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		schema   *jsonschema.Schema
		instance any
		wantPath string
		want     []jsonschema.Segment
	}{
		"root keyword": {
			schema:   &jsonschema.Schema{Type: "string"},
			instance: 1,
			wantPath: "/type",
			want:     []jsonschema.Segment{{Key: "type"}},
		},
		"property key escaped in path but verbatim in segment": {
			schema: &jsonschema.Schema{
				Type:       "object",
				Properties: map[string]*jsonschema.Schema{"a/b": {Type: "string"}},
			},
			instance: map[string]any{"a/b": 1},
			wantPath: "/properties/a~1b/type",
			want: []jsonschema.Segment{
				{Key: "properties"},
				{Key: "a/b"},
				{Key: "type"},
			},
		},
		"list applicator branch carries the index in typed form": {
			schema: &jsonschema.Schema{
				AllOf: []*jsonschema.Schema{{Type: "string"}, {Type: "integer"}},
			},
			instance: "x",
			wantPath: "/allOf/1/type",
			want: []jsonschema.Segment{
				{Key: "allOf"},
				{Index: 1, IsIndex: true},
				{Key: "type"},
			},
		},
		"property named like a keyword stays a key segment": {
			schema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"items": {Type: "string"},
				},
			},
			instance: map[string]any{"items": 1},
			wantPath: "/properties/items/type",
			want: []jsonschema.Segment{
				{Key: "properties"},
				{Key: "items"},
				{Key: "type"},
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := jsonschema.Validate(t.Context(), tc.schema, tc.instance)

			var ve *jsonschema.ValidationError

			require.ErrorAs(t, err, &ve)

			leaves := ve.Leaves()
			require.Len(t, leaves, 1)

			assert.Equal(t, tc.wantPath, leaves[0].SchemaPath)
			assert.Equal(t, tc.want, leaves[0].SchemaSegments())
		})
	}
}

// TestValidationError_SchemaSegments_RenderEqualsSchemaPath asserts that
// re-rendering every error node's SchemaSegments as an RFC 6901 pointer
// reproduces its SchemaPath byte for byte, across the whole error tree of a
// schema mixing map applicators, list applicators, and leaf keywords.
func TestValidationError_SchemaSegments_RenderEqualsSchemaPath(t *testing.T) {
	t.Parallel()

	render := func(segs []jsonschema.Segment) string {
		var b strings.Builder

		for _, seg := range segs {
			b.WriteString("/")

			if seg.IsIndex {
				b.WriteString(strconv.Itoa(seg.Index))
			} else {
				key := strings.ReplaceAll(seg.Key, "~", "~0")
				b.WriteString(strings.ReplaceAll(key, "/", "~1"))
			}
		}

		return b.String()
	}

	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"a~x": {Type: "string", MinLength: jsonschema.Ptr(3)},
			"b/y": {
				AnyOf: []*jsonschema.Schema{
					{Type: "integer"},
					{Type: "object", Required: []string{"z"}},
				},
			},
			"arr": {
				Type:  "array",
				Items: &jsonschema.Schema{Pattern: "^x"},
			},
		},
	}

	err := jsonschema.Validate(t.Context(), schema, map[string]any{
		"a~x": "no",
		"b/y": "neither",
		"arr": []any{"y"},
	})

	var ve *jsonschema.ValidationError

	require.ErrorAs(t, err, &ve)

	var walkTree func(e *jsonschema.ValidationError)

	checked := 0
	walkTree = func(e *jsonschema.ValidationError) {
		assert.Equal(t, e.SchemaPath, render(e.SchemaSegments()),
			"re-rendered segments must reproduce the pointer %q", e.SchemaPath)

		checked++

		for _, cause := range e.Causes {
			walkTree(cause)
		}
	}
	walkTree(ve)

	assert.Greater(t, checked, 4, "the error tree should exercise several locations")
}

func TestValidationError_SchemaSegments_HandConstructed(t *testing.T) {
	t.Parallel()

	ve := &jsonschema.ValidationError{SchemaPath: "/properties/a/type"}

	assert.Nil(t, ve.SchemaSegments())
}

func TestValidationError_TargetsKey(t *testing.T) {
	t.Parallel()

	key := []string{
		"additionalProperties", "propertyNames", "required",
		"minProperties", "maxProperties",
		"minItems", "maxItems", "uniqueItems",
		"contains", "minContains", "maxContains",
	}
	value := []string{
		"type", "enum", "const", "pattern", "minimum", "maximum",
		"minLength", "maxLength", "format", "multipleOf", "",
	}

	for _, keyword := range key {
		t.Run("key/"+keyword, func(t *testing.T) {
			t.Parallel()

			assert.True(t, (&jsonschema.ValidationError{Keyword: keyword}).TargetsKey())
		})
	}

	for _, keyword := range value {
		t.Run("value/"+keyword, func(t *testing.T) {
			t.Parallel()

			assert.False(t, (&jsonschema.ValidationError{Keyword: keyword}).TargetsKey())
		})
	}
}
