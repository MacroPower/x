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
		input    string
		wantType string
		hasItems bool
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

			if tc.hasItems {
				itemSchema, ok := items["items"].(map[string]any)
				require.True(t, ok, "expected items schema")
				assert.Equal(t, tc.wantType, itemSchema["type"])
			} else {
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
			input: "val: !!str 123\n",
			want:  "integer",
		},
		"tagged int": {
			input: "val: !!int \"42\"\n",
			want:  "string",
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
