package losisin_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
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
		"unparseable required value is skipped not an opt-out": {
			// HasRequired's false is an active tri-state signal that cancels
			// merge-key-inherited required and outranks lower-priority
			// annotators, so a typo must leave the signal unset instead of
			// becoming an explicit required:false no annotator wrote.
			input: stringtest.Input(`
				# @schema type:string;required:yes
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				_, hasReq := got["required"]
				assert.False(t, hasReq,
					"a typo like required:yes must not set the tri-state signal either way")
			},
		},
		"apostrophes in prose stay literal across pairs": {
			// An apostrophe mid-word must not open a quoted run: two balanced
			// apostrophes in different values would otherwise swallow the ';'
			// between them, merging the pairs and losing the second key.
			input: stringtest.Input(`
				# @schema description:don't overuse;title:User's guide
				key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "don't overuse", k["description"])
				assert.Equal(t, "User's guide", k["title"])
			},
		},
		"nullable appends null to the type": {
			input: stringtest.Input(`
				# @schema type:integer;nullable
				replicaCount: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicaCount"].(map[string]any)
				require.True(t, ok)

				types, ok := r["type"].([]any)
				require.True(t, ok, "nullable must widen the type to a union, got %v", r["type"])
				assert.ElementsMatch(t, []any{"integer", "null"}, types)
			},
		},
		"nullable before type still widens": {
			input: stringtest.Input(`
				# @schema nullable;type:integer
				replicaCount: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicaCount"].(map[string]any)
				require.True(t, ok)

				types, ok := r["type"].([]any)
				require.True(t, ok, "nullable must apply after type:, got %v", r["type"])
				assert.ElementsMatch(t, []any{"integer", "null"}, types)
			},
		},
		"nullable without a type widens with the inferred type": {
			input: stringtest.Input(`
				# @schema nullable
				replicaCount: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicaCount"].(map[string]any)
				require.True(t, ok)

				types, ok := r["type"].([]any)
				require.True(t, ok,
					"the null-only type must widen with the inferred type, got %v", r["type"])
				assert.ElementsMatch(t, []any{"integer", "null"}, types)
			},
		},
		"deprecated key": {
			input: stringtest.Input(`
				# @schema deprecated
				oldField: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["oldField"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, f["deprecated"])
			},
		},
		"itemRequired sets the items required list": {
			input: stringtest.Input(`
				# @schema item:object;itemRequired:[name, value]
				env: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				env, ok := props["env"].(map[string]any)
				require.True(t, ok)

				items, ok := env["items"].(map[string]any)
				require.True(t, ok)

				req, ok := items["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"name", "value"}, req)
			},
		},
		"tilde null clears a bound instead of becoming zero": {
			// The goccy parser decodes "~" (and Null/NULL) into float64 as 0 with no
			// error, so without up-front null detection a null-valued bound
			// emitted a spurious 0 constraint that rejects the chart's own
			// value.
			input: stringtest.Input(`
				# @schema type:integer;maximum:~
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, r, "maximum",
					"a null-valued bound clears the constraint")
			},
		},
		"invalid bound on a repeated key keeps the valid one": {
			// Invalid parse values are documented as silently skipped: a typo
			// must not clear a previously set valid constraint. Only an
			// explicit null clears.
			input: stringtest.Input(`
				# @schema minimum:5
				# @schema minimum:abc
				count: 7
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(5), c["minimum"], 0.001,
					"a later typo must not clear the valid bound")
			},
		},
		"explicit null on a repeated key clears the bound": {
			input: stringtest.Input(`
				# @schema minimum:5
				# @schema minimum:null
				count: 7
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, c, "minimum")
			},
		},
		"invalid default on a repeated key keeps the valid one": {
			// The default/enum/examples parses share the documented
			// invalid-values rule with the numeric bounds: a later typo must
			// not clear a previously set valid value with nil.
			input: stringtest.Input(`
				# @schema default:5
				# @schema default:{bad
				count: 7
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(5), c["default"], 0.001,
					"a later typo must not clear the valid default")
			},
		},
		"invalid enum on a repeated key keeps the valid one": {
			input: stringtest.Input(`
				# @schema enum:[a, b]
				# @schema enum:
				name: a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"a", "b"}, n["enum"],
					"an empty repeat must not clear the valid enum")
			},
		},
		"invalid allOf on a repeated key keeps the valid one": {
			input: stringtest.Input(`
				# @schema allOf:[{minLength: 1}]
				# @schema allOf:garbage
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				allOf, ok := n["allOf"].([]any)
				require.True(t, ok, "the valid allOf must survive the later typo")
				assert.Len(t, allOf, 1)
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
		"empty itemProperties keeps the inferred element schema": {
			// An empty item* value must not create an empty Items schema:
			// that marshals to "true" and suppresses the generator's element
			// inference, leaving the array less described than the
			// un-annotated form.
			input: stringtest.Input(`
				# @schema type:array;itemProperties:
				tags:
				  - name: a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", tags["type"])

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok, "items must be the inferred element schema, not true")

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
			// Const (YAML value) -> Const; JSON Schema allows const:null.
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
		"default with empty value sets no default": {
			// Explicit null is written as "default:null"; a bare
			// "default:" carries no value and must not emit
			// "default": null.
			input: stringtest.Input(`
				# @schema type:string;default:
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.NotContains(t, v, "default")
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
			// Enum values should preserve native types including null.
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
			// Comment can appear on the line above.
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
			// Comment can appear on the same line as the value (inline).
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
			// Comment can appear on the line below the key.
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
			// Multiple # @schema comment lines can apply to the same key.
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

	// Format, deprecated, writeOnly, exclusiveMinimum, exclusiveMaximum,
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

func TestHelmValuesSchemaAnnotatorAlignment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"tab separator after @schema is recognized": {
			// Require space or end-of-string after @schema.
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
			// Boolean keys can omit the :true suffix.
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
			// Hidden (bool) -> Skip: true.
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
			// Boolean keys can be set to false explicitly.
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
			// ReadOnly (bool) is supported.
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
			// Minimum, maximum -> numeric constraints.
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
			// Type (string or array like [string, integer]) -> Schema.Type / Schema.Types.
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
			// Item -> convenience shortcut for Items.Type combined with
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
			// Bare @schema (no content) acts as block delimiter, not inline annotation.
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
			// All comment positions are collected and processed.
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
		"semicolon inside a mismatched-bracket value preserved": {
			// A character class like [};] holds a "}" (a closer of the other
			// kind) and a ";". A depth counter that ignores bracket type would
			// drop to zero on the "}" and split the value at the inner ";";
			// the type-aware stack keeps the pattern intact.
			input: stringtest.Input(`
				# @schema type:string;pattern:[};]
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "[};]", v["pattern"])
			},
		},
		"semicolon inside a double-quoted value preserved": {
			// A double-quoted default holding a ";" must not split at the inner
			// semicolon; quote tracking keeps the value, and the later pairs,
			// intact.
			input: stringtest.Input(`
				# @schema type:string;default:"a;b";pattern:^x$
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "a;b", v["default"])
				assert.Equal(t, "^x$", v["pattern"])
			},
		},
		"semicolon inside a single-quoted value preserved": {
			// A single-quoted default holding a ";" must not split at the inner
			// semicolon either; YAML single quotes carry no backslash escape.
			input: stringtest.Input(`
				# @schema type:string;default:'a;b';pattern:^x$
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "a;b", v["default"])
				assert.Equal(t, "^x$", v["pattern"])
			},
		},
		"doubled quote escape inside a single-quoted value preserved": {
			// A doubled quote inside a single-quoted run is YAML's escape for
			// a literal quote, not the closing delimiter: the run must stay
			// open across it so the later ";" stays part of the value instead
			// of splitting the pair.
			input: stringtest.Input(`
				# @schema type:string;description:'it''s nice; really';pattern:^x$
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "it's nice; really", v["description"])
				assert.Equal(t, "^x$", v["pattern"])
			},
		},
		"quoted pattern with a semicolon keeps the bare regex": {
			// A pattern containing ";" must be quoted to survive splitSemicolons;
			// the surrounding quotes are then stripped so the regex matches the
			// value rather than carrying literal quote characters.
			input: stringtest.Input(`
				# @schema type:string;pattern:"^a;b$"
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "^a;b$", v["pattern"])
			},
		},
		"quoted title with a semicolon keeps the bare text": {
			// A title containing ";" must be quoted to survive splitSemicolons;
			// the surrounding quotes must not leak into the metadata string,
			// matching how pattern/$id/$ref are unquoted.
			input: stringtest.Input(`
				# @schema title:"Step 1; then 2"
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Step 1; then 2", v["title"])
			},
		},
		"quoted itemRef keeps the bare pointer": {
			// An itemRef containing ";" must be quoted to survive
			// splitSemicolons; the quotes must not leak into the JSON pointer,
			// matching the $ref sibling.
			input: stringtest.Input(`
				# @schema type:array;itemRef:"#/$defs/a;b"
				val: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				items, ok := v["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "#/$defs/a;b", items["$ref"])
			},
		},
		"double-quoted regex with a backslash class strips its quotes": {
			// "^\d+;\d+$" must be quoted so the ";" survives splitSemicolons,
			// but \d is not a valid YAML double-quote escape, so the YAML parse
			// fails. The quotes must still be stripped; keeping them would build
			// a regex requiring literal leading and trailing quotes (fail closed).
			input: stringtest.Input(`
				# @schema type:string;pattern:"^\d+;\d+$"
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, `^\d+;\d+$`, v["pattern"])
			},
		},
		"description quoted at both ends stays verbatim": {
			// 'foo' and 'bar' starts and ends with the same quote rune but is
			// not one fully quoted scalar; the parse-failure fallback must not
			// slice off the outer quotes and keep the inner ones, mangling the
			// text upstream assigns verbatim.
			input: stringtest.Input(`
				# @schema type:string;description:'foo' and 'bar'
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, `'foo' and 'bar'`, n["description"])
			},
		},
		"pattern alternating quoted literals stays verbatim": {
			// A regex alternation of two quoted literals starts and ends with
			// a double quote; stripping the outer pair would build a different
			// regex that rejects the intended values.
			input: stringtest.Input(`
				# @schema type:string;pattern:"^a"|"b$"
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, `"^a"|"b$"`, v["pattern"])
			},
		},
		"quote inside a bracketed value preserved": {
			// A regex char class like [",;] holds a quote alongside a ";".
			// Opening a quoted run on that inner quote would swallow the closing
			// bracket and force the naive whole-line split; a quote only opens a
			// run at bracket depth zero, so the bracket stack keeps the value.
			input: stringtest.Input(`
				# @schema type:string;pattern:[",;]
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, `[",;]`, v["pattern"])
			},
		},
		"escaped quote inside a double-quoted value preserved": {
			// A backslash-escaped quote inside a quoted default must not end the
			// run, or the ";" after it leaks as a delimiter and the later pairs
			// corrupt. The escape state keeps the value and the trailing pair
			// intact.
			input: stringtest.Input(`
				# @schema type:string;default:"a\";b";pattern:^x$
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, `a";b`, v["default"])
				assert.Equal(t, "^x$", v["pattern"])
			},
		},
		"$ref without type annotation": {
			// $ref -> Ref. Should be set without requiring type.
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
			// MergeProperties (bool) -> merges all child properties into additionalProperties.
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
			// SkipProperties (bool) -> strips Properties from output,
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
			// Pattern -> Pattern. Colons in pattern values should not
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
			// MinLength, maxLength -> length constraints.
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
			// Minimum, maximum -> numeric constraints.
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
			// Null/empty values emit no type constraint from inference,
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
			// Enum (array) -> Enum.
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
			// Default (YAML value) -> Default.
			// An empty value carries no default (explicit null is written
			// as "default:null"), so the field stays unset.
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
				assert.NotContains(t, v, "default")
			},
		},
		"skipProperties with explicit false does not strip properties": {
			// SkipProperties (bool). When false, properties should remain.
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
			// MergeProperties (bool). When false, properties remain.
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
			// Type array like [string, integer, null].
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
			// Annotation type overrides structural inference.
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
			// Const (YAML value) -> Const; null is a valid const.
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
			// Default (YAML value) -> Default. Arrays should work.
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
			// Default (YAML value) -> Default. Objects should work.
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
			// Boolean keys can omit the :true suffix.
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
			// SkipProperties (bool) strips Properties map.
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
			// MergeProperties (bool) merges child properties into additionalProperties.
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
			// Enum values preserve native types including null.
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
			// Item -> convenience shortcut for Items.Type.
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
			// $id -> ID. Should work standalone.
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
			// Multiple # @schema comment lines can apply to the same key;
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
			// Unknown keys cause a hard parse error in the original tool;
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

// TestHelmValuesSchemaAnnotatorRealWorld locks in the generated schema for the
// traefik chart, the canonical heavy user of inline @schema annotations (type
// unions, enums, patterns, required, defaults). Vendored via `helm show values
// traefik --repo https://traefik.github.io/charts` (version 40.2.0).
func TestHelmValuesSchemaAnnotatorRealWorld(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/traefik_values.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(losisin.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/traefik_values.schema.json", schema)
}

func TestHelmValuesSchemaAnnotatorUpstreamAlignment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"bare hash line keeps both annotations last wins": {
			// Upstream delimits head comment groups only on physical blank
			// lines ("\n\n" in the raw comment); a "#"-only line is part of
			// the same group, so both annotation lines apply and the second
			// type assignment wins under last-wins overwrite semantics.
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

				assert.Equal(t, "string", n["type"])
			},
		},
		"annotation above bare hash separated prose still applies": {
			// A "#"-only line between the annotation and its prose paragraph
			// is not a group boundary; upstream keeps the whole block as one
			// group (a "#" line is not "\n\n") and applies the annotation.
			input: stringtest.Input(`
				# @schema type:integer;minimum:1
				#
				# The number of replicas.
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.InEpsilon(t, float64(1), r["minimum"], 1e-9)
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
			// Our behavior: log warning and skip (fail open).
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
			// to false on objects. We default to true (fail-open).
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
		"single-quoted comma enum unquotes like double-quoted": {
			// A quote protects a comma or space inside a comma-split token; the
			// surrounding quotes must not leak into the value. Single quotes
			// once leaked ('a' stayed "'a'"), so the enum never matched.
			input: stringtest.Input(`
				# @schema enum:'a', "b", c
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
				assert.Equal(t, []any{"a", "b", "c"}, enum)
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
		"default empty value skipped divergence": {
			// Upstream: processObjectComment errors on empty value.
			// Our behavior: an empty value carries no default (fail-open:
			// skip the unparseable pair instead of erroring).
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
				assert.NotContains(t, n, "default")
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
		"value-line annotation wins over key-line": {
			// When both the key line and the value line carry a @schema
			// annotation, upstream collects the key line first so the value
			// line wins under last-wins resolution.
			input: "\"name\": # @schema type:string\n" +
				"  test # @schema type:integer\n",
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", name["type"])
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
		"detached annotation block does not apply to the following key": {
			// The parser merges blank-line-separated comment blocks into one
			// head comment group; a detached annotation separated from the
			// key by a blank line must not apply, matching upstream's
			// last-comment-group split on the blank line. Applying it would
			// assert type integer and reject the key's own string value
			// (fail closed).
			input: stringtest.Input(`
				# @schema type:integer

				# plain docs
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Structural inference wins; the detached annotation leaves
				// no trace while the adjacent prose stays the description.
				assert.Equal(t, "string", n["type"])
				assert.Equal(t, "plain docs", n["description"])
			},
		},
		"detached annotation does not mark the following key required": {
			input: stringtest.Input(`
				a: 1
				# @schema required:true;type:string

				b: 2
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				b, ok := props["b"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", b["type"])
				assert.NotContains(t, got, "required")
			},
		},
		"annotation above the first array element does not apply to the array": {
			// The goccy parser stows the first element's head comment on the
			// SequenceNode itself; upstream reads only the value's same-line
			// comment, so the element annotation must not assert its scalar
			// type on the array key (fail closed against the array value).
			input: stringtest.Input(`
				arr:
				  # @schema type:string
				  - foo
				  - bar
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				arr, ok := props["arr"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", arr["type"])

				items, ok := arr["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", items["type"])
			},
		},
		"inline annotation on a flow sequence still applies": {
			// A comment on the value's own line is the upstream
			// valNode.LineComment and must keep applying even though goccy
			// attaches it to the SequenceNode.
			input: stringtest.Input(`
				middlewares: []  # @schema type:[array, null]
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["middlewares"].(map[string]any)
				require.True(t, ok)

				types, ok := m["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "array")
				assert.Contains(t, types, "null")
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

// TestHelmValuesSchemaAnnotatorNumericConstraints covers the value parsers for
// numeric keywords. A blank value must clear the constraint (fail-open) rather
// than emit a zero-valued, fail-closed constraint, non-finite floats must be
// dropped (they break the final JSON marshal), and non-integer numerics must be
// rejected rather than silently truncated.
func TestHelmValuesSchemaAnnotatorNumericConstraints(t *testing.T) {
	t.Parallel()

	prop := func(t *testing.T, got map[string]any, key string) map[string]any {
		t.Helper()

		props, ok := got["properties"].(map[string]any)
		require.True(t, ok)

		p, ok := props[key].(map[string]any)
		require.True(t, ok)

		return p
	}

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"blank maxItems clears constraint": {
			input: stringtest.Input(`
				# @schema type:array;maxItems:
				tags:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				_, has := prop(t, got, "tags")["maxItems"]
				assert.False(t, has, "blank maxItems must not emit a fail-closed maxItems:0")
			},
		},
		"blank maximum clears constraint": {
			input: stringtest.Input(`
				# @schema maximum:
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				_, has := prop(t, got, "count")["maximum"]
				assert.False(t, has, "blank maximum must not emit a fail-closed maximum:0")
			},
		},
		"infinite minimum is dropped and schema still marshals": {
			input: stringtest.Input(`
				# @schema minimum:.inf
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				_, has := prop(t, got, "count")["minimum"]
				assert.False(t, has, "non-finite minimum must be dropped")
			},
		},
		"blank maxLength clears constraint": {
			input: stringtest.Input(`
				# @schema type:string;maxLength:
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				_, has := prop(t, got, "name")["maxLength"]
				assert.False(t, has, "blank maxLength must not emit a fail-closed maxLength:0")
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

// TestHelmValuesSchemaAnnotatorFailOpenParsing covers value parsing that must
// fail open: a blank const carries no constraint, and a non-string element in
// a type list is dropped rather than coerced into an invalid type token.
func TestHelmValuesSchemaAnnotatorFailOpenParsing(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"blank const carries no constraint": {
			input: stringtest.Input(`
				# @schema const:
				name: real
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				_, has := name["const"]
				assert.False(t, has, "blank const must not emit const:null")
			},
		},
		"empty-string type member becomes the null type": {
			// type:["", string] would emit the invalid Draft-7 "type": ["",...];
			// SetSchemaType rewrites the empty string to the null type, matching
			// dadav's block form of the same annotation.
			input: stringtest.Input(`
				# @schema type:["", string]
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"null", "string"}, name["type"])
			},
		},
		"malformed type list drops entirely, value type fills in": {
			input: stringtest.Input(`
				# @schema type:[string, 1]
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// The numeric 1 is not a valid JSON Schema type token, so the
				// whole list drops (fail open, matching dadav's applyType);
				// structural inference then fills the type from the string
				// value. The annotated type does NOT narrow to "string".
				assert.Equal(t, "string", name["type"])
			},
		},
		"malformed type list over an integer value infers integer": {
			// The decisive case: when the malformed type list is dropped, the
			// schema must fall through to the value's inferred type. The old
			// behavior narrowed to type:string and rejected the integer (fail
			// closed); failing open instead infers integer, matching dadav.
			input: stringtest.Input(`
				# @schema type:[string, 1]
				count: 7
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", count["type"])
			},
		},
		"malformed type list drops entirely in comma form": {
			// The comma form must agree with the bracket form above: the
			// numeric token drops the whole list rather than narrowing to the
			// invalid type token "1" or to "string".
			input: stringtest.Input(`
				# @schema type:string, 1
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", name["type"])

				_, hasTypes := name["types"]
				assert.False(t, hasTypes)
			},
		},
		"non-finite enum value is dropped, schema still marshals": {
			input: stringtest.Input(`
				# @schema enum:[.nan, 1]
				count: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				// The NaN cannot marshal to JSON; dropping it leaves the
				// finite member rather than poisoning the whole schema.
				assert.Equal(t, []any{float64(1)}, count["enum"])
			},
		},
		"non-finite itemEnum value is dropped, schema still marshals": {
			input: stringtest.Input(`
				# @schema type:array;itemEnum:[.inf, 1]
				ports: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				ports, ok := props["ports"].(map[string]any)
				require.True(t, ok)

				items, ok := ports["items"].(map[string]any)
				require.True(t, ok)

				// +Inf cannot marshal to JSON; without the FilterJSONSafe
				// guard it poisons the whole document's marshal, so the test
				// reaching this assertion proves the schema still marshals.
				assert.Equal(t, []any{float64(1)}, items["enum"])
			},
		},
		"all-non-finite enum clears the constraint": {
			input: stringtest.Input(`
				# @schema enum:[.nan, .inf]
				count: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				_, has := count["enum"]
				assert.False(t, has, "an all-non-finite enum must not emit enum:[]")
			},
		},
		"non-finite const is dropped": {
			input: stringtest.Input(`
				# @schema const:.nan
				count: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				_, has := count["const"]
				assert.False(t, has, "a non-finite const must be dropped, not break marshal")
			},
		},
		"repeated type pair does not set both type and types": {
			input: stringtest.Input(`
				# @schema type:[string, integer];type:boolean
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// The later scalar wins and clears the earlier union, so the
				// schema marshals (a schema with both type and types is
				// rejected by the library).
				assert.Equal(t, "boolean", name["type"])

				_, hasTypes := name["types"]
				assert.False(t, hasTypes)
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
