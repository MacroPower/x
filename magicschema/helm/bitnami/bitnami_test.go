package bitnami_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/bitnami"
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

func TestBitnamiAnnotator(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"basic param": {
			input: stringtest.Input(`
				## @param replicas Number of replicas
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.Equal(t, "Number of replicas", r["description"])
			},
		},
		"param with type modifier": {
			input: stringtest.Input(`
				## @param name [string] The release name
				name: my-release
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Equal(t, "The release name", n["description"])
			},
		},
		"nullable modifier": {
			input: stringtest.Input(`
				## @param val [string, nullable] A nullable value
				val: ""
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				types, ok := v["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"skip annotation": {
			input: stringtest.Input(`
				## @skip secret
				secret: password
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
		"nested key path": {
			input: stringtest.Input(`
				## @param image.repository Container image name
				image:
				  repository: nginx
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imageProps, ok := image["properties"].(map[string]any)
				require.True(t, ok)

				repo, ok := imageProps["repository"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Container image name", repo["description"])
			},
		},
		"ignored annotations not misparsed": {
			input: stringtest.Input(`
				## @section My Section
				## @extra extra.key Description
				## @param name The name
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The name", n["description"])

				// @section and @extra should not produce spurious properties.
				assert.NotContains(t, props, "section")
				assert.NotContains(t, props, "extra")
			},
		},
		"descriptionStart and descriptionEnd ignored": {
			input: stringtest.Input(`
				## @descriptionStart
				## This is a section description.
				## @descriptionEnd
				## @param name The name
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The name", n["description"])
			},
		},
		"extra with modifiers ignored": {
			input: stringtest.Input(`
				## @extra extra.key [string] Extra param for README only
				## @param name The name
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The name", n["description"])

				// @extra should not produce any property.
				assert.NotContains(t, props, "extra")
			},
		},
		"default modifier": {
			input: stringtest.Input(`
				## @param val [string, default: custom] Description
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "Description", v["description"])
				assert.Equal(t, "custom", v["default"])
			},
		},
		"unrecognized modifiers ignored": {
			input: stringtest.Input(`
				## @param val [string, foobar] Description
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "Description", v["description"])
			},
		},
		"array index in param key path": {
			input: stringtest.Input(`
				## @param jobs[0].nameOverride Override name for the first job
				jobs:
				  - nameOverride: my-job
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				jobs, ok := props["jobs"].(map[string]any)
				require.True(t, ok)

				items, ok := jobs["items"].(map[string]any)
				require.True(t, ok)

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)

				no, ok := itemProps["nameOverride"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Override name for the first job", no["description"])
			},
		},
		"array index in skip key path": {
			input: stringtest.Input(`
				## @skip items[0]
				items:
				  - secret: password
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "items")
				assert.Contains(t, props, "name")
			},
		},
		"multiple array indices in key path": {
			input: stringtest.Input(`
				## @param jobs[0].containers[1].image Container image
				jobs:
				  - containers:
				      - image: nginx
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				jobs, ok := props["jobs"].(map[string]any)
				require.True(t, ok)

				items, ok := jobs["items"].(map[string]any)
				require.True(t, ok)

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)

				containers, ok := itemProps["containers"].(map[string]any)
				require.True(t, ok)

				cItems, ok := containers["items"].(map[string]any)
				require.True(t, ok)

				cItemProps, ok := cItems["properties"].(map[string]any)
				require.True(t, ok)

				img, ok := cItemProps["image"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Container image", img["description"])
			},
		},
		"object modifier sets type": {
			input: stringtest.Input(`
				## @param annotations [object] Service annotations
				annotations: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["annotations"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", a["type"])
				assert.Equal(t, "Service annotations", a["description"])
			},
		},
		"number modifier": {
			input: stringtest.Input(`
				## @param port [number] Port number
				port: 80
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", p["type"])
			},
		},
		"integer modifier": {
			input: stringtest.Input(`
				## @param count [integer] Count value
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", c["type"])
			},
		},
		"boolean modifier": {
			input: stringtest.Input(`
				## @param enabled [boolean] Feature toggle
				enabled: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				e, ok := props["enabled"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "boolean", e["type"])
			},
		},
		"array modifier": {
			input: stringtest.Input(`
				## @param items [array] List of items
				items: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				i, ok := props["items"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", i["type"])
			},
		},
		"modifiers without spaces": {
			input: stringtest.Input(`
				## @param val [array,nullable] No spaces between modifiers
				val: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				types, ok := v["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "array")
				assert.Contains(t, types, "null")
			},
		},
		"default with numeric value": {
			input: stringtest.Input(`
				## @param port [number, default: 8080] Port number
				port: 80
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", p["type"])
				assert.InDelta(t, float64(8080), p["default"], 0)
			},
		},
		"default with boolean value": {
			input: stringtest.Input(`
				## @param enabled [boolean, default: true] Feature flag
				enabled: false
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				e, ok := props["enabled"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "boolean", e["type"])
				assert.Equal(t, true, e["default"])
			},
		},
		"skip with trailing description ignored": {
			input: stringtest.Input(`
				## @skip secret This should be ignored
				secret: password
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
		"param with leading whitespace": {
			input: stringtest.Input(`
				  ## @param name [string] Indented annotation
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				assert.Equal(t, "Indented annotation", n["description"])
			},
		},
		"param with no modifiers and no description": {
			input: stringtest.Input(`
				## @param name
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", n["type"])
				// Empty description is omitted from JSON output.
				assert.NotContains(t, n, "description")
			},
		},
		"param with empty modifiers": {
			input: stringtest.Input(`
				## @param name [] Description
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Empty modifiers should not set a type; inferred from value.
				assert.Equal(t, "string", n["type"])
				assert.Equal(t, "Description", n["description"])
			},
		},
		"unannotated key uses inferred type": {
			input: stringtest.Input(`
				## @param annotated [string] Annotated key
				annotated: hello
				unannotated: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Unannotated key still appears with inferred type.
				u, ok := props["unannotated"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", u["type"])
			},
		},
		"skip omits object and its children": {
			input: stringtest.Input(`
				## @skip config
				config:
				  host: localhost
				  port: 8080
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				assert.NotContains(t, props, "config")
				assert.Contains(t, props, "name")
			},
		},
		"default overrides yaml value": {
			input: stringtest.Input(`
				## @param cpu [string, default: 100m] CPU limit
				cpu: 250m
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cpu"].(map[string]any)
				require.True(t, ok)

				// Default from annotation, not from YAML value.
				assert.Equal(t, "100m", c["default"])
			},
		},
		"default with url-like value containing colons": {
			input: stringtest.Input(`
				## @param registry [string, default: https://example.com] Registry URL
				registry: https://other.com
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["registry"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "https://example.com", r["default"])
			},
		},
		"nullable without type infers from value": {
			input: stringtest.Input(`
				## @param count [nullable] A nullable count
				count: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				// Nullable alone produces ["null"], type inference adds
				// "integer" from the YAML value via the generator.
				types, ok := c["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "null")
			},
		},
		"multiple params for same parent object": {
			input: stringtest.Input(`
				## @param service.type [string] Service type
				## @param service.port [number] Service port
				service:
				  type: ClusterIP
				  port: 80
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				svc, ok := props["service"].(map[string]any)
				require.True(t, ok)

				svcProps, ok := svc["properties"].(map[string]any)
				require.True(t, ok)

				svcType, ok := svcProps["type"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", svcType["type"])
				assert.Equal(t, "Service type", svcType["description"])

				svcPort, ok := svcProps["port"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "number", svcPort["type"])
				assert.Equal(t, "Service port", svcPort["description"])
			},
		},
		"deeply nested param path": {
			input: stringtest.Input(`
				## @param a.b.c.d [string] Deep value
				a:
				  b:
				    c:
				      d: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["a"].(map[string]any)
				require.True(t, ok)

				aProps, ok := a["properties"].(map[string]any)
				require.True(t, ok)

				b, ok := aProps["b"].(map[string]any)
				require.True(t, ok)

				bProps, ok := b["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := bProps["c"].(map[string]any)
				require.True(t, ok)

				cProps, ok := c["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := cProps["d"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", d["type"])
				assert.Equal(t, "Deep value", d["description"])
			},
		},
		"skip with array index on nested child": {
			input: stringtest.Input(`
				## @skip jobs[0].secret
				jobs:
				  - secret: hidden
				    name: my-job
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				jobs, ok := props["jobs"].(map[string]any)
				require.True(t, ok)

				items, ok := jobs["items"].(map[string]any)
				require.True(t, ok)

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)

				// "secret" should be skipped.
				assert.NotContains(t, itemProps, "secret")
				// "name" should still appear.
				assert.Contains(t, itemProps, "name")
			},
		},
		"skip parent omits annotated children": {
			input: stringtest.Input(`
				## @skip config
				## @param config.host [string] Hostname
				## @param config.port [integer] Port
				config:
				  host: localhost
				  port: 8080
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Parent is skipped, children should not appear.
				assert.NotContains(t, props, "config")
				assert.Contains(t, props, "name")
			},
		},
		"last param wins for duplicate key paths": {
			input: stringtest.Input(`
				## @param name [string] First description
				## @param name [integer] Second description
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Last @param for the same key path wins during Prepare.
				assert.Equal(t, "integer", n["type"])
				assert.Equal(t, "Second description", n["description"])
			},
		},
		"extra with array index ignored": {
			input: stringtest.Input(`
				## @extra items[0].key [string] Extra param
				## @param name The name
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The name", n["description"])
				assert.NotContains(t, props, "items")
			},
		},
		"default with empty value": {
			input: stringtest.Input(`
				## @param val [string, default: ] Empty default
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "Empty default", v["description"])
				// Empty default should be null (YAML unmarshals empty string
				// to nil).
				assert.Nil(t, v["default"])
			},
		},
		"param with nested path no modifiers": {
			input: stringtest.Input(`
				## @param server.host The server hostname
				server:
				  host: localhost
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				server, ok := props["server"].(map[string]any)
				require.True(t, ok)

				serverProps, ok := server["properties"].(map[string]any)
				require.True(t, ok)

				host, ok := serverProps["host"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", host["type"])
				assert.Equal(t, "The server hostname", host["description"])
			},
		},
		"nullable with object type": {
			input: stringtest.Input(`
				## @param config [object, nullable] Optional config
				config: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				types, ok := c["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "object")
				assert.Contains(t, types, "null")
			},
		},
		"nullable with array type": {
			input: stringtest.Input(`
				## @param items [array, nullable] Optional items
				items: []
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				i, ok := props["items"].(map[string]any)
				require.True(t, ok)

				types, ok := i["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "array")
				assert.Contains(t, types, "null")
			},
		},
		"default with null value": {
			input: stringtest.Input(`
				## @param val [string, default: null] Nullable default
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				// "null" as a YAML value unmarshals to nil.
				assert.Nil(t, v["default"])
			},
		},
		"default with integer value zero": {
			input: stringtest.Input(`
				## @param count [integer, default: 0] Zero default
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", c["type"])
				assert.InDelta(t, float64(0), c["default"], 0)
			},
		},
		"skip annotation does not interfere with sibling params": {
			input: stringtest.Input(`
				## @skip hidden
				## @param visible [string] Visible param
				hidden: secret
				visible: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "hidden")

				v, ok := props["visible"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Equal(t, "Visible param", v["description"])
			},
		},
		"single hash comment not matched": {
			input: stringtest.Input(`
				# @param name [string] Single hash annotation
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Single # should not be parsed as bitnami annotation;
				// type should be inferred from value, not from modifier.
				assert.Equal(t, "string", n["type"])
				assert.NotContains(t, n, "description")
			},
		},
		"description with brackets not parsed as modifiers": {
			input: stringtest.Input(`
				## @param name Description with [brackets] in it
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)

				// Type inferred from YAML value, not from bracket text.
				assert.Equal(t, "string", n["type"])
				assert.Equal(t, "Description with [brackets] in it", n["description"])
			},
		},
		"key with slash in name handled gracefully": {
			input: stringtest.Input(`
				## @param annotations.prometheus.io/scrape Enable scraping
				annotations:
				  prometheus.io/scrape: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// The key path annotations.prometheus.io/scrape won't match
				// the YAML structure exactly (the YAML key is literally
				// "prometheus.io/scrape" under annotations), but the annotator
				// should not error. The annotation is simply unused.
				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, props, "annotations")
			},
		},
		"multiple skip annotations for different paths": {
			input: stringtest.Input(`
				## @skip secret1
				## @skip secret2
				secret1: pwd1
				secret2: pwd2
				name: visible
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				assert.NotContains(t, props, "secret1")
				assert.NotContains(t, props, "secret2")
				assert.Contains(t, props, "name")
			},
		},
		"extra annotation does not create property for matching yaml key": {
			input: stringtest.Input(`
				## @extra config.extra [string] README only
				config:
				  extra: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Config.extra still exists (from YAML structure) but should
				// not have bitnami annotation applied (type from @extra).
				config, ok := props["config"].(map[string]any)
				require.True(t, ok)

				configProps, ok := config["properties"].(map[string]any)
				require.True(t, ok)

				e, ok := configProps["extra"].(map[string]any)
				require.True(t, ok)

				// Type inferred from value, not from @extra modifier.
				assert.Equal(t, "string", e["type"])
				assert.NotContains(t, e, "description")
			},
		},
		"default only modifier without type": {
			input: stringtest.Input(`
				## @param image.registry [default: REGISTRY_NAME] Kubewatch image registry
				image:
				  registry: docker.io
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imageProps, ok := image["properties"].(map[string]any)
				require.True(t, ok)

				reg, ok := imageProps["registry"].(map[string]any)
				require.True(t, ok)

				// Type inferred from YAML value since no type modifier.
				assert.Equal(t, "string", reg["type"])
				// Default from annotation overrides YAML value.
				assert.Equal(t, "REGISTRY_NAME", reg["default"])
				assert.Equal(t, "Kubewatch image registry", reg["description"])
			},
		},
		"param for nonexistent yaml key ignored": {
			input: stringtest.Input(`
				## @param ghost [string] This key does not exist in YAML
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// The annotated key "ghost" doesn't exist in YAML, so
				// the annotation is unused. Only "name" appears.
				assert.NotContains(t, props, "ghost")
				assert.Contains(t, props, "name")
			},
		},
		"nullable order string then nullable": {
			input: stringtest.Input(`
				## @param val [string,nullable] Desc
				val: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				types, ok := v["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"nullable order nullable then string": {
			input: stringtest.Input(`
				## @param val [nullable,string] Desc
				val: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Modifier order doesn't affect our output.
				types, ok := v["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"object modifier with child properties": {
			// Upstream excludes [object]-annotated params from schema
			// (they're README-only summaries). We include them since we
			// generate schema only, not README tables.
			input: stringtest.Input(`
				## @param webhook [object] Enable Webhook notifications
				webhook:
				  enabled: false
				  url: ""
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				w, ok := props["webhook"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", w["type"])
				assert.Equal(t, "Enable Webhook notifications", w["description"])

				// Children should still appear in the schema.
				wProps, ok := w["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, wProps, "enabled")
				assert.Contains(t, wProps, "url")
			},
		},
		"string modifier on literal block scalar": {
			input: stringtest.Input(`
				## @param configuration [string] haproxy configuration
				configuration: |
				  global
				    log stdout
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["configuration"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", c["type"])
				assert.Equal(t, "haproxy configuration", c["description"])
			},
		},
		"array modifier on non-array value overrides type": {
			// Upstream: [array] modifier changes type to "array" regardless
			// of the YAML value type. Tested from upstream test-values.yaml:
			// arrayEmptyModifier has YAML value "value" (a string).
			input: stringtest.Input(`
				## @param arrayEmptyModifier [array] Test empty array modifier
				arrayEmptyModifier: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["arrayEmptyModifier"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", a["type"])
				assert.Equal(t, "Test empty array modifier", a["description"])
			},
		},
		"array modifier on array of objects": {
			// Upstream: [array] modifier on a complex array. The upstream
			// forceSchemaArrayModifier test. The [array] modifier forces
			// the type to "array" and the YAML structure provides items.
			input: stringtest.Input(`
				## @param forceSchemaArray [array] Force array schema
				forceSchemaArray:
				  - w: x
				    y:
				      - z
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["forceSchemaArray"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", a["type"])
				assert.Equal(t, "Force array schema", a["description"])

				// Items should be inferred from the array elements.
				items, ok := a["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "object", items["type"])
			},
		},
		"nullable with null yaml value": {
			// Upstream requires [nullable] for null values or it throws
			// an error. We accept null values without [nullable] (fail-open).
			// With [nullable], upstream converts null to JSON null default.
			input: stringtest.Input(`
				## @param nullable [nullable] Nullable parameter
				nullable: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["nullable"].(map[string]any)
				require.True(t, ok)

				types, ok := n["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "null")
				assert.Equal(t, "Nullable parameter", n["description"])
			},
		},
		"nullable with non-null value": {
			// Upstream: nullable with a non-null value keeps the value
			// as the default. We don't set defaults from YAML values.
			input: stringtest.Input(`
				## @param nullableNotNull [nullable] Nullable with value
				nullableNotNull: somestring
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["nullableNotNull"].(map[string]any)
				require.True(t, ok)

				// Nullable-only: type from annotation is just "null",
				// but inference adds "string" from the YAML value.
				types, ok := n["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "null")
				assert.Equal(t, "Nullable with value", n["description"])
				// No default since we don't set defaults from YAML values.
				assert.NotContains(t, n, "default")
			},
		},
		"unannotated keys included via structural inference": {
			// Divergence from upstream: upstream excludes unannotated YAML
			// keys from the schema. We include them via structural inference.
			input: stringtest.Input(`
				## @param annotated [string] Annotated key
				annotated: hello
				unannotated_string: world
				unannotated_int: 42
				unannotated_bool: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Annotated key has annotation-provided type.
				a, ok := props["annotated"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", a["type"])
				assert.Equal(t, "Annotated key", a["description"])

				// Unannotated keys have inferred types.
				s, ok := props["unannotated_string"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", s["type"])

				i, ok := props["unannotated_int"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", i["type"])

				b, ok := props["unannotated_bool"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "boolean", b["type"])
			},
		},
		"skip on nested child preserves parent and siblings": {
			// From upstream test-values.yaml: @skip image.tag skips only
			// that child, other siblings (registry, pullPolicy) remain.
			input: stringtest.Input(`
				## @param image.registry [string] Image registry
				## @skip image.tag
				## @param image.pullPolicy Image pull policy
				image:
				  registry: docker.io
				  tag: latest
				  pullPolicy: IfNotPresent
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imageProps, ok := image["properties"].(map[string]any)
				require.True(t, ok)

				// Registry and pullPolicy should be present.
				assert.Contains(t, imageProps, "registry")
				assert.Contains(t, imageProps, "pullPolicy")
				// Tag should be skipped.
				assert.NotContains(t, imageProps, "tag")
			},
		},
		"null yaml value without nullable no error": {
			// Upstream throws an error for null values without [nullable].
			// We follow fail-open: null produces no type constraint.
			input: stringtest.Input(`
				## @param val A value
				val: null
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// No type constraint for null value (fail-open).
				assert.NotContains(t, v, "type")
				assert.Equal(t, "A value", v["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(bitnami.New()),
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

func TestBitnamiPrepare(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		content string
		keyPath string
		want    func(*testing.T, *magicschema.AnnotationResult)
	}{
		"param produces annotation": {
			content: stringtest.Input(`
				## @param name [string] The name
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "string", result.Schema.Type)
				assert.Equal(t, "The name", result.Schema.Description)
			},
		},
		"skip produces skip result": {
			content: stringtest.Input(`
				## @skip secret
				secret: val
			`),
			keyPath: "secret",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				assert.True(t, result.Skip)
			},
		},
		"unmatched key returns nil": {
			content: stringtest.Input(`
				## @param name [string] The name
				name: test
			`),
			keyPath: "other",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		"nullable with type": {
			content: stringtest.Input(`
				## @param val [string, nullable] Desc
				val: test
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, []string{"string", "null"}, result.Schema.Types)
				assert.Empty(t, result.Schema.Type)
			},
		},
		"nullable without type": {
			content: stringtest.Input(`
				## @param val [nullable] Desc
				val: test
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, []string{"null"}, result.Schema.Types)
				assert.Empty(t, result.Schema.Type)
			},
		},
		"default modifier value": {
			content: stringtest.Input(`
				## @param val [string, default: myval] Desc
				val: actual
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.NotNil(t, result.Schema.Default)
				assert.JSONEq(t, `"myval"`, string(result.Schema.Default))
			},
		},
		"array index normalization": {
			content: stringtest.Input(`
				## @param items[0].name Name
				items:
				  - name: test
			`),
			keyPath: "items.name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "Name", result.Schema.Description)
			},
		},
		"prepare resets state from previous call": {
			content: stringtest.Input(`
				## @param name [string] Name
				name: test
			`),
			keyPath: "oldkey",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// After re-preparing with different content, old keys should not match.
				assert.Nil(t, result)
			},
		},
		"section tag ignored": {
			content: stringtest.Input(`
				## @section Common
				## @param name Desc
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "Desc", result.Schema.Description)
			},
		},
		"extra tag ignored": {
			content: stringtest.Input(`
				## @extra extra.key [string] Extra
				## @param name Desc
				name: test
			`),
			keyPath: "extra.key",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// @extra should not be parsed as @param.
				assert.Nil(t, result)
			},
		},
		"last param wins for duplicate key": {
			content: stringtest.Input(`
				## @param name [string] First
				## @param name [integer] Second
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "integer", result.Schema.Type)
				assert.Equal(t, "Second", result.Schema.Description)
			},
		},
		"skip also uses normalized key path": {
			content: stringtest.Input(`
				## @skip jobs[0].secret
				jobs:
				  - secret: hidden
			`),
			keyPath: "jobs.secret",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				assert.True(t, result.Skip)
			},
		},
		"descriptionStart ignored": {
			content: stringtest.Input(`
				## @descriptionStart
				## Some block.
				## @descriptionEnd
				## @param name Desc
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "Desc", result.Schema.Description)
			},
		},
		"default with url containing colons": {
			content: stringtest.Input(`
				## @param url [string, default: https://example.com:8080/path] URL
				url: other
			`),
			keyPath: "url",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.NotNil(t, result.Schema.Default)
				assert.JSONEq(t, `"https://example.com:8080/path"`, string(result.Schema.Default))
			},
		},
		"multiple modifiers all recognized": {
			content: stringtest.Input(`
				## @param val [string, nullable, default: hello] Desc
				val: test
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, []string{"string", "null"}, result.Schema.Types)
				assert.NotNil(t, result.Schema.Default)
				assert.JSONEq(t, `"hello"`, string(result.Schema.Default))
			},
		},
		"empty content produces no annotations": {
			content: "",
			keyPath: "anything",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		"content with only comments and no params": {
			content: stringtest.Input(`
				## @section MySection
				## Just a comment
			`),
			keyPath: "anything",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		"single hash not matched as param": {
			content: stringtest.Input(`
				# @param name [string] Single hash
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// Single # should not match bitnami ## format.
				assert.Nil(t, result)
			},
		},
		"single hash not matched as skip": {
			content: stringtest.Input(`
				# @skip secret
				secret: val
			`),
			keyPath: "secret",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// Single # should not match bitnami ## format.
				assert.Nil(t, result)
			},
		},
		"key path with slash in name": {
			content: stringtest.Input(`
				## @param prometheus.io/scrape Enable scraping
			`),
			keyPath: "prometheus.io/scrape",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "Enable scraping", result.Schema.Description)
			},
		},
		"multiple skips all registered": {
			content: stringtest.Input(`
				## @skip key1
				## @skip key2
			`),
			keyPath: "key1",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				assert.True(t, result.Skip)
			},
		},
		"skip does not match param with same prefix": {
			content: stringtest.Input(`
				## @skip config
				## @param configMap [string] Map name
			`),
			keyPath: "configMap",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// @skip config should not affect configMap.
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "string", result.Schema.Type)
				assert.Equal(t, "Map name", result.Schema.Description)
			},
		},
		"param with no description": {
			content: stringtest.Input(`
				## @param name [string]
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "string", result.Schema.Type)
				assert.Empty(t, result.Schema.Description)
			},
		},
		"default with boolean false": {
			content: stringtest.Input(`
				## @param val [boolean, default: false] Desc
				val: true
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "boolean", result.Schema.Type)
				assert.JSONEq(t, "false", string(result.Schema.Default))
			},
		},
		"prepare resets skips from previous call": {
			content: stringtest.Input(`
				## @param name [string] Name
				name: test
			`),
			keyPath: "oldskip",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				// After re-preparing with different content, old skips should not match.
				assert.Nil(t, result)
			},
		},
		"default with array value": {
			content: stringtest.Input(`
				## @param val [array, default: [a, b]] Desc
				val: []
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "array", result.Schema.Type)
				// Comma in default value interacts with modifier splitting:
				// "default: [a" is parsed as default, " b]" is unknown and ignored.
				// YAML unmarshal of "[a" fails, so Default is nil.
				// This is a known limitation of the bitnami comma-separated format.
				assert.Nil(t, result.Schema.Default)
			},
		},
		"param with multiple whitespace before hash": {
			content: stringtest.Input(`
				    ##   @param   name   [string]   Desc
				name: test
			`),
			keyPath: "name",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, "string", result.Schema.Type)
				assert.Equal(t, "Desc", result.Schema.Description)
			},
		},
		"skip with nested array index": {
			content: stringtest.Input(`
				## @skip jobs[0].containers[1].secret
			`),
			keyPath: "jobs.containers.secret",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				assert.True(t, result.Skip)
			},
		},
		"default only modifier without type": {
			content: stringtest.Input(`
				## @param registry [default: REGISTRY_NAME] Image registry
				registry: docker.io
			`),
			keyPath: "registry",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				// No type modifier -- type is empty, deferred to inference.
				assert.Empty(t, result.Schema.Type)
				assert.Empty(t, result.Schema.Types)
				assert.NotNil(t, result.Schema.Default)
				assert.JSONEq(t, `"REGISTRY_NAME"`, string(result.Schema.Default))
				assert.Equal(t, "Image registry", result.Schema.Description)
			},
		},
		"modifier order does not affect output": {
			content: stringtest.Input(`
				## @param val [nullable,string,default: x] Desc
				val: test
			`),
			keyPath: "val",
			want: func(t *testing.T, result *magicschema.AnnotationResult) {
				t.Helper()
				require.NotNil(t, result)
				require.NotNil(t, result.Schema)
				assert.Equal(t, []string{"string", "null"}, result.Schema.Types)
				assert.NotNil(t, result.Schema.Default)
				assert.JSONEq(t, `"x"`, string(result.Schema.Default))
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ann := bitnami.New()

			// For the "prepare resets state" tests, first ForContent with old content.
			if name == "prepare resets state from previous call" {
				_, err := ann.ForContent([]byte(stringtest.Input(`
					## @param oldkey [string] Old
					oldkey: val
				`)))
				require.NoError(t, err)
			}

			if name == "prepare resets skips from previous call" {
				_, err := ann.ForContent([]byte(stringtest.Input(`
					## @skip oldskip
					oldskip: val
				`)))
				require.NoError(t, err)
			}

			prepared, err := ann.ForContent([]byte(tc.content))
			require.NoError(t, err)

			result := prepared.Annotate(nil, tc.keyPath)
			tc.want(t, result)
		})
	}
}

func TestBitnamiAnnotatorFromFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/bitnami.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(bitnami.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/bitnami.schema.json", schema)
}
