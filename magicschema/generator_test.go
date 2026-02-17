package magicschema_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
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

	// Tags are unwrapped by unwrapNode, so inference uses the underlying
	// value node type rather than the YAML tag itself.
	tcs := map[string]struct {
		input    string
		wantKey  string
		wantType string
	}{
		"!!str tag unwraps to underlying integer": {
			input:    "val: !!str 123\n",
			wantKey:  "val",
			wantType: "integer",
		},
		"!!int tag unwraps to underlying string": {
			input:    "count: !!int \"42\"\n",
			wantKey:  "count",
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

func TestGeneratorMultiDocument(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input    string
		wantKeys []string
	}{
		"merges all documents with union semantics": {
			input:    "key1: value1\n---\nkey2: value2\n",
			wantKeys: []string{"key1", "key2"},
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
		"multi-line head comments joined with spaces": {
			input: "# First line of description\n# Second line of description\nval: 42\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", val["type"])

				desc, ok := val["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "First line of description")
				assert.Contains(t, desc, "Second line of description")
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

func TestConfigNewGeneratorUnknownAnnotator(t *testing.T) {
	t.Parallel()

	cfg := magicschema.NewConfig()
	cfg.Registry = make(magicschema.Registry)
	cfg.Registry.Add(dadav.New())

	cfg.Annotators = "helm-schema,nonexistent-annotator"

	gen, err := cfg.NewGenerator()
	require.Error(t, err)
	require.ErrorIs(t, err, magicschema.ErrInvalidOption)
	assert.Nil(t, gen)
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
