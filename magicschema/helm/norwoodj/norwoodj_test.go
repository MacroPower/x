package norwoodj_test

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
	"go.jacobcolvin.com/x/magicschema/helm/norwoodj"
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

func TestHelmDocsAnnotator(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  func(*testing.T, map[string]any)
	}{
		"array-index key path applies to the items schema": {
			// Bracketed indices normalize to the walker's index-free paths,
			// the same rule bitnami applies; without it the annotation is
			// stored under a path the walker never asks for while the
			// comment is also suppressed from the fallback description.
			input: stringtest.Input(`
				# jobs[0].name -- Job name from helm-docs
				jobs:
				  - name: a
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

				name, ok := itemProps["name"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Job name from helm-docs", name["description"])
			},
		},
		"annotation-lookalike inside a block scalar is data": {
			// An old-style "# key -- desc" line inside a "|" scalar is string
			// data; treating it as an annotation would attach a wrong
			// description to the real key.
			input: stringtest.Input(`
				script: |
				  # config.item -- fake description from string data
				config:
				  item: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				config, ok := props["config"].(map[string]any)
				require.True(t, ok)

				configProps, ok := config["properties"].(map[string]any)
				require.True(t, ok)

				item, ok := configProps["item"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, item, "description",
					"the fake old-style line inside the scalar must not attach")
			},
		},
		"simple description": {
			input: stringtest.Input(`
				# -- Number of replicas
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Number of replicas", r["description"])
			},
		},
		"prose continuation starting with a marker word is kept": {
			// "@sections" begins with "@section" but is prose, not the
			// "@section" annotation (which needs a " -- " separator). It must
			// stay in the description rather than be silently consumed.
			input: stringtest.Input(`
				# -- Main description.
				# @sections of the chart are documented below.
				key: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, k["description"], "sections of the chart are documented below")
			},
		},
		"new-style description keeps the old-style type hint": {
			// The old-style file scan records a type hint; a new-style "# --"
			// override that omits the type inherits it (per-field precedence)
			// rather than letting structural inference backfill a different
			// type from the value.
			input: stringtest.Input(`
				# config.mode -- (int) old description

				config:
				  # -- new description
				  mode: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				config, ok := props["config"].(map[string]any)
				require.True(t, ok)

				cprops, ok := config["properties"].(map[string]any)
				require.True(t, ok)

				mode, ok := cprops["mode"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", mode["type"])
				assert.Equal(t, "new description", mode["description"])
			},
		},
		"detached old-style @ignore is honored despite a new-style child comment": {
			// The old-style file scan records @ignore for image.tag from a block
			// attached to a different key, so @ignore is not in the tag node's own
			// head comment. The node's new-style "# --" comment overrides the
			// description but must not clear the inherited skip.
			input: stringtest.Input(`
				# image.tag -- Old desc
				# @ignore
				unrelated: 1
				image:
				  # -- The image tag
				  tag: latest
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				iprops, hasProps := image["properties"].(map[string]any)
				if hasProps {
					assert.NotContains(t, iprops, "tag")
				}
			},
		},
		"standalone @default uses last when no description line": {
			// Without a "# --" line the @default scan must still be last-wins,
			// matching the "# --" path, so the resolved default does not depend
			// on whether a description line is present.
			input: stringtest.Input(`
				# @default -- first
				# @default -- second
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "second", v["default"])
			},
		},
		"old-style block default stays with its own key": {
			// An old-style block's @default documents the block's key path,
			// which the ForContent file scan already delivers there; the
			// node-level standalone-@default extension must not also attach
			// it to the physically following node.
			input: stringtest.Input(`
				# other.thing -- desc for other
				# @default -- 99
				name: hello
				other:
				  thing: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["name"].(map[string]any)
				require.True(t, ok)
				assert.NotContains(t, n, "default",
					"the old-style block's @default must not leak onto the following node")

				other, ok := props["other"].(map[string]any)
				require.True(t, ok)

				oprops, ok := other["properties"].(map[string]any)
				require.True(t, ok)

				thing, ok := oprops["thing"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "desc for other", thing["description"])
				assert.InDelta(t, float64(99), thing["default"], 0.001)
			},
		},
		"type hint string": {
			input: stringtest.Input(`
				# -- (string) Container image
				image: nginx
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				i, ok := props["image"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", i["type"])
				assert.Equal(t, "Container image", i["description"])
			},
		},
		"type hint int": {
			input: stringtest.Input(`
				# -- (int) Port number
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", p["type"])
			},
		},
		"type hint list": {
			input: stringtest.Input(`
				# -- (list) Container args
				args:
				  - arg1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["args"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", a["type"])
			},
		},
		"type hint object": {
			input: stringtest.Input(`
				# -- (object) Labels
				labels:
				  app: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
			},
		},
		"compound type tpl/string": {
			input: stringtest.Input(`
				# -- (tpl/string) Template string
				tpl: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["tpl"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"contradictory scalar compound type drops to inference": {
			// "string/int" has a concrete scalar leading segment, not a modifier
			// like tpl, so it cannot mean "integer". Asserting integer on the
			// quoted string value would reject it; fall through to inference,
			// which types the value as a string instead.
			input: stringtest.Input(`
				# -- (string/int) the port
				port: "8080"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["port"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"empty parens kept in description": {
			// An empty "()" names no type, so upstream helm-docs keeps it in
			// the description rather than stripping it as a type hint.
			input: stringtest.Input(`
				# -- () the value
				mode: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["mode"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "() the value", m["description"])
			},
		},
		"ignore annotation": {
			input: stringtest.Input(`
				# @ignore
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
		"ignore as foot comment on last key": {
			// A trailing "# @ignore" after the last key attaches as a foot
			// comment; it must still skip the key.
			input: stringtest.Input(`
				keep: yes
				name: test
				# @ignore
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, props, "keep")
				assert.NotContains(t, props, "name")
			},
		},
		"ignore above the first sequence element does not skip the array key": {
			// The goccy parser stows the first element's head comment on the
			// SequenceNode itself; reading it as the value's comment would
			// delete the whole array key, while upstream removes only the
			// matching sequence item. Keeping the key is the fail-open side
			// of that divergence.
			input: stringtest.Input(`
				list:
				  # @ignore
				  - item1
				  - item2
				keep: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, props, "keep")

				list, ok := props["list"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", list["type"])
			},
		},
		"inline ignore on a flow sequence still skips": {
			// A comment on the value's own line is a genuine inline comment,
			// not a stowed element comment, so the documented inline @ignore
			// divergence keeps applying to sequence values.
			input: stringtest.Input(`
				list: [] # @ignore
				keep: true
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "list")
				assert.Contains(t, props, "keep")
			},
		},
		"detached ignore block does not skip the following key": {
			// The parser merges blank-line-separated comment blocks into one
			// head comment group; an @ignore for a commented-out key,
			// separated from the next key by a blank line, must not delete
			// that key. Upstream reads yaml.v3's HeadComment, which excludes
			// blank-line-separated blocks.
			input: stringtest.Input(`
				# @ignore
				# secretInternal: xyz

				# -- Public key
				publicKey: abc
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["publicKey"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Public key", p["description"])
			},
		},
		"file header mentioning ignore does not skip the following key": {
			input: stringtest.Input(`
				# this file uses @ignore annotations below

				# -- Public key
				publicKey: abc
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["publicKey"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Public key", p["description"])
			},
		},
		"detached default block does not apply to the following key": {
			// Standalone @default scopes to the comment block adjacent to the
			// key; a @default in a blank-line-detached earlier block belongs
			// to whatever it once documented, not to the following key.
			input: stringtest.Input(`
				# @default -- 42

				publicKey: abc
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["publicKey"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", p["type"])
				assert.NotContains(t, p, "default")
			},
		},
		"detached description block does not apply to the following key": {
			// A "# --" description for a deleted key, separated from the next
			// key by a blank line, stays detached rather than becoming the
			// next key's description.
			input: stringtest.Input(`
				# -- description for a key that was deleted

				newKey: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["newKey"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", n["type"])
				assert.NotContains(t, n, "description")
			},
		},
		"multi-line continuation with blank comment": {
			input: stringtest.Input(`
				# -- First line
				#
				# Second line
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "First line")
				assert.Contains(t, desc, "Second line")
			},
		},
		"empty marker then continuation has no leading space": {
			// A bare "# --" leaves the description empty; the first
			// continuation line must seed it directly, not append " " + line,
			// which would emit a description with a stray leading space.
			input: stringtest.Input(`
				# --
				# The description
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The description", v["description"])
			},
		},
		"raw mode continuation": {
			input: stringtest.Input(`
				# -- First line
				# @raw
				# Second line
				# Third line
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "First line")
				assert.Contains(t, desc, "Second line")
				assert.Contains(t, desc, "Third line")
				// In raw mode, lines are joined with newlines, not spaces.
				assert.Contains(t, desc, "\n")
			},
		},
		"old-style with string type hint": {
			input: stringtest.Input(`
				# key.path -- (string) Description
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", p["type"])
				assert.Equal(t, "Description", p["description"])
			},
		},
		"old-style description containing -- is preserved": {
			// The greedy regex bound the last " -- " into the key, mis-keying
			// the entry and dropping the description via the annotation guard.
			// Splitting on the first separator keeps the real key and the full
			// description (which itself contains " -- ").
			input: stringtest.Input(`
				# image.tag -- the tag -- use latest
				image:
				  tag: stable
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				img, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imgProps, ok := img["properties"].(map[string]any)
				require.True(t, ok)

				tag, ok := imgProps["tag"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "the tag -- use latest", tag["description"])
			},
		},
		"old-style with int type hint": {
			input: stringtest.Input(`
				# key.path -- (int) Port number
				key:
				  path: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", p["type"])
				assert.Equal(t, "Port number", p["description"])
			},
		},
		"old-style with compound type hint": {
			input: stringtest.Input(`
				# key.path -- (tpl/object) Template object
				key:
				  path:
				    foo: bar
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", p["type"])
				assert.Equal(t, "Template object", p["description"])
			},
		},
		"compound type list/string maps to array": {
			// The container segment wins for "list/<known type>": the value is
			// a list of strings, so the structural type is array. Last-segment
			// resolution alone mislabeled it as the trailing "string".
			input: stringtest.Input(`
				# -- (list/string) The names
				names:
				  - a
				  - b
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["names"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", n["type"])
				assert.Equal(t, "The names", n["description"])
			},
		},
		"nested container type list/list/string maps to array": {
			// A nested-container hint must resolve to its OUTERMOST container.
			// Inspecting only the segment before the last slash ("list/list")
			// missed the container and asserted the scalar trailing "string" on
			// a value that is actually a list.
			input: stringtest.Input(`
				# -- (list/list/string) Rows of names
				rows:
				  - - a
				    - b
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["rows"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", r["type"])
				assert.Equal(t, "Rows of names", r["description"])
			},
		},
		"compound type list/int maps to array": {
			input: stringtest.Input(`
				# -- (list/int) The ports
				ports:
				  - 80
				  - 443
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["ports"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", p["type"])
			},
		},
		"old-style with unknown type hint": {
			input: stringtest.Input(`
				# key.path -- (unknown) Description
				key:
				  path: 123
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				// Unknown type hint is ignored; type comes from value inference, not annotation.
				assert.Equal(t, "Description", p["description"])
			},
		},
		"default override": {
			input: stringtest.Input(`
				# @default -- custom-value
				# -- Description
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description", v["description"])
				assert.Equal(t, "custom-value", v["default"])
			},
		},
		"default override keeps native yaml types": {
			input: stringtest.Input(`
				# @default -- 80
				# -- Port number
				port: 8080
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["port"].(map[string]any)
				require.True(t, ok)

				// @default values parse as YAML, so numbers stay numeric
				// instead of being quoted as JSON strings.
				assert.InEpsilon(t, float64(80), p["default"], 0.0001)
			},
		},
		"notationType does not leak into continuation": {
			input: stringtest.Input(`
				# -- Description of value
				# @notationType -- helm
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description of value", desc)
				assert.NotContains(t, desc, "@notationType")
				assert.NotContains(t, desc, "helm")
			},
		},
		"section does not leak into continuation": {
			input: stringtest.Input(`
				# -- Description of value
				# @section -- Security
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description of value", desc)
				assert.NotContains(t, desc, "@section")
				assert.NotContains(t, desc, "Security")
			},
		},
		"section not parsed as old-style description": {
			input: stringtest.Input(`
				# @section -- Security
				section: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				s, ok := props["section"].(map[string]any)
				require.True(t, ok)

				// @section should be ignored, not parsed as old-style desc.
				assert.Empty(t, s["description"])
			},
		},
		"notationType not parsed as old-style description": {
			input: stringtest.Input(`
				# @notationType -- helm
				notationType: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["notationType"].(map[string]any)
				require.True(t, ok)

				// @notationType should be ignored, not parsed as old-style desc.
				assert.Empty(t, n["description"])
			},
		},
		"continuation with mixed ignored annotations": {
			input: stringtest.Input(`
				# -- Main description
				# More details
				# @notationType -- helm
				# Final line
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "Main description")
				assert.Contains(t, desc, "More details")
				assert.Contains(t, desc, "Final line")
				assert.NotContains(t, desc, "@notationType")
			},
		},
		"ignore as substring check": {
			input: stringtest.Input(`
				# some text @ignore more text
				secret: value
				name: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// @ignore is a substring check, so even embedded in comment it triggers skip.
				assert.NotContains(t, props, "secret")
				assert.Contains(t, props, "name")
			},
		},
		"type hint float": {
			input: stringtest.Input(`
				# -- (float) CPU limit
				cpu: 1.5
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cpu"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "number", c["type"])
			},
		},
		"type hint bool": {
			input: stringtest.Input(`
				# -- (bool) Enable feature
				enabled: false
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
		"type hint dict": {
			input: stringtest.Input(`
				# -- (dict) Extra labels
				labels:
				  app: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", l["type"])
			},
		},
		"type hint yaml": {
			// A bare (yaml) hint is a render notation, not a type assertion;
			// it contributes no type constraint, so the string type here comes
			// from structural inference over the scalar value.
			input: stringtest.Input(`
				# -- (yaml) Raw content
				content: data
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["content"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", c["type"])
				assert.Equal(t, "Raw content", c["description"])
			},
		},
		"type hint yaml on mapping keeps object type": {
			// The (yaml) hint marks a value rendered as a YAML block, not a
			// string; on a mapping the structural object type stands so the
			// schema accepts the chart's own values (fail open).
			input: stringtest.Input(`
				# -- (yaml) Extra config rendered as yaml
				config:
				  a: 1
				  b: 2
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["config"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", c["type"])
				assert.Equal(t, "Extra config rendered as yaml", c["description"])

				cprops, ok := c["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, cprops, "a")
				assert.Contains(t, cprops, "b")
			},
		},
		"type hint yaml on sequence keeps array type": {
			input: stringtest.Input(`
				# -- (yaml) Extra manifests rendered as yaml
				manifests:
				  - kind: ConfigMap
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["manifests"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", m["type"])
			},
		},
		"type hint tpl": {
			// A bare (tpl) hint is a render notation, not a type assertion;
			// it contributes no type constraint, so the string type here comes
			// from structural inference over the scalar value.
			input: stringtest.Input(`
				# -- (tpl) Templated value
				tpl: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				f, ok := props["tpl"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", f["type"])
			},
		},
		"type hint tpl on mapping keeps object type": {
			// Like (yaml), a bare (tpl) hint marks templated content; on a
			// mapping the structural object type stands so the schema accepts
			// the chart's own values (fail open).
			input: stringtest.Input(`
				# -- (tpl) Templated annotations
				annotations:
				  role: "{{ .Values.role }}"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["annotations"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", a["type"])
			},
		},
		"deep compound type": {
			input: stringtest.Input(`
				# -- (k8s/storage/persistent-volume/access-modes) Access modes
				modes:
				  - ReadWriteOnce
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				m, ok := props["modes"].(map[string]any)
				require.True(t, ok)

				// Last segment "access-modes" is not in mapping, so type hint is ignored.
				// Type comes from value inference (array).
				assert.Equal(t, "Access modes", m["description"])
			},
		},
		"multiple dash-dash lines uses last": {
			input: stringtest.Input(`
				# -- First description
				# -- Second description
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Only the LAST # -- line is used.
				assert.Equal(t, "Second description", v["description"])
			},
		},
		"non-comment line terminates continuation": {
			input: stringtest.Input(`
				# -- Description
				other: value
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// The "# -- Description" comment is on "other", not "val".
				o, ok := props["other"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description", o["description"])
			},
		},
		"default does not leak into continuation": {
			input: stringtest.Input(`
				# -- Description
				# @default -- custom
				# More text
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "Description")
				assert.Contains(t, desc, "More text")
				assert.NotContains(t, desc, "@default")
				assert.NotContains(t, desc, "custom")
				assert.Equal(t, "custom", v["default"])
			},
		},
		"default not parsed as old-style description": {
			input: stringtest.Input(`
				# @default -- custom-val
				defaultKey: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["defaultKey"].(map[string]any)
				require.True(t, ok)

				// @default should not be parsed as old-style key description.
				assert.Equal(t, "custom-val", d["default"])
			},
		},
		"tpl/array compound type": {
			input: stringtest.Input(`
				# -- (tpl/array) Templated array
				arr:
				  - item
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["arr"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", a["type"])
			},
		},
		"tpl/object compound type": {
			input: stringtest.Input(`
				# -- (tpl/object) Templated obj
				obj:
				  key: val
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				o, ok := props["obj"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "object", o["type"])
			},
		},
		"unrecognized compound type silently ignored": {
			input: stringtest.Input(`
				# -- (list/csv) CSV list
				csv: "a,b,c"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["csv"].(map[string]any)
				require.True(t, ok)

				// "csv" is not in the mapping, so type hint is ignored.
				// Type comes from value inference (string).
				assert.Equal(t, "CSV list", c["description"])
			},
		},
		"raw mode with blank comment lines": {
			input: stringtest.Input(`
				# -- Start
				# @raw
				#
				# After blank
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// Blank comment lines in raw mode produce a newline separator.
				assert.Equal(t, stringtest.JoinLF("Start", "", "After blank"), desc)
			},
		},
		"raw prefix word does not activate raw mode": {
			// "@rawData" must not be mistaken for the "@raw" marker: it stays
			// description text rather than switching to raw (newline) joining.
			input: stringtest.Input(`
				# -- Start
				# @rawData here
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Start @rawData here", v["description"])
			},
		},
		"raw mode under a bare marker preserves leading blank lines": {
			// A bare "# --" then @raw then a blank line must keep the leading
			// newline separators, like upstream. Seeding an empty description
			// directly under raw mode would swallow them and collapse the
			// multi-line raw text to just its last paragraph.
			input: stringtest.Input(`
				# --
				# @raw
				#
				# After
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, stringtest.JoinLF("", "", "After"), desc)
			},
		},
		"raw before dash-dash does not activate raw mode": {
			input: stringtest.Input(`
				# @raw
				# -- First line
				# Second line
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// @raw before # -- is not in continuation, so normal join with spaces.
				assert.Equal(t, "First line Second line", desc)
			},
		},
		"default in continuation after dash-dash": {
			input: stringtest.Input(`
				# -- Description text
				# @default -- override-val
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description text", v["description"])
				assert.Equal(t, "override-val", v["default"])
			},
		},
		"raw mode preserves leading spaces": {
			input: stringtest.Input(`
				# -- Desc
				# @raw
				#  - item one
				#  - item two
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// Raw mode preserves the leading space from #  - item.
				assert.Equal(t, stringtest.JoinLF("Desc", " - item one", " - item two"), desc)
			},
		},
		"multiple dash-dash with raw on first ignores raw": {
			input: stringtest.Input(`
				# -- First
				# @raw
				# -- Second
				# continuation
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// Only the LAST # -- line is used. The @raw was between
				// the first and second # --, so it's before the last
				// # -- and not in its continuation.
				assert.Equal(t, "Second continuation", desc)
			},
		},
		"string/email compound type": {
			input: stringtest.Input(`
				# -- (string/email) Email address
				email: test@example.com
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				e, ok := props["email"].(map[string]any)
				require.True(t, ok)

				// "email" is not in the mapping, so type is ignored.
				assert.Equal(t, "Email address", e["description"])
			},
		},
		"old-style comment does not leak to parent description": {
			input: stringtest.Input(`
				# image.tag -- (string) Old-style image tag
				image:
				  tag: latest
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				img, ok := props["image"].(map[string]any)
				require.True(t, ok)

				// The old-style comment targets image.tag, not image itself.
				// The image key should NOT get the old-style comment as its description.
				assert.Empty(t, img["description"])

				imgProps, ok := img["properties"].(map[string]any)
				require.True(t, ok)

				tag, ok := imgProps["tag"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", tag["type"])
				assert.Equal(t, "Old-style image tag", tag["description"])
			},
		},
		"dotless old-style comment annotates the top-level key": {
			// "# note -- be careful" has a single dotless token before " -- ".
			// Upstream helm-docs accepts any non-empty key, so the comment is
			// an old-style annotation for the top-level "note" key. It must
			// annotate that key exactly once and not also leak as a fallback
			// description on the unrelated key it happens to sit above.
			input: stringtest.Input(`
				# note -- be careful with this
				config:
				  enabled: true
				note: hello
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				note, ok := props["note"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "be careful with this", note["description"],
					"the old-style comment annotates the key it names")

				config, ok := props["config"].(map[string]any)
				require.True(t, ok)
				assert.Empty(t, config["description"],
					"the annotation marker must not leak to the adjacent key")
			},
		},
		"top-level old-style annotation applies description type and default": {
			// A top-level dotless key in an old-style comment matches the
			// upstream scanner (any non-empty key qualifies), so the
			// description, the (int) type hint, and the @default override
			// all reach the schema instead of the marker line leaking
			// verbatim into the description.
			input: stringtest.Input(`
				# replicas -- (int) Number of replicas
				# @default -- 1
				replicas: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				r, ok := props["replicas"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Number of replicas", r["description"])
				assert.Equal(t, "integer", r["type"])
				assert.InEpsilon(t, float64(1), r["default"], 0.0001)
			},
		},
		"old-style in head comment discarded for current node": {
			// Upstream getDescriptionFromNode discards the auto-description when
			// ParseComment returns a non-empty key. The old-style comment targets
			// "other.path", so the "parent" node should NOT receive it.
			input: stringtest.Input(`
				# other.path -- Wrong node desc
				parent:
				  child: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["parent"].(map[string]any)
				require.True(t, ok)

				// The "parent" node must NOT have the old-style description
				// that targets "other.path".
				assert.Empty(t, p["description"])
			},
		},
		"stacked old-style comments do not merge": {
			// Two old-style "# key.path -- desc" lines with no YAML between them
			// must each annotate their own key. Treating the second as a
			// continuation of the first pollutes image.repository's description
			// and leaves image.tag undocumented.
			input: stringtest.Input(`
				# image.repository -- The image repository
				# image.tag -- The image tag
				image:
				  repository: nginx
				  tag: latest
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
				assert.Equal(t, "The image repository", repo["description"])

				tag, ok := imageProps["tag"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "The image tag", tag["description"])
			},
		},
		"old-style with multi-line continuation": {
			input: stringtest.Input(`
				# key.path -- First line
				# More details
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				desc, ok := p["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "First line")
				assert.Contains(t, desc, "More details")
			},
		},
		"new-style description keeps old-style default": {
			// A node with a new-style "# -- desc" head comment and a file-level
			// old-style entry carrying a @default must keep the new-style
			// description and the old-style default (per-field precedence),
			// rather than discarding the old entry wholesale.
			input: stringtest.Input(`
				# key.path -- (string) old description
				# @default -- olddefault
				key:
				  # -- new description
				  path: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "new description", p["description"])
				assert.Equal(t, "olddefault", p["default"])
			},
		},
		"old-style with default override": {
			input: stringtest.Input(`
				# key.path -- Description
				# @default -- custom
				key:
				  path: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description", p["description"])
				assert.Equal(t, "custom", p["default"])
			},
		},
		"empty node default keeps old-style default": {
			// A node-level standalone "# @default -- " with no value must not
			// overwrite the old-style default with null; the real old-style
			// default survives.
			input: stringtest.JoinLF(
				"# foo.bar -- (int) desc",
				"# @default -- 5",
				"",
				"foo:",
				"  # @default -- ",
				"  bar: 1",
			),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				foo, ok := props["foo"].(map[string]any)
				require.True(t, ok)

				fooProps, ok := foo["properties"].(map[string]any)
				require.True(t, ok)

				bar, ok := fooProps["bar"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "desc", bar["description"])
				assert.Equal(t, "integer", bar["type"])
				assert.InEpsilon(t, float64(5), bar["default"], 0.0001)
			},
		},
		"old-style with section consumed": {
			input: stringtest.Input(`
				# key.path -- Description
				# @section -- Security
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				desc, ok := p["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description", desc)
				assert.NotContains(t, desc, "@section")
				assert.NotContains(t, desc, "Security")
			},
		},
		"old-style with notationType consumed": {
			input: stringtest.Input(`
				# key.path -- Description
				# @notationType -- tpl
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				desc, ok := p["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description", desc)
				assert.NotContains(t, desc, "@notationType")
			},
		},
		"old-style with raw mode": {
			input: stringtest.Input(`
				# key.path -- Description
				# @raw
				# Line one
				# Line two
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				desc, ok := p["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "Description")
				assert.Contains(t, desc, "\n")
				assert.Contains(t, desc, "Line one")
				assert.Contains(t, desc, "Line two")
			},
		},
		"old-style with ignore": {
			input: stringtest.Input(`
				# key.path -- Description
				# @ignore
				key:
				  path: value
				other: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Key has @ignore in its comment, so key should be skipped.
				// The @ignore is detected per-node via collectComments,
				// and the old-style comment applies to key.path.
				// The @ignore is part of the comment on the "key" node.
				assert.NotContains(t, props, "key")
				assert.Contains(t, props, "other")
			},
		},
		"standalone default without description": {
			input: stringtest.Input(`
				# @default -- custom-val
				key: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "custom-val", k["default"])
			},
		},
		"path type hint silently ignored": {
			input: stringtest.Input(`
				# -- (path) Static directory
				staticRoot: /opt/django/static
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				s, ok := props["staticRoot"].(map[string]any)
				require.True(t, ok)

				// "path" is not in the type mapping, so type comes from value inference.
				assert.Equal(t, "string", s["type"])
				assert.Equal(t, "Static directory", s["description"])
			},
		},
		"map type hint silently ignored": {
			input: stringtest.Input(`
				# -- (map) The labels
				labels:
				  app: test
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["labels"].(map[string]any)
				require.True(t, ok)

				// "map" is not in the type mapping, so type comes from value inference.
				assert.Equal(t, "object", l["type"])
				assert.Equal(t, "The labels", l["description"])
			},
		},
		"empty description after dash-dash": {
			input: stringtest.Input(`
				# --
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// An empty "# --" line produces an empty description.
				assert.Empty(t, v["description"])
			},
		},
		"type hint only with no description": {
			input: stringtest.Input(`
				# -- (string)
				val: 123
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "string", v["type"])
				assert.Empty(t, v["description"])
			},
		},
		"new-style takes precedence over old-style for same path": {
			// When a node has a new-style head comment (# -- desc), it takes
			// precedence over any old-style comment targeting the same key path
			// via Prepare. The annotator checks new-style first and only falls
			// back to old-style when new-style is nil.
			input: stringtest.Input(`
				# key.path -- Old-style desc
				other: 1
				# -- New-style desc
				key:
				  path: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				// The "key" node has a new-style head comment.
				assert.Equal(t, "New-style desc", k["description"])

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				// The old-style comment targets key.path, which also matches.
				assert.Equal(t, "Old-style desc", p["description"])
			},
		},
		"ignore in continuation does not leak into description": {
			// When @ignore appears after # -- in continuation lines,
			// it should be consumed and not leak into the description.
			// The @ignore causes the entire node to be skipped.
			input: stringtest.Input(`
				# -- Description text
				# @ignore
				val: x
				kept: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// @ignore causes the node to be skipped entirely.
				assert.NotContains(t, props, "val")
				assert.Contains(t, props, "kept")
			},
		},
		"at-raw requires space after hash": {
			// # @raw requires at least one space between # and @raw.
			// #@raw should NOT activate raw mode.
			input: stringtest.Input(`
				# -- Description
				#@raw
				# Line two
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)

				// #@raw doesn't activate raw mode, so lines are joined with spaces.
				// The @raw text leaks into description since it's treated as continuation.
				assert.NotContains(t, desc, "\n")
			},
		},
		"deeply nested old-style key path": {
			input: stringtest.Input(`
				# a.b.c.d -- Deep description
				a:
				  b:
				    c:
				      d: value
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

				assert.Equal(t, "Deep description", d["description"])
			},
		},
		"multiple old-style comments for different paths": {
			// Old-style comments must be separated by non-comment lines
			// (the YAML key-value pairs themselves). Consecutive old-style
			// comment lines are treated as a single block per upstream behavior.
			input: stringtest.Input(`
				# image.repo -- Repo description
				image:
				  repo: nginx
				# image.tag -- Tag description
				  tag: latest
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				img, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imgProps, ok := img["properties"].(map[string]any)
				require.True(t, ok)

				repo, ok := imgProps["repo"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Repo description", repo["description"])

				tag, ok := imgProps["tag"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Tag description", tag["description"])
			},
		},
		"description whitespace trimming": {
			input: stringtest.JoinLF(
				"# --   Spaces around   ",
				"val: x",
			),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)

				// Description should be trimmed.
				assert.Equal(t, "Spaces around", desc)
			},
		},
		"old-style non-existent path ignored gracefully": {
			// An old-style comment targeting a key path that doesn't exist
			// in the YAML is simply never matched.
			input: stringtest.Input(`
				# nonexistent.path -- Ghost description
				actual: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				a, ok := props["actual"].(map[string]any)
				require.True(t, ok)

				// The old-style comment targets nonexistent.path, not actual.
				assert.Empty(t, a["description"])
			},
		},
		"default with empty value": {
			input: stringtest.JoinLF(
				"# @default -- ",
				"# -- Description",
				"val: actual",
			),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description", v["description"])

				// Empty default value should still be set.
				assert.Empty(t, v["default"])
			},
		},
		"ignore on nested object skips entire subtree": {
			input: stringtest.Input(`
				# @ignore
				nested:
				  child1: a
				  child2: b
				kept: value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "nested")
				assert.Contains(t, props, "kept")
			},
		},
		"compound type with single segment": {
			// A bare "tpl" hint (not compound) is a render notation that
			// asserts no type; structural inference supplies string here.
			input: stringtest.Input(`
				# -- (tpl) A template
				val: x
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
		"old-style with at-ignore in continuation": {
			// When @ignore appears in continuation of an old-style comment,
			// it should be consumed and not leak into description.
			input: stringtest.Input(`
				# key.path -- Description
				# @ignore
				key:
				  path: value
				other: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// @ignore is in the comment attached to "key" node,
				// so the entire "key" subtree should be skipped.
				assert.NotContains(t, props, "key")
				assert.Contains(t, props, "other")
			},
		},
		"no annotation produces nil result": {
			// A key with no helm-docs comments at all should produce nil from annotator.
			input: "plain: value",
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["plain"].(map[string]any)
				require.True(t, ok)

				// Type comes from structural inference (string), no description.
				assert.Equal(t, "string", p["type"])
				assert.Empty(t, p["description"])
			},
		},
		"description from non-MappingValueNode returns nil": {
			// The annotator should return nil for non-MappingValueNode nodes.
			// This is implicitly tested - just ensures no panic on scalar input.
			input: "simple: value",
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)
				assert.Contains(t, props, "simple")
			},
		},
		"default before multiple dash-dash lines is preserved": {
			// @default before the last # -- group should be preserved.
			input: stringtest.Input(`
				# @default -- preserved-val
				# -- First description
				# -- Second description
				val: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// Last # -- line is used for description.
				assert.Equal(t, "Second description", v["description"])
				// @default from before the last # -- group is preserved.
				assert.Equal(t, "preserved-val", v["default"])
			},
		},
		"continuation after default in same block": {
			input: stringtest.Input(`
				# -- Start desc
				# @default -- myval
				# Continued text
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "Start desc")
				assert.Contains(t, desc, "Continued text")
				assert.NotContains(t, desc, "@default")
				assert.Equal(t, "myval", v["default"])
			},
		},
		"dash-dash without space before description": {
			// # --description (no space between -- and text) should still work.
			input: stringtest.Input(`
				# --No space
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// The regex \s*(.*)$ after -- captures with zero-or-more space.
				assert.Equal(t, "No space", v["description"])
			},
		},
		"multiple default lines uses last": {
			input: stringtest.Input(`
				# -- Description
				# @default -- first
				# @default -- second
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Description", v["description"])
				// The later @default should win.
				assert.Equal(t, "second", v["default"])
			},
		},
		"at-default without separator not parsed as default": {
			// "# @default custom-val" (without --) should not set a default.
			// It leaks into description as a regular continuation line.
			input: stringtest.Input(`
				# -- Description
				# @default custom-val
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				// No default since @default requires "-- " separator.
				assert.Nil(t, v["default"])

				// The line leaks into description as continuation.
				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "Description")
				assert.Contains(t, desc, "@default custom-val")
			},
		},
		"ignore in new-style continuation sets skip": {
			// @ignore appearing in continuation of a new-style comment
			// should cause the node to be skipped.
			input: stringtest.Input(`
				# -- Description
				# @ignore
				val: x
				kept: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "val")
				assert.Contains(t, props, "kept")
			},
		},
		"old-style ignore in continuation via parseCommentBlock": {
			// When @ignore appears in continuation of an old-style block
			// processed via Prepare, skip should be set.
			input: stringtest.Input(`
				# key.path -- Description
				# @ignore
				key:
				  path: value
				other: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// The @ignore causes skip for the key subtree.
				assert.NotContains(t, props, "key")
				assert.Contains(t, props, "other")
			},
		},
		"raw mode with at-raw immediately after dash-dash": {
			// @raw immediately after # -- should activate raw mode for continuation.
			input: stringtest.Input(`
				# -- Description
				# @raw
				# Line 1
				# Line 2
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, stringtest.JoinLF("Description", "Line 1", "Line 2"), desc)
			},
		},
		"old-style and new-style in same block uses last dash-dash": {
			// When old-style "# key -- desc" and new-style "# -- desc"
			// appear in the same comment block, the issue #96 workaround
			// takes only the last "# --" group. The old-style key info
			// is lost since the last group is new-style.
			input: stringtest.Input(`
				# parent.child -- (int) Old-style child
				# -- New-style wins
				parent:
				  child: 42
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := props["parent"].(map[string]any)
				require.True(t, ok)

				// The new-style "# -- New-style wins" takes precedence
				// on the parent node.
				assert.Equal(t, "New-style wins", p["description"])
			},
		},
		"empty input produces no annotations": {
			input: "\n",
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				// Empty input should produce a valid schema with no properties.
				assert.NotContains(t, got, "properties")
			},
		},
		"ignore embedded in description text": {
			// @ignore embedded within other text (not on its own annotation line)
			// should still trigger skip via substring check.
			input: stringtest.Input(`
				# check @ignore here
				val: x
				kept: 1
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				assert.NotContains(t, props, "val")
				assert.Contains(t, props, "kept")
			},
		},
		"old-style with multiple continuation and default at end": {
			input: stringtest.Input(`
				# key.path -- First line
				# Second line
				# @default -- endval
				key:
				  path: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				desc, ok := p["description"].(string)
				require.True(t, ok)
				assert.Contains(t, desc, "First line")
				assert.Contains(t, desc, "Second line")
				assert.NotContains(t, desc, "@default")
				assert.Equal(t, "endval", p["default"])
			},
		},
		"section without separator consumed": {
			// @section without " -- " separator should still be consumed
			// and not leak into the description.
			input: stringtest.Input(`
				# -- Description of value
				# @section Security
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description of value", desc)
				assert.NotContains(t, desc, "@section")
				assert.NotContains(t, desc, "Security")
			},
		},
		"notationType without separator consumed": {
			// @notationType without " -- " separator should still be consumed
			// and not leak into the description.
			input: stringtest.Input(`
				# -- Description of value
				# @notationType helm
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				assert.Equal(t, "Description of value", desc)
				assert.NotContains(t, desc, "@notationType")
				assert.NotContains(t, desc, "helm")
			},
		},
		"blank comment in normal mode produces double space": {
			// Blank comment lines (#) in normal mode append " " + empty,
			// resulting in double space in the description.
			input: stringtest.Input(`
				# -- First
				#
				# Second
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// Blank comment line appends " " (space + empty), then next
				// continuation appends " Second", producing double space.
				assert.Equal(t, "First  Second", desc)
			},
		},
		"raw with multiple spaces before at-raw": {
			// # @raw with extra spaces between # and @ should still activate raw mode.
			input: stringtest.Input(`
				# -- Description
				#  @raw
				# Line 1
				# Line 2
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// @raw with extra space activates raw mode.
				assert.Contains(t, desc, "\n")
				assert.Equal(t, stringtest.JoinLF("Description", "Line 1", "Line 2"), desc)
			},
		},
		"old-style at end of file without trailing yaml": {
			// Old-style comment at end of file with no YAML key after it
			// is still processed by Prepare's trailing block handling.
			input: stringtest.Input(`
				key:
				  path: value
				# key.path -- Trailing comment
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				kProps, ok := k["properties"].(map[string]any)
				require.True(t, ok)

				p, ok := kProps["path"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Trailing comment", p["description"])
			},
		},
		"ignore on value inline comment": {
			// @ignore on the value node's inline comment should also
			// trigger skip (substring check on all comments).
			input: stringtest.Input(`
				secret: value # @ignore
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
		"nil value produces no type constraint": {
			// Divergence: upstream defaults nil to "string" type. We emit no
			// type constraint (fail-open principle).
			input: stringtest.Input(`
				# -- A nil value
				nilVal:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				n, ok := props["nilVal"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "A nil value", n["description"])
				// No type constraint for nil values (fail-open divergence).
				assert.Nil(t, n["type"])
			},
		},
		"notationType does not become type fallback": {
			// Divergence: upstream uses @notationType as a type fallback when
			// no (type) hint is present. We ignore @notationType entirely.
			input: stringtest.Input(`
				# -- A string with notation
				# @notationType -- yaml
				val: some-value
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "A string with notation", v["description"])
				// Type comes from value inference (string), not @notationType.
				assert.Equal(t, "string", v["type"])
			},
		},
		"standalone default without dash-dash divergence": {
			// Divergence: upstream requires "# --" to be present for
			// getDescriptionFromNode to produce any output. A standalone
			// @default without "# --" produces nothing upstream. We detect
			// standalone @default and set the schema default.
			input: stringtest.Input(`
				# @default -- standalone-val
				key: actual
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				k, ok := props["key"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "standalone-val", k["default"])
			},
		},
		"standalone default keeps old-style description": {
			// A standalone "# @default --" on a node must not shadow the
			// old-style "# key.path -- desc" description scanned from the file;
			// the description survives and the @default supplies the default.
			input: stringtest.Input(`
				# image.tag -- The image tag
				image:
				  # @default -- 1.2.3
				  tag: latest
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				image, ok := props["image"].(map[string]any)
				require.True(t, ok)

				imageProps, ok := image["properties"].(map[string]any)
				require.True(t, ok)

				tag, ok := imageProps["tag"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "The image tag", tag["description"])
				assert.Equal(t, "1.2.3", tag["default"])
			},
		},
		"multiline raw description matches upstream": {
			// From upstream TestMultilineRawDescription: description with @raw and
			// @default. Verifies raw joining with blank comment lines.
			input: stringtest.Input(`
				# -- (list) I mean, dogs are quite nice too...
				# @raw
				#
				# List of default dogs:
				#  - Umbra
				#  - Penumbra
				#  - Somnus
				#
				# @default -- The list of dogs that _I_ own
				dogs:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["dogs"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", d["type"])
				assert.Equal(t, "The list of dogs that _I_ own", d["default"])

				desc, ok := d["description"].(string)
				require.True(t, ok)

				// Raw mode joins with newlines. Blank "#" produces empty line.
				want := stringtest.JoinLF(
					"I mean, dogs are quite nice too...",
					"",
					"List of default dogs:",
					" - Umbra",
					" - Penumbra",
					" - Somnus",
					"",
				)
				assert.Equal(t, want, desc)
			},
		},
		"section with annotations matches upstream ordering": {
			// From upstream TestSectionWithAnnotations: @default, @raw, and
			// @section in the same block. Section is consumed, other annotations
			// work correctly.
			input: stringtest.Input(`
				# -- This describes a lion
				# @default -- Rawr
				# @section -- Feline Section
				lion:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["lion"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "This describes a lion", l["description"])
				assert.Equal(t, "Rawr", l["default"])

				// @section is consumed but not in schema output.
			},
		},
		"raw with section matches upstream": {
			// From upstream TestSectionWithAnnotations: @raw + @section combo.
			input: stringtest.Input(`
				# -- This describes a cat
				# @raw
				# -Rawr
				# @section -- Feline Section
				cat:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cat"].(map[string]any)
				require.True(t, ok)

				// In raw mode, continuation joined with \n. @section is consumed.
				// Upstream produces "This describes a cat\n-Rawr".
				assert.Equal(t, stringtest.JoinLF("This describes a cat", "-Rawr"), c["description"])
			},
		},
		"type hint with section matches upstream": {
			// From upstream TestSectionWithAnnotations: (int) type hint + @section.
			input: stringtest.Input(`
				# -- (int) This describes a leopard
				# @section -- Feline Section
				leopard:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["leopard"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "integer", l["type"])
				assert.Equal(t, "This describes a leopard", l["description"])
			},
		},
		"commented-out key triggers issue 96 workaround": {
			// Upstream issue #96: when a commented-out key bleeds into
			// the next key's HeadComment, only the last "# --" group is used.
			input: stringtest.Input(`
				# -- before desc
				before: 1

				# -- commented desc
				#commented:

				# -- after desc
				after: 3
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				b, ok := props["before"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "before desc", b["description"])

				a, ok := props["after"].(map[string]any)
				require.True(t, ok)
				// The "after" key's HeadComment contains both
				// "# -- commented desc" and "#commented:" and "# -- after desc".
				// The issue #96 workaround takes the last "# --" group.
				assert.Equal(t, "after desc", a["description"])
			},
		},
		"multiline continuation without raw matches upstream": {
			// From upstream TestAutoMultilineDescription: continuation text
			// joined with spaces (normal mode), with @default.
			input: stringtest.Input(`
				# -- The best kind of animal probably, allow me to list their many varied benefits.
				# Cats are very funny, and quite friendly, in almost all cases
				# @default -- The list of cats that _I_ own
				cats:
				  - echo
				  - foxtrot
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cats"].(map[string]any)
				require.True(t, ok)

				desc, ok := c["description"].(string)
				require.True(t, ok)
				assert.Equal(
					t,
					"The best kind of animal probably, allow me to list their many varied benefits. Cats are very funny, and quite friendly, in almost all cases",
					desc,
				)
				assert.Equal(t, "The list of cats that _I_ own", c["default"])
			},
		},
		"custom declared type list/csv verbatim upstream mapped here": {
			// Upstream TestExtractCustomDeclaredType: stores "list/csv" verbatim.
			// We map compound types using last segment: "csv" is not in our
			// mapping, so type falls through to structural inference.
			input: stringtest.Input(`
				# -- (list/csv) My animals lists but annotated as csv field
				cats: "mike,ralph"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cats"].(map[string]any)
				require.True(t, ok)

				// "csv" is not in our mapping, type comes from value (string).
				assert.Equal(t, "string", c["type"])
				assert.Equal(t, "My animals lists but annotated as csv field", c["description"])
			},
		},
		"custom declared type string/email verbatim upstream mapped here": {
			// Upstream TestExtractCustomDeclaredType: stores "string/email" verbatim.
			// We map compound types using last segment: "email" is not in our
			// mapping, so type falls through to structural inference.
			input: stringtest.Input(`
				# -- (string/email) This has to be email address
				email: "owner@home.org"
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				e, ok := props["email"].(map[string]any)
				require.True(t, ok)

				// "email" is not in our mapping, type comes from value (string).
				assert.Equal(t, "string", e["type"])
				assert.Equal(t, "This has to be email address", e["description"])
			},
		},
		"nil value with type hint uses mapped type": {
			// Upstream TestAutoMultilineDescriptionWithoutValue: nil value
			// with (list) type hint sets type to "list" in upstream. We map
			// to "array".
			input: stringtest.Input(`
				# -- (list) I mean, dogs are quite nice too...
				# @default -- The list of dogs that _I_ own
				dogs:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				d, ok := props["dogs"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", d["type"])
				assert.Equal(t, "I mean, dogs are quite nice too...", d["description"])
				assert.Equal(t, "The list of dogs that _I_ own", d["default"])
			},
		},
		"non-annotated comment does not produce description": {
			// Upstream TestSimpleAutoDoc: "# doesn't show up" without "# --"
			// does not produce a description. Matches upstream behavior where
			// getDescriptionFromNode requires "# --" to be present.
			input: stringtest.Input(`
				# doesn't show up
				hello: world
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				h, ok := props["hello"].(map[string]any)
				require.True(t, ok)

				// No description since the comment lacks "# --".
				assert.Equal(t, "string", h["type"])
			},
		},
		"notationType on nil value does not become type": {
			// Divergence: upstream TestExtractValueNotationType shows that
			// @notationType on a nil value with a (list) type hint uses
			// "list" as the type. Without a type hint, @notationType would
			// become the type fallback. We ignore @notationType entirely
			// and emit no type constraint for nil values.
			input: stringtest.Input(`
				# -- Declaring as yaml
				# @notationType -- yaml
				lizards:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				l, ok := props["lizards"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "Declaring as yaml", l["description"])
				// No type constraint: @notationType is ignored, nil value
				// produces no type (fail-open).
				assert.Nil(t, l["type"])
			},
		},
		"second raw line in continuation appended to description": {
			// When @raw is already active and a second "# @raw" line appears,
			// the second @raw does not re-activate raw mode (already active).
			// Instead, it falls through to continuation and is appended.
			// This matches upstream behavior.
			input: stringtest.Input(`
				# -- Description
				# @raw
				# Line 1
				# @raw
				# Line 2
				val: x
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				v, ok := props["val"].(map[string]any)
				require.True(t, ok)

				desc, ok := v["description"].(string)
				require.True(t, ok)
				// Second @raw falls through to continuation, captured as "@raw".
				assert.Equal(t, stringtest.JoinLF("Description", "Line 1", "@raw", "Line 2"), desc)
			},
		},
		"different sections matches upstream behavior": {
			// From upstream TestDifferentSections: @raw with " - Moooe"
			// produces raw description with newline joining.
			input: stringtest.Input(`
				# -- This describes a cow
				# @raw
				# - Moooe
				# @section -- Cow Section
				cow:
			`),
			want: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				c, ok := props["cow"].(map[string]any)
				require.True(t, ok)

				// Raw mode with @section consumed.
				assert.Equal(t, stringtest.JoinLF("This describes a cow", "- Moooe"), c["description"])
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(norwoodj.New()),
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

func TestHelmDocsAnnotatorForContentResetsState(t *testing.T) {
	t.Parallel()

	// First file has an old-style comment for key.path.
	// Second file has no comments.
	// After a second ForContent call, the old-style state should be reset.
	ann := norwoodj.New()

	firstFile := stringtest.Input(`
		# key.path -- First file desc
		key:
		  path: value
	`)
	secondFile := stringtest.Input(`
		key:
		  path: value
	`)

	// First ForContent with old-style comment.
	_, err := ann.ForContent([]byte(firstFile))
	require.NoError(t, err)

	// Second ForContent with no comments returns a fresh clone.
	prepared, err := ann.ForContent([]byte(secondFile))
	require.NoError(t, err)

	// Generate with second file only (no comments).
	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(prepared),
	)
	schema, err := gen.Generate([]byte(secondFile))
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	k, ok := props["key"].(map[string]any)
	require.True(t, ok)

	kProps, ok := k["properties"].(map[string]any)
	require.True(t, ok)

	p, ok := kProps["path"].(map[string]any)
	require.True(t, ok)

	// No description should be set since ForContent was called again
	// with a file that has no old-style comments.
	assert.Empty(t, p["description"])
}

func TestHelmDocsAnnotatorFromFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/helm_docs.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(norwoodj.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/helm_docs.schema.json", schema)
}

// TestHelmDocsAnnotatorRealWorld generates a schema for the grafana loki
// chart's values.yaml, which carries helm-docs # -- annotations on over a
// thousand properties, including (type) hints and @default overrides.
//
// Vendored via `helm show values loki --repo
// https://grafana.github.io/helm-charts --version 6.55.0`.
func TestHelmDocsAnnotatorRealWorld(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/loki_values.yaml")
	require.NoError(t, err)

	gen := magicschema.NewGenerator(
		magicschema.WithAnnotators(norwoodj.New()),
	)
	schema, err := gen.Generate(data)
	require.NoError(t, err)

	assertGolden(t, "testdata/loki_values.schema.json", schema)
}

// TestHelmDocsAnnotatorNewStyleSeparatorInDescription covers a new-style
// "# -- description" whose text itself contains " -- ". The shared helm-docs
// regex needs whitespace before the "--" separator, so it would bind the last
// " -- " on the line and misread the leading text as an old-style key path,
// dropping the description entirely. The marker must be recognized by position
// so the full description survives.
func TestHelmDocsAnnotatorNewStyleSeparatorInDescription(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  string
	}{
		"internal separator": {
			input: stringtest.Input(`
				# -- See the docs -- section 3 for details
				name: test
			`),
			want: "See the docs -- section 3 for details",
		},
		"trailing separator": {
			input: stringtest.Input(`
				# -- A description ending in a marker --
				name: test
			`),
			want: "A description ending in a marker --",
		},
		"plain description still works": {
			input: stringtest.Input(`
				# -- A normal description
				name: test
			`),
			want: "A normal description",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gen := magicschema.NewGenerator(
				magicschema.WithAnnotators(norwoodj.New()),
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

			assert.Equal(t, tc.want, n["description"])
		})
	}
}
