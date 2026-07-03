package dadav_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/magicschema"
	"go.jacobcolvin.com/x/magicschema/helm/dadav"
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

func TestHelmSchemaAnnotator(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"type and constraints": {
			input: stringtest.Input(`
				# @schema
				# type: integer
				# minimum: 1
				# maximum: 100
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.InDelta(t, float64(1), r["minimum"], 0.001)
				assert.InDelta(t, float64(100), r["maximum"], 0.001)
			},
		},
		"required as bool": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# required: true
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
			},
		},
		"description from block": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# description: A test field
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "A test field", f["description"])
			},
		},
		"enum values": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# enum: [debug, info, warn, error]
				# @schema
				logLevel: info
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["logLevel"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
				assert.NotNil(t, f["enum"])
			},
		},
		"pattern": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# pattern: "^[a-z]+$"
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "^[a-z]+$", f["pattern"])
			},
		},
		"additional properties false": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# additionalProperties: false
				# @schema
				config:
				  key: value
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
		"x-custom annotations": {
			input: stringtest.Input(`
				# @schema
				# x-order: 10
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(10), f["x-order"], 0.001)
			},
		},
		"x-custom annotation with non-finite float is dropped": {
			// A NaN/Inf cannot be marshaled to JSON; storing it in Extra would
			// break the whole document's final marshal, so the key is dropped
			// (fail open) like every other value path.
			input: stringtest.Input(`
				# @schema
				# type: object
				# x-bad: .nan
				# x-ok: 10
				# @schema
				field: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				_, hasBad := f["x-bad"]
				assert.False(t, hasBad, "non-finite x- value must be dropped")
				assert.InDelta(t, float64(10), f["x-ok"], 0.001)
			},
		},
		"dependencies string array": {
			input: stringtest.Input(`
				# @schema
				# dependencies:
				#   enabled: [host, port]
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				deps, ok := f["dependencies"].(map[string]any)
				require.True(t, ok)

				enabled, ok := deps["enabled"].([]any)
				require.True(t, ok)
				assert.Contains(t, enabled, "host")
				assert.Contains(t, enabled, "port")
			},
		},
		"dependency schema that cannot round-trip is dropped": {
			// The "bad" dependency carries a non-finite number, so ToSubSchema
			// returns nil; it must be dropped rather than stored as an invalid
			// "bad": null, while the valid "good" dependency survives.
			input: stringtest.Input(`
				# @schema
				# dependencies:
				#   good: [a, b]
				#   bad:
				#     const: .nan
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				deps, ok := f["dependencies"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"a", "b"}, deps["good"])
				assert.NotContains(t, deps, "bad")
			},
		},
		"bare hash line separates description paragraphs": {
			// A "#"-only line is a paragraph separator within one description,
			// not a group boundary: upstream delimits comment groups only on
			// physical blank lines and keeps the "#" line as a blank line in
			// the joined description.
			input: stringtest.Input(`
				# Old context about something
				#
				# Actual description
				# @schema
				# type: integer
				# @schema
				key: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"Old context about something",
					"",
					"Actual description",
				), f["description"])
			},
		},
		"definitions and ref": {
			input: stringtest.Input(`
				# @schema
				# $ref: "#/definitions/port"
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "#/definitions/port", f["$ref"])
			},
		},
		"double hash content lines": {
			input: stringtest.Input(`
				## @schema
				## type: string
				## pattern: "^[a-z]+$"
				## @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
				assert.Equal(t, "^[a-z]+$", f["pattern"])
			},
		},
		"description from comment outside block": {
			input: stringtest.Input(`
				# My field description
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "My field description", f["description"])
			},
		},
		"description with helm-docs prefix stripped": {
			input: stringtest.Input(`
				# -- My field description
				# @schema
				# type: integer
				# @schema
				field: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "My field description", f["description"])
			},
		},
		"schema block adjacent to schema.root block": {
			input: stringtest.Input(`
				# @schema.root
				# title: Chart
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.Equal(t, "Chart", got["title"])
			},
		},
		"duplicate required members deduplicate": {
			// Draft 7 requires the required array's elements to be unique; a
			// repeated member would make the emitted document fail
			// metaschema validation.
			input: stringtest.Input(`
				# @schema
				# required: [a, a]
				# @schema
				foo:
				  a: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				foo, ok := props["foo"].(map[string]any)
				require.True(t, ok)

				req, ok := foo["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"a"}, req)
			},
		},
		"root block above explicit document start marker": {
			// The goccy parser turns a comment header above "---" into a
			// comment-only document, so the root block never reaches
			// Annotate; the ForContent leading-comment scan must pick it up,
			// matching upstream's document head comment check.
			input: stringtest.Input(`
				# @schema.root
				# title: MyChart
				# @schema.root
				---
				first: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "MyChart", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, props, "first")
			},
		},
		"block description not set when explicit description exists": {
			input: stringtest.Input(`
				# Some comment
				# @schema
				# type: string
				# description: Explicit desc
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Explicit desc", f["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(dadav.New()),
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

func TestHelmSchemaRootAnnotation(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		opts  []magicschema.Option
		want  func(*testing.T, map[string]any)
	}{
		"root title and description propagate": {
			input: stringtest.Input(`
				# @schema.root
				# title: My Chart
				# description: A chart description
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "My Chart", got["title"])
				assert.Equal(t, "A chart description", got["description"])
			},
		},
		"inline schema annotation inside root block is skipped": {
			// An inline "# @schema key:value" (helm-values-schema format)
			// landing inside a root block must be skipped, not appended into
			// the root YAML where its '@' makes goccy reject the whole block
			// and drop the legitimate title and description with it.
			input: stringtest.Input(`
				# @schema.root
				# title: My Chart
				# @schema type:string
				# description: A chart description
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "My Chart", got["title"])
				assert.Equal(t, "A chart description", got["description"])
			},
		},
		"cli flags override root values": {
			input: stringtest.Input(`
				# @schema.root
				# title: Root Title
				# description: Root Description
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			opts: []magicschema.Option{
				magicschema.WithTitle("CLI Title"),
				magicschema.WithDescription("CLI Description"),
			},
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "CLI Title", got["title"])
				assert.Equal(t, "CLI Description", got["description"])
			},
		},
		"root x-custom annotations propagate": {
			input: stringtest.Input(`
				# @schema.root
				# x-generated-by: magic_schema
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "magic_schema", got["x-generated-by"])
			},
		},
		"root deprecated propagates": {
			input: stringtest.Input(`
				# @schema.root
				# deprecated: true
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, true, got["deprecated"])
			},
		},
		"root additionalProperties override": {
			input: stringtest.Input(`
				# @schema.root
				# additionalProperties: false
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, false, got["additionalProperties"])
			},
		},
		"strict outranks root additionalProperties": {
			// CLI-level settings override annotator values, so a root
			// block cannot reopen a schema the user locked down.
			input: stringtest.Input(`
				# @schema.root
				# additionalProperties: true
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			opts: []magicschema.Option{
				magicschema.WithStrict(true),
			},
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, false, got["additionalProperties"])
			},
		},
		"root marker inside schema block is junk": {
			// A bare @schema.root line between @schema delimiters must
			// not toggle root state and swallow the rest of the block.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema.root
				# title: mytitle
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				replicas, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				// Both lines around the junk marker stay in the block.
				assert.Equal(t, "string", replicas["type"])
				assert.Equal(t, "mytitle", replicas["title"])

				// Nothing leaks into the root schema.
				assert.Nil(t, got["title"])
			},
		},
		"root block after unclosed schema block is not absorbed": {
			// Inside an unclosed @schema block a bare @schema.root marker
			// keeps its delimiter role, so the root block's fields reach the
			// root schema instead of leaking into the property schema.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema.root
				# title: RootTitle
				# @schema.root
				first: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "RootTitle", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				first, ok := props["first"].(map[string]any)
				require.True(t, ok)

				// Unclosed @schema content up to the root block still applies.
				assert.Equal(t, "string", first["type"])
				// Root content must not leak into the property schema.
				assert.Nil(t, first["title"])
			},
		},
		"unclosed root block is ignored": {
			// A @schema.root block still open when the comment ends is
			// silently ignored, unlike unclosed @schema blocks which are
			// processed best-effort.
			input: stringtest.Input(`
				# @schema.root
				# title: RootTitle
				first: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Empty(t, got["title"])
			},
		},
		"unclosed root block after unclosed schema block is ignored": {
			// The root marker inside the unclosed @schema block claims the
			// following lines for a root block, and that root block is itself
			// unclosed, so its content applies nowhere.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema.root
				# title: RootTitle
				first: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Empty(t, got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				first, ok := props["first"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", first["type"])
				assert.Nil(t, first["title"])
			},
		},
		"root block closed by a schema delimiter still applies": {
			// A @schema delimiter closes an open root block (root content
			// cannot extend into schema blocks), so the content before it is
			// kept; only a root block still open at the end of the comment is
			// unclosed and ignored.
			input: stringtest.Input(`
				# @schema.root
				# title: RootTitle
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "RootTitle", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
			},
		},
		"non-propagated fields ignored from root": {
			input: stringtest.Input(`
				# @schema.root
				# type: string
				# enum: [a, b]
				# title: Root Title
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				// Title should propagate.
				assert.Equal(t, "Root Title", got["title"])
				// Type should NOT propagate from root (it should be "object" from structure).
				assert.Equal(t, "object", got["type"])
				// Enum should NOT propagate from root.
				assert.Nil(t, got["enum"])
			},
		},
		"root $ref propagates": {
			input: stringtest.Input(`
				# @schema.root
				# $ref: "#/definitions/root"
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "#/definitions/root", got["$ref"])
				// $ref governs the document; the structural type and
				// properties Draft 7 would ignore beside it are dropped.
				assert.NotContains(t, got, "type")
				assert.NotContains(t, got, "properties")
			},
		},
		"root examples propagate": {
			input: stringtest.Input(`
				# @schema.root
				# examples:
				#   - name: example1
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				examples, ok := got["examples"].([]any)
				require.True(t, ok)
				assert.Len(t, examples, 1)
			},
		},
		"root readOnly propagates": {
			input: stringtest.Input(`
				# @schema.root
				# readOnly: true
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, true, got["readOnly"])
			},
		},
		"root writeOnly propagates": {
			input: stringtest.Input(`
				# @schema.root
				# writeOnly: true
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, true, got["writeOnly"])
			},
		},
		"root pattern not propagated": {
			input: stringtest.Input(`
				# @schema.root
				# pattern: "^[a-z]+$"
				# title: My Chart
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "My Chart", got["title"])
				// Pattern should NOT propagate from root.
				assert.Nil(t, got["pattern"])
			},
		},
		"root numeric constraints not propagated": {
			input: stringtest.Input(`
				# @schema.root
				# minimum: 1
				# maximum: 100
				# title: Chart
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "Chart", got["title"])
				// Numeric constraints should NOT propagate.
				assert.Nil(t, got["minimum"])
				assert.Nil(t, got["maximum"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := append([]magicschema.Option{
				magicschema.WithAnnotators(dadav.New()),
			}, tc.opts...)

			gen := magicschema.NewGenerator(opts...)
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

// firstMappingValue parses input with comments and returns the document's
// first mapping value node, which carries the head comment root extraction
// reads.
func firstMappingValue(t *testing.T, input string) *ast.MappingValueNode {
	t.Helper()

	file, err := parser.ParseBytes([]byte(input), parser.ParseComments)
	require.NoError(t, err)
	require.NotEmpty(t, file.Docs)

	switch body := file.Docs[0].Body.(type) {
	case *ast.MappingNode:
		require.NotEmpty(t, body.Values)

		return body.Values[0]

	case *ast.MappingValueNode:
		return body
	}

	require.FailNow(t, "document body is not a mapping")

	return nil
}

func TestHelmSchemaRootSchemaUnfiltered(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, *jsonschema.Schema)
	}{
		"root schema carries non-propagated fields": {
			// The root schema stores the full parse; the generator's
			// applyRootAnnotations is the single propagation gate, so direct
			// RootSchema callers see every field the block sets while the
			// generated document receives only the documented subset (see
			// TestHelmSchemaRootAnnotation).
			input: stringtest.Input(`
				# @schema.root
				# title: Chart
				# type: string
				# pattern: "^[a-z]+$"
				# minimum: 1
				# @schema.root
				replicas: 3
			`),
			want: func(t *testing.T, root *jsonschema.Schema) {
				t.Helper()

				require.NotNil(t, root)
				assert.Equal(t, "Chart", root.Title)
				assert.Equal(t, "string", root.Type)
				assert.Equal(t, "^[a-z]+$", root.Pattern)

				require.NotNil(t, root.Minimum)
				assert.InDelta(t, float64(1), *root.Minimum, 0.001)
			},
		},
		"no root block leaves the root schema nil": {
			input: stringtest.Input(`
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, root *jsonschema.Schema) {
				t.Helper()
				assert.Nil(t, root)
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ann := dadav.New()
			ann.Annotate(firstMappingValue(t, tc.input), "replicas")

			tc.want(t, ann.RootSchema())
		})
	}
}

func TestHelmSchemaAnnotatorEdgeCases(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"if/then/else conditional schemas": {
			input: stringtest.Input(`
				# @schema
				# if:
				#   properties:
				#     mode:
				#       const: advanced
				# then:
				#   required: [config]
				# else:
				#   properties:
				#     config:
				#       type: string
				# @schema
				mode: simple
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["mode"].(map[string]any)
				require.True(t, ok)

				assert.NotNil(t, m["if"], "if should be present")
				assert.NotNil(t, m["then"], "then should be present")
				assert.NotNil(t, m["else"], "else should be present")
			},
		},
		"allOf composition": {
			input: stringtest.Input(`
				# @schema
				# allOf:
				#   - type: object
				#   - required: [name]
				# @schema
				config:
				  name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				allOf, ok := c["allOf"].([]any)
				require.True(t, ok)
				assert.Len(t, allOf, 2)
			},
		},
		"anyOf composition": {
			input: stringtest.Input(`
				# @schema
				# anyOf:
				#   - type: string
				#   - type: integer
				# @schema
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				anyOf, ok := v["anyOf"].([]any)
				require.True(t, ok)
				assert.Len(t, anyOf, 2)
			},
		},
		"oneOf composition": {
			input: stringtest.Input(`
				# @schema
				# oneOf:
				#   - type: string
				#   - type: integer
				# @schema
				val: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				oneOf, ok := v["oneOf"].([]any)
				require.True(t, ok)
				assert.Len(t, oneOf, 2)
			},
		},
		"contentEncoding and contentMediaType": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# contentEncoding: base64
				# contentMediaType: application/octet-stream
				# @schema
				cert: dGVzdA==
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cert"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "base64", c["contentEncoding"])
				assert.Equal(t, "application/octet-stream", c["contentMediaType"])
			},
		},
		"$comment field": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# $comment: Internal use only
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Internal use only", f["$comment"])
			},
		},
		"multiple type array": {
			input: stringtest.Input(`
				# @schema
				# type: [string, "null"]
				# @schema
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
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"unquoted null in type array": {
			// YAML null in a type array (without quotes) should be
			// interpreted as the "null" JSON Schema type string, matching
			// upstream behavior.
			input: stringtest.Input(`
				# @schema
				# type: [string, null]
				# @schema
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
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"definitions with ref": {
			input: stringtest.Input(`
				# @schema
				# definitions:
				#   port:
				#     type: integer
				#     minimum: 1
				#     maximum: 65535
				# $ref: "#/definitions/port"
				# @schema
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "#/definitions/port", p["$ref"])

				defs, ok := p["definitions"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, defs, "port")
			},
		},
		"$defs with ref": {
			input: stringtest.Input(`
				# @schema
				# $defs:
				#   port:
				#     type: integer
				#     minimum: 1
				#     maximum: 65535
				# $ref: "#/$defs/port"
				# @schema
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "#/$defs/port", p["$ref"])

				defs, ok := p["$defs"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, defs, "port")
			},
		},
		"definitions and $defs in one block": {
			// The jsonschema marshaler rejects a schema carrying both
			// definitions and $defs, which would fail the whole document's
			// marshal (checked by the harness below). The definitions key
			// takes precedence and the $defs entry drops, independent of the
			// order the block's keys are iterated in.
			input: stringtest.Input(`
				# @schema
				# definitions:
				#   a:
				#     type: string
				# $defs:
				#   b:
				#     type: string
				# @schema
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				defs, ok := k["definitions"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, defs, "a")

				assert.Nil(t, k["$defs"])
			},
		},
		"unparseable $defs keeps definitions": {
			// A $defs value that is not a mapping never displaces a valid
			// definitions sibling, in either iteration order.
			input: stringtest.Input(`
				# @schema
				# $defs: not-a-map
				# definitions:
				#   a:
				#     type: string
				# @schema
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				defs, ok := k["definitions"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, defs, "a")

				assert.Nil(t, k["$defs"])
			},
		},
		"all-unparseable definitions keeps $defs": {
			// A definitions map whose entries all fail the sub-schema round
			// trip carries no usable value, so it never displaces a valid
			// $defs sibling: an unparseable winner cannot drop a valid loser.
			input: stringtest.Input(`
				# @schema
				# definitions:
				#   bad: 7
				# $defs:
				#   good:
				#     type: string
				# @schema
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				defs, ok := k["$defs"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, defs, "good")

				assert.Nil(t, k["definitions"])
			},
		},
		"uniqueItems constraint": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# uniqueItems: true
				# @schema
				tags:
				  - a
				  - b
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tags, ok := props["tags"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, tags["uniqueItems"])
			},
		},
		"minProperties and maxProperties": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# minProperties: 1
				# maxProperties: 10
				# @schema
				labels:
				  app: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				labels, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(1), labels["minProperties"], 0.001)
				assert.InDelta(t, float64(10), labels["maxProperties"], 0.001)
			},
		},
		"deprecated flag": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# deprecated: true
				# @schema
				oldField: val
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
		"readOnly flag": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# readOnly: true
				# @schema
				status: running
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["status"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, f["readOnly"])
			},
		},
		"writeOnly flag": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# writeOnly: true
				# @schema
				password: secret
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["password"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, f["writeOnly"])
			},
		},
		"required false does not mark required": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# required: false
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Field should NOT be in the required array.
				if req, ok := got["required"].([]any); ok {
					assert.NotContains(t, req, "field")
				}
			},
		},
		"empty schema block infers type": {
			input: stringtest.Input(`
				# @schema
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Empty block produces no annotation, so the
				// generator infers type from the YAML value.
				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"not composition": {
			input: stringtest.Input(`
				# @schema
				# not:
				#   type: "null"
				# @schema
				val: something
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				notVal, ok := v["not"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "null", notVal["type"])
			},
		},
		"$id metadata": {
			input: stringtest.Input(`
				# @schema
				# $id: "#/config"
				# @schema
				config:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "#/config", c["$id"])
			},
		},
		"default value": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# default: hello
				# @schema
				greeting: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["greeting"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "hello", f["default"])
			},
		},
		"const value": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# const: v1
				# @schema
				version: v1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["version"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "v1", f["const"])
			},
		},
		"format field": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# format: email
				# @schema
				email: test@example.com
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["email"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "email", f["format"])
			},
		},
		"items sub-schema": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# items:
				#   type: string
				# @schema
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
		"properties within block": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# properties:
				#   name:
				#     type: string
				#   age:
				#     type: integer
				# @schema
				person:
				  name: Alice
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				person, ok := props["person"].(map[string]any)
				require.True(t, ok)

				innerProps, ok := person["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, innerProps, "name")
				assert.Contains(t, innerProps, "age")
			},
		},
		"patternProperties within block": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# patternProperties:
				#   "^x-":
				#     type: string
				# @schema
				annotations: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				ann, ok := props["annotations"].(map[string]any)
				require.True(t, ok)

				pp, ok := ann["patternProperties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, pp, "^x-")
			},
		},
		"propertyNames within block": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# propertyNames:
				#   pattern: "^[a-z]+$"
				# @schema
				data: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["data"].(map[string]any)
				require.True(t, ok)

				pn, ok := d["propertyNames"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "^[a-z]+$", pn["pattern"])
			},
		},
		"contains sub-schema": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# contains:
				#   type: string
				#   const: required-item
				# @schema
				mixed:
				  - required-item
				  - 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["mixed"].(map[string]any)
				require.True(t, ok)

				contains, ok := m["contains"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", contains["type"])
				assert.Equal(t, "required-item", contains["const"])
			},
		},
		"items tuple form is preserved as an array of schemas": {
			// Draft 7 tuple validation: items as an array of schemas. It must be
			// kept as a positional items array, not dropped -- dropping it loses
			// the tuple constraint while a sibling additionalItems survives,
			// yielding an over-restrictive, fail-closed schema.
			input: stringtest.Input(`
				# @schema
				# type: array
				# items:
				#   - type: string
				#   - type: integer
				# @schema
				tuple:
				  - first
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tup, ok := props["tuple"].(map[string]any)
				require.True(t, ok)

				items, ok := tup["items"].([]any)
				require.True(t, ok, "tuple items must marshal as an array of schemas")
				require.Len(t, items, 2)

				first, ok := items[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", first["type"])

				second, ok := items[1].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", second["type"])
			},
		},
		"additionalItems sub-schema": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# additionalItems:
				#   type: string
				# @schema
				tuple:
				  - first
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tup, ok := props["tuple"].(map[string]any)
				require.True(t, ok)

				ai, ok := tup["additionalItems"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", ai["type"])
			},
		},
		"additionalItems boolean false": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# additionalItems: false
				# @schema
				tuple:
				  - first
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tup, ok := props["tuple"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, tup["additionalItems"])
			},
		},
		"additionalItems boolean true": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# additionalItems: true
				# @schema
				tuple:
				  - first
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				tup, ok := props["tuple"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, tup["additionalItems"])
			},
		},
		"dependencies with schema value": {
			input: stringtest.Input(`
				# @schema
				# dependencies:
				#   mode:
				#     properties:
				#       config:
				#         type: object
				#     required: [config]
				# @schema
				feature:
				  mode: advanced
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["feature"].(map[string]any)
				require.True(t, ok)

				deps, ok := f["dependencies"].(map[string]any)
				require.True(t, ok)

				mode, ok := deps["mode"].(map[string]any)
				require.True(t, ok)
				assert.NotNil(t, mode["properties"])
			},
		},
		"required as array of strings": {
			input: stringtest.Input(`
				# @schema
				# required: [name, version]
				# @schema
				metadata:
				  name: chart
				  version: "1.0"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["metadata"].(map[string]any)
				require.True(t, ok)

				req, ok := m["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
				assert.Contains(t, req, "version")
			},
		},
		"required and dependencies drop non-string list members": {
			// Both annotations share the same narrowing policy for malformed
			// string lists: keep the string members, drop the rest.
			input: stringtest.Input(`
				# @schema
				# required: [name, 1]
				# dependencies:
				#   name: [version, 2]
				# @schema
				metadata:
				  name: chart
				  version: "1.0"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["metadata"].(map[string]any)
				require.True(t, ok)

				req, ok := m["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"name"}, req)

				deps, ok := m["dependencies"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, []any{"version"}, deps["name"])
			},
		},
		"unannotated key not marked required": {
			input: stringtest.Input(`
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Unannotated keys should never be required per fail-open.
				if req, ok := got["required"].([]any); ok {
					assert.NotContains(t, req, "field")
				}
			},
		},
		"examples array": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# examples: [foo, bar]
				# @schema
				field: baz
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				examples, ok := f["examples"].([]any)
				require.True(t, ok)
				assert.Len(t, examples, 2)
				assert.Contains(t, examples, "foo")
				assert.Contains(t, examples, "bar")
			},
		},
		"exclusiveMinimum and exclusiveMaximum": {
			input: stringtest.Input(`
				# @schema
				# type: number
				# exclusiveMinimum: 0
				# exclusiveMaximum: 100
				# @schema
				val: 50
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(0), v["exclusiveMinimum"], 0.001)
				assert.InDelta(t, float64(100), v["exclusiveMaximum"], 0.001)
			},
		},
		"multipleOf": {
			input: stringtest.Input(`
				# @schema
				# type: integer
				# multipleOf: 5
				# @schema
				count: 10
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(5), c["multipleOf"], 0.001)
			},
		},
		"multiple x-custom annotations": {
			input: stringtest.Input(`
				# @schema
				# x-order: 1
				# x-category: basic
				# x-hidden: true
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.InDelta(t, float64(1), f["x-order"], 0.001)
				assert.Equal(t, "basic", f["x-category"])
				assert.Equal(t, true, f["x-hidden"])
			},
		},
		"additionalProperties as schema": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# additionalProperties:
				#   type: string
				# @schema
				data:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["data"].(map[string]any)
				require.True(t, ok)

				ap, ok := d["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", ap["type"])
			},
		},
		"multiple schema blocks concatenated": {
			// Toggle behavior means multiple @schema blocks are concatenated.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				# @schema
				# enum: [a, b]
				# @schema
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", k["type"])

				enum, ok := k["enum"].([]any)
				require.True(t, ok)
				assert.Contains(t, enum, "a")
				assert.Contains(t, enum, "b")
			},
		},
		"bare type null falls through to inference": {
			// Upstream: bare `type: null` (without quotes) produces nil/empty
			// string from YAML unmarshal, which falls through to type inference.
			input: stringtest.Input(`
				# @schema
				# type: null
				# @schema
				val: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Bare null type falls through to inference -- value is integer.
				assert.Equal(t, "integer", v["type"])
			},
		},
		"three schema blocks concatenated": {
			// Three toggled blocks should concatenate all content.
			input: stringtest.Input(`
				# @schema
				# type: integer
				# @schema
				# @schema
				# minimum: 0
				# @schema
				# @schema
				# maximum: 100
				# @schema
				val: 50
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", v["type"])
				assert.InDelta(t, float64(0), v["minimum"], 0.001)
				assert.InDelta(t, float64(100), v["maximum"], 0.001)
			},
		},
		"space-separated @schema .root is the inline form, not a root delimiter": {
			// "@schema .root" with a space is the helm-values-schema inline
			// form, which the block annotator ignores. It must not open a
			// root block and swallow the following description, the way the
			// contiguous "@schema.root" delimiter would.
			input: stringtest.Input(`
				# @schema .root
				# My real description
				# @schema
				# type: integer
				# @schema
				val: 50
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", v["type"])
				assert.Equal(t, "My real description", v["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(dadav.New()),
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

func TestHelmSchemaAnnotatorAlignment(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		opts  []magicschema.Option
		want  func(*testing.T, map[string]any)
	}{
		"unclosed schema block still processes content": {
			// Best-effort: unclosed @schema blocks should still yield parsed content.
			input: stringtest.Input(`
				# @schema
				# type: string
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"malformed yaml in block returns nil annotation": {
			// Best-effort: malformed YAML in a @schema block should log a warning
			// and fall back to type inference from the value.
			input: stringtest.Input(`
				# @schema
				# type: [invalid yaml: {{{
				# @schema
				field: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Malformed block is skipped; type inferred from YAML value.
				assert.Equal(t, "integer", f["type"])
			},
		},
		"inline @schema content not treated as block delimiter": {
			// @schema lines with content after them (losisin format) should be
			// ignored by this annotator.
			input: stringtest.Input(`
				# @schema type:string;required
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// No @schema block found, type inferred from YAML value.
				assert.Equal(t, "string", f["type"])

				// Should NOT be required (losisin format not parsed here).
				if req, ok := got["required"].([]any); ok {
					assert.NotContains(t, req, "field")
				}
			},
		},
		"title field in schema block": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# title: My Field Title
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "My Field Title", f["title"])
			},
		},
		"additionalProperties true in block": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# additionalProperties: true
				# @schema
				config:
				  key: value
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
		"fail-open: additionalProperties defaults to true on objects": {
			// Default additionalProperties to true (unlike dadav which defaults false).
			input: stringtest.Input(`
				# @schema
				# type: object
				# @schema
				config:
				  key: value
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
		"strict mode sets additionalProperties false on objects": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# @schema
				config:
				  key: value
			`),
			opts: []magicschema.Option{
				magicschema.WithStrict(true),
			},
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, false, c["additionalProperties"])
			},
		},
		"fail-open: unannotated keys never required": {
			// Dadav marks all unannotated keys as required; we don't.
			input: stringtest.Input(`
				name: my-release
				replicas: 3
				image:
				  repository: nginx
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// No properties should be required.
				req, ok := got["required"].([]any)
				if ok {
					assert.Empty(t, req, "unannotated keys should not be required")
				}
			},
		},
		"fail-open: partially annotated keys only mark explicit required": {
			// Mix of annotated and unannotated keys. Only explicitly required keys
			// should appear in the required array.
			input: stringtest.Input(`
				# @schema
				# type: string
				# required: true
				# @schema
				name: test
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				req, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Contains(t, req, "name")
				assert.NotContains(t, req, "replicas")
			},
		},
		"ref preserved as-is without file resolution": {
			// Ref values are preserved as-is without file resolution.
			input: stringtest.Input(`
				# @schema
				# $ref: "./schemas/base.json#/definitions/port"
				# @schema
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "./schemas/base.json#/definitions/port", p["$ref"])
			},
		},
		"description keeps paragraphs separated by bare hash line": {
			// Upstream's leadingCommentsRemover splits only on physical blank
			// lines ("\n{2,}"); a "#"-only line is kept as a blank line in the
			// joined description, so both paragraphs survive.
			input: stringtest.Input(`
				# Section header
				# More context
				#
				# Actual description for replicas
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"Section header",
					"More context",
					"",
					"Actual description for replicas",
				), r["description"])
			},
		},
		"description with helm-docs double-dash prefix stripped": {
			// Helm-docs-style "-- " prefixes are also removed from descriptions.
			input: stringtest.Input(`
				# -- Description with helm-docs prefix
				# @schema
				# type: string
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description with helm-docs prefix", f["description"])
			},
		},
		"explicit description in block overrides comment": {
			// Description from block takes priority over comment.
			input: stringtest.Input(`
				# Comment description
				# @schema
				# type: string
				# description: Block description
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Block description", f["description"])
			},
		},
		"double-hash comments parse correctly": {
			// Up to two leading hash characters plus optional space are stripped.
			input: stringtest.Input(`
				## @schema
				## type: integer
				## minimum: 0
				## @schema
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", c["type"])
				assert.InDelta(t, float64(0), c["minimum"], 0.001)
			},
		},
		"dependencies mixed string-array and schema values": {
			input: stringtest.Input(`
				# @schema
				# dependencies:
				#   enabled: [host, port]
				#   mode:
				#     properties:
				#       config:
				#         type: object
				# @schema
				service:
				  host: localhost
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				s, ok := props["service"].(map[string]any)
				require.True(t, ok)

				deps, ok := s["dependencies"].(map[string]any)
				require.True(t, ok)

				// String-array dependency.
				enabled, ok := deps["enabled"].([]any)
				require.True(t, ok)
				assert.Contains(t, enabled, "host")
				assert.Contains(t, enabled, "port")

				// Schema dependency.
				mode, ok := deps["mode"].(map[string]any)
				require.True(t, ok)
				assert.NotNil(t, mode["properties"])
			},
		},
		"root block non-propagated fields ignored": {
			// Non-propagated fields (type, enum, pattern, numeric constraints)
			// must be ignored from root blocks.
			input: stringtest.Input(`
				# @schema.root
				# title: Chart
				# type: string
				# enum: [a, b]
				# pattern: "^[a-z]"
				# minimum: 0
				# maximum: 100
				# minLength: 1
				# maxLength: 50
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "Chart", got["title"])
				assert.Equal(t, "object", got["type"])
				assert.Nil(t, got["enum"])
				assert.Nil(t, got["pattern"])
				assert.Nil(t, got["minimum"])
				assert.Nil(t, got["maximum"])
				assert.Nil(t, got["minLength"])
				assert.Nil(t, got["maxLength"])
			},
		},
		"schema.root and schema blocks adjacent in same comment": {
			input: stringtest.Input(`
				# @schema.root
				# title: Chart
				# description: My Chart
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "Chart", got["title"])
				assert.Equal(t, "My Chart", got["description"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
			},
		},
		"unknown schema keys silently ignored": {
			// Best-effort -- unknown keys should not cause errors.
			input: stringtest.Input(`
				# @schema
				# type: string
				# unknownField: something
				# anotherUnknown: 42
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
				// Unknown keys should not appear in output.
				assert.Nil(t, f["unknownField"])
				assert.Nil(t, f["anotherUnknown"])
			},
		},
		"annotation parse failure is not fatal": {
			// Annotation parse failures are not fatal.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				valid: test
				# @schema
				# : invalid yaml key
				# @schema
				other: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Valid annotation should still work.
				v, ok := props["valid"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", v["type"])

				// The other field should still be present (inferred).
				assert.Contains(t, props, "other")
			},
		},
		"nested object with annotations": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# @schema
				image:
				  # @schema
				  # type: string
				  # pattern: "^[a-z]"
				  # @schema
				  repository: nginx
				  # @schema
				  # type: string
				  # @schema
				  tag: latest
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				img, ok := props["image"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", img["type"])

				innerProps, ok := img["properties"].(map[string]any)
				require.True(t, ok)

				repo, ok := innerProps["repository"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", repo["type"])
				assert.Equal(t, "^[a-z]", repo["pattern"])

				tag, ok := innerProps["tag"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", tag["type"])
			},
		},
		"array with items annotation": {
			input: stringtest.Input(`
				# @schema
				# type: array
				# items:
				#   type: object
				#   properties:
				#     name:
				#       type: string
				#     port:
				#       type: integer
				# @schema
				services:
				  - name: web
				    port: 80
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				s, ok := props["services"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", s["type"])

				items, ok := s["items"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "object", items["type"])

				itemProps, ok := items["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, itemProps, "name")
				assert.Contains(t, itemProps, "port")
			},
		},
		"empty object with annotation": {
			input: stringtest.Input(`
				# @schema
				# type: object
				# additionalProperties:
				#   type: string
				# @schema
				labels: {}
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])

				ap, ok := l["additionalProperties"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", ap["type"])
			},
		},
		"schema.root on non-first key is ignored": {
			// A @schema.root block on a non-first key should be ignored.
			input: stringtest.Input(`
				# @schema
				# type: integer
				# @schema
				replicas: 3
				# @schema.root
				# title: Should Be Ignored
				# @schema.root
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Title from @schema.root on non-first key should not propagate.
				assert.Empty(t, got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "integer", r["type"])

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", n["type"])
			},
		},
		"schema.root with trailing content is not a delimiter": {
			// @schema.root lines are delimiters only when bare (no trailing
			// content), consistent with @schema block delimiter handling.
			input: stringtest.Input(`
				# @schema.root something extra
				# title: Should Be Ignored
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// The @schema.root with trailing content should not open a block,
				// so no root title should be propagated.
				assert.Empty(t, got["title"])
			},
		},
		"schema.root on first key is parsed": {
			// Root blocks on the first key should be properly parsed.
			input: stringtest.Input(`
				# @schema.root
				# title: First Key Root
				# description: Root desc
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "First Key Root", got["title"])
				assert.Equal(t, "Root desc", got["description"])
			},
		},
		"interleaved root block content does not leak into schema block": {
			// @schema.root content is stripped before @schema block parsing.
			// Root content between root delimiters must not leak into schema blocks.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				# @schema.root
				# title: My Chart
				# @schema.root
				# @schema
				# pattern: "^[a-z]+$"
				# @schema
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "My Chart", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", k["type"])
				assert.Equal(t, "^[a-z]+$", k["pattern"])
				// Root title must not leak into the property schema.
				assert.Empty(t, k["title"])
			},
		},
		"detached annotation block does not apply to the following key": {
			// The parser merges blank-line-separated comment blocks into one
			// head comment group; a stale block (a commented-out previous key
			// with its annotations) separated from the key by a blank line
			// must not apply, matching upstream's leadingCommentsRemover.
			// Applying it would emit type string and reject the key's own
			// integer value (fail closed).
			input: stringtest.Input(`
				# Example configuration (disabled):
				# @schema
				# type: string
				# @schema
				# oldKey: value

				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				// Structural inference wins; the stale block and its prose
				// leave no trace.
				assert.Equal(t, "integer", r["type"])
				assert.Empty(t, r["description"])
			},
		},
		"file header separated by blank line does not leak into description": {
			input: stringtest.Input(`
				# File header comment

				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.Empty(t, r["description"])
			},
		},
		"adjacent block applies despite detached stale block above": {
			// Only the run touching the key is parsed, so the adjacent block
			// wins and the detached one is ignored -- the upstream sees the
			// same text after leadingCommentsRemover stripping.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema

				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
			},
		},
		"root block separated by blank line still applies": {
			// Root extraction reads the full merged comment group: upstream
			// exempts root parsing from leadingCommentsRemover and checks the
			// document head comment for the blank-line-separated case.
			input: stringtest.Input(`
				# @schema.root
				# title: My Chart
				# @schema.root

				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "My Chart", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", r["type"])
				assert.Empty(t, r["description"])
			},
		},
		"root block content excluded from description extraction": {
			// Root block content between delimiters must not become descriptions.
			input: stringtest.Input(`
				# @schema.root
				# title: Chart Title
				# @schema.root
				# Actual description
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "Chart Title", got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				// Description should be "Actual description", not "title: Chart Title".
				assert.Equal(t, "Actual description", r["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := append([]magicschema.Option{
				magicschema.WithAnnotators(dadav.New()),
			}, tc.opts...)

			gen := magicschema.NewGenerator(opts...)
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

func TestHelmSchemaAnnotatorForContentResetsState(t *testing.T) {
	t.Parallel()

	// When processing multiple files, ForContent returns a fresh clone
	// so that @schema.root blocks in the second file are processed correctly.
	ann := dadav.New()

	// First file: has a @schema.root block.
	file1 := []byte(stringtest.Input(`
		# @schema.root
		# title: First File
		# @schema.root
		# @schema
		# type: integer
		# @schema
		replicas: 3
	`) + "\n")

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(ann),
	)
	schema1, err := gen.Generate(file1)
	require.NoError(t, err)

	out1, err := json.Marshal(schema1)
	require.NoError(t, err)

	var got1 map[string]any

	require.NoError(t, json.Unmarshal(out1, &got1))
	assert.Equal(t, "First File", got1["title"])

	// Second file: has a different @schema.root block.
	// ForContent returns a fresh clone so this file's root block is processed.
	file2 := []byte(stringtest.Input(`
		# @schema.root
		# title: Second File
		# @schema.root
		# @schema
		# type: string
		# @schema
		name: test
	`) + "\n")

	schema2, err := gen.Generate(file2)
	require.NoError(t, err)

	out2, err := json.Marshal(schema2)
	require.NoError(t, err)

	var got2 map[string]any

	require.NoError(t, json.Unmarshal(out2, &got2))
	assert.Equal(t, "Second File", got2["title"])
}

func TestHelmSchemaAnnotatorUnit(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, *dadav.Annotator, map[string]any)
	}{
		"Name returns helm-schema": {
			input: stringtest.Input(`
				field: value
			`),
			want: func(t *testing.T, ann *dadav.Annotator, _ map[string]any) {
				t.Helper()
				assert.Equal(t, "helm-schema", ann.Name())
			},
		},
		"RootSchema returns nil when no root block": {
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()
				assert.Empty(t, got["title"])
			},
		},
		"RootSchema returns schema when root block present": {
			input: stringtest.Input(`
				# @schema.root
				# title: Test
				# @schema.root
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()
				assert.Equal(t, "Test", got["title"])
			},
		},
		"multi-line non-annotation comment joined with newlines": {
			input: stringtest.Input(`
				# First line of description
				# Second line of description
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"First line of description",
					"Second line of description",
				), f["description"])
			},
		},
		"description with yaml example keeps newlines and indentation": {
			input: stringtest.Input(`
				# Example config:
				#   foo: bar
				#   baz:
				#     - 1
				# @schema
				# type: object
				# @schema
				field: {}
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"Example config:",
					"  foo: bar",
					"  baz:",
					"    - 1",
				), f["description"])
			},
		},
		"empty root block produces no root schema fields": {
			input: stringtest.Input(`
				# @schema.root
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, ann *dadav.Annotator, got map[string]any) {
				t.Helper()
				// Empty root block should still create a root schema (empty).
				// But because it has no fields, it won't affect the output.
				assert.Empty(t, got["title"])
			},
		},
		"schema.root lines inside schema block are skipped": {
			// @schema.root lines (the delimiters themselves) are skipped
			// inside a @schema block. However, content between @schema.root
			// delimiters that appears inside an @schema block is still treated
			// as block content (the @schema.root lines don't create sub-blocks
			// within @schema blocks).
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema.root
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// The @schema.root line is skipped, so the block only
				// contains "type: string" before the closing delimiter.
				assert.Equal(t, "string", f["type"])
			},
		},
		"required explicitly false via HasRequired pointer": {
			// HasRequired is *bool to distinguish "explicitly false" from
			// "not set".
			input: stringtest.Input(`
				# @schema
				# type: string
				# required: false
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				// Field should NOT appear in required array.
				if req, ok := got["required"].([]any); ok {
					assert.NotContains(t, req, "field")
				}
			},
		},
		"annotate returns nil for non-MappingValueNode": {
			// The annotator should only handle MappingValueNode.
			input: stringtest.Input(`
				field: value
			`),
			want: func(t *testing.T, ann *dadav.Annotator, _ map[string]any) {
				t.Helper()
				// Calling Annotate with a non-MappingValueNode returns nil.
				// We can't easily construct an arbitrary ast.Node in tests,
				// but we verify the Name is correct.
				assert.Equal(t, "helm-schema", ann.Name())
			},
		},
		"root block double-hash delimiters": {
			input: stringtest.Input(`
				## @schema.root
				## title: Double Hash Root
				## @schema.root
				## @schema
				## type: integer
				## @schema
				replicas: 3
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()
				assert.Equal(t, "Double Hash Root", got["title"])
			},
		},
		"schema block with only unknown keys still returns annotation": {
			// Unknown keys are silently ignored but the result
			// should still be an annotation (not nil), so that description
			// extraction works.
			input: stringtest.Input(`
				# Description text
				# @schema
				# unknownKey: value
				# @schema
				field: test
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Type should be inferred from YAML value since block has no type.
				assert.Equal(t, "string", f["type"])
				// Description should still be extracted from non-annotation comment.
				assert.Equal(t, "Description text", f["description"])
			},
		},
		"bare hash separator keeps every description paragraph": {
			// A "#"-only line does not delimit comment groups (only physical
			// blank lines do, and the head-comment run narrowing handles
			// those), so the whole run joins into one multi-paragraph
			// description, matching upstream.
			input: stringtest.Input(`
				# Old section header
				# More old context
				#
				# Line one of actual desc
				# Line two of actual desc
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, stringtest.JoinLF(
					"Old section header",
					"More old context",
					"",
					"Line one of actual desc",
					"Line two of actual desc",
				), f["description"])
			},
		},
		"type as single-element array normalizes to string": {
			// When type is an array with exactly one element, it should be
			// stored as a single Type string, not a Types array.
			input: stringtest.Input(`
				# @schema
				# type: [string]
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"type array with no usable members defers to inference": {
			// A type array whose members are all non-string and non-null
			// (e.g. [1, 2]) yields no usable type strings. The schema must
			// leave Type/Types unset so value inference fills the type,
			// rather than setting an empty Types that collides with the
			// inferred Type and breaks the whole document's marshal.
			input: stringtest.Input(`
				# @schema
				# type: [1, 2]
				# @schema
				field: hello
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Type is inferred from the value, and the schema marshals.
				assert.Equal(t, "string", f["type"])
			},
		},
		"empty type array does not emit an empty type keyword": {
			// type: [] must not surface as the invalid Draft-7 "type": [].
			input: stringtest.Input(`
				# @schema
				# type: []
				# @schema
				field: hello
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
				assert.NotEmpty(t, f["type"], "type must not be an empty array")
			},
		},
		"mixed type array with a non-string member drops the type": {
			// type: [string, 1] cannot narrow to [string] without rejecting an
			// integer the value may take, so the whole type is dropped and the
			// value's type is inferred instead (fail open).
			input: stringtest.Input(`
				# @schema
				# type: [string, 1]
				# @schema
				field: 42
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", f["type"])
			},
		},
		"empty-string type member becomes the null type": {
			// type: [string, ''] would emit the invalid Draft-7 "type": ["",...];
			// the empty string normalizes to the null type instead.
			input: stringtest.Input(`
				# @schema
				# type: [string, '']
				# @schema
				field: x
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"string", "null"}, f["type"])
			},
		},
		"double-hash description outside block stripped correctly": {
			// Double-hash comments used as descriptions must not
			// leave a stray '#' in the description text.
			input: stringtest.Input(`
				## Double hash description
				## @schema
				## type: integer
				## @schema
				field: 42
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Double hash description", f["description"])
			},
		},
		"annotation-like comments not extracted as description": {
			// Comments that look like annotation markers are not
			// extracted as plain descriptions.
			input: stringtest.Input(`
				# @param field [string] A parameter description
				# @schema
				# type: string
				# @schema
				field: value
			`),
			want: func(t *testing.T, _ *dadav.Annotator, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// @param comment should be filtered out, not used as description.
				assert.Empty(t, f["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ann := dadav.New()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(ann),
			)
			schema, err := gen.Generate([]byte(tc.input))
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))
			tc.want(t, ann, got)
		})
	}
}

func TestHelmSchemaUpstreamBehavior(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"const null preserved": {
			// Upstream tracks constWasSet to preserve const: null in JSON.
			// Our jsonschema library uses *any, so nil const emits null when
			// the pointer is non-nil.
			input: stringtest.Input(`
				# @schema
				# type: string
				# const: null
				# @schema
				field:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Const should be present with null value.
				_, hasConst := f["const"]
				assert.True(t, hasConst, "const should be present even when null")
				assert.Nil(t, f["const"], "const value should be null")
			},
		},
		"type array with null element": {
			// Upstream handles null in type arrays by converting !!null tag
			// to the string "null". Our YAML unmarshal handles this via the
			// annotation block content being parsed as YAML.
			input: stringtest.Input(`
				# @schema
				# type: [string, "null"]
				# @schema
				field: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				types, ok := f["type"].([]any)
				require.True(t, ok)
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "null")
			},
		},
		"junk-suffix delimiter toggles block": {
			// Upstream toggles blocks on any "# @schema"-prefixed line, so
			// junk suffixes such as "@schema@" (seen in cilium's
			// values.yaml) still delimit a block. The block content must be
			// parsed and must not leak into the description.
			input: stringtest.Input(`
				# -- Configure the kube-proxy replacement.
				# @schema@
				# type: [string, boolean]
				# @schema@
				field: "false"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				types, ok := f["type"].([]any)
				require.True(t, ok, "type union from the @schema@ block should apply")
				assert.Contains(t, types, "string")
				assert.Contains(t, types, "boolean")

				if desc, hasDesc := f["description"].(string); hasDesc {
					assert.NotContains(t, desc, "type:",
						"block content must not leak into the description")
				}
			},
		},
		"null type inside oneOf branch": {
			// A YAML null inside a nested type array (cilium's
			// envoy.log.defaultLevel) must become the "null" type string,
			// not an empty string, across the ToSubSchema round trip. The
			// single-member array collapses to the scalar type, matching
			// upstream's StringOrArrayOfString marshal.
			input: stringtest.Input(`
				# @schema
				# oneOf:
				# - type: [null]
				# - enum: [trace, debug, info]
				# @schema
				field: ~
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				oneOf, ok := f["oneOf"].([]any)
				require.True(t, ok)
				require.Len(t, oneOf, 2)

				branch, ok := oneOf[0].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "null", branch["type"])
			},
		},
		"duplicate members in nested type array collapse": {
			// A YAML null beside the "null" string in a nested type array
			// normalizes to two "null" members; a JSON Schema type array must
			// have unique members, so the duplicate drops and the remaining
			// member collapses to the scalar type instead of emitting the
			// spec-invalid ["null", "null"].
			input: stringtest.Input(`
				# @schema
				# items:
				#   type: ["null", null]
				# @schema
				field: [a]
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				items, ok := f["items"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "null", items["type"])
			},
		},
		"empty type array in nested schema leaves the type unset": {
			// A nested type: [] must not surface as the invalid Draft-7
			// "type": [] -- an empty type array matches nothing (fail
			// closed). The type is left unset, so the otherwise-empty items
			// schema marshals as the validate-everything boolean form.
			input: stringtest.Input(`
				# @schema
				# type: array
				# items:
				#   type: []
				# @schema
				field:
				  - a
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, true, f["items"],
					"an empty type array must not reach the output")
			},
		},
		"multiple blocks with regular comments between them": {
			// Upstream and our implementation both use toggle semantics.
			// Regular comments between blocks should not interfere.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				# This is a normal comment
				# @schema
				# pattern: "^[a-z]+$"
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
				assert.Equal(t, "^[a-z]+$", f["pattern"])
				// Normal comment between blocks becomes description.
				assert.Equal(t, "This is a normal comment", f["description"])
			},
		},
		"empty annotation block between two valued blocks": {
			// Empty block (toggle on then off) between two real blocks.
			// Content from both real blocks should be concatenated.
			input: stringtest.Input(`
				# @schema
				# type: integer
				# @schema
				# @schema
				# @schema
				# @schema
				# minimum: 0
				# @schema
				val: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", v["type"])
				assert.InDelta(t, float64(0), v["minimum"], 0.001)
			},
		},
		"upstream: no title auto-generation": {
			// Upstream auto-generates title from key name. We deliberately
			// do NOT do this.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				myField: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["myField"].(map[string]any)
				require.True(t, ok)

				// We should NOT auto-generate title (divergence from upstream).
				assert.Empty(t, f["title"])
			},
		},
		"upstream: no default auto-generation": {
			// Upstream auto-generates default from the YAML value. We
			// deliberately do NOT do this.
			input: stringtest.Input(`
				# @schema
				# type: integer
				# @schema
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				// We should NOT auto-generate default (divergence from upstream).
				assert.Nil(t, p["default"])
			},
		},
		"upstream: $ref not resolved": {
			// Upstream resolves relative file $ref paths. We preserve as-is.
			input: stringtest.Input(`
				# @schema
				# $ref: ./schemas/base.json#/definitions/port
				# @schema
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				// $ref should be preserved as-is.
				assert.Equal(t, "./schemas/base.json#/definitions/port", p["$ref"])
			},
		},
		"description from explicit block overrides comment": {
			// Both upstream and our implementation: explicit description in
			// block takes priority over comment-derived description.
			input: stringtest.Input(`
				# Some comment
				# @schema
				# description: Block description
				# type: string
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Block description", f["description"])
			},
		},
		"all root annotation fields propagated": {
			// Verify all supported root fields propagate.
			input: stringtest.Input(`
				# @schema.root
				# title: Root Title
				# description: Root Desc
				# $ref: "#/definitions/root"
				# deprecated: true
				# readOnly: true
				# writeOnly: true
				# x-helm-version: "3.0"
				# examples:
				#   - example1
				# @schema.root
				# @schema
				# type: string
				# @schema
				field: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				assert.Equal(t, "Root Title", got["title"])
				assert.Equal(t, "Root Desc", got["description"])
				assert.Equal(t, "#/definitions/root", got["$ref"])
				assert.Equal(t, true, got["deprecated"])
				assert.Equal(t, true, got["readOnly"])
				assert.Equal(t, true, got["writeOnly"])
				assert.Equal(t, "3.0", got["x-helm-version"])

				examples, ok := got["examples"].([]any)
				require.True(t, ok)
				assert.Len(t, examples, 1)
			},
		},
		"nested object with no annotation recurses": {
			// When an object type is annotated but no properties are
			// specified, child properties should be recursed into from
			// the YAML structure. This matches upstream behavior.
			input: stringtest.Input(`
				# @schema
				# type: object
				# @schema
				config:
				  host: localhost
				  port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				innerProps, ok := c["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, innerProps, "host")
				assert.Contains(t, innerProps, "port")
			},
		},
		"annotated object with properties does not recurse": {
			// When properties are specified in the annotation block,
			// child YAML structure should NOT override them. This matches
			// upstream behavior.
			input: stringtest.Input(`
				# @schema
				# type: object
				# properties:
				#   name:
				#     type: string
				# @schema
				config:
				  host: localhost
				  port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				innerProps, ok := c["properties"].(map[string]any)
				require.True(t, ok)
				// Only the annotation-defined property, not YAML children.
				assert.Contains(t, innerProps, "name")
				assert.NotContains(t, innerProps, "host")
				assert.NotContains(t, innerProps, "port")
			},
		},
		"annotated array with items does not infer from values": {
			// When items are specified in the annotation, YAML array
			// values should not override them. Matches upstream.
			input: stringtest.Input(`
				# @schema
				# type: array
				# items:
				#   type: integer
				# @schema
				vals:
				  - hello
				  - world
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["vals"].(map[string]any)
				require.True(t, ok)

				items, ok := v["items"].(map[string]any)
				require.True(t, ok)
				// Should use annotation items type, not inferred string.
				assert.Equal(t, "integer", items["type"])
			},
		},
		"no type annotation infers from YAML value": {
			// Both upstream and our implementation infer type from YAML
			// when no type annotation is given.
			input: stringtest.Input(`
				# @schema
				# description: A counter
				# @schema
				count: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["count"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", c["type"])
				assert.Equal(t, "A counter", c["description"])
			},
		},
		"default null preserved unlike upstream": {
			// Upstream silently drops default: null because it uses
			// Go's omitempty on an interface{} field. We preserve it
			// via json.RawMessage("null") from DefaultValue.
			input: stringtest.Input(`
				# @schema
				# type: string
				# default: null
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				_, hasDefault := f["default"]
				assert.True(t, hasDefault, "default should be present even when null")
				assert.Nil(t, f["default"], "default value should be null")
			},
		},
		"no $defs rewriting to definitions": {
			// Upstream rewrites $defs to definitions and #/$defs/ refs to
			// #/definitions/. We preserve both as written.
			input: stringtest.Input(`
				# @schema
				# $defs:
				#   myType:
				#     type: string
				# $ref: "#/$defs/myType"
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// $ref should NOT be rewritten (divergence from upstream).
				assert.Equal(t, "#/$defs/myType", f["$ref"])
				// $defs should be preserved, not rewritten to definitions.
				assert.NotNil(t, f["$defs"])
				assert.Nil(t, f["definitions"])
			},
		},
		"no global property injected": {
			// Upstream auto-injects a "global" property of type object.
			// We do not (divergence).
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "global")
			},
		},
		"inline @schema format not treated as delimiter": {
			// Upstream would treat "# @schema type:string" as a delimiter
			// due to HasPrefix matching. We explicitly skip such lines.
			input: stringtest.Input(`
				# @schema type:string;required
				# @schema
				# type: integer
				# @schema
				field: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// The inline @schema line should be ignored; the block
				// annotation with type: integer should apply.
				assert.Equal(t, "integer", f["type"])
			},
		},
		"@schema.root with trailing content not treated as delimiter": {
			// Upstream would treat "# @schema.root trailing" as a delimiter
			// due to HasPrefix matching. We require bare delimiters.
			input: stringtest.Input(`
				# @schema.root trailing content here
				# title: Should Not Apply
				# @schema.root
				# @schema
				# type: integer
				# @schema
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Title should NOT be set since the opening delimiter
				// had trailing content and was rejected.
				assert.Empty(t, got["title"])
			},
		},
		"duplicate keys in schema block use last value": {
			// YAML semantics: duplicate keys in a mapping are resolved
			// by the last occurrence. Upstream relies on yaml.Unmarshal
			// for this behavior; we do the same.
			input: stringtest.Input(`
				# @schema
				# type: string
				# type: integer
				# @schema
				field: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", f["type"])
			},
		},
		"@schema block with only whitespace content": {
			// A block that contains only whitespace/empty lines produces
			// no annotation, falling through to type inference.
			input: stringtest.Input(`
				# @schema
				#
				#
				# @schema
				field: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Empty block means no annotation; type inferred from YAML.
				assert.Equal(t, "string", f["type"])
			},
		},
		"nested mapping root block ignored": {
			// Root blocks in nested mapping's first key should not
			// affect the parent schema. The upstream processes them
			// but silently discards the result for nested mappings.
			input: stringtest.Input(`
				# @schema
				# type: object
				# @schema
				config:
				  # @schema.root
				  # title: Nested Root
				  # @schema.root
				  # @schema
				  # type: string
				  # @schema
				  key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Nested root title should NOT leak to top level.
				assert.Empty(t, got["title"])

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				innerProps, ok := c["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := innerProps["key"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", k["type"])
			},
		},
		"inline @schema with losisin format ignored between blocks": {
			// An inline @schema line (losisin format) between two block
			// annotations should not interfere with block parsing.
			input: stringtest.Input(`
				# @schema
				# type: string
				# @schema
				# @schema type:integer;required
				# @schema
				# pattern: "^[a-z]"
				# @schema
				field: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["field"].(map[string]any)
				require.True(t, ok)

				// Block 1: type: string. Inline @schema skipped. Block 2: pattern.
				assert.Equal(t, "string", f["type"])
				assert.Equal(t, "^[a-z]", f["pattern"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(dadav.New()),
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

func TestHelmSchemaAnnotatorFromFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/helm_schema.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(dadav.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/helm_schema.schema.json", schema)
}

// TestHelmSchemaAnnotatorRealWorld generates a schema for the cilium chart's
// values.yaml, which annotates properties with @schema blocks throughout,
// interleaved with helm-docs comments the annotator must not treat as
// descriptions.
//
// Vendored via `helm show values cilium --repo https://helm.cilium.io
// --version 1.18.7`.
func TestHelmSchemaAnnotatorRealWorld(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/cilium_values.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(dadav.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/cilium_values.schema.json", schema)
}

// TestHelmSchemaAnnotatorNumericCoercion covers the numeric coercers. Non-finite
// floats must be dropped (they break the whole schema's final JSON marshal), and
// integer constraints above the int range must be rejected rather than wrapped
// to a negative value.
func TestHelmSchemaAnnotatorNumericCoercion(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"infinite minimum is dropped": {
			input: stringtest.Input(`
				# @schema
				# minimum: .inf
				# @schema
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				_, has := count["minimum"]
				assert.False(t, has, "non-finite minimum must be dropped")
			},
		},
		"NaN maximum is dropped": {
			input: stringtest.Input(`
				# @schema
				# maximum: .nan
				# @schema
				count: 5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				count, ok := props["count"].(map[string]any)
				require.True(t, ok)

				_, has := count["maximum"]
				assert.False(t, has, "non-finite maximum must be dropped")
			},
		},
		"overflowing maxLength is rejected not wrapped": {
			input: stringtest.Input(`
				# @schema
				# maxLength: 18446744073709551615
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				ml, has := name["maxLength"]
				if has {
					n, ok := ml.(float64)
					require.True(t, ok)
					assert.GreaterOrEqual(t, n, float64(0),
						"an out-of-range maxLength must never wrap to a negative bound")
				}
			},
		},
		"float maxLength of exactly 2^63 is rejected": {
			// A float64 cannot represent MaxInt64; the nearest value is 2^63
			// itself, whose int conversion is implementation-defined
			// (saturating on arm64, wrapping negative on amd64). The bound
			// check must reject it so the same input cannot produce
			// different schemas per architecture.
			input: stringtest.Input(`
				# @schema
				# maxLength: 9223372036854775808.0
				# @schema
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				name, ok := props["name"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, name, "maxLength",
					"a float bound at exactly 2^63 must be dropped, not converted")
			},
		},
		"non-finite enum value is dropped, schema still marshals": {
			input: stringtest.Input(`
				# @schema
				# enum: [.nan, 1]
				# @schema
				count: 5
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
		"non-finite const is dropped": {
			input: stringtest.Input(`
				# @schema
				# const: .inf
				# @schema
				count: 5
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
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(dadav.New()),
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
