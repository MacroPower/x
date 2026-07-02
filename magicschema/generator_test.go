package magicschema_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm"
	"go.jacobcolvin.com/x/magicschema/helm/bitnami"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
	"go.jacobcolvin.com/x/magicschema/helm/losisin"
)

func TestGeneratorBasic(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  map[string]any
	}{
		"simple scalar types": {
			input: "name: test\ncount: 3\nratio: 1.5\nenabled: true\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"count":   map[string]any{"type": "integer"},
					"ratio":   map[string]any{"type": "number"},
					"enabled": map[string]any{"type": "boolean"},
				},
			},
		},
		"null value has no type constraint": {
			input: "value: null\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{},
				},
			},
		},
		"empty value has no type constraint": {
			input: "value:\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{},
				},
			},
		},
		"nested objects": {
			input: "parent:\n  child: value\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"parent": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"child": map[string]any{"type": "string"},
						},
						"additionalProperties": true,
					},
				},
			},
		},
		"array": {
			input: "items:\n  - one\n  - two\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"items": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		},
		"empty array": {
			input: "items: []\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"items": map[string]any{"type": "array"},
				},
			},
		},
		"comments as descriptions": {
			input: "# Number of replicas\nreplicas: 3\n",
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"replicas": map[string]any{
						"type":        "integer",
						"description": "Number of replicas",
					},
				},
			},
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

			// Check properties match expected.
			assertPropertiesMatch(t, tc.want, got)
		})
	}
}

func TestGeneratorEmptyInput(t *testing.T) {
	t.Parallel()

	gen := magicschema.NewGenerator()

	tcs := map[string]struct {
		input []byte
	}{
		"nil input": {
			input: nil,
		},
		"empty bytes": {
			input: []byte(""),
		},
		"whitespace only": {
			input: []byte("   \n\n  "),
		},
		"comment only": {
			input: []byte("# just a comment\n"),
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			schema, err := gen.Generate(tc.input)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			// Empty input produces schema with only $schema field.
			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			assert.Equal(t, "http://json-schema.org/draft-07/schema#", got["$schema"])
			assert.Nil(t, got["type"])
			assert.Nil(t, got["properties"])
		})
	}

	t.Run("no arguments", func(t *testing.T) {
		t.Parallel()

		schema, err := gen.Generate()
		require.NoError(t, err)

		out, err := json.Marshal(schema)
		require.NoError(t, err)

		var got map[string]any

		require.NoError(t, json.Unmarshal(out, &got))
		assert.Equal(t, "http://json-schema.org/draft-07/schema#", got["$schema"])
		assert.Nil(t, got["type"])
		assert.Nil(t, got["properties"])
	})
}

func TestGeneratorInvalidYAML(t *testing.T) {
	t.Parallel()

	gen := magicschema.NewGenerator()

	schema, err := gen.Generate([]byte(":\n  invalid: [yaml\n"))
	require.Error(t, err)
	require.ErrorIs(t, err, magicschema.ErrInvalidYAML)
	assert.Nil(t, schema)
}

func TestGeneratorOptions(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		opts  []magicschema.Option
		input string
		check func(*testing.T, map[string]any)
	}{
		"with title": {
			opts:  []magicschema.Option{magicschema.WithTitle("My Schema")},
			input: "key: value\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "My Schema", got["title"])
			},
		},
		"with description": {
			opts:  []magicschema.Option{magicschema.WithDescription("A description")},
			input: "key: value\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "A description", got["description"])
			},
		},
		"with id": {
			opts:  []magicschema.Option{magicschema.WithID("https://example.com/schema")},
			input: "key: value\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "https://example.com/schema", got["$id"])
			},
		},
		"with strict": {
			opts:  []magicschema.Option{magicschema.WithStrict(true)},
			input: "key: value\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, false, got["additionalProperties"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.check(t, got)
		})
	}
}

func TestGeneratorInferDefaults(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		opts   []magicschema.Option
		inputs []string
		check  func(*testing.T, map[string]any, string)
	}{
		"scalars record their values": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				name: test
				count: 3
				ratio: 1.5
				enabled: true
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `"test"`, propertyAt(t, got, "name")["default"])
				assertJSONValue(t, `3`, propertyAt(t, got, "count")["default"])
				assertJSONValue(t, `1.5`, propertyAt(t, got, "ratio")["default"])
				assertJSONValue(t, `true`, propertyAt(t, got, "enabled")["default"])
			},
		},
		"null and empty values record a null default": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				empty:
				explicit: null
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				for _, key := range []string{"empty", "explicit"} {
					prop := propertyAt(t, got, key)
					d, ok := prop["default"]
					require.True(t, ok, "expected a default on %s", key)
					assert.Nil(t, d, "expected a null default on %s", key)
				}
			},
		},
		"tagged empty scalar records a null default": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			// A tagged but absent value parses only as the last entry in its
			// mapping, so each tag is its own single-key input; the three
			// merge into one schema.
			inputs: []string{
				"intval: !!int\n",
				"strval: !!str\n",
				"boolval: !!bool\n",
			},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				// Type inference is suppressed for a tagged but absent value,
				// so the recorded default must be the null the value actually
				// holds, not a coerced zero (0, "").
				for _, key := range []string{"intval", "strval", "boolval"} {
					prop := propertyAt(t, got, key)
					d, ok := prop["default"]
					require.True(t, ok, "expected a default on %s", key)
					assert.Nil(t, d, "expected a null default on %s", key)
					assert.NotContains(t, prop, "type", "tagged-empty %s should carry no type", key)
				}
			},
		},
		"scalar array records the full observed list": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				tags:
				  - one
				  - two
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				tags := propertyAt(t, got, "tags")
				assertJSONValue(t, `["one", "two"]`, tags["default"])

				items, ok := tags["items"].(map[string]any)
				require.True(t, ok)
				assert.NotContains(t, items, "default")
			},
		},
		"empty array records an empty list": {
			opts:   []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{"tags: []\n"},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `[]`, propertyAt(t, got, "tags")["default"])
			},
		},
		"objects record no default but their children do": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				parent:
				  child: value
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assert.NotContains(t, propertyAt(t, got, "parent"), "default")
				assertJSONValue(t, `"value"`, propertyAt(t, got, "parent", "child")["default"])
			},
		},
		"sequence of mappings records the list but not items": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				containers:
				  - name: app
				    port: 8080
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				containers := propertyAt(t, got, "containers")
				assertJSONValue(t, `[{"name": "app", "port": 8080}]`, containers["default"])

				items, ok := containers["items"].(map[string]any)
				require.True(t, ok)

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)

				for key, prop := range itemProps {
					propMap, ok := prop.(map[string]any)
					require.True(t, ok)
					assert.NotContains(t, propMap, "default", "items property %s", key)
				}
			},
		},
		"annotator default wins over the observed value": {
			opts: []magicschema.Option{
				magicschema.WithAnnotators(losisin.New()),
				magicschema.WithInferDefaults(true),
			},
			inputs: []string{stringtest.Input(`
				# @schema default:99
				port: 8080
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `99`, propertyAt(t, got, "port")["default"])
			},
		},
		"annotation without a default records the observed value": {
			opts: []magicschema.Option{
				magicschema.WithAnnotators(losisin.New()),
				magicschema.WithInferDefaults(true),
			},
			inputs: []string{stringtest.Input(`
				# @schema type:integer
				port: 8080
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				port := propertyAt(t, got, "port")
				assert.Equal(t, "integer", port["type"])
				assertJSONValue(t, `8080`, port["default"])
			},
		},
		"merge key properties record observed values": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				defaults: &defaults
				  timeout: 30
				  retries: 3
				production:
				  <<: *defaults
				  timeout: 60
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `3`, propertyAt(t, got, "production", "retries")["default"])
				// The explicit key overrides the merged-in property.
				assertJSONValue(t, `60`, propertyAt(t, got, "production", "timeout")["default"])
			},
		},
		"mergeProperties surfaces the first property default": {
			opts: []magicschema.Option{
				magicschema.WithAnnotators(losisin.New()),
				magicschema.WithInferDefaults(true),
			},
			inputs: []string{stringtest.Input(`
				# @schema mergeProperties:true
				env:
				  a: 1
				  b: 2
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				// Locks in current behavior: schema merge keeps the first
				// property's default in additionalProperties, as arbitrary
				// as the first-wins type or description there.
				ap, ok := propertyAt(t, got, "env")["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assertJSONValue(t, `1`, ap["default"])
			},
		},
		"multi-input merge keeps the first default": {
			opts:   []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{"port: 1\n", "port: 2\n"},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `1`, propertyAt(t, got, "port")["default"])
			},
		},
		"null first input keeps the null default": {
			opts:   []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{"port:\n", "port: 2\n"},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				port := propertyAt(t, got, "port")
				d, ok := port["default"]
				require.True(t, ok, "expected a default on port")
				assert.Nil(t, d, "expected a null default on port")
			},
		},
		"option off records no defaults": {
			inputs: []string{stringtest.Input(`
				name: test
				tags:
				  - a
				empty:
			`)},
			check: func(t *testing.T, _ map[string]any, raw string) {
				t.Helper()

				assert.NotContains(t, raw, `"default"`)
			},
		},
		"explicit tags coerce recorded values": {
			opts:   []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{"tagged: !!str 123\ncount: !!int \"5\"\n"},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `"123"`, propertyAt(t, got, "tagged")["default"])
				assertJSONValue(t, `5`, propertyAt(t, got, "count")["default"])
			},
		},
		"non-finite floats record no default": {
			opts:   []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{"nan: .nan\ninf: .inf\n"},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				for _, key := range []string{"nan", "inf"} {
					prop := propertyAt(t, got, key)
					assert.Equal(t, "number", prop["type"])
					assert.NotContains(t, prop, "default")
				}
			},
		},
		"sequence alias to an outside anchor resolves in the default": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				anchor: &vals
				  - 1
				  - 2
				wrapper:
				  - *vals
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				assertJSONValue(t, `[[1, 2]]`, propertyAt(t, got, "wrapper")["default"])
			},
		},
		"alias cycle inside a sequence records no default": {
			opts: []magicschema.Option{magicschema.WithInferDefaults(true)},
			inputs: []string{stringtest.Input(`
				cyc: &c
				  - *c
				  - 1
			`)},
			check: func(t *testing.T, got map[string]any, _ string) {
				t.Helper()

				cyc := propertyAt(t, got, "cyc")
				assert.Equal(t, "array", cyc["type"])
				assert.NotContains(t, cyc, "default")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, 0, len(tc.inputs))
			for _, input := range tc.inputs {
				inputs = append(inputs, []byte(input))
			}

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.check(t, got, string(out))
		})
	}
}

func TestGeneratorBrokenAlias(t *testing.T) {
	t.Parallel()

	// An alias with no in-scope anchor resolves to null (see resolveAliases),
	// so it must behave like a genuine null everywhere inference gates on it:
	// never panic, record a null default, widen item types to include null,
	// and keep the sibling mappings' property schemas.
	tcs := map[string]struct {
		opts  []magicschema.Option
		input string
		check func(*testing.T, map[string]any)
	}{
		"annotated broken-alias value records a null default": {
			opts: []magicschema.Option{
				magicschema.WithAnnotators(losisin.New()),
				magicschema.WithInferDefaults(true),
			},
			input: stringtest.Input(`
				# @schema type:string
				foo: *missing
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				foo := propertyAt(t, got, "foo")
				assert.Equal(t, "string", foo["type"])

				d, ok := foo["default"]
				require.True(t, ok, "expected a default on foo")
				assert.Nil(t, d)
			},
		},
		"unannotated broken-alias value records a null default": {
			opts: []magicschema.Option{
				magicschema.WithInferDefaults(true),
			},
			input: stringtest.Input(`
				foo: *missing
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				foo := propertyAt(t, got, "foo")

				d, ok := foo["default"]
				require.True(t, ok, "expected a default on foo")
				assert.Nil(t, d)
			},
		},
		"broken alias among mappings keeps properties and widens to null": {
			input: stringtest.Input(`
				items:
				  - a: 1
				  - *missing
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				items, ok := propertyAt(t, got, "items")["items"].(map[string]any)
				require.True(t, ok)
				assert.ElementsMatch(t, []any{"object", "null"}, items["type"])
				assert.Equal(t, "integer", propertyAt(t, items, "a")["type"])
			},
		},
		"broken alias in a scalar list widens the item type to null": {
			input: stringtest.Input(`
				nums:
				  - 1
				  - *missing
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				items, ok := propertyAt(t, got, "nums")["items"].(map[string]any)
				require.True(t, ok)
				assert.ElementsMatch(t, []any{"integer", "null"}, items["type"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.check(t, got)
		})
	}
}

func TestGeneratorSkipAndMergePropertiesTogether(t *testing.T) {
	t.Parallel()

	// Both skipProperties and mergeProperties can be set on one node. The
	// merge fold must run first so the child schemas survive in
	// additionalProperties rather than being dropped by the strip.
	gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))

	schema, err := gen.Generate([]byte(stringtest.Input(`
		# @schema skipProperties:true;mergeProperties:true
		config:
		  a: 1
		  b: 2
	`)))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	config := propertyAt(t, got, "config")
	assert.NotContains(t, config, "properties")

	ap, ok := config["additionalProperties"].(map[string]any)
	require.True(t, ok, "child schemas should fold into additionalProperties")
	assert.Equal(t, "integer", ap["type"])
}

func TestGeneratorStrictSkipAndMergeProperties(t *testing.T) {
	t.Parallel()

	// Skip- and merge-properties annotations hide a node's property map, so
	// the strict-mode structural additionalProperties:false must not survive
	// on that node: the schema would validate only the empty object and
	// reject the source file itself. An annotation-set additionalProperties
	// stays authoritative. The root keeps its strict false throughout.
	tcs := map[string]struct {
		input string
		check func(*testing.T, map[string]any)
	}{
		"skipProperties keeps additionalProperties open": {
			input: stringtest.Input(`
				# @schema skipProperties:true
				resources:
				  limits:
				    cpu: 100m
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				resources := propertyAt(t, got, "resources")
				assert.NotContains(t, resources, "properties")
				assert.Equal(t, true, resources["additionalProperties"])
			},
		},
		"mergeProperties on an empty mapping keeps additionalProperties open": {
			input: stringtest.Input(`
				# @schema mergeProperties:true
				labels: {}
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				labels := propertyAt(t, got, "labels")
				assert.NotContains(t, labels, "properties")
				assert.Equal(t, true, labels["additionalProperties"])
			},
		},
		"annotation-set additionalProperties stays authoritative": {
			input: stringtest.Input(`
				# @schema skipProperties:true;additionalProperties:false
				resources:
				  limits:
				    cpu: 100m
			`),
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				resources := propertyAt(t, got, "resources")
				assert.NotContains(t, resources, "properties")
				assert.Equal(t, false, resources["additionalProperties"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithStrict(true),
				magicschema.WithAnnotators(losisin.New()),
			)

			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			assert.Equal(t, false, got["additionalProperties"],
				"strict mode must keep the root additionalProperties false")
			tc.check(t, got)
		})
	}
}

func TestGeneratorInferDefaultsGolden(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/infer_defaults.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(magicschema.WithInferDefaults(true))
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/infer_defaults.schema.json", schema)
}

func TestGeneratorFromFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/basic.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/basic.schema.json", schema)
}

func TestGeneratorRealWorld(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/realworld.yaml")
	require.NoError(t, err)

	cfg := magicschema.NewConfig()
	cfg.Registry = helm.DefaultRegistry()
	cfg.Annotators = "helm-schema,helm-values-schema,bitnami,helm-docs"

	gen, err := cfg.NewGenerator()
	require.NoError(t, err)

	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/realworld.schema.json", schema)
}

func TestGeneratorMultiFile(t *testing.T) {
	t.Parallel()

	dataA, err := os.ReadFile("testdata/merge_a.yaml")
	require.NoError(t, err)

	dataB, err := os.ReadFile("testdata/merge_b.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate(dataA, dataB)
	require.NoError(t, err)

	assertGolden(t, "testdata/merge.schema.json", schema)
}

func TestGeneratorAnchorsAndAliases(t *testing.T) {
	t.Parallel()

	input := `
defaults: &defaults
  timeout: 30
  retries: 3

production:
  <<: *defaults
  timeout: 60
`

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "defaults")
	assert.Contains(t, props, "production")

	production, ok := props["production"].(map[string]any)
	require.True(t, ok)

	prodProps, ok := production["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, prodProps, "timeout")
	assert.Contains(t, prodProps, "retries")
}

func TestGeneratorWrappedMappingKeys(t *testing.T) {
	t.Parallel()

	// Anchored, tagged, and alias keys must emit the resolved key text as the
	// property name, not the raw source syntax ("&k foo", "!!str foo", "*k"):
	// under WithStrict the raw spelling would produce a schema the source
	// document itself fails to validate against.
	tcs := map[string]struct {
		input string
		path  []string
		want  string
	}{
		"anchored key": {
			input: "&k foo: 1\n",
			path:  []string{"properties"},
			want:  "foo",
		},
		"tagged key": {
			input: "!!str foo: 1\n",
			path:  []string{"properties"},
			want:  "foo",
		},
		"alias key": {
			input: "name: &k foo\nmap:\n  *k : value\n",
			path:  []string{"properties", "map", "properties"},
			want:  "foo",
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

			node := got
			for _, p := range tc.path {
				next, ok := node[p].(map[string]any)
				require.True(t, ok, "missing %q in %v", p, node)

				node = next
			}

			assert.Contains(t, node, tc.want)
		})
	}
}

func TestGeneratorRedefinedAnchorNearestPreceding(t *testing.T) {
	t.Parallel()

	// A redefined anchor name binds each alias to the nearest preceding
	// definition, not the last one: useFirst sees the integer, useSecond the
	// string, matching the YAML spec and goccy's own loader.
	input := "first:\n  v: &a 1\nuseFirst: *a\nsecond:\n  v: &a \"hello\"\nuseSecond: *a\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	useFirst, ok := props["useFirst"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "integer", useFirst["type"])

	useSecond, ok := props["useSecond"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", useSecond["type"])
}

func TestGeneratorScalarArrayAliasItems(t *testing.T) {
	t.Parallel()

	// The single scalar element is an alias to an integer anchor. Items
	// inference must resolve the alias so the element keeps its type, the same
	// way the all-mappings branch resolves aliases.
	input := `
base: &port 8080
ports:
  - *port
`

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	ports, ok := props["ports"].(map[string]any)
	require.True(t, ok)

	items, ok := ports["items"].(map[string]any)
	require.True(t, ok, "alias-valued scalar element should still yield an items type")
	assert.Equal(t, "integer", items["type"])
}

func TestGeneratorLiteralBlocks(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string
	}{
		"literal block scalar": {
			input: "description: |\n  This is a\n  multi-line value\n",
			want:  "string",
		},
		"folded block scalar": {
			input: "summary: >\n  This is a\n  folded value\n",
			want:  "string",
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
			require.True(t, ok, "expected properties in output")

			for key, prop := range props {
				propMap, ok := prop.(map[string]any)
				require.True(t, ok, "expected property %s to be a map", key)
				assert.Equal(t, tc.want, propMap["type"], "property %s type mismatch", key)
			}
		})
	}
}

func TestGeneratorTaggedValues(t *testing.T) {
	t.Parallel()

	// Explicit YAML tags are authoritative: a loader coerces the scalar to
	// the tagged type, so the schema must reflect the tag even when the
	// literal looks like another type. Unknown tags fall through to the
	// underlying value node.
	tcs := map[string]struct {
		input    string
		wantKey  string
		wantType string // empty means no type constraint
	}{
		"!!str tag forces string": {
			input:    "val: !!str 123\n",
			wantKey:  "val",
			wantType: "string",
		},
		"!!int tag forces integer": {
			input:    "count: !!int \"42\"\n",
			wantKey:  "count",
			wantType: "integer",
		},
		"!!float tag forces number": {
			input:    "ratio: !!float 1\n",
			wantKey:  "ratio",
			wantType: "number",
		},
		"!!bool tag forces boolean": {
			input:    "flag: !!bool \"true\"\n",
			wantKey:  "flag",
			wantType: "boolean",
		},
		"!!null tag yields no constraint": {
			input:    "nothing: !!null whatever\n",
			wantKey:  "nothing",
			wantType: "",
		},
		"!!timestamp tag forces string": {
			input:    "when: !!timestamp 2024-01-01\n",
			wantKey:  "when",
			wantType: "string",
		},
		"custom tag falls through to value": {
			input:    "custom: !mytag hello\n",
			wantKey:  "custom",
			wantType: "string",
		},
		"anchored tag keeps the tag type": {
			input:    "a: &x !!str 123\nb: *x\n",
			wantKey:  "b",
			wantType: "string",
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
			require.True(t, ok, "expected properties in output")

			if tc.wantType == "" {
				// No type constraint: the property may marshal as the
				// true schema or a map without a type key.
				if prop, isMap := props[tc.wantKey].(map[string]any); isMap {
					assert.Nil(t, prop["type"], "property %s should have no type", tc.wantKey)
				}

				return
			}

			prop, ok := props[tc.wantKey].(map[string]any)
			require.True(t, ok, "missing property: %s", tc.wantKey)
			assert.Equal(t, tc.wantType, prop["type"], "property %s type mismatch", tc.wantKey)
		})
	}
}

func TestGeneratorSpecialFloats(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input   string
		wantKey string
	}{
		"positive infinity": {
			input:   "pos_inf: .inf\n",
			wantKey: "pos_inf",
		},
		"negative infinity": {
			input:   "neg_inf: -.inf\n",
			wantKey: "neg_inf",
		},
		"not a number": {
			input:   "nan_val: .nan\n",
			wantKey: "nan_val",
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
			require.True(t, ok, "expected properties in output")

			prop, ok := props[tc.wantKey].(map[string]any)
			require.True(t, ok, "missing property: %s", tc.wantKey)
			assert.Equal(t, "number", prop["type"], "property %s should be number", tc.wantKey)
		})
	}
}

func TestGeneratorConcurrentUse(t *testing.T) {
	t.Parallel()

	// Generator documents that it is safe for concurrent use, resting on the
	// per-document copy plus annotator cloning. This exercises that guarantee
	// under -race with all four prototype annotators and inputs that touch the
	// shared paths: annotations, anchors/merge keys, and multi-document streams.
	cfg := magicschema.NewConfig()
	cfg.Registry = helm.DefaultRegistry()
	cfg.Annotators = strings.Join(helm.DefaultNames(), ",")

	gen, err := cfg.NewGenerator()
	require.NoError(t, err)

	inputs := [][]byte{
		[]byte(stringtest.Input(`
			## @param replicas Number of replicas
			# @schema minimum:1
			replicas: 3
			defaults: &d
			  timeout: 30
			production:
			  <<: *d
			  timeout: 60
		`)),
		[]byte("a: 1\n---\nb: 2\n"),
		[]byte(stringtest.Input(`
			# -- A description
			name: test
			tags:
			  - a
			  - b
		`)),
	}

	var wg sync.WaitGroup

	for range 50 {
		for _, in := range inputs {
			wg.Add(1)

			go func(in []byte) {
				defer wg.Done()

				_, genErr := gen.Generate(in)
				assert.NoError(t, genErr)
			}(in)
		}
	}

	wg.Wait()
}

func TestGeneratorMultiDocumentAnnotatorIsolation(t *testing.T) {
	t.Parallel()

	// The first document declares a bitnami @param for "foo" but its only key
	// is "unrelated"; the second has key "foo" with no annotation. A
	// content-scanning annotator must see only its own document, so the @param
	// must not bleed across the boundary and attach a description to foo.
	input := stringtest.Input(`
		## @param foo A leaked description
		unrelated: 1
		---
		foo: 2
	`)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(bitnami.New()),
	)
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	foo, ok := props["foo"].(map[string]any)
	require.True(t, ok)

	_, hasDesc := foo["description"]
	assert.False(t, hasDesc, "doc1's @param must not bleed into doc2's foo")
}

func TestGeneratorDotSeparatorAnnotatorIsolation(t *testing.T) {
	t.Parallel()

	// The same isolation must hold when documents are separated by the "..."
	// end marker rather than "---". A splitter that recognizes only "---" finds
	// one segment, the count mismatches the two parsed documents, and the whole
	// stream is scanned for every document -- so doc1's @param would bleed into
	// doc2's foo.
	input := stringtest.Input(`
		## @param foo A leaked description
		unrelated: 1
		...
		foo: 2
	`)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(bitnami.New()),
	)
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	foo, ok := props["foo"].(map[string]any)
	require.True(t, ok)

	_, hasDesc := foo["description"]
	assert.False(t, hasDesc, "doc1's @param must not bleed across a ... boundary")
}

func TestGeneratorMultiDocument(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input    string
		wantKeys []string
		check    func(*testing.T, map[string]any)
	}{
		"merges all documents with union semantics": {
			input:    "key1: value1\n---\nkey2: value2\n",
			wantKeys: []string{"key1", "key2"},
		},
		"null in one document widens to a nullable union": {
			input:    "val: null\n---\nval: hello\n",
			wantKeys: []string{"val"},
			check: func(t *testing.T, props map[string]any) {
				t.Helper()

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"string", "null"}, val["type"])
			},
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
			require.True(t, ok, "expected properties in output")

			for _, key := range tc.wantKeys {
				assert.Contains(t, props, key)
			}

			if tc.check != nil {
				tc.check(t, props)
			}
		})
	}
}

func TestGeneratorAllAnnotators(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		check func(*testing.T, map[string]any)
	}{
		"mixed annotation styles": {
			// Helm-schema block annotation on "timeout",
			// helm-values-schema inline annotation on "retries",
			// bitnami @param annotation on "port", and
			// plain (no annotation) on "debug".
			input: "" +
				"# @schema\n" +
				"# type: integer\n" +
				"# minimum: 1\n" +
				"# @schema\n" +
				"timeout: 30\n" +
				"# @schema type:integer;minimum:0\n" +
				"retries: 3\n" +
				"## @param port Listen port\n" +
				"port: 8080\n" +
				"debug: true\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Timeout is annotated by helm-schema with type integer and minimum.
				timeout, ok := props["timeout"].(map[string]any)
				require.True(t, ok, "missing property: timeout")
				assert.Equal(t, "integer", timeout["type"])
				assert.InDelta(t, 1, timeout["minimum"], 0.01)

				// Retries is annotated by helm-values-schema with type integer and minimum.
				retries, ok := props["retries"].(map[string]any)
				require.True(t, ok, "missing property: retries")
				assert.Equal(t, "integer", retries["type"])
				assert.InDelta(t, 0, retries["minimum"], 0.01)

				// Port is annotated by bitnami with description.
				port, ok := props["port"].(map[string]any)
				require.True(t, ok, "missing property: port")
				assert.Equal(t, "Listen port", port["description"])

				// Debug is a plain value with inferred type.
				debug, ok := props["debug"].(map[string]any)
				require.True(t, ok, "missing property: debug")
				assert.Equal(t, "boolean", debug["type"])
			},
		},
		"enum and const across annotators do not combine": {
			// The helm-values-schema annotator (lower priority) sets enum
			// while helm-schema (higher priority) sets const on the same
			// key. The higher-priority value-set constraint must win
			// outright: emitting both enum and const AND-combines to reject
			// every value (fail closed).
			input: "" +
				"# @schema enum:[a, b]\n" +
				"# @schema\n" +
				"# const: c\n" +
				"# @schema\n" +
				"mode: a\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				mode, ok := props["mode"].(map[string]any)
				require.True(t, ok, "missing property: mode")

				// Because helm-schema is highest priority, its const wins and
				// the lower-priority enum is not pulled in alongside it.
				assert.Equal(t, "c", mode["const"])

				_, hasEnum := mode["enum"]
				assert.False(t, hasEnum, "enum must not accompany const")
			},
		},
		"lower-priority const incompatible with higher type is dropped": {
			// The helm-schema annotator (higher priority) sets type:string;
			// helm-values-schema (lower) sets type:integer;const:5. Filling the
			// const beside the string type would emit
			// {"type":"string","const":5}, which no value satisfies (fail
			// closed). The incompatible const is dropped.
			input: "" +
				"# @schema type:integer;const:5\n" +
				"# @schema\n" +
				"# type: string\n" +
				"# @schema\n" +
				"mode: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				mode, ok := props["mode"].(map[string]any)
				require.True(t, ok, "missing property: mode")

				assert.Equal(t, "string", mode["type"])

				_, hasConst := mode["const"]
				assert.False(t, hasConst, "incompatible const must be dropped")
			},
		},
		"structural type incompatible with annotated enum is not filled": {
			// The helm-values-schema annotator sets a string enum on a key
			// whose value is an integer. Filling the inferred integer type
			// beside the string enum would emit
			// {"type":"integer","enum":["debug","info"]}, which no value
			// satisfies (fail closed). The type stays unset and the enum
			// stands alone.
			input: "" +
				"# @schema enum:[debug, info]\n" +
				"verbosity: 3\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				verbosity, ok := props["verbosity"].(map[string]any)
				require.True(t, ok, "missing property: verbosity")

				assert.Equal(t, []any{"debug", "info"}, verbosity["enum"])

				_, hasType := verbosity["type"]
				assert.False(t, hasType, "incompatible inferred type must not be filled")
			},
		},
		"structural type incompatible with annotated const is not filled": {
			// The helm-values-schema annotator sets a string const on a key
			// whose value is an integer. The inferred integer type must not
			// be grafted beside it.
			input: "" +
				"# @schema const:hello\n" +
				"port: 8080\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				port, ok := props["port"].(map[string]any)
				require.True(t, ok, "missing property: port")

				assert.Equal(t, "hello", port["const"])

				_, hasType := port["type"]
				assert.False(t, hasType, "incompatible inferred type must not be filled")
			},
		},
		"structural string type incompatible with integer const is not filled": {
			// The reverse direction: a helm-schema block sets an integer
			// const on a key whose value is a string. The inferred string
			// type must not be grafted beside it.
			input: "" +
				"# @schema\n" +
				"# const: 5\n" +
				"# @schema\n" +
				"key: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				key, ok := props["key"].(map[string]any)
				require.True(t, ok, "missing property: key")

				assert.InDelta(t, 5, key["const"], 0.01)

				_, hasType := key["type"]
				assert.False(t, hasType, "incompatible inferred type must not be filled")
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := magicschema.NewConfig()
			cfg.Registry = helm.DefaultRegistry()
			cfg.Annotators = "helm-schema,helm-values-schema,bitnami,helm-docs"

			gen, err := cfg.NewGenerator()
			require.NoError(t, err)

			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			tc.check(t, got)
		})
	}
}

func TestGeneratorFallbackInference(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		opts  []magicschema.Option
		check func(*testing.T, map[string]any)
	}{
		"unannotated YAML produces descriptions from comments": {
			input: "# Number of pod replicas\nreplicas: 3\n\nimage:\n  # Container image repository\n  repository: nginx\n  # Image tag to deploy\n  tag: latest\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				replicas, ok := props["replicas"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", replicas["type"])
				assert.Equal(t, "Number of pod replicas", replicas["description"])

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imageProps, ok := image["properties"].(map[string]any)
				require.True(t, ok)

				repo, ok := imageProps["repository"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", repo["type"])
				assert.Equal(t, "Container image repository", repo["description"])

				tag, ok := imageProps["tag"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", tag["type"])
				assert.Equal(t, "Image tag to deploy", tag["description"])
			},
		},
		"multi-line head comments joined with newlines": {
			input: "# First line of description\n# Second line of description\nval: 42\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", val["type"])

				assert.Equal(t, stringtest.JoinLF(
					"First line of description",
					"Second line of description",
				), val["description"])
			},
		},
		"comment with yaml example keeps newlines and indentation": {
			input: "# Example config:\n#   foo: bar\n#   baz:\n#     - 1\nfield: {}\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				field, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"Example config:",
					"  foo: bar",
					"  baz:",
					"    - 1",
				), field["description"])
			},
		},
		"whitespace-only comment line separates paragraphs": {
			// A whitespace-only comment line inside a contiguous block is a
			// paragraph separator, so both paragraphs survive with a blank
			// line between them and per-line indentation preserved.
			input: "#   earlier text\n#  \n#   kept text\nval: 1\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, stringtest.JoinLF(
					"  earlier text",
					"",
					"  kept text",
				), val["description"])
			},
		},
		"inline comment on same-line key-value": {
			input: "replicas: 3 # number of instances\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				replicas, ok := props["replicas"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", replicas["type"])

				// The inline comment should be used as description.
				desc, hasDesc := replicas["description"].(string)
				require.True(t, hasDesc, "expected description on replicas")
				assert.Contains(t, desc, "number of instances")
			},
		},
		"inline comment on null value": {
			input: "config: #description of config\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				config, ok := props["config"].(map[string]any)
				if ok {
					// The inline comment on a null value is attached to the
					// value node and should be used as description.
					desc, hasDesc := config["description"].(string)
					require.True(t, hasDesc, "expected description on config")
					assert.Contains(t, desc, "description of config")
				}
			},
		},
		"helm-docs -- comments not extracted as fallback descriptions": {
			input: "# -- Description text\nreplicas: 3\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				replicas, ok := props["replicas"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", replicas["type"])
				// The -- comment should NOT become a description.
				assert.Empty(t, replicas["description"])
			},
		},
		"annotation-like comments not extracted as plain descriptions": {
			input: "# @schema type:string\nname: test\n",
			opts:  []magicschema.Option{magicschema.WithAnnotators()},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)
				// The @schema comment should not become a description when
				// no annotators are enabled.
				assert.Empty(t, name["description"], "annotation-like comment should not become description")
			},
		},
		"schema block content not extracted as plain descriptions": {
			input: "# Real description\n# @schema\n# type: string\n# @schema\nname: test\n",
			opts:  []magicschema.Option{magicschema.WithAnnotators()},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)
				// Content inside the @schema fences is annotation data, not
				// prose; only the plain comment becomes the description.
				assert.Equal(t, "Real description", name["description"])
			},
		},
		"junk-suffix schema fences not extracted as plain descriptions": {
			input: "# Real description\n# @schema@\n# type: [null, string]\n# @schema@\nname: test\n",
			opts:  []magicschema.Option{magicschema.WithAnnotators()},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)
				// Upstream helm-schema treats any "# @schema"-prefixed line
				// as a block delimiter, so "@schema@" fences (cilium) hide
				// their content from fallback descriptions too.
				assert.Equal(t, "Real description", name["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(tc.opts...)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.check(t, got)
		})
	}
}

func TestGeneratorHeadCommentAttribution(t *testing.T) {
	t.Parallel()

	// The goccy parser bundles separate comment blocks (file headers,
	// commented-out examples for a previous key) into one head comment
	// group on the following key, erasing the blank lines between them.
	// Only the comment run physically touching the key documents it; a
	// "#"-only line inside that run separates paragraphs of one
	// description. Property paths are dot-separated; an empty want means
	// the property must have no description.
	tcs := map[string]struct {
		input string
		want  map[string]string
	}{
		"file header not attributed to first key": {
			input: stringtest.Input(`
				# Default values for my-chart.
				# This is a YAML-formatted file.

				# Number of replicas.
				replicaCount: 1
			`),
			want: map[string]string{
				"replicaCount": "Number of replicas.",
			},
		},
		"non-adjacent comment block dropped": {
			input: stringtest.Input(`
				# Example on how to configure extraEnvs
				# - name: FOO
				#   value: bar

				tls: false
			`),
			want: map[string]string{
				"tls": "",
			},
		},
		"indented stray comment not attributed to next key": {
			input: stringtest.Input(`
				some_map: {}
				  # commented example for some_map's children
				nodeSelector: {}
			`),
			want: map[string]string{
				"some_map":     "",
				"nodeSelector": "",
			},
		},
		"adjacent comment kept": {
			input: stringtest.Input(`
				# Redis address.
				cache: ""
			`),
			want: map[string]string{
				"cache": "Redis address.",
			},
		},
		"non-adjacent example dropped and adjacent description kept": {
			input: stringtest.Input(`
				# Example on how to configure extraEnvs
				# - name: FOO
				#   value: bar

				# enable tls on the podinfo service
				tls: false
			`),
			want: map[string]string{
				"tls": "enable tls on the podinfo service",
			},
		},
		"deeper-indented run directly above adjacent description dropped": {
			input: stringtest.Input(`
				foo: {}
				  # commented child for foo
				# real doc for bar
				bar: 1
			`),
			want: map[string]string{
				"foo": "",
				"bar": "real doc for bar",
			},
		},
		"under-indented comment attributed to nested key": {
			input: stringtest.Input(`
				foo:
				# doc for bar
				  bar: 1
			`),
			want: map[string]string{
				"foo.bar": "doc for bar",
			},
		},
		"hash-only lines separate paragraphs and all paragraphs kept": {
			input: stringtest.Input(`
				# assertNoLeakedSecrets checks that secret values are not exposed.
				#
				# To pass values without exposing them, use variable expansion.
				#
				# Alternatively, disable this check by setting assertNoLeakedSecrets to false.
				assertNoLeakedSecrets: true
			`),
			want: map[string]string{
				"assertNoLeakedSecrets": stringtest.JoinLF(
					"assertNoLeakedSecrets checks that secret values are not exposed.",
					"",
					"To pass values without exposing them, use variable expansion.",
					"",
					"Alternatively, disable this check by setting assertNoLeakedSecrets to false.",
				),
			},
		},
		"unrelated block dropped and both paragraphs kept": {
			input: stringtest.Input(`
				# Unrelated block for the previous key.

				# Real docs paragraph one.
				#
				# Real docs paragraph two.
				key: 1
			`),
			want: map[string]string{
				"key": stringtest.JoinLF(
					"Real docs paragraph one.",
					"",
					"Real docs paragraph two.",
				),
			},
		},
		"consecutive separator lines collapse to one paragraph break": {
			input: stringtest.Input(`
				# para one
				#
				#
				# para two
				key: 1
			`),
			want: map[string]string{
				"key": stringtest.JoinLF(
					"para one",
					"",
					"para two",
				),
			},
		},
		"misaligned hash-only separator keeps both paragraphs": {
			// The "#"-only separator sits flush-left while the prose comments
			// are indented to the nested key's column. Its column is
			// meaningless, so it must not reset the run and drop the leading
			// paragraph.
			input: stringtest.JoinLF(
				"service:",
				"  # Para one A",
				"  # Para one B",
				"#",
				"  # Para two",
				"  type: ClusterIP",
				"",
			),
			want: map[string]string{
				"service.type": stringtest.JoinLF(
					"Para one A",
					"Para one B",
					"",
					"Para two",
				),
			},
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

			for path, want := range tc.want {
				prop := propertyAt(t, got, strings.Split(path, ".")...)

				if want == "" {
					assert.NotContains(t, prop, "description", "property %s", path)
				} else {
					assert.Equal(t, want, prop["description"], "property %s", path)
				}
			}
		})
	}
}

func TestGeneratorCRLFHeadComments(t *testing.T) {
	t.Parallel()

	// The goccy lexer counts each carriage return toward its line number, so
	// CRLF and lone-CR input desynchronize the Position.Line that comment
	// attribution reasons about. The generator folds line endings before
	// parsing, so every line-ending style yields the same descriptions as
	// its LF twin: a multi-line head comment stays intact and a file header
	// is still dropped.
	lines := []string{
		"# Default values for my-chart.",
		"",
		"# How many replicas to run.",
		"# Increase for high availability.",
		"replicaCount: 1",
		"",
	}
	lf := stringtest.JoinLF(lines...)

	want := stringtest.JoinLF(
		"How many replicas to run.",
		"Increase for high availability.",
	)

	inputs := map[string]string{
		"lf":   lf,
		"crlf": stringtest.JoinCRLF(lines...),
		"cr":   strings.ReplaceAll(lf, "\n", "\r"),
	}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			replicaCount := propertyAt(t, got, "replicaCount")
			assert.Equal(t, want, replicaCount["description"])
		})
	}
}

func TestGeneratorArrayOfMappingObjects(t *testing.T) {
	t.Parallel()

	input := "containers:\n  - name: app\n    image: nginx\n  - name: sidecar\n    port: 8080\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	containers, ok := props["containers"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", containers["type"])

	// Items schema should merge properties from all elements.
	items, ok := containers["items"].(map[string]any)
	require.True(t, ok)

	itemProps, ok := items["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, itemProps, "name")
	assert.Contains(t, itemProps, "image")
	assert.Contains(t, itemProps, "port")
}

func TestGeneratorArrayOfMappingObjectsWithNull(t *testing.T) {
	t.Parallel()

	// A null element among mappings must not collapse the items schema to a
	// bare object-or-null: the per-property schemas survive and the null only
	// widens the item type to allow null.
	input := "containers:\n  - name: app\n    image: nginx\n  -\n  - name: sidecar\n    port: 8080\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	containers, ok := props["containers"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", containers["type"])

	items, ok := containers["items"].(map[string]any)
	require.True(t, ok)

	// Property schemas are preserved despite the null element.
	itemProps, ok := items["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, itemProps, "name")
	assert.Contains(t, itemProps, "image")
	assert.Contains(t, itemProps, "port")

	// The null widens the item type to [object, null].
	assert.Contains(t, items["type"], "object")
	assert.Contains(t, items["type"], "null")
}

func TestGeneratorDeeplyNested(t *testing.T) {
	t.Parallel()

	input := `
level1:
  level2:
    level3:
      level4:
        value: deep
`

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	// Navigate down to the deepest level.
	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	l1, ok := props["level1"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", l1["type"])

	l1Props, ok := l1["properties"].(map[string]any)
	require.True(t, ok)

	l2, ok := l1Props["level2"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", l2["type"])

	l2Props, ok := l2["properties"].(map[string]any)
	require.True(t, ok)

	l3, ok := l2Props["level3"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", l3["type"])

	l3Props, ok := l3["properties"].(map[string]any)
	require.True(t, ok)

	l4, ok := l3Props["level4"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", l4["type"])

	l4Props, ok := l4["properties"].(map[string]any)
	require.True(t, ok)

	val, ok := l4Props["value"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", val["type"])
}

func TestGeneratorRootAdditionalPropertiesNonObject(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
	}{
		"sequence root": {
			input: "- a\n- b\n",
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

			// Non-object root schemas should not have additionalProperties.
			assert.Nil(t, got["additionalProperties"],
				"non-object root should not have additionalProperties")
		})
	}
}

func TestGeneratorAnnotatedNodeKeepsPlainComment(t *testing.T) {
	t.Parallel()

	// An annotation that sets only a type must not suppress the plain
	// comment description sitting in the same comment group.
	input := "# A helpful description\n# @schema type:integer\nfoo: 1\n"

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	foo, ok := props["foo"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "integer", foo["type"])
	assert.Equal(t, "A helpful description", foo["description"])
}

func TestGeneratorDepthBoundUniform(t *testing.T) {
	t.Parallel()

	// 600 nested mappings sit beyond half the walk bound but within the
	// bound itself. The walk used to consume two depth units per level on
	// the plain-nesting path (walkNode and walkMapping each counted),
	// cutting off at ~500 levels instead of the documented 1000.
	const levels = 600

	var sb strings.Builder

	for i := range levels {
		sb.WriteString(strings.Repeat("  ", i))
		fmt.Fprintf(&sb, "l%d:\n", i)
	}

	sb.WriteString(strings.Repeat("  ", levels))
	sb.WriteString("leaf: 1\n")

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(sb.String()))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	cur := got

	for i := range levels {
		props, ok := cur["properties"].(map[string]any)
		require.True(t, ok, "missing properties at level %d", i)

		cur, ok = props[fmt.Sprintf("l%d", i)].(map[string]any)
		require.True(t, ok, "missing property l%d", i)
	}

	props, ok := cur["properties"].(map[string]any)
	require.True(t, ok, "leaf level was cut off by the depth bound")

	leaf, ok := props["leaf"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "integer", leaf["type"])
}

func TestGeneratorAliasFanOut(t *testing.T) {
	t.Parallel()

	// Chained anchors that each reference the previous level twice expand
	// to 2^n node visits. Without a visit budget this hangs for minutes;
	// with it, the walk cuts off and fails open.
	var sb strings.Builder

	sb.WriteString("l0: &l0 {a: 1}\n")

	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&sb, "l%d: &l%d {x: *l%d, y: *l%d}\n", i, i, i-1, i-1)
	}

	done := make(chan error, 1)

	go func() {
		gen := magicschema.NewGenerator()
		_, err := gen.Generate([]byte(sb.String()))

		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("alias fan-out was not bounded: Generate did not return within 30s")
	}
}

func TestGeneratorEmptyMidStreamDocument(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
	}{
		"blank document mid-stream": {
			input: "foo: 1\n---\n\n---\nbar: 2\n",
		},
		"consecutive separators": {
			input: "foo: 1\n---\n---\nbar: 2\n",
		},
		"multiple empty documents": {
			input: "foo: 1\n---\n\n---\n\n---\nbar: 2\n",
		},
		"empty document opened by separator with trailing comment": {
			input: "foo: 1\n--- # c\n\n---\nbar: 2\n",
		},
		"empty document closed by separator with trailing comment": {
			input: "foo: 1\n---\n\n--- # c\nbar: 2\n",
		},
		"tab before the separator's trailing comment": {
			input: "foo: 1\n---\t# c\n\n---\nbar: 2\n",
		},
		"empty document closed by content-carrying document start": {
			input: "foo: 1\n---\n--- {bar: 2}\n",
		},
		"empty document closed by content-carrying start across blanks": {
			input: "foo: 1\n---\n\n--- {bar: 2}\n",
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

			// Documents after an empty one must stay in the union.
			assert.Contains(t, props, "foo")
			assert.Contains(t, props, "bar")
		})
	}
}

func TestGeneratorNonValueDocumentBodies(t *testing.T) {
	t.Parallel()

	// A comment-only "---" block or a leading directive parses to a non-value
	// document body. It must contribute nothing to the union rather than fold
	// in an empty schema that widens the root to [object, null] (and, under
	// strict mode, flips additionalProperties back to permissive).
	tcs := map[string]struct {
		input string
	}{
		"comment-only mid-stream document": {
			input: "replicas: 3\n---\n# just a comment\n---\nreplicas: 1\n",
		},
		"comment-only trailing document": {
			input: "replicas: 3\n---\n# trailing comment\n",
		},
		"leading directive": {
			input: "%YAML 1.2\n---\nreplicas: 3\n",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(magicschema.WithStrict(true))
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			// The root stays a plain object: the non-value body neither widens
			// the type to include null nor defeats strict mode.
			assert.Equal(t, "object", got["type"])
			assert.Equal(t, false, got["additionalProperties"])
		})
	}
}

func TestGeneratorByteOrderMark(t *testing.T) {
	t.Parallel()

	// Files saved with a UTF-8 BOM must not leak the mark into the first
	// property key.
	input := "\xef\xbb\xbffoo: 1\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	assert.Contains(t, props, "foo")
	assert.NotContains(t, props, "\ufefffoo")
}

func TestGeneratorStrictRootAdditionalProperties(t *testing.T) {
	t.Parallel()

	// A @schema.root block setting additionalProperties: true must not
	// defeat strict mode: the generator-level setting outranks annotators.
	input := "# @schema.root\n# additionalProperties: true\n# @schema.root\nreplicas: 3\n"

	gen := magicschema.NewGenerator(
		magicschema.WithStrict(true),
		magicschema.WithAnnotators(dadav.New()),
	)
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	assert.Equal(t, false, got["additionalProperties"],
		"strict mode must keep the root additionalProperties false")
}

func TestConfigNewGeneratorUnknownAnnotator(t *testing.T) {
	t.Parallel()

	cfg := magicschema.NewConfig()
	cfg.Registry = make(magicschema.Registry)
	cfg.Registry.Add(dadav.New())

	cfg.Annotators = "helm-schema,nonexistent-annotator"

	gen, err := cfg.NewGenerator()
	require.Error(t, err)
	require.ErrorIs(t, err, magicschema.ErrInvalidOption)
	require.ErrorIs(t, err, magicschema.ErrUnknownAnnotator)
	require.EqualError(t, err, `invalid option: unknown annotator "nonexistent-annotator"`)
	assert.Nil(t, gen)
}

func TestGeneratorGenerateFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	pathA := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(pathA,
		[]byte("# Primary description\nreplicas: 3\n"), 0o600))

	pathB := filepath.Join(dir, "b.yaml")
	require.NoError(t, os.WriteFile(pathB,
		[]byte("# Secondary description\nreplicas: 5\nname: test\n"), 0o600))

	gen := magicschema.NewGenerator()
	schema, err := gen.GenerateFiles(pathA, pathB)
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	// Union semantics: properties from both files appear.
	assert.Contains(t, props, "replicas")
	assert.Contains(t, props, "name")

	// Merged metadata is first-input-wins, so the primary file's
	// description survives the merge.
	replicas, ok := props["replicas"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Primary description", replicas["description"])
}

func TestGeneratorGenerateFilesReadError(t *testing.T) {
	t.Parallel()

	gen := magicschema.NewGenerator()

	schema, err := gen.GenerateFiles(filepath.Join(t.TempDir(), "missing.yaml"))
	require.ErrorIs(t, err, magicschema.ErrReadInput)
	require.ErrorIs(t, err, os.ErrNotExist,
		"the wrapped *os.PathError must stay inspectable")
	assert.Nil(t, schema)
}

func TestGeneratorGenerateFilesZeroPaths(t *testing.T) {
	t.Parallel()

	gen := magicschema.NewGenerator()

	schema, err := gen.GenerateFiles()
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	// Zero paths behave like Generate with zero inputs: the true schema
	// with only the draft URI set.
	assert.JSONEq(t, `{"$schema": "http://json-schema.org/draft-07/schema#"}`, string(out))
}

func TestGeneratorEmptyMapping(t *testing.T) {
	t.Parallel()

	input := "config: {}\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	config, ok := props["config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", config["type"])
}

func TestGeneratorMultipleInputsWithOneEmpty(t *testing.T) {
	t.Parallel()

	inputA := "replicas: 3\nname: test\n"
	inputB := ""

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(inputA), []byte(inputB))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	// The non-empty input's properties should still be present.
	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "replicas")
	assert.Contains(t, props, "name")

	// A blank input contributes the true schema (no type constraint), so
	// the typeless side widens the merged root to accept null as well.
	assert.Equal(t, []any{"object", "null"}, got["type"])
}

// propertyAt walks a marshaled schema map down the named properties and
// returns the schema at the end of the path.
func propertyAt(t *testing.T, schema map[string]any, names ...string) map[string]any {
	t.Helper()

	cur := schema

	for _, name := range names {
		props, ok := cur["properties"].(map[string]any)
		require.True(t, ok, "expected properties")

		cur, ok = props[name].(map[string]any)
		require.True(t, ok, "missing property: %s", name)
	}

	return cur
}

// assertJSONValue asserts that a decoded JSON value matches the expected
// JSON text when re-marshaled, sidestepping float64 round-trip types.
func assertJSONValue(t *testing.T, want string, got any) {
	t.Helper()

	b, err := json.Marshal(got)
	require.NoError(t, err)

	assert.JSONEq(t, want, string(b))
}

// assertPropertiesMatch checks that all expected properties exist in got
// with matching types and descriptions.
func assertPropertiesMatch(t *testing.T, want, got map[string]any) {
	t.Helper()

	wantProps, wantHasProps := want["properties"].(map[string]any)
	gotProps, gotHasProps := got["properties"].(map[string]any)

	if !wantHasProps {
		return
	}

	require.True(t, gotHasProps, "expected properties in output")

	for key, wantProp := range wantProps {
		gotProp, ok := gotProps[key]
		require.True(t, ok, "missing property: %s", key)

		wantMap, wantIsMap := wantProp.(map[string]any)
		gotMap, gotIsMap := gotProp.(map[string]any)

		if !wantIsMap || !gotIsMap {
			continue
		}

		if wantType, ok := wantMap["type"]; ok {
			assert.Equal(t, wantType, gotMap["type"], "property %s type mismatch", key)
		}

		if wantDesc, ok := wantMap["description"]; ok {
			assert.Equal(t, wantDesc, gotMap["description"], "property %s description mismatch", key)
		}

		// Recurse into nested properties.
		if _, hasSubProps := wantMap["properties"]; hasSubProps {
			assertPropertiesMatch(t, wantMap, gotMap)
		}
	}
}

func TestGeneratorAnchorCycle(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
	}{
		"self-referential anchor": {
			input: "a: &x\n  b: *x\n",
		},
		"mutually recursive anchors": {
			input: "a: &x\n  b: &y\n    c: *x\nd: *y\n",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Alias cycles must not crash the walk; the recursion bound
			// cuts the subtree off fail-open.
			gen := magicschema.NewGenerator()
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)
			require.NotNil(t, schema)
		})
	}
}

func TestGeneratorQuotedKeys(t *testing.T) {
	t.Parallel()

	input := "\"my.key\": 1\n'other': two\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	// Property names carry no quote characters from the YAML source.
	assert.Contains(t, props, "my.key")
	assert.Contains(t, props, "other")
	assert.NotContains(t, props, `"my.key"`)
	assert.NotContains(t, props, "'other'")
}

func TestGeneratorNullOnlyAnnotatedType(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  any
	}{
		"null-only type widens with value type": {
			input: "# @schema type:null\nport: 8080\n",
			want:  []any{"integer", "null"},
		},
		"null-only type with null value stays null": {
			input: "# @schema type:null\nport:\n",
			want:  "null",
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

			port, ok := props["port"].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, tc.want, port["type"])
		})
	}
}

func TestGeneratorMergeKeyRequired(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		key   string
		want  []any
	}{
		"merge key required deduplicates with direct annotation": {
			input: "base: &base\n" +
				"  # @schema required:true\n" +
				"  name: x\n" +
				"spec:\n" +
				"  <<: *base\n" +
				"  # @schema required:true\n" +
				"  name: y\n",
			key:  "spec",
			want: []any{"name"},
		},
		"sequence merge keys carry required": {
			input: "a: &a\n" +
				"  # @schema required:true\n" +
				"  x: 1\n" +
				"b: &b\n" +
				"  # @schema required:true\n" +
				"  y: 2\n" +
				"spec:\n" +
				"  <<: [*a, *b]\n",
			key:  "spec",
			want: []any{"x", "y"},
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

			spec, ok := props[tc.key].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, tc.want, spec["required"])
		})
	}
}

func TestGeneratorExplicitNotRequiredOverridesMergeKey(t *testing.T) {
	t.Parallel()

	// A "<<" merge brings in a key its source mapping marked required:true.
	// An explicit required:false on the merged-in key must clear it: the
	// fail-open principle never marks a property required against intent.
	input := "base: &base\n" +
		"  # @schema required:true\n" +
		"  name: x\n" +
		"spec:\n" +
		"  <<: *base\n" +
		"  # @schema required:false\n" +
		"  name: y\n"

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	spec, ok := props["spec"].(map[string]any)
	require.True(t, ok)

	assert.NotContains(t, spec, "required",
		"explicit required:false must clear the merge-key-inherited required")
}

func TestGeneratorExplicitNotRequiredBeforeMergeKey(t *testing.T) {
	t.Parallel()

	// The mirror of TestGeneratorExplicitNotRequiredOverridesMergeKey: the
	// explicit required:false precedes the "<<" merge key in source order, so
	// the merge must not re-add the required key it would otherwise inherit.
	input := "base: &base\n" +
		"  # @schema required:true\n" +
		"  name: x\n" +
		"spec:\n" +
		"  # @schema required:false\n" +
		"  name: y\n" +
		"  <<: *base\n"

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	spec := propertyAt(t, got, "spec")
	assert.NotContains(t, spec, "required",
		"required:false before a merge key must still clear the inherited required")
}

func TestGeneratorHiddenPropertyOverridesMergeKey(t *testing.T) {
	t.Parallel()

	// An explicit hidden:true omits a property even when a "<<" merge key
	// supplies the same key, in either source order: a merge key before the
	// property must not survive the skip, and one after must not re-insert it.
	tcs := map[string]string{
		"merge key after the hidden property": "base: &base\n  name: x\n" +
			"spec:\n  # @schema hidden:true\n  name: y\n  <<: *base\n",
		"merge key before the hidden property": "base: &base\n  name: x\n" +
			"spec:\n  <<: *base\n  # @schema hidden:true\n  name: y\n",
	}

	for name, input := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
			schema, err := gen.Generate([]byte(input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			spec := propertyAt(t, got, "spec")
			if props, ok := spec["properties"].(map[string]any); ok {
				assert.NotContains(t, props, "name", "a hidden property must not be re-inserted by a merge key")
			}
		})
	}
}

func TestGeneratorRequiredUnderAnnotatedParent(t *testing.T) {
	t.Parallel()

	// A child's required:true accumulates on the structural schema; an
	// annotation on the parent object (here additionalProperties:false)
	// must not drop it (traefik's hub.namespaces).
	input := "# @schema additionalProperties:false\n" +
		"hub:\n" +
		"  # @schema required:true\n" +
		"  namespaces: []\n"

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(losisin.New()),
	)
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	hub, ok := props["hub"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, false, hub["additionalProperties"])
	assert.Equal(t, []any{"namespaces"}, hub["required"])
}

func TestGeneratorAnnotationMarkersExcludedFromDescriptions(t *testing.T) {
	t.Parallel()

	// Annotation markers anywhere in a comment block stay out of fallback
	// descriptions, not just markers on the first line.
	input := "# Real description\n# @default -- foo\nreplicas: 3\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	replicas, ok := props["replicas"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "Real description", replicas["description"])
}
