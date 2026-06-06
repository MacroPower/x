package magicschema_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
)

func TestMergeMultipleInputs(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		inputA string
		inputB string
		opts   []magicschema.Option
		check  func(*testing.T, map[string]any)
	}{
		"union of properties": {
			inputA: "a: 1\nb: hello\n",
			inputB: "b: world\nc: true\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, props, "a")
				assert.Contains(t, props, "b")
				assert.Contains(t, props, "c")
			},
		},
		"same type preserved": {
			inputA: "count: 1\n",
			inputB: "count: 5\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", count["type"])
			},
		},
		"integer and number widen to number": {
			inputA: "val: 1\n",
			inputB: "val: 1.5\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", val["type"])
			},
		},
		"incompatible types remove constraint": {
			inputA: "val: hello\n",
			inputB: "val: 42\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				if ok {
					assert.Nil(t, val["type"])
				} else {
					// When merged to no constraints, val may be bool true.
					assert.Equal(t, true, props["val"])
				}
			},
		},
		"null merges transparently": {
			inputA: "val: null\n",
			inputB: "val: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", val["type"])
			},
		},
		"nested object union": {
			inputA: "obj:\n  a: 1\n",
			inputB: "obj:\n  b: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				obj, ok := props["obj"].(map[string]any)
				require.True(t, ok)

				objProps, ok := obj["properties"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, objProps, "a")
				assert.Contains(t, objProps, "b")
			},
		},
		"items schema merging": {
			inputA: "list:\n  - hello\n",
			inputB: "list:\n  - 42\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				list, ok := props["list"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", list["type"])

				// Items should have no type constraint because string and integer
				// are incompatible and widen to nothing.
				items, ok := list["items"].(map[string]any)
				if ok {
					assert.Nil(t, items["type"], "incompatible item types should widen to no type constraint")
				}

				// If items is not a map (e.g. true schema), that also satisfies
				// "no type constraint".
			},
		},
		"required intersection": {
			inputA: "# @schema\n# required: true\n# @schema\nname: test\nval: a\n",
			inputB: "name: other\nval: b\n",
			opts: []magicschema.Option{
				magicschema.WithAnnotators(dadav.New()),
			},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				// InputB does not mark "name" as required, so the intersection
				// should be empty.
				req, ok := got["required"].([]any)
				if ok {
					assert.NotContains(t, req, "name")
				}
			},
		},
		"additionalProperties fail-open": {
			inputA: "# @schema\n# type: object\n# additionalProperties: false\n# @schema\nconfig:\n  key: value\n",
			inputB: "config:\n  key: value\n",
			opts: []magicschema.Option{
				magicschema.WithAnnotators(dadav.New()),
			},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				config, ok := props["config"].(map[string]any)
				require.True(t, ok)

				// Merge should fail-open: one side has additionalProperties: false
				// and the other side has no constraint (treated as true/open),
				// so the merged result should not be false.
				ap := config["additionalProperties"]
				assert.NotEqual(t, false, ap, "merged additionalProperties should fail-open")
			},
		},
		"additionalProperties fail-open with type mismatch": {
			// File A has config as string (no AP), file B has config as object
			// with strict mode (AP: false). Types widen away, but AP should
			// still be open because nil (unset) is treated as open.
			inputA: "config: somevalue\n",
			inputB: "config:\n  key: value\n",
			opts: []magicschema.Option{
				magicschema.WithStrict(true),
			},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				config, ok := props["config"].(map[string]any)
				require.True(t, ok)

				// Type should be widened away (incompatible: string + object).
				assert.Nil(t, config["type"], "incompatible types should widen to no type constraint")

				// AP should be open: nil (from string side) + false (from strict
				// object side) = true per fail-open semantics.
				ap := config["additionalProperties"]
				assert.NotEqual(t, false, ap, "merged additionalProperties should fail-open when one side is unset")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate([]byte(tc.inputA), []byte(tc.inputB))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.check(t, got)
		})
	}
}

func TestMergeTypeWidening(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		inputA   string
		inputB   string
		wantType string // empty means no type constraint
	}{
		"integer + number -> number": {
			inputA:   "val: 1\n",
			inputB:   "val: 1.5\n",
			wantType: "number",
		},
		"number + integer -> number": {
			inputA:   "val: 1.5\n",
			inputB:   "val: 1\n",
			wantType: "number",
		},
		"integer + string -> no constraint": {
			inputA:   "val: 42\n",
			inputB:   "val: hello\n",
			wantType: "",
		},
		"string + integer -> no constraint": {
			inputA:   "val: hello\n",
			inputB:   "val: 42\n",
			wantType: "",
		},
		"boolean + string -> no constraint": {
			inputA:   "val: true\n",
			inputB:   "val: hello\n",
			wantType: "",
		},
		"string + boolean -> no constraint": {
			inputA:   "val: hello\n",
			inputB:   "val: true\n",
			wantType: "",
		},
		"number + string -> no constraint": {
			inputA:   "val: 3.14\n",
			inputB:   "val: hello\n",
			wantType: "",
		},
		"string + number -> no constraint": {
			inputA:   "val: hello\n",
			inputB:   "val: 3.14\n",
			wantType: "",
		},
		"array + string -> no constraint": {
			inputA:   "val:\n  - a\n",
			inputB:   "val: hello\n",
			wantType: "",
		},
		"string + array -> no constraint": {
			inputA:   "val: hello\n",
			inputB:   "val:\n  - a\n",
			wantType: "",
		},
		"object + string -> no constraint": {
			inputA:   "val:\n  key: x\n",
			inputB:   "val: hello\n",
			wantType: "",
		},
		"object + integer -> no constraint": {
			inputA:   "val:\n  key: x\n",
			inputB:   "val: 42\n",
			wantType: "",
		},
		"any type + null -> same type (string)": {
			inputA:   "val: hello\n",
			inputB:   "val: null\n",
			wantType: "string",
		},
		"null + any type (integer)": {
			inputA:   "val: null\n",
			inputB:   "val: 42\n",
			wantType: "integer",
		},
		"null + any type (boolean)": {
			inputA:   "val: null\n",
			inputB:   "val: true\n",
			wantType: "boolean",
		},
		"null + any type (number)": {
			inputA:   "val: null\n",
			inputB:   "val: 3.14\n",
			wantType: "number",
		},
		"null + any type (array)": {
			inputA:   "val: null\n",
			inputB:   "val:\n  - a\n",
			wantType: "array",
		},
		"null + any type (object)": {
			inputA:   "val: null\n",
			inputB:   "val:\n  key: x\n",
			wantType: "object",
		},
		"same type (string) -> string": {
			inputA:   "val: hello\n",
			inputB:   "val: world\n",
			wantType: "string",
		},
		"same type (integer) -> integer": {
			inputA:   "val: 1\n",
			inputB:   "val: 2\n",
			wantType: "integer",
		},
		"same type (boolean) -> boolean": {
			inputA:   "val: true\n",
			inputB:   "val: false\n",
			wantType: "boolean",
		},
		"same type (number) -> number": {
			inputA:   "val: 1.1\n",
			inputB:   "val: 2.2\n",
			wantType: "number",
		},
		"boolean + integer -> no constraint": {
			inputA:   "val: true\n",
			inputB:   "val: 42\n",
			wantType: "",
		},
		"boolean + number -> no constraint": {
			inputA:   "val: true\n",
			inputB:   "val: 3.14\n",
			wantType: "",
		},
		"boolean + array -> no constraint": {
			inputA:   "val: true\n",
			inputB:   "val:\n  - a\n",
			wantType: "",
		},
		"integer + array -> no constraint": {
			inputA:   "val: 42\n",
			inputB:   "val:\n  - a\n",
			wantType: "",
		},
		"number + array -> no constraint": {
			inputA:   "val: 3.14\n",
			inputB:   "val:\n  - a\n",
			wantType: "",
		},
		"array + object -> no constraint": {
			inputA:   "val:\n  - a\n",
			inputB:   "val:\n  key: x\n",
			wantType: "",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(tc.inputA), []byte(tc.inputB))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			if tc.wantType == "" {
				// No type constraint: property may be true (true schema)
				// or a map without a "type" key.
				val, isMap := props["val"].(map[string]any)
				if isMap {
					assert.Nil(t, val["type"], "expected no type constraint")
				}
			} else {
				val, ok := props["val"].(map[string]any)
				require.True(t, ok, "expected val to be a map")
				assert.Equal(t, tc.wantType, val["type"])
			}
		})
	}
}

func TestMergeAnnotatedConstraints(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		inputA string
		inputB string
		opts   []magicschema.Option
		check  func(*testing.T, map[string]any)
	}{
		"nullable type union widens with scalar type": {
			inputA: "# @schema type:[string, null]\nhost:\n",
			inputB: "host: example.com\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				host, ok := props["host"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"string", "null"}, host["type"])
			},
		},
		"enum union when both sides constrain": {
			inputA: "# @schema enum:[a, b]\nsize: a\n",
			inputB: "# @schema enum:[b, c]\nsize: c\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				size, ok := props["size"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"a", "b", "c"}, size["enum"])
			},
		},
		"enum dropped when one side is unconstrained": {
			inputA: "# @schema enum:[a, b]\nsize: a\n",
			inputB: "size: d\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				size, ok := props["size"].(map[string]any)
				require.True(t, ok)

				assert.Nil(t, size["enum"])
			},
		},
		"minimum widens to the smaller bound": {
			inputA: "# @schema minimum:3\nport: 8080\n",
			inputB: "# @schema minimum:1\nport: 8081\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				port, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.InEpsilon(t, float64(1), port["minimum"], 0.0001)
			},
		},
		"false additionalProperties yields to constrained schema": {
			inputA: "# @schema\n# additionalProperties: false\n# @schema\nconf:\n  a: 1\n",
			inputB: "# @schema\n# additionalProperties:\n#   type: string\n# @schema\nconf:\n  b: x\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				ap, ok := conf["additionalProperties"].(map[string]any)
				require.True(t, ok, "expected constrained additionalProperties, got %v", conf["additionalProperties"])
				assert.Equal(t, "string", ap["type"])
			},
		},
		"constrained additionalProperties survives merge when both sides agree": {
			// A pattern-only additionalProperties schema has no type,
			// properties, or items; it must not be mistaken for the "true"
			// schema and collapsed, which would drop the shared pattern.
			inputA: "# @schema\n# additionalProperties:\n#   pattern: ^[a-z]+$\n# @schema\nconf:\n  a: x\n",
			inputB: "# @schema\n# additionalProperties:\n#   pattern: ^[a-z]+$\n# @schema\nconf:\n  b: y\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				ap, ok := conf["additionalProperties"].(map[string]any)
				require.True(t, ok, "expected constrained additionalProperties, got %v", conf["additionalProperties"])
				assert.Equal(t, "^[a-z]+$", ap["pattern"])
			},
		},
		"root annotations apply from the first input": {
			inputA: "# @schema.root\n# title: My Chart\n# @schema.root\nreplicas: 3\n",
			inputB: "name: x\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "My Chart", got["title"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate([]byte(tc.inputA), []byte(tc.inputB))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			tc.check(t, got)
		})
	}
}
