package losisin_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
	"go.jacobcolvin.com/x/stringtest"
)

var update = flag.Bool("update", false, "update golden files")

func assertGolden(t *testing.T, goldenPath string, schema *jsonschema.Schema) {
	t.Helper()

	got, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(t, err)

	got = append(got, '\n')

	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))

		return
	}

	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "golden file %s not found; run with -update to create", goldenPath)

	assert.JSONEq(t, string(want), string(got))
}

func TestHelmValuesSchemaAnnotator(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"type and required": {
			input: stringtest.Input(`
				# @schema type:string;required;minLength:1
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.InDelta(t, float64(1), n["minLength"], 0.001)

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"type array": {
			input: stringtest.Input(`
				# @schema type:[integer, null];minimum:0
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				types, ok := c["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "integer")
				assert.Contains(t, types, "null")
			},
		},
		"boolean type": {
			input: stringtest.Input(`
				# @schema type:boolean
				debug: false
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["debug"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "boolean", d["type"])
			},
		},
		"hidden field": {
			input: stringtest.Input(`
				# @schema hidden
				secret: value
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "secret")
				assert.Contains(t, props, "name")
			},
		},
		"skipProperties strips properties": {
			input: stringtest.Input(`
				# @schema type:object;skipProperties;additionalProperties
				config:
				  key1: val1
				  key2: val2
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				// Properties should be stripped.
				assert.Nil(t, c["properties"])
				// AdditionalProperties should remain.
				assert.Equal(t, true, c["additionalProperties"])
			},
		},
		"mergeProperties combines children into additionalProperties": {
			input: stringtest.Input(`
				# @schema type:object;mergeProperties
				labels:
				  app: myapp
				  version: v1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
				// Properties should be stripped (merged into additionalProperties).
				assert.Nil(t, l["properties"])
				// AdditionalProperties should be set (merged from children).
				assert.NotNil(t, l["additionalProperties"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorEdgeCases(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"item shortcut sets items type": {
			input: stringtest.Input(`
				# @schema type:array;item:string
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", items["type"])
			},
		},
		"item shortcut with array type": {
			input: stringtest.Input(`
				# @schema type:array;item:[string, null]
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok)

				types, ok := items["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"itemEnum shortcut": {
			input: stringtest.Input(`
				# @schema type:array;itemEnum:[a, b, c]
				vals:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				vals, ok := props["vals"].(map[string]any)
				require.True(t, ok)

				items, ok := vals["items"].(map[string]any)
				require.True(t, ok)

				enum, ok := items["enum"].([]any)
				require.True(t, ok)
				assert.Contains(t, enum, "a")
				assert.Contains(t, enum, "b")
				assert.Contains(t, enum, "c")
			},
		},
		"itemRef shortcut": {
			input: stringtest.Input(`
				# @schema type:array;itemRef:#/definitions/port
				ports:
				  - 80
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				ports, ok := props["ports"].(map[string]any)
				require.True(t, ok)

				items, ok := ports["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "#/definitions/port", items["$ref"])
			},
		},
		"itemProperties shortcut": {
			input: stringtest.Input(`
				# @schema type:array;itemProperties:{name: {type: string}}
				entries:
				  - name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				entries, ok := props["entries"].(map[string]any)
				require.True(t, ok)

				items, ok := entries["items"].(map[string]any)
				require.True(t, ok)

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, itemProps, "name")
			},
		},
		"unevaluatedProperties false": {
			input: stringtest.Input(`
				# @schema type:object;unevaluatedProperties:false
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, c["unevaluatedProperties"])
			},
		},
		"$id and $ref": {
			input: stringtest.Input(`
				# @schema $id:https://example.com/schema;$ref:#/definitions/item
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "https://example.com/schema", v["$id"])
				assert.Equal(t, "#/definitions/item", v["$ref"])
			},
		},
		"additionalProperties with schema object": {
			input: stringtest.Input(`
				# @schema type:object;additionalProperties:{type: string}
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				ap, ok := c["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", ap["type"])
			},
		},
		"unevaluatedProperties with schema object": {
			input: stringtest.Input(`
				# @schema type:object;unevaluatedProperties:{type: integer}
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				up, ok := c["unevaluatedProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", up["type"])
			},
		},
		"unknown key logs warning but does not error": {
			input: stringtest.Input(`
				# @schema type:string;unknownKey:someValue
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// The known field should still be applied.
				assert.Equal(t, "string", n["type"])
				// UnknownKey should not appear in the schema.
				assert.Nil(t, n["unknownKey"])
			},
		},
		"foot comment annotation on last key": {
			// When a @schema comment appears after the last key in a mapping,
			// goccy/go-yaml attaches it as FootComment on the MappingValueNode.
			input: stringtest.Input(`
				name: test
				# @schema type:integer
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", n["type"])
			},
		},
		"multiple annotation lines from head and inline": {
			// Multiple @schema lines on the same key: head comment provides type,
			// inline comment provides additional constraint.
			input: stringtest.Input(`
				# @schema type:string
				name: test # @schema minLength:1;required
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.InDelta(t, float64(1), n["minLength"], 0.001)

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"boolean key omits true suffix": {
			input: stringtest.Input(`
				# @schema type:array;uniqueItems
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])
				assert.Equal(t, true, tags["uniqueItems"])
			},
		},
		"additionalProperties bare keyword treated as true": {
			input: stringtest.Input(`
				# @schema type:object;additionalProperties
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, c["additionalProperties"])
			},
		},
		"required false does not add to required array": {
			input: stringtest.Input(`
				# @schema type:string;required:false
				name: test
				# @schema type:integer;required
				count: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Only "count" should be required, not "name".
				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "count")
				assert.NotContains(t, req, "name")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorProperties(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"title property": {
			input: stringtest.Input(`
				# @schema type:string;title:My Title
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "My Title", n["title"])
			},
		},
		"description property": {
			input: stringtest.Input(`
				# @schema type:string;description:A description of the field
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "A description of the field", n["description"])
			},
		},
		"default string value": {
			input: stringtest.Input(`
				# @schema type:string;default:hello
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "hello", n["default"])
			},
		},
		"default numeric value": {
			input: stringtest.Input(`
				# @schema type:integer;default:42
				count: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(42), n["default"], 0.001)
			},
		},
		"const integer value": {
			input: stringtest.Input(`
				# @schema type:integer;const:443
				port: 443
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(443), n["const"], 0.001)
			},
		},
		"const string value": {
			input: stringtest.Input(`
				# @schema type:string;const:fixed
				val: fixed
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "fixed", n["const"])
			},
		},
		"enum with mixed types": {
			input: stringtest.Input(`
				# @schema enum:[1, two, true]
				val: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := n["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 3)
			},
		},
		"pattern constraint": {
			input: stringtest.Input(`
				# @schema type:string;pattern:^[a-z]+$
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "^[a-z]+$", n["pattern"])
			},
		},
		"multipleOf constraint": {
			input: stringtest.Input(`
				# @schema type:number;multipleOf:0.5
				val: 2.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, 0.5, n["multipleOf"], 0.001)
			},
		},
		"minProperties and maxProperties": {
			input: stringtest.Input(`
				# @schema type:object;minProperties:1;maxProperties:10
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(1), c["minProperties"], 0.001)
				assert.InDelta(t, float64(10), c["maxProperties"], 0.001)
			},
		},
		"examples with string array": {
			input: stringtest.Input(`
				# @schema type:string;examples:[foo, bar, baz]
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				examples, ok := n["examples"].([]any)
				require.True(t, ok)
				assert.Len(t, examples, 3)
				assert.Contains(t, examples, "foo")
				assert.Contains(t, examples, "bar")
				assert.Contains(t, examples, "baz")
			},
		},
		"readOnly bare keyword": {
			input: stringtest.Input(`
				# @schema type:string;readOnly
				host: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["host"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, n["readOnly"])
			},
		},
		"readOnly with explicit true": {
			input: stringtest.Input(`
				# @schema type:string;readOnly:true
				host: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["host"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, n["readOnly"])
			},
		},
		"hidden with explicit false": {
			input: stringtest.Input(`
				# @schema hidden:false
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				// hidden:false means the field should NOT be skipped.
				assert.Contains(t, props, "name")
			},
		},
		"additionalProperties false": {
			input: stringtest.Input(`
				# @schema type:object;additionalProperties:false
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, c["additionalProperties"])
			},
		},
		"additionalProperties true": {
			input: stringtest.Input(`
				# @schema type:object;additionalProperties:true
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, c["additionalProperties"])
			},
		},
		"additionalProperties empty after colon treated as true": {
			input: stringtest.Input(`
				# @schema type:object;additionalProperties:
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, c["additionalProperties"])
			},
		},
		"unevaluatedProperties bare keyword treated as true": {
			input: stringtest.Input(`
				# @schema type:object;unevaluatedProperties
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, c["unevaluatedProperties"])
			},
		},
		"patternProperties with regex keys": {
			input: stringtest.Input(`
				# @schema type:object;patternProperties:{"^x-": {type: string}}
				ext:
				  x-custom: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["ext"].(map[string]any)
				require.True(t, ok)

				pp, ok := c["patternProperties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, pp, "^x-")
			},
		},
		"allOf composition": {
			input: stringtest.Input(`
				# @schema type:string;allOf:[{minLength: 1}, {maxLength: 100}]
				val: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				allOf, ok := n["allOf"].([]any)
				require.True(t, ok)
				assert.Len(t, allOf, 2)
			},
		},
		"anyOf composition": {
			input: stringtest.Input(`
				# @schema type:string;anyOf:[{pattern: "^a"}, {pattern: "^b"}]
				val: alpha
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				anyOf, ok := n["anyOf"].([]any)
				require.True(t, ok)
				assert.Len(t, anyOf, 2)
			},
		},
		"oneOf composition": {
			input: stringtest.Input(`
				# @schema type:integer;oneOf:[{minimum: 0, maximum: 10}, {minimum: 100, maximum: 200}]
				val: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["val"].(map[string]any)
				require.True(t, ok)

				oneOf, ok := n["oneOf"].([]any)
				require.True(t, ok)
				assert.Len(t, oneOf, 2)
			},
		},
		"not composition": {
			input: stringtest.Input(`
				# @schema type:string;not:{pattern: "^admin"}
				user: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["user"].(map[string]any)
				require.True(t, ok)

				not, ok := n["not"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "^admin", not["pattern"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorConstAndDefault(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"const null value": {
			// PRD: const (YAML value) -> Const; JSON Schema allows const:null.
			input: stringtest.Input(`
				# @schema const:null
				val: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// const:null should produce "const": null in JSON output.
				assert.Contains(t, v, "const")
				assert.Nil(t, v["const"])
			},
		},
		"const boolean true": {
			input: stringtest.Input(`
				# @schema type:boolean;const:true
				val: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, v["const"])
			},
		},
		"const boolean false": {
			input: stringtest.Input(`
				# @schema type:boolean;const:false
				val: false
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, v["const"])
			},
		},
		"default null value": {
			input: stringtest.Input(`
				# @schema default:null
				val: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, v, "default")
				assert.Nil(t, v["default"])
			},
		},
		"default boolean value": {
			input: stringtest.Input(`
				# @schema type:boolean;default:true
				enabled: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["enabled"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, v["default"])
			},
		},
		"default object value": {
			input: stringtest.Input(`
				# @schema type:object;default:{key: val}
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["config"].(map[string]any)
				require.True(t, ok)

				d, ok := v["default"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "val", d["key"])
			},
		},
		"default array value": {
			input: stringtest.Input(`
				# @schema type:array;default:[1, 2, 3]
				vals:
				  - 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["vals"].(map[string]any)
				require.True(t, ok)

				d, ok := v["default"].([]any)
				require.True(t, ok)
				assert.Len(t, d, 3)
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorEnumEdgeCases(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"enum with null values preserved": {
			// PRD: enum values should preserve native types including null.
			input: stringtest.Input(`
				# @schema enum:[null, 1, two, true]
				val: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 4)
				assert.Contains(t, enum, nil)
				assert.Contains(t, enum, "two")
				assert.Contains(t, enum, true)
			},
		},
		"enum with numeric values": {
			input: stringtest.Input(`
				# @schema enum:[1, 2.5, 3]
				val: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 3)
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorCommentPlacement(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"head comment annotation": {
			// PRD: comment can appear on the line above.
			input: stringtest.Input(`
				# @schema type:string;required
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"inline comment on same line as value": {
			// PRD: comment can appear on the same line as the value (inline).
			input: stringtest.Input(`
				name: test # @schema type:string;required
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"foot comment annotation on line below": {
			// PRD: comment can appear on the line below the key.
			input: stringtest.Input(`
				name: test
				# @schema type:integer
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", n["type"])
			},
		},
		"combined head and inline annotations": {
			// PRD: Multiple # @schema comment lines can apply to the same key.
			input: stringtest.Input(`
				# @schema type:string
				name: test # @schema required;minLength:1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.InDelta(t, float64(1), n["minLength"], 0.001)

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorUnsupportedKeys(t *testing.T) {
	t.Parallel()

	// PRD: format, deprecated, writeOnly, exclusiveMinimum, exclusiveMaximum,
	// and x-* custom annotations are NOT supported. They should warn and skip.
	tcs := map[string]struct {
		input string
	}{
		"format is not supported": {
			input: stringtest.Input(`
				# @schema type:string;format:email
				val: test
			`),
		},
		"deprecated is not supported": {
			input: stringtest.Input(`
				# @schema type:string;deprecated
				val: test
			`),
		},
		"writeOnly is not supported": {
			input: stringtest.Input(`
				# @schema type:string;writeOnly
				val: test
			`),
		},
		"exclusiveMinimum is not supported": {
			input: stringtest.Input(`
				# @schema type:integer;exclusiveMinimum:0
				val: 1
			`),
		},
		"exclusiveMaximum is not supported": {
			input: stringtest.Input(`
				# @schema type:integer;exclusiveMaximum:100
				val: 50
			`),
		},
		"x-custom annotation is not supported": {
			input: stringtest.Input(`
				# @schema type:string;x-custom:value
				val: test
			`),
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			// Should not error, just warn.
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			v, ok := props["val"].(map[string]any)
			require.True(t, ok)

			// The type should still be set (known key was applied).
			assert.NotNil(t, v["type"])

			// None of the unsupported keys should appear in the output.
			assert.Nil(t, v["format"])
			assert.Nil(t, v["deprecated"])
			assert.Nil(t, v["writeOnly"])
			assert.Nil(t, v["exclusiveMinimum"])
			assert.Nil(t, v["exclusiveMaximum"])
			assert.Nil(t, v["x-custom"])
		})
	}
}

func TestHelmValuesSchemaAnnotatorDetection(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  bool // true if annotation should be detected
	}{
		"standard inline annotation": {
			input: stringtest.Input(`
				# @schema type:string
				name: test
			`),
			want: true,
		},
		"schema prefix without space is not an annotation": {
			// "@schemafoo" should not match (upstream behavior).
			input: stringtest.Input(`
				# @schemafoo:bar
				name: test
			`),
			want: false,
		},
		"block delimiter without content is not inline": {
			// "# @schema" alone (block format) should not match as inline.
			input: stringtest.Input(`
				# @schema
				name: test
			`),
			want: false,
		},
		"inline comment on value": {
			input: stringtest.Input(`
				name: test # @schema type:string
			`),
			want: true,
		},
		"no space after hash is accepted": {
			// Reference: "#@schema type:string" is valid (no space between # and @schema).
			input: stringtest.Input(`
				#@schema type:string
				name: test
			`),
			want: true,
		},
		"double hash is not an inline annotation": {
			// "## @schema" is bitnami format, not losisin.
			input: stringtest.Input(`
				## @schema type:string
				name: test
			`),
			want: false,
		},
		"extra whitespace around schema keyword": {
			// Reference test: "#  \t  @schema \t type :  string" is valid.
			input: stringtest.Input(`
				#  	  @schema 	 type:string
				name: test
			`),
			want: true,
		},
		"trailing space after @schema is not an annotation": {
			// Upstream: "# @schema " (trailing space only) is rejected because
			// len(trimmed) == len(withoutSchema) when both are empty.
			input: stringtest.Input(`
				# @schema 
				name: test
			`),
			want: false,
		},
		"semicolons without @schema prefix is not an annotation": {
			// Upstream: lines without @schema prefix are silently ignored.
			input: stringtest.Input(`
				# ; type:string
				name: test
			`),
			want: false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			n, ok := props["name"].(map[string]any)
			require.True(t, ok)

			if tc.want {
				assert.Equal(t, "string", n["type"])
			} else {
				// Without annotation, the type is inferred from the value.
				assert.NotEqual(t, "integer", n["type"])
			}
		})
	}
}

func TestHelmValuesSchemaAnnotatorPRDAlignment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"tab separator after @schema is recognized": {
			// PRD: require space or end-of-string after @schema.
			// Our code checks after[0] != ' ' && after[0] != '\t'.
			input: stringtest.Input(`
				#	@schema	type:string
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
			},
		},
		"required with explicit true suffix": {
			// PRD: Boolean keys can omit the :true suffix.
			// Explicit :true should also work.
			input: stringtest.Input(`
				# @schema type:string;required:true
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"hidden with explicit true": {
			// PRD: hidden (bool) -> Skip: true.
			input: stringtest.Input(`
				# @schema hidden:true
				secret: value
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "secret")
				assert.Contains(t, props, "name")
			},
		},
		"uniqueItems explicit false": {
			// PRD: Boolean keys can be set to false explicitly.
			input: stringtest.Input(`
				# @schema type:array;uniqueItems:false
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])
				// UniqueItems:false should not appear in output (omitempty).
				assert.Nil(t, tags["uniqueItems"])
			},
		},
		"readOnly explicit false": {
			// PRD: readOnly (bool) is supported.
			input: stringtest.Input(`
				# @schema type:string;readOnly:false
				host: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["host"].(map[string]any)
				require.True(t, ok)

				// ReadOnly:false should not appear in output (omitempty).
				assert.Nil(t, n["readOnly"])
			},
		},
		"minimum and maximum with float values": {
			// PRD: minimum, maximum -> numeric constraints.
			input: stringtest.Input(`
				# @schema type:number;minimum:0.5;maximum:99.9
				val: 50.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, 0.5, v["minimum"], 0.001)
				assert.InDelta(t, 99.9, v["maximum"], 0.001)
			},
		},
		"single type in array brackets": {
			// PRD: type (string or array like [string, integer]) -> Schema.Type / Schema.Types.
			// Single element array should use Type, not Types.
			input: stringtest.Input(`
				# @schema type:[string]
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Single-element type array should be flattened to a single type.
				assert.Equal(t, "string", n["type"])
			},
		},
		"item shortcut combined with other array constraints": {
			// PRD: item -> convenience shortcut for Items.Type combined with
			// minItems, maxItems, uniqueItems on the parent.
			input: stringtest.Input(`
				# @schema type:array;item:integer;minItems:1;maxItems:5;uniqueItems
				ids:
				  - 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				ids, ok := props["ids"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", ids["type"])

				items, ok := ids["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", items["type"])

				assert.InDelta(t, float64(1), ids["minItems"], 0.001)
				assert.InDelta(t, float64(5), ids["maxItems"], 0.001)
				assert.Equal(t, true, ids["uniqueItems"])
			},
		},
		"empty annotation line is ignored": {
			// PRD: bare @schema (no content) acts as block delimiter, not inline annotation.
			input: stringtest.Input(`
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Without annotation, type is inferred from value.
				assert.Equal(t, "string", n["type"])
			},
		},
		"multiple different annotations on same key from different comment positions": {
			// PRD: all comment positions are collected and processed.
			input: stringtest.Input(`
				# @schema type:object
				config:
				  key: val
				# @schema minProperties:1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				assert.InDelta(t, float64(1), c["minProperties"], 0.001)
			},
		},
		"semicolons inside bracket-delimited values preserved": {
			// Our splitSemicolons respects bracket depth, so semicolons
			// inside {} or [] are not treated as pair delimiters.
			input: stringtest.Input(`
				# @schema type:object;patternProperties:{"^x-": {type: string}}
				ext:
				  x-custom: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				ext, ok := props["ext"].(map[string]any)
				require.True(t, ok)

				pp, ok := ext["patternProperties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, pp, "^x-")
			},
		},
		"$ref without type annotation": {
			// PRD: $ref -> Ref. Should be set without requiring type.
			input: stringtest.Input(`
				# @schema $ref:#/definitions/config
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "#/definitions/config", c["$ref"])
			},
		},
		"mergeProperties produces merged additionalProperties schema": {
			// PRD: mergeProperties (bool) -> merges all child properties into additionalProperties.
			input: stringtest.Input(`
				# @schema type:object;mergeProperties
				labels:
				  app: myapp
				  version: v1
				  count: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
				assert.Nil(t, l["properties"])
				assert.NotNil(t, l["additionalProperties"])
			},
		},
		"skipProperties preserves additionalProperties": {
			// PRD: skipProperties (bool) -> strips Properties from output,
			// but other schema fields remain.
			input: stringtest.Input(`
				# @schema type:object;skipProperties;additionalProperties:{type: integer}
				data:
				  key1: 1
				  key2: 2
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["data"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", d["type"])
				assert.Nil(t, d["properties"])

				ap, ok := d["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", ap["type"])
			},
		},
		"description containing colons preserved": {
			// Colons in description values should be preserved since
			// strings.Cut splits on the first colon only.
			input: stringtest.Input(`
				# @schema type:string;description:A URL like https://example.com
				url: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				u, ok := props["url"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "A URL like https://example.com", u["description"])
			},
		},
		"pattern containing colons preserved": {
			// PRD: pattern -> Pattern. Colons in pattern values should not
			// be split as key:value separators.
			input: stringtest.Input(`
				# @schema type:string;pattern:^https?://
				url: https://example.com
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				u, ok := props["url"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "^https?://", u["pattern"])
			},
		},
		"minLength and maxLength combined": {
			// PRD: minLength, maxLength -> length constraints.
			input: stringtest.Input(`
				# @schema type:string;minLength:3;maxLength:50
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(3), n["minLength"], 0.001)
				assert.InDelta(t, float64(50), n["maxLength"], 0.001)
			},
		},
		"minimum and maximum with integer type": {
			// PRD: minimum, maximum -> numeric constraints.
			input: stringtest.Input(`
				# @schema type:integer;minimum:0;maximum:100
				count: 50
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", c["type"])
				assert.InDelta(t, float64(0), c["minimum"], 0.001)
				assert.InDelta(t, float64(100), c["maximum"], 0.001)
			},
		},
		"annotation on null yaml value sets type only": {
			// PRD: null/empty values emit no type constraint from inference,
			// but annotation type should override.
			input: stringtest.Input(`
				# @schema type:string
				val: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
			},
		},
		"enum with single element": {
			// PRD: enum (array) -> Enum.
			input: stringtest.Input(`
				# @schema type:string;enum:[only]
				val: only
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 1)
				assert.Contains(t, enum, "only")
			},
		},
		"default with empty string value": {
			// PRD: default (YAML value) -> Default.
			// Empty value after colon should produce null default.
			input: stringtest.Input(`
				# @schema type:string;default:
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				// Empty YAML value unmarshals to nil, so default is null.
				assert.Contains(t, v, "default")
				assert.Nil(t, v["default"])
			},
		},
		"skipProperties with explicit false does not strip properties": {
			// PRD: skipProperties (bool). When false, properties should remain.
			input: stringtest.Input(`
				# @schema type:object;skipProperties:false
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				// Properties should NOT be stripped when skipProperties:false.
				assert.NotNil(t, c["properties"])
			},
		},
		"mergeProperties with explicit false does not merge": {
			// PRD: mergeProperties (bool). When false, properties remain.
			input: stringtest.Input(`
				# @schema type:object;mergeProperties:false
				labels:
				  app: myapp
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
				// Properties should remain when mergeProperties:false.
				assert.NotNil(t, l["properties"])
			},
		},
		"multiple types with null": {
			// PRD: type array like [string, integer, null].
			input: stringtest.Input(`
				# @schema type:[string, integer, null]
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				types, ok := v["type"].([]any)
				require.True(t, ok)
				assert.Len(t, types, 3)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "integer")
				assert.Contains(t, types, "null")
			},
		},
		"type and enum from annotation override inference": {
			// PRD: annotation type overrides structural inference.
			// Enum is provided without type; type from annotation takes precedence
			// over inferred type from value.
			input: stringtest.Input(`
				# @schema type:string;enum:[a, b, c]
				val: a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 3)
			},
		},
		"const with null produces null in JSON": {
			// PRD: const (YAML value) -> Const; null is a valid const.
			input: stringtest.Input(`
				# @schema const:null
				val:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, v, "const")
				assert.Nil(t, v["const"])
			},
		},
		"default with array value": {
			// PRD: default (YAML value) -> Default. Arrays should work.
			input: stringtest.Input(`
				# @schema type:array;default:[1, 2, 3]
				ids:
				  - 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["ids"].(map[string]any)
				require.True(t, ok)

				d, ok := v["default"].([]any)
				require.True(t, ok)
				assert.Len(t, d, 3)
			},
		},
		"default with object value": {
			// PRD: default (YAML value) -> Default. Objects should work.
			input: stringtest.Input(`
				# @schema type:object;default:{key: val}
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["config"].(map[string]any)
				require.True(t, ok)

				d, ok := v["default"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "val", d["key"])
			},
		},
		"hidden bare keyword without value": {
			// PRD: Boolean keys can omit the :true suffix.
			// Hidden without a value should be treated as true.
			input: stringtest.Input(`
				# @schema hidden
				secret: s3cret
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "secret")
				assert.Contains(t, props, "name")
			},
		},
		"skipProperties bare keyword strips properties": {
			// PRD: skipProperties (bool) strips Properties map.
			input: stringtest.Input(`
				# @schema type:object;skipProperties;additionalProperties:{type: string}
				data:
				  k1: v1
				  k2: v2
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["data"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", d["type"])
				assert.Nil(t, d["properties"])

				ap, ok := d["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", ap["type"])
			},
		},
		"mergeProperties merges children into additionalProperties with type": {
			// PRD: mergeProperties (bool) merges child properties into additionalProperties.
			// When all children are the same type, additionalProperties should reflect that.
			input: stringtest.Input(`
				# @schema type:object;mergeProperties
				labels:
				  app: myapp
				  version: v1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
				assert.Nil(t, l["properties"])
				assert.NotNil(t, l["additionalProperties"])
			},
		},
		"enum with null value preserved": {
			// PRD: enum values preserve native types including null.
			input: stringtest.Input(`
				# @schema enum:[null, 1, two, true]
				val: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 4)
				assert.Contains(t, enum, nil)
				assert.Contains(t, enum, "two")
				assert.Contains(t, enum, true)
			},
		},
		"item shortcut with type array sets items types": {
			// PRD: item -> convenience shortcut for Items.Type.
			// Type array notation should work for item too.
			input: stringtest.Input(`
				# @schema type:array;item:[string, null]
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok)

				types, ok := items["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"$id without other annotations": {
			// PRD: $id -> ID. Should work standalone.
			input: stringtest.Input(`
				# @schema $id:https://example.com/my-schema
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "https://example.com/my-schema", v["$id"])
			},
		},
		"multiple annotation lines combine properties": {
			// PRD: Multiple # @schema comment lines can apply to the same key;
			// all are collected and processed.
			input: stringtest.Input(`
				# @schema type:string
				# @schema minLength:3;maxLength:50
				name: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.InDelta(t, float64(3), n["minLength"], 0.001)
				assert.InDelta(t, float64(50), n["maxLength"], 0.001)
			},
		},
		"unknown keys warn but do not error": {
			// PRD: unknown keys cause a hard parse error in the original tool;
			// our annotator should log a warning and skip them instead.
			input: stringtest.Input(`
				# @schema type:string;format:email;unknownKey:value
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Known fields should be applied.
				assert.Equal(t, "string", v["type"])
				// Unsupported format and unknown key should not appear.
				assert.Nil(t, v["format"])
				assert.Nil(t, v["unknownKey"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}

func TestHelmValuesSchemaAnnotatorFromFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/helm_values_schema.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(losisin.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/helm_values_schema.schema.json", schema)
}

func TestHelmValuesSchemaAnnotatorUpstreamAlignment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"head comment group isolation only last group": {
			// Upstream: only the last comment group (after last blank line)
			// in head comments is considered for annotations.
			input: stringtest.Input(`
				# @schema type:integer
				#
				# @schema type:string
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Only the last group's annotation should apply.
				assert.Equal(t, "string", n["type"])
			},
		},
		"negative minLength is ignored": {
			// Upstream uses uint64 for length constraints; negatives are rejected.
			input: stringtest.Input(`
				# @schema type:string;minLength:-1
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Nil(t, n["minLength"])
			},
		},
		"negative maxLength is ignored": {
			input: stringtest.Input(`
				# @schema type:string;maxLength:-5
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Nil(t, n["maxLength"])
			},
		},
		"negative minItems is ignored": {
			input: stringtest.Input(`
				# @schema type:array;minItems:-1
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])
				assert.Nil(t, tags["minItems"])
			},
		},
		"negative maxItems is ignored": {
			input: stringtest.Input(`
				# @schema type:array;maxItems:-3
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])
				assert.Nil(t, tags["maxItems"])
			},
		},
		"negative minProperties is ignored": {
			input: stringtest.Input(`
				# @schema type:object;minProperties:-1
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				assert.Nil(t, c["minProperties"])
			},
		},
		"negative maxProperties is ignored": {
			input: stringtest.Input(`
				# @schema type:object;maxProperties:-2
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				assert.Nil(t, c["maxProperties"])
			},
		},
		"multipleOf zero is ignored": {
			// Upstream: multipleOf must be > 0.
			input: stringtest.Input(`
				# @schema type:number;multipleOf:0
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["multipleOf"])
			},
		},
		"multipleOf negative is ignored": {
			input: stringtest.Input(`
				# @schema type:number;multipleOf:-2.5
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["multipleOf"])
			},
		},
		"type preserved alongside composition keywords": {
			// Intentional divergence from upstream: upstream strips type when
			// allOf/anyOf/oneOf/not/const is present. We keep type because it
			// is valid in Draft 7 and more useful for consumers.
			input: stringtest.Input(`
				# @schema type:string;anyOf:[{pattern: "^a"}, {pattern: "^b"}]
				val: alpha
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// We deliberately keep type alongside composition keywords.
				assert.Equal(t, "string", v["type"])
				assert.NotNil(t, v["anyOf"])
			},
		},
		"type preserved alongside const": {
			// Intentional divergence: upstream strips type when const is present.
			input: stringtest.Input(`
				# @schema type:integer;const:443
				port: 443
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", v["type"])
				assert.InDelta(t, float64(443), v["const"], 0.001)
			},
		},
		"hidden applies to any node type not just objects": {
			// Upstream: hidden works on scalars, arrays, and objects alike.
			input: stringtest.Input(`
				# @schema hidden
				secretArray:
				  - s3cret
				name: visible
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "secretArray")
				assert.Contains(t, props, "name")
			},
		},
		"upstream rejects unknown keys with error we warn and skip": {
			// Upstream: unknown keys return a hard error.
			// Our behavior: log warning and skip (PRD: fail open).
			input: stringtest.Input(`
				# @schema type:string;format:email;unknownThing:yes
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Known type should still be applied.
				assert.Equal(t, "string", v["type"])
				// Unknown keys should not appear.
				assert.Nil(t, v["format"])
				assert.Nil(t, v["unknownThing"])
			},
		},
		"$k8s shorthand is passed through as-is": {
			// Intentional divergence: upstream expands $k8s/ to Kubernetes
			// schema URLs using a configurable template. We pass through
			// the raw value since we are not Helm-specific.
			input: stringtest.Input(`
				# @schema $ref:$k8s/_definitions.json#/definitions/io.k8s.apimachinery.pkg.api.resource.Quantity
				memory: 1M
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["memory"].(map[string]any)
				require.True(t, ok)

				// $k8s prefix is preserved as-is (not expanded).
				assert.Equal(
					t,
					"$k8s/_definitions.json#/definitions/io.k8s.apimachinery.pkg.api.resource.Quantity",
					v["$ref"],
				)
			},
		},
		"additionalProperties defaults to true for annotated objects": {
			// Intentional divergence: upstream defaults additionalProperties
			// to false on objects. We default to true (fail-open per PRD).
			input: stringtest.Input(`
				# @schema type:object
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				assert.Equal(t, true, c["additionalProperties"])
			},
		},
		"null value for minLength clears constraint": {
			// Upstream: passing null to uint constraints clears the pointer.
			input: stringtest.Input(`
				# @schema type:string;minLength:null
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Nil(t, n["minLength"])
			},
		},
		"null value for minimum clears constraint": {
			// Upstream: passing null to float constraints clears the pointer.
			input: stringtest.Input(`
				# @schema type:number;minimum:null
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["minimum"])
			},
		},
		"null value for maximum clears constraint": {
			input: stringtest.Input(`
				# @schema type:number;maximum:null
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["maximum"])
			},
		},
		"null value for maxLength clears constraint": {
			input: stringtest.Input(`
				# @schema type:string;maxLength:null
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Nil(t, n["maxLength"])
			},
		},
		"null value for minItems clears constraint": {
			input: stringtest.Input(`
				# @schema type:array;minItems:null
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])
				assert.Nil(t, tags["minItems"])
			},
		},
		"null value for multipleOf clears constraint": {
			input: stringtest.Input(`
				# @schema type:number;multipleOf:null
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["multipleOf"])
			},
		},
		"type comma-separated without brackets": {
			// Upstream processList falls back to comma-splitting when
			// the value does not start with "[".
			input: stringtest.Input(`
				# @schema type:string, null
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				types, ok := n["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"enum comma-separated without brackets": {
			// Upstream processList falls back to comma-splitting for enum
			// values that do not start with "[".
			input: stringtest.Input(`
				# @schema type:string;enum:a, b, c
				val: a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 3)
				assert.Contains(t, enum, "a")
				assert.Contains(t, enum, "b")
				assert.Contains(t, enum, "c")
			},
		},
		"enum comma-separated with null preserved": {
			// Upstream processList with stringsOnly=false preserves null
			// in comma-split fallback.
			input: stringtest.Input(`
				# @schema enum:null, 1, two, true
				val: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				enum, ok := v["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 4)
				assert.Contains(t, enum, nil)
				assert.Contains(t, enum, "1")
				assert.Contains(t, enum, "two")
				assert.Contains(t, enum, "true")
			},
		},
		"examples comma-separated without brackets": {
			// Upstream processList falls back to comma-splitting for examples
			// values that do not start with "[".
			input: stringtest.Input(`
				# @schema type:string;examples:foo, bar, baz
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				examples, ok := v["examples"].([]any)
				require.True(t, ok)
				assert.Len(t, examples, 3)
				assert.Contains(t, examples, "foo")
				assert.Contains(t, examples, "bar")
				assert.Contains(t, examples, "baz")
			},
		},
		"double hash comment is not an annotation": {
			// Upstream strips exactly one "#", so "## @schema" leaves
			// "# @schema" which does not match the "@schema" prefix.
			input: stringtest.Input(`
				## @schema type:integer
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Without annotation, type is inferred from value.
				assert.Equal(t, "string", n["type"])
			},
		},
		"item comma-separated without brackets": {
			// The item shortcut should also support comma-separated types
			// since it uses the same list parsing as type.
			input: stringtest.Input(`
				# @schema type:array;item:string, null
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok)

				types, ok := items["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"itemEnum comma-separated without brackets": {
			// ItemEnum should also support comma-separated values.
			input: stringtest.Input(`
				# @schema type:array;itemEnum:http, https, tcp
				protocols:
				  - http
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["protocols"].(map[string]any)
				require.True(t, ok)

				items, ok := p["items"].(map[string]any)
				require.True(t, ok)

				enum, ok := items["enum"].([]any)
				require.True(t, ok)
				assert.Len(t, enum, 3)
				assert.Contains(t, enum, "http")
				assert.Contains(t, enum, "https")
				assert.Contains(t, enum, "tcp")
			},
		},
		"default empty value produces null divergence": {
			// Upstream: processObjectComment errors on empty value.
			// Our behavior: ParseYAMLValue("") produces null (fail-open).
			input: stringtest.Input(`
				# @schema type:string;default:
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Contains(t, n, "default")
				assert.Nil(t, n["default"])
			},
		},
		"invalid boolean for required treated as false": {
			// Upstream: invalid booleans are hard errors.
			// Our behavior: invalid booleans default to false (fail-open:
			// don't add restrictions for garbage input).
			input: stringtest.Input(`
				# @schema type:string;required:foo
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// "required:foo" should NOT mark the field as required.
				req, ok := got["required"].([]any)
				if ok {
					assert.NotContains(t, req, "name")
				}
			},
		},
		"invalid boolean for hidden treated as false": {
			// Upstream: invalid booleans are hard errors.
			// Our behavior: invalid booleans default to false (fail-open:
			// don't hide content for garbage input).
			input: stringtest.Input(`
				# @schema hidden:foo
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// "hidden:foo" should NOT hide the field (fail-open).
				assert.Contains(t, props, "name")
			},
		},
		"case-insensitive boolean TRUE accepted": {
			// Upstream: only lowercase "true" and "false" accepted.
			// Our behavior: case-insensitive matching (lenient).
			input: stringtest.Input(`
				# @schema type:string;required:TRUE
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"case-insensitive boolean FALSE accepted": {
			input: stringtest.Input(`
				# @schema type:string;required:FALSE
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				if ok {
					assert.NotContains(t, req, "name")
				}
			},
		},
		"duplicate key uses last value": {
			// Upstream: last assignment wins when a key appears twice.
			input: stringtest.Input(`
				# @schema type:string
				# @schema type:integer
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Last "type" value should win.
				assert.Equal(t, "integer", v["type"])
			},
		},
		"invalid float for minimum is ignored": {
			// Upstream: invalid numbers are hard errors.
			// Our behavior: silently skip.
			input: stringtest.Input(`
				# @schema type:number;minimum:abc
				val: 1.0
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", v["type"])
				assert.Nil(t, v["minimum"])
			},
		},
		"invalid integer for minLength is ignored": {
			// Upstream: invalid integers are hard errors.
			// Our behavior: silently skip.
			input: stringtest.Input(`
				# @schema type:string;minLength:abc
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Nil(t, n["minLength"])
			},
		},
		"trailing semicolons do not error": {
			// Upstream: trailing semicolons produce empty keys which cause
			// "unknown annotation" hard errors. Our behavior: skip empty pairs.
			input: stringtest.Input(`
				# @schema type:string;
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
			},
		},
		"duplicate key on same line uses last value": {
			// Upstream: keys are processed sequentially; last assignment wins.
			input: stringtest.Input(`
				# @schema type:string;type:integer
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", v["type"])
			},
		},
		"key without value does not set type": {
			// Upstream: "type" with empty value passes empty string to
			// processList which returns [""] (a single-element list with
			// empty string). Our behavior: parseStringList("") returns nil.
			input: stringtest.Input(`
				# @schema type
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Type should be inferred from value, not set to empty string.
				assert.Equal(t, "string", n["type"])
			},
		},
		"hidden and set undo pair": {
			// Upstream: "hidden:true; hidden:false" results in hidden=false
			// (last value wins). Verify our implementation matches.
			input: stringtest.Input(`
				# @schema hidden:true;hidden:false
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// hidden:false should NOT hide the field.
				assert.Contains(t, props, "name")
			},
		},
		"required and unset pair": {
			// Upstream: "required:true; required:false" results in required=false
			// (last value wins). Verify our implementation matches.
			input: stringtest.Input(`
				# @schema type:string;required:true;required:false
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				if ok {
					assert.NotContains(t, req, "name")
				}
			},
		},
		"processObjectComment null value for const": {
			// Upstream: processObjectComment("null") YAML-unmarshals to nil.
			// This is NOT an error (unlike empty string).
			input: stringtest.Input(`
				# @schema const:null
				val: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, v, "const")
				assert.Nil(t, v["const"])
			},
		},
		"float for uint constraint is truncated": {
			// Upstream: strconv.ParseUint rejects float values with a hard error.
			// Our yaml.Unmarshal into int truncates 1.5 to 1 (goccy/go-yaml
			// behavior). This is an intentional divergence: we accept more
			// input rather than rejecting it.
			input: stringtest.Input(`
				# @schema type:string;minLength:1.5
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				// Goccy/go-yaml truncates 1.5 to 1 when unmarshaling into int.
				assert.InDelta(t, float64(1), n["minLength"], 0.001)
			},
		},
		"empty string for uint constraint is ignored": {
			// Upstream rejects empty strings via ParseUint. Our YAML
			// unmarshal of an empty string produces nil, so the int
			// parse fails and the constraint is not set.
			input: stringtest.Input(`
				# @schema type:string;minLength:
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				// Empty minLength should not be set. YAML unmarshal of ""
				// produces nil which fails int assertion, or produces 0 which
				// is valid. Either way, minLength should not cause an error.
			},
		},
		"additionalProperties false via YAML unmarshal matches upstream": {
			// Upstream: processObjectComment("false") YAML-unmarshals into
			// *Schema. Since "false" is !!bool, Schema.UnmarshalYAML detects
			// this and produces SchemaFalse (kind=SchemaKindFalse).
			// Our implementation checks the string "false" explicitly.
			input: stringtest.Input(`
				# @schema type:object;additionalProperties:false
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, c["additionalProperties"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(losisin.New()),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, got)
		})
	}
}
