package magicschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
)

func TestIsAnnotationComment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  bool
	}{
		"@schema block": {
			input: "@schema",
			want:  true,
		},
		"@schema inline": {
			input: "@schema type:string",
			want:  true,
		},
		"@param bitnami": {
			input: "@param key.path [string] description",
			want:  true,
		},
		"@skip bitnami": {
			input: "@skip key.path",
			want:  true,
		},
		"@section bitnami": {
			input: "@section Title",
			want:  true,
		},
		"@extra bitnami": {
			input: "@extra key.path description",
			want:  true,
		},
		"@descriptionStart bitnami": {
			input: "@descriptionStart",
			want:  true,
		},
		"@descriptionEnd bitnami": {
			input: "@descriptionEnd",
			want:  true,
		},
		"helm-docs double dash with space": {
			input: "-- description text",
			want:  true,
		},
		"helm-docs double dash alone": {
			input: "--",
			want:  true,
		},
		"helm-docs double dash without space": {
			// The norwoodj annotator accepts "# --" with no following
			// space, so the stripped form must be treated as a marker
			// too; otherwise "--text" leaks into descriptions.
			input: "--text",
			want:  true,
		},
		"@ignore helm-docs": {
			input: "@ignore",
			want:  true,
		},
		"@raw helm-docs": {
			input: "@raw",
			want:  true,
		},
		"@notationType helm-docs": {
			input: "@notationType -- json",
			want:  true,
		},
		"@default helm-docs": {
			input: "@default -- value",
			want:  true,
		},
		"regular comment": {
			input: "This is a regular comment",
			want:  false,
		},
		"old-style helm-docs key path": {
			input: "image.tag -- the image tag",
			want:  true,
		},
		"prose with dotted word before dashes": {
			input: "Use the v1.2 API -- it is stable",
			want:  false,
		},
		"empty string": {
			input: "",
			want:  false,
		},
		"leading whitespace annotation": {
			input: "  @schema type:string",
			want:  true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := magicschema.IsAnnotationComment(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSchemaBlockNotLeakedAsDescription(t *testing.T) {
	t.Parallel()

	// A blank line inside a @schema block splits the head comment run, so the
	// kept run begins mid-block. The block content (annotation data, not prose)
	// must not leak into the structural description even though its opening
	// fence was discarded with the earlier run.
	input := "# @schema\n# type: string\n\n# enum:\n#   - a\n# @schema\nkey: 1\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	key, ok := props["key"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "integer", key["type"])
	assert.NotContains(t, key, "description")
}

func TestArrayElementHeadCommentNotLeakedAsArrayDescription(t *testing.T) {
	t.Parallel()

	// The goccy parser attaches a sequence's first-element head comment to the
	// array value node. It documents the element, not the array, so it must
	// not become the array property's description.
	input := "parent:\n  # item desc\n  - a\n  - b\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	parent, ok := props["parent"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "array", parent["type"])
	assert.NotContains(t, parent, "description")
}

func TestThreeHashSchemaMarkerIsNotAFence(t *testing.T) {
	t.Parallel()

	// "### @schema" (three or more hashes) is not a block fence: the dadav
	// annotator caps marker hashes at two, so it never opens a block here.
	// The structural description path must agree and not treat the line as a
	// fence that swallows the description that follows it.
	input := "### @schema\n# A real description\nkey: 5\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	key, ok := props["key"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "integer", key["type"])
	assert.Equal(t, "A real description", key["description"])
}

func TestInferTypes(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string
	}{
		"boolean true": {
			input: "val: true\n",
			want:  "boolean",
		},
		"boolean false": {
			input: "val: false\n",
			want:  "boolean",
		},
		"integer": {
			input: "val: 42\n",
			want:  "integer",
		},
		"negative integer": {
			input: "val: -5\n",
			want:  "integer",
		},
		"float": {
			input: "val: 3.14\n",
			want:  "number",
		},
		"string": {
			input: "val: hello\n",
			want:  "string",
		},
		"quoted string": {
			input: "val: \"123\"\n",
			want:  "string",
		},
		"array": {
			input: "val:\n  - a\n  - b\n",
			want:  "array",
		},
		"object": {
			input: "val:\n  key: value\n",
			want:  "object",
		},
		"null": {
			input: "val: null\n",
			want:  "",
		},
		"empty": {
			input: "val:\n",
			want:  "",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			if tc.want == "" {
				// No type constraint: the property may be "true" (true schema)
				// or a map without a "type" key.
				val, isMap := props["val"].(map[string]any)
				if isMap {
					assert.Empty(t, val["type"], "expected no type constraint")
				} else {
					assert.Equal(t, true, props["val"], "expected true schema")
				}
			} else {
				val, ok := props["val"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, tc.want, val["type"])
			}
		})
	}
}

func TestInferArrayItems(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input     string
		wantType  string
		wantTypes []any // type union; takes precedence over wantType
		hasItems  bool
	}{
		"string array": {
			input:    "items:\n  - hello\n  - world\n",
			wantType: "string",
			hasItems: true,
		},
		"integer array": {
			input:    "items:\n  - 1\n  - 2\n  - 3\n",
			wantType: "integer",
			hasItems: true,
		},
		"mixed number array": {
			input:    "items:\n  - 1\n  - 2.5\n",
			wantType: "number",
			hasItems: true,
		},
		"mixed incompatible array": {
			input:    "items:\n  - hello\n  - 42\n",
			hasItems: false,
		},
		"empty array": {
			input:    "items: []\n",
			hasItems: false,
		},
		"typed array with null element": {
			input:     "items:\n  - 1\n  - null\n",
			wantTypes: []any{"integer", "null"},
			hasItems:  true,
		},
		"typed array with empty element": {
			input:     "items:\n  - hello\n  -\n",
			wantTypes: []any{"string", "null"},
			hasItems:  true,
		},
		"all-null array": {
			input:    "items:\n  - null\n  - ~\n",
			hasItems: false,
		},
		"mixed incompatible array with null element": {
			input:    "items:\n  - hello\n  - 42\n  - null\n",
			hasItems: false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			items, ok := props["items"].(map[string]any)
			require.True(t, ok)

			switch {
			case len(tc.wantTypes) > 0:
				itemSchema, ok := items["items"].(map[string]any)
				require.True(t, ok, "expected items schema")
				assert.Equal(t, tc.wantTypes, itemSchema["type"])

			case tc.hasItems:
				itemSchema, ok := items["items"].(map[string]any)
				require.True(t, ok, "expected items schema")
				assert.Equal(t, tc.wantType, itemSchema["type"])

			default:
				assert.Nil(t, items["items"])
			}
		})
	}
}

func TestInferEdgeCases(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string // expected type for "val" property, empty means no type
	}{
		"literal block scalar": {
			input: "val: |\n  multi\n  line\n",
			want:  "string",
		},
		"folded block scalar": {
			input: "val: >\n  folded\n  line\n",
			want:  "string",
		},
		"tagged string": {
			// The explicit tag is authoritative over the literal's
			// apparent type, since loaders coerce to the tagged type.
			input: "val: !!str 123\n",
			want:  "string",
		},
		"tagged int": {
			input: "val: !!int \"42\"\n",
			want:  "integer",
		},
		"positive infinity": {
			input: "val: .inf\n",
			want:  "number",
		},
		"negative infinity": {
			input: "val: -.inf\n",
			want:  "number",
		},
		"nan": {
			input: "val: .nan\n",
			want:  "number",
		},
		"empty mapping": {
			input: "val: {}\n",
			want:  "object",
		},
		"empty sequence": {
			input: "val: []\n",
			want:  "array",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			if tc.want == "" {
				val, isMap := props["val"].(map[string]any)
				if isMap {
					assert.Empty(t, val["type"], "expected no type constraint")
				} else {
					assert.Equal(t, true, props["val"], "expected true schema")
				}
			} else {
				val, ok := props["val"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, tc.want, val["type"])
			}
		})
	}
}
