package magicschema_test

import (
	"encoding/json"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

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
		"prose extending @section": {
			// Word markers count only as whole tokens: prose whose first
			// word merely extends a marker stays description text.
			input: "@sections of the chart are documented here",
			want:  false,
		},
		"prose extending @default": {
			input: "@defaults are merged with overrides",
			want:  false,
		},
		"prose extending @skip": {
			input: "@skipped keys are listed in the README",
			want:  false,
		},
		"prose extending @param": {
			input: "@parameters are described below",
			want:  false,
		},
		"junk suffix on @schema": {
			// The @schema fence family is deliberately boundary-less:
			// upstream helm-schema fences on any "@schema" prefix.
			input: "@schema@",
			want:  true,
		},
		"old-style helm-docs key path": {
			input: "image.tag -- the image tag",
			want:  true,
		},
		"prose with dotted word before dashes": {
			input: "Use the v1.2 API -- it is stable",
			want:  false,
		},
		"abbreviation e.g. before dashes": {
			// "e.g." leaves a trailing empty dot segment, so it is prose, not
			// a key path, and the description must survive.
			input: "e.g. -- a worked example",
			want:  false,
		},
		"abbreviation i.e. before dashes": {
			input: "i.e. -- that is, the canonical form",
			want:  false,
		},
		"single dotless token before dashes": {
			// A single dotless token is a valid top-level key in upstream
			// helm-docs old-style comments, so the line counts as an
			// annotation marker and the norwoodj scan records it; the
			// fallback extractor suppresses it here so the same comment is
			// not also attributed to the key it happens to sit above.
			input: "note -- be careful with this",
			want:  true,
		},
		"version-like token before dashes": {
			input: "v1.2 -- the API version to target",
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

func TestIsHelmDocsKeyPath(t *testing.T) {
	t.Parallel()

	tcs := map[string]bool{
		"image.tag":                  true,
		"global.image.registry":      true,
		"controller.service.enabled": true,
		"":                           false,
		"replicaCount":               true, // a dotless token is a top-level key
		"note":                       true,
		"e.g.":                       false, // trailing empty segment
		"i.e.":                       false,
		"etc.":                       false,
		"v1.2":                       false, // digit-led segment
		"1.2.3":                      false,
		"a b.c":                      false, // contains whitespace
		"a..b":                       false, // empty middle segment
		".leading":                   false,
		"trailing.":                  false,
	}

	for input, want := range tcs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, want, magicschema.IsHelmDocsKeyPath(input))
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

func TestFlowSequenceInlineCommentBecomesDescription(t *testing.T) {
	t.Parallel()

	// An inline comment on a one-line flow sequence sits on the key's own
	// line, so it documents the array itself -- unlike a block sequence's
	// stowed first-element head comment -- and becomes the description, the
	// same as inline comments on scalars and flow mappings.
	input := "key: [1, 2] # flow seq description\n"

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

	assert.Equal(t, "array", key["type"])
	assert.Equal(t, "flow seq description", key["description"])
}

func TestIsStowedSequenceCommentNilComment(t *testing.T) {
	t.Parallel()

	// A nil comment group is no comment at all: report false rather than
	// dereferencing it, matching the nil-tolerant HeadCommentRun and
	// CollectNodeComments siblings.
	file, err := parser.ParseBytes([]byte("key: [1, 2]\n"), parser.ParseComments)
	require.NoError(t, err)

	var seq ast.Node

	for _, node := range ast.Filter(ast.SequenceType, file.Docs[0]) {
		seq = node
	}

	require.NotNil(t, seq)

	assert.False(t, magicschema.IsStowedSequenceComment(seq, nil))
	assert.False(t, magicschema.IsStowedSequenceComment(nil, nil))
}

func TestTaggedEmptyListElementAddsNullToItems(t *testing.T) {
	t.Parallel()

	// A known tag on an empty scalar holds a null (see inferType), so as a
	// list element it adds "null" to the items type the same way a literal
	// null element does, and the source list validates.
	input := "list:\n  - 1\n  - 2\n  - !!int\n"

	gen := magicschema.NewGenerator()
	schema, err := gen.Generate([]byte(input))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	list, ok := props["list"].(map[string]any)
	require.True(t, ok)

	items, ok := list["items"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, []any{"integer", "null"}, items["type"])
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

func TestHeadCommentRun(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input        string
		key          string
		want         []string
		wantInSchema bool
		wantInRoot   bool
	}{
		"single adjacent run": {
			input: stringtest.Input(`
				# docs
				name: test
			`),
			key:  "name",
			want: []string{"# docs"},
		},
		"no head comment": {
			input: "name: test",
			key:   "name",
			want:  nil,
		},
		"inline comment is not a head run": {
			input: "name: test # inline",
			key:   "name",
			want:  nil,
		},
		"detached block dropped": {
			// The parser merges both blocks into one head comment group; the
			// blank line survives only in token positions, so only the run
			// touching the key comes back.
			input: stringtest.Input(`
				# stale docs

				# real docs
				name: test
			`),
			key:  "name",
			want: []string{"# real docs"},
		},
		"no run touches the key": {
			input: stringtest.Input(`
				# stale docs

				name: test
			`),
			key:  "name",
			want: nil,
		},
		"commented-out child of previous key dropped by column": {
			// The comment sits directly above the key, but its column marks it
			// as a commented-out child of the previous key, not documentation
			// for this one.
			input: stringtest.Input(`
				parent:
				  # commented-out: sibling
				other: 2
			`),
			key:  "other",
			want: nil,
		},
		"outdented comment still documents the key": {
			// A run is discarded only when indented past the key's column; an
			// outdented comment still counts.
			input: stringtest.Input(`
				parent:
				# outdented docs
				  child: 1
			`),
			key:  "child",
			want: []string{"# outdented docs"},
		},
		"nested key with adjacent run": {
			input: stringtest.Input(`
				parent:
				  # child docs
				  child: 1
			`),
			key:  "child",
			want: []string{"# child docs"},
		},
		"paragraph separator keeps the run alive": {
			input: stringtest.Input(`
				# para one
				#
				# para two
				name: test
			`),
			key:  "name",
			want: []string{"# para one", "#", "# para two"},
		},
		"run beginning inside a schema block": {
			// The blank line splits the @schema block, so the kept run begins
			// mid-block; inSchema reports the fence state its opening line
			// inherits from the discarded prefix.
			input: stringtest.Input(`
				# @schema
				# type: string

				# enum:
				#   - a
				# @schema
				key: 1
			`),
			key:          "key",
			want:         []string{"# enum:", "#   - a", "# @schema"},
			wantInSchema: true,
		},
		"run beginning inside a root block": {
			input: stringtest.Input(`
				# @schema.root
				# title: Root

				# docs
				key: 1
			`),
			key:        "key",
			want:       []string{"# docs"},
			wantInRoot: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mvn := findMappingValue(t, tc.input, tc.key)

			run, inSchema, inRoot := magicschema.HeadCommentRun(mvn)
			assert.Equal(t, tc.want, run)
			assert.Equal(t, tc.wantInSchema, inSchema)
			assert.Equal(t, tc.wantInRoot, inRoot)
		})
	}
}

func TestHeadCommentRunFailOpen(t *testing.T) {
	t.Parallel()

	t.Run("unpositioned comment token attributes the whole group", func(t *testing.T) {
		t.Parallel()

		mvn := findMappingValue(t, "# stale docs\n\n# real docs\nname: test", "name")

		comment := mvn.GetComment()
		require.NotNil(t, comment)
		require.NotEmpty(t, comment.Comments)

		// Without positions the physical layout cannot be reconstructed, so
		// the whole merged group is attributed to the key (fail open).
		comment.Comments[0].Token.Position = nil

		run, inSchema, inRoot := magicschema.HeadCommentRun(mvn)
		assert.Equal(t, []string{"# stale docs", "# real docs"}, run)
		assert.False(t, inSchema)
		assert.False(t, inRoot)
	})

	t.Run("non-mapping-value node", func(t *testing.T) {
		t.Parallel()

		file, err := parser.ParseBytes([]byte("- a\n- b\n"), parser.ParseComments)
		require.NoError(t, err)
		require.NotEmpty(t, file.Docs)

		run, inSchema, inRoot := magicschema.HeadCommentRun(file.Docs[0].Body)
		assert.Nil(t, run)
		assert.False(t, inSchema)
		assert.False(t, inRoot)
	})

	t.Run("nil node", func(t *testing.T) {
		t.Parallel()

		run, inSchema, inRoot := magicschema.HeadCommentRun(nil)
		assert.Nil(t, run)
		assert.False(t, inSchema)
		assert.False(t, inRoot)
	})
}

// findMappingValue parses input with comments and returns the mapping value
// node whose key matches key, searching depth-first.
func findMappingValue(t *testing.T, input, key string) *ast.MappingValueNode {
	t.Helper()

	file, err := parser.ParseBytes([]byte(input), parser.ParseComments)
	require.NoError(t, err)
	require.NotEmpty(t, file.Docs)

	var find func(node ast.Node) *ast.MappingValueNode

	find = func(node ast.Node) *ast.MappingValueNode {
		switch n := node.(type) {
		case *ast.MappingNode:
			for _, v := range n.Values {
				if found := find(v); found != nil {
					return found
				}
			}

		case *ast.MappingValueNode:
			if n.Key != nil && n.Key.GetToken() != nil && n.Key.GetToken().Value == key {
				return n
			}

			return find(n.Value)
		}

		return nil
	}

	mvn := find(file.Docs[0].Body)
	require.NotNil(t, mvn, "key %q not found", key)

	return mvn
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
		"tagged string keeps its type": {
			input: "val: !!str 8080\n",
			want:  "string",
		},
		"bool tag on empty scalar drops the type": {
			// "val: !!bool" with no value is a null; asserting boolean would
			// reject the null the source holds, so fail open to no constraint.
			input: "val: !!bool\n",
			want:  "",
		},
		"int tag on empty scalar drops the type": {
			input: "val: !!int\n",
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
		"incompatible element sandwiched between compatible ones": {
			// The integer between two strings must drop the items constraint
			// (fail open), not be forgotten so the items type re-settles on
			// string and rejects the integer the list actually holds.
			input:    "items:\n  - hello\n  - 42\n  - world\n",
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
