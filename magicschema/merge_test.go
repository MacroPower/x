package magicschema_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
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
		"null widens to a nullable type union": {
			inputA: "val: null\n",
			inputB: "val: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, ok := props["val"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"string", "null"}, val["type"])
			},
		},
		"overlay null keeps base object valid": {
			// The Helm idiom: an overlay clears a base value by setting
			// it to null, and that overlay must still validate against
			// the merged schema.
			inputA: "resources:\n  limits: null\n",
			inputB: "resources:\n  limits:\n    cpu: 100m\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				resources, ok := props["resources"].(map[string]any)
				require.True(t, ok)

				resourcesProps, ok := resources["properties"].(map[string]any)
				require.True(t, ok)

				limits, ok := resourcesProps["limits"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"object", "null"}, limits["type"])

				limitsProps, ok := limits["properties"].(map[string]any)
				require.True(t, ok)

				assert.Contains(t, limitsProps, "cpu")
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
		"one-sided items drops the constraint (fail open)": {
			// A typed list merged with an empty list: the empty side places
			// no items constraint, so the union must accept any element. A
			// one-sided items schema grafted onto the union would fail closed,
			// rejecting elements the empty side accepted.
			inputA: "list:\n  - hello\n  - world\n",
			inputB: "list: []\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				list, ok := props["list"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "array", list["type"])
				assert.Nil(t, list["items"], "a one-sided items constraint must not survive the union")
			},
		},
		"overlay null keeps base array items": {
			// The Helm idiom for arrays: an overlay clears a base list by
			// setting it to null. The null side carries no array evidence, so
			// the base file's element schema survives the [array, null]
			// widening, matching the properties an object keeps through the
			// same merge.
			inputA: "list:\n  - 1\n  - 2\n",
			inputB: "list: null\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				list, ok := props["list"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"array", "null"}, list["type"])

				items, ok := list["items"].(map[string]any)
				require.True(t, ok, "the typed side's items must survive a null merge")
				assert.Equal(t, "integer", items["type"])
			},
		},
		"base null keeps overlay array items": {
			// The mirror of the case above: the null side comes first, so the
			// merge keeps the other side's items through the b-side branch.
			inputA: "list: null\n",
			inputB: "list:\n  - 1\n  - 2\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				list, ok := props["list"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"array", "null"}, list["type"])

				items, ok := list["items"].(map[string]any)
				require.True(t, ok, "the typed side's items must survive a null merge")
				assert.Equal(t, "integer", items["type"])
			},
		},
		"incompatible widen drops object constraints": {
			// When object + string widens away the type, the object-specific
			// keywords must drop too: a schema with properties but no type
			// still constrains object instances, failing closed.
			inputA: "val:\n  key: x\n",
			inputB: "val: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, isMap := props["val"].(map[string]any)
				if !isMap {
					return // true schema satisfies "no constraint"
				}

				assert.Nil(t, val["type"])
				assert.Nil(t, val["properties"])
				assert.Nil(t, val["additionalProperties"])
				assert.Nil(t, val["required"])
			},
		},
		"incompatible widen drops array items": {
			inputA: "val:\n  - a\n",
			inputB: "val: hello\n",
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				val, isMap := props["val"].(map[string]any)
				if !isMap {
					return // true schema satisfies "no constraint"
				}

				assert.Nil(t, val["type"])
				assert.Nil(t, val["items"])
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
		"required intersection is sorted deterministically": {
			// Both inputs require the same children but list them in opposite
			// order. The merged required array must be sorted so the output
			// does not depend on which file is merged first.
			inputA: "# @schema\n# required: [alpha, beta]\n# @schema\nparent:\n  alpha: 1\n  beta: 2\n",
			inputB: "# @schema\n# required: [beta, alpha]\n# @schema\nparent:\n  alpha: 1\n  beta: 2\n",
			opts: []magicschema.Option{
				magicschema.WithAnnotators(dadav.New()),
			},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				parent, ok := props["parent"].(map[string]any)
				require.True(t, ok)

				req, ok := parent["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"alpha", "beta"}, req)
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

				config, isMap := props["config"].(map[string]any)
				if !isMap {
					// Dropping the type drops the object-specific keywords
					// with it, so the fully unconstrained merge marshals as
					// the true schema, which also fails open.
					assert.Equal(t, true, props["config"])

					return
				}

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
		inputA    string
		inputB    string
		wantType  string // single type; empty means no type constraint
		wantTypes []any  // type union; takes precedence over wantType
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
		"string + null -> [string, null]": {
			inputA:    "val: hello\n",
			inputB:    "val: null\n",
			wantTypes: []any{"string", "null"},
		},
		"null + integer -> [integer, null]": {
			inputA:    "val: null\n",
			inputB:    "val: 42\n",
			wantTypes: []any{"integer", "null"},
		},
		"null + boolean -> [boolean, null]": {
			inputA:    "val: null\n",
			inputB:    "val: true\n",
			wantTypes: []any{"boolean", "null"},
		},
		"null + number -> [number, null]": {
			inputA:    "val: null\n",
			inputB:    "val: 3.14\n",
			wantTypes: []any{"number", "null"},
		},
		"null + array -> [array, null]": {
			inputA:    "val: null\n",
			inputB:    "val:\n  - a\n",
			wantTypes: []any{"array", "null"},
		},
		"null + object -> [object, null]": {
			inputA:    "val: null\n",
			inputB:    "val:\n  key: x\n",
			wantTypes: []any{"object", "null"},
		},
		"null + null -> no constraint": {
			inputA:   "val: null\n",
			inputB:   "val: null\n",
			wantType: "",
		},
		"empty value + integer -> [integer, null]": {
			// An empty value parses to the same null node as the explicit
			// null, ~, Null, and NULL spellings, so all of them carry null
			// into the union.
			inputA:    "val:\n",
			inputB:    "val: 42\n",
			wantTypes: []any{"integer", "null"},
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

			switch {
			case len(tc.wantTypes) > 0:
				val, ok := props["val"].(map[string]any)
				require.True(t, ok, "expected val to be a map")
				assert.Equal(t, tc.wantTypes, val["type"])

			case tc.wantType == "":
				// No type constraint: property may be true (true schema)
				// or a map without a "type" key.
				val, isMap := props["val"].(map[string]any)
				if isMap {
					assert.Nil(t, val["type"], "expected no type constraint")
				}

			default:
				val, ok := props["val"].(map[string]any)
				require.True(t, ok, "expected val to be a map")
				assert.Equal(t, tc.wantType, val["type"])
			}
		})
	}
}

func TestMergeNullCarryThreeInputs(t *testing.T) {
	t.Parallel()

	// The null carry is order-independent: a null in any one input widens
	// the merged type the same way regardless of merge order.
	tcs := map[string]struct {
		inputs []string
	}{
		"null first":  {inputs: []string{"val: null\n", "val: a\n", "val: b\n"}},
		"null middle": {inputs: []string{"val: a\n", "val: null\n", "val: b\n"}},
		"null last":   {inputs: []string{"val: a\n", "val: b\n", "val: null\n"}},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			val, ok := props["val"].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, []any{"string", "null"}, val["type"])
		})
	}
}

func TestMergeIncompatibleThreeInputsStaysFailOpen(t *testing.T) {
	t.Parallel()

	// An incompatible pair widens the type away to "accept everything". A
	// third input must not let the merge read that absent type as a null and
	// re-emit [type, null]: that would reject the incompatible input and
	// falsely admit null. The result must stay typeless, with no internal
	// marker leaking into the output.
	tcs := map[string]struct {
		inputs []string
	}{
		"string, integer, string": {
			inputs: []string{"val: foo\n", "val: 1\n", "val: bar\n"},
		},
		"string, integer, integer": {
			inputs: []string{"val: foo\n", "val: 1\n", "val: 2\n"},
		},
		"incompatible last": {
			inputs: []string{"val: 1\n", "val: 2\n", "val: foo\n"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator()
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			assert.NotContains(t, string(out), "typeless_union",
				"internal merge marker must not leak into the output")

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			// No type constraint: val may be the true schema (JSON true) or a
			// map without a "type" key.
			if val, isMap := props["val"].(map[string]any); isMap {
				assert.Nil(t, val["type"], "incompatible union must stay typeless")
			} else {
				assert.Equal(t, true, props["val"], "expected the true schema")
			}
		})
	}
}

func TestMergeTypelessConstraintThreeInputsStaysFailOpen(t *testing.T) {
	t.Parallel()

	// A typeless annotation-only constraint schema (pattern, no type since the
	// value is null) merged against a typed input yields a typeless union, and
	// the one-sided pattern drops (fail open). A third input with a different
	// type must not let the merge read that bare typeless result as a null and
	// re-emit [type, null]: that would reject the values the earlier inputs
	// hold and falsely constrain the union. The result must stay typeless,
	// with no internal marker leaking into the output.
	tcs := map[string]struct {
		inputs []string
	}{
		"constraint first": {
			inputs: []string{"# @schema pattern:^a\nkey:\n", "key: xyz\n", "key: 42\n"},
		},
		"constraint middle": {
			inputs: []string{"key: xyz\n", "# @schema pattern:^a\nkey:\n", "key: 42\n"},
		},
		"constraint last": {
			inputs: []string{"key: xyz\n", "key: 42\n", "# @schema pattern:^a\nkey:\n"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			assert.NotContains(t, string(out), "typeless_union",
				"internal merge marker must not leak into the output")

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			// No type constraint: key may be the true schema (JSON true) or a
			// map without a "type" key.
			if key, isMap := props["key"].(map[string]any); isMap {
				assert.Nil(t, key["type"], "typeless constraint union must stay typeless")
			} else {
				assert.Equal(t, true, props["key"], "expected the true schema")
			}
		})
	}
}

func TestMergeTwoTypelessConstraintsStayFailOpen(t *testing.T) {
	t.Parallel()

	// Merging two typeless annotation-only constraint schemas (differing
	// patterns, so both drop) must produce a marked typeless union, not a
	// bare empty schema: a later typed input would read the bare schema as a
	// null and emit a [type, null] union that rejects values the constraint
	// inputs accepted, and the output would depend on input order.
	tcs := map[string]struct {
		inputs []string
	}{
		"constraints first": {
			inputs: []string{"# @schema pattern:^a\nkey:\n", "# @schema pattern:^b\nkey:\n", "key: 42\n"},
		},
		"constraints split": {
			inputs: []string{"# @schema pattern:^a\nkey:\n", "key: 42\n", "# @schema pattern:^b\nkey:\n"},
		},
		"constraints last": {
			inputs: []string{"key: 42\n", "# @schema pattern:^a\nkey:\n", "# @schema pattern:^b\nkey:\n"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			assert.NotContains(t, string(out), "typeless_union",
				"internal merge marker must not leak into the output")

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			if key, isMap := props["key"].(map[string]any); isMap {
				assert.Nil(t, key["type"], "typeless constraint union must stay typeless")
			} else {
				assert.Equal(t, true, props["key"], "expected the true schema")
			}
		})
	}
}

func TestMergeTypelessConstraintDropsOneSidedItems(t *testing.T) {
	t.Parallel()

	// A typeless constraint schema (pattern, no type) permits any array with
	// any elements, so its instances do reach items: grafting the typed
	// side's items onto the typeless union would reject arrays the
	// constraint input accepted (fail closed). Unlike a null stand-in, whose
	// instances never reach items, the constraint side must drop them.
	inputs := [][]byte{
		[]byte("# @schema pattern:^a\nkey:\n"),
		[]byte("key: [x, y]\n"),
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
	schema, err := gen.Generate(inputs...)
	require.NoError(t, err)

	out, err := json.Marshal(schema)
	require.NoError(t, err)

	var got map[string]any

	require.NoError(t, json.Unmarshal(out, &got))

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok)

	if key, isMap := props["key"].(map[string]any); isMap {
		assert.Nil(t, key["items"], "one-sided items must drop against a typeless constraint side")
	} else {
		assert.Equal(t, true, props["key"], "expected the true schema")
	}
}

func TestConcurrentGenerateSharedAnnotatorPrototype(t *testing.T) {
	t.Parallel()

	// An annotator may return the same cached AnnotationResult from every
	// call, and its sub-schemas are aliased (not cloned) into the merged
	// tree. The output walk that strips the internal typeless-union marker
	// must not write into schemas that do not carry it -- even a no-op map
	// delete is a write -- or concurrent Generate calls race on the shared
	// prototype (caught by -race).
	shared := &magicschema.AnnotationResult{
		Schema: &jsonschema.Schema{
			Type: "array",
			Items: &jsonschema.Schema{
				Type:  "string",
				Extra: map[string]any{"x-note": "shared"},
			},
		},
	}

	gen := magicschema.NewGenerator(magicschema.WithAnnotators(
		stubAnnotator{name: "shared", result: shared},
	))

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			_, err := gen.Generate([]byte("list:\n  - a\n"))
			assert.NoError(t, err)
		})
	}

	wg.Wait()
}

func TestMergeIncompatibleEnumOrderIndependent(t *testing.T) {
	t.Parallel()

	// Three enum-constrained inputs whose types are incompatible (integer vs
	// string) fold to a typeless union. The unioned value set must survive
	// regardless of fold order: the two-input incompatible path kept the enum
	// while the multi-input typeless fast path once dropped it, so the surviving
	// constraint depended on input order.
	enumOf := func(t *testing.T, inputs ...string) []any {
		t.Helper()

		bs := make([][]byte, len(inputs))
		for i, in := range inputs {
			bs[i] = []byte(in)
		}

		gen := magicschema.NewGenerator(magicschema.WithAnnotators(losisin.New()))
		schema, err := gen.Generate(bs...)
		require.NoError(t, err)

		out, err := json.Marshal(schema)
		require.NoError(t, err)

		assert.NotContains(t, string(out), "typeless_union",
			"internal merge marker must not leak into the output")

		var got map[string]any

		require.NoError(t, json.Unmarshal(out, &got))

		props, ok := got["properties"].(map[string]any)
		require.True(t, ok)

		val, ok := props["val"].(map[string]any)
		require.True(t, ok)

		assert.Nil(t, val["type"], "incompatible union must stay typeless")

		enum, ok := val["enum"].([]any)
		require.True(t, ok, "the unioned enum must survive the incompatible fold")

		return enum
	}

	intInput := "# @schema type:integer;enum:[1, 2]\nval: 1\n"
	strA := "# @schema type:string;enum:[a]\nval: a\n"
	strB := "# @schema type:string;enum:[b]\nval: b\n"

	forward := enumOf(t, intInput, strA, strB)
	reverse := enumOf(t, strB, strA, intInput)

	assert.ElementsMatch(t, []any{float64(1), float64(2), "a", "b"}, forward)
	assert.Equal(t, forward, reverse,
		"the merged enum must be byte-identical regardless of fold order")
}

func TestMergeIncompatibleMetadataFoldOrderIndependent(t *testing.T) {
	t.Parallel()

	// One annotated input carries informational metadata (default, examples,
	// deprecated, $comment, and an x-* annotation); the other two inputs hold
	// incompatible types, so the fold widens the type away. The metadata must
	// survive no matter which position the annotated input occupies: the
	// two-input incompatible path carries it onto the marked typeless union,
	// and the multi-input fast path must carry the same set, or which
	// metadata survives depends on how many inputs fold through the marked
	// schema.
	annotated := "# @schema\n" +
		"# type: string\n" +
		"# default: keepme\n" +
		"# examples: [keepme]\n" +
		"# deprecated: true\n" +
		"# $comment: keepme\n" +
		"# x-custom: keepme\n" +
		"# @schema\n" +
		"val: hello\n"

	tcs := map[string]struct {
		inputs []string
	}{
		"annotated first":  {inputs: []string{annotated, "val: 1\n", "val: true\n"}},
		"annotated middle": {inputs: []string{"val: 1\n", annotated, "val: true\n"}},
		"annotated last":   {inputs: []string{"val: 1\n", "val: true\n", annotated}},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator(magicschema.WithAnnotators(dadav.New()))
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			assert.NotContains(t, string(out), "typeless_union",
				"internal merge marker must not leak into the output")

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			val, ok := props["val"].(map[string]any)
			require.True(t, ok, "the carried metadata must survive the fold")

			assert.Nil(t, val["type"], "incompatible union must stay typeless")
			assert.Equal(t, "keepme", val["default"])
			assert.Equal(t, []any{"keepme"}, val["examples"])
			assert.Equal(t, true, val["deprecated"])
			assert.Equal(t, "keepme", val["$comment"])
			assert.Equal(t, "keepme", val["x-custom"])
		})
	}
}

func TestMergeIncompatibleThreeInputsKeepsInferredDefault(t *testing.T) {
	t.Parallel()

	// Inferred defaults carry first-wins through a union merge, so the first
	// input's default must survive an incompatible three-input fold exactly as
	// it survives a two-input one: routing the marked typeless schema through
	// the multi-input fast path must not drop it.
	tcs := map[string]struct {
		inputs []string
		want   any
	}{
		"string first":  {inputs: []string{"val: hello\n", "val: 5\n", "val: world\n"}, want: "hello"},
		"integer first": {inputs: []string{"val: 5\n", "val: hello\n", "val: world\n"}, want: float64(5)},
		"two inputs":    {inputs: []string{"val: hello\n", "val: 5\n"}, want: "hello"},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inputs := make([][]byte, len(tc.inputs))
			for i, in := range tc.inputs {
				inputs[i] = []byte(in)
			}

			gen := magicschema.NewGenerator(magicschema.WithInferDefaults(true))
			schema, err := gen.Generate(inputs...)
			require.NoError(t, err)

			out, err := json.Marshal(schema)
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(out, &got))

			props, ok := got["properties"].(map[string]any)
			require.True(t, ok)

			val, ok := props["val"].(map[string]any)
			require.True(t, ok, "the carried default must survive the fold")

			assert.Nil(t, val["type"], "incompatible union must stay typeless")
			assert.Equal(t, tc.want, val["default"])
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
		"identity keywords survive a union merge": {
			// $comment is set by a higher-priority annotator on the first input
			// and must not vanish when a second input is merged in: it
			// annotates rather than constrains, so it carries first-wins like
			// description.
			inputA: "# @schema\n# $comment: keepme\n# type: string\n# @schema\nmode: x\n",
			inputB: "mode: y\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				mode, ok := props["mode"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "keepme", mode["$comment"])
			},
		},
		"null-only annotated type widens with typed file": {
			inputA: "# @schema type:null\nhost:\n",
			inputB: "host: example.com\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				host, ok := props["host"].(map[string]any)
				require.True(t, ok)

				// A single null, not ["string","null","null"]: the null
				// member from the annotation and the null carried for the
				// typeless side deduplicate.
				assert.Equal(t, []any{"string", "null"}, host["type"])
			},
		},
		"duplicate type member does not swallow the other input's type": {
			// A degenerate [string, string] union must not compare equal to
			// [string, integer] and drop integer: that would emit a
			// non-permissive schema rejecting the integer the other input
			// allows. The incompatible merge instead fails open.
			inputA: "# @schema type:[string, string]\nhost:\n",
			inputB: "# @schema type:[string, integer]\nhost:\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				// Host fails open to no type constraint (the true schema or
				// a typeless map); it must never be ["string", "string"].
				if host, ok := props["host"].(map[string]any); ok {
					assert.Nil(t, host["type"], "expected no type constraint")
				}
			},
		},
		"typeless constraint schema does not inject null": {
			// A typeless additionalProperties constraint (pattern) merged
			// against a typed one must not widen to [type, null]: neither
			// input allowed a null value, so the union stays fail-open
			// instead of claiming null is valid.
			inputA: "# @schema\n# additionalProperties:\n#   pattern: ^[a-z]+$\n# @schema\nconf:\n  a: x\n",
			inputB: "# @schema\n# additionalProperties:\n#   type: string\n# @schema\nconf:\n  b: y\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				// The merged additionalProperties fails open (true); it must
				// never be {"type": ["string", "null"]}.
				if ap, ok := conf["additionalProperties"].(map[string]any); ok {
					assert.NotContains(t, ap["type"], "null")
				}
			},
		},
		"typeless additionalProperties-only schema does not inject null": {
			// A typeless schema whose only constraint is additionalProperties
			// permits every non-object value, so merging it against a typed
			// object must stay fail-open rather than widen to [object, null]
			// and reject the strings/numbers the typeless side accepted.
			inputA: "# @schema\n# additionalProperties:\n#   type: string\n# @schema\nconf:\n",
			inputB: "conf:\n  a: 1\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				// Conf stays typeless (fail-open); it must never gain a
				// ["object", "null"] type from the additionalProperties-only side.
				assert.Nil(t, conf["type"])
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
		"enum union is canonically ordered regardless of input order": {
			// The raw merge order would be [c, b, a]; the canonical JSON sort
			// makes the emitted enum byte-stable so swapping equivalent inputs
			// cannot change the output.
			inputA: "# @schema enum:[c, b]\nsize: c\n",
			inputB: "# @schema enum:[a]\nsize: a\n",
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
		"const and enum union as value sets": {
			// A const is a single-value enum, so const:a + enum:[a, b]
			// unions to enum:[a, b] instead of dropping both constraints.
			inputA: "# @schema const:a\nsize: a\n",
			inputB: "# @schema enum:[a, b]\nsize: b\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				size, ok := props["size"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"a", "b"}, size["enum"])
				assert.Nil(t, size["const"])
			},
		},
		"differing consts union to enum": {
			inputA: "# @schema const:a\nsize: a\n",
			inputB: "# @schema const:b\nsize: b\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				size, ok := props["size"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, []any{"a", "b"}, size["enum"])
				assert.Nil(t, size["const"])
			},
		},
		"equal consts stay const": {
			inputA: "# @schema const:a\nsize: a\n",
			inputB: "# @schema const:a\nsize: a\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				size, ok := props["size"].(map[string]any)
				require.True(t, ok)

				assert.Equal(t, "a", size["const"])
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
		"type-specific bounds drop when types are incompatible": {
			inputA: "# @schema minimum:1\nport: 8080\n",
			inputB: "# @schema minimum:2\nport: hello\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(losisin.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				port, isMap := props["port"].(map[string]any)
				if !isMap {
					return // true schema satisfies "no constraint"
				}

				assert.Nil(t, port["type"])
				assert.Nil(t, port["minimum"])
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
		"identical patternProperties kept across merge": {
			inputA: "# @schema\n# patternProperties:\n#   ^x-:\n#     type: string\n# @schema\nconf:\n  a: x\n",
			inputB: "# @schema\n# patternProperties:\n#   ^x-:\n#     type: string\n# @schema\nconf:\n  b: y\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				pp, ok := conf["patternProperties"].(map[string]any)
				require.True(t, ok, "identical patternProperties should survive the merge, got %v", conf)

				sub, ok := pp["^x-"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "string", sub["type"])
			},
		},
		"differing patternProperties dropped": {
			inputA: "# @schema\n# patternProperties:\n#   ^x-:\n#     type: string\n# @schema\nconf:\n  a: x\n",
			inputB: "# @schema\n# patternProperties:\n#   ^x-:\n#     type: integer\n# @schema\nconf:\n  b: y\n",
			opts:   []magicschema.Option{magicschema.WithAnnotators(dadav.New())},
			check: func(t *testing.T, got map[string]any) {
				t.Helper()

				props, ok := got["properties"].(map[string]any)
				require.True(t, ok)

				conf, ok := props["conf"].(map[string]any)
				require.True(t, ok)

				assert.Nil(t, conf["patternProperties"], "conflicting patternProperties should drop")
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
