package jsonschema_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
	"go.jacobcolvin.com/x/jsonschema/internal/tagmodel"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/alpha"
	"go.jacobcolvin.com/x/jsonschema/internal/testtypes/beta"
)

// viewMutator is a tag interpreter that writes through every pointer it is
// handed except the canvas: the sibling's base through Parent, and its own
// base. Each is a private copy, so none of the writes reaches the output.
type viewMutator struct{}

func (viewMutator) Interpret(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
	field.Base.Description = "through base"
	field.Parent.Description = "through parent"

	for _, sibling := range field.Parent.Properties {
		sibling.Description = "through a sibling"
	}

	field.Canvas.Title = "through the canvas"

	return nil
}

// heldPointers is what a tag interpreter keeps after it returns: the values
// it handed the canvas or the facade, each still reachable through a pointer
// or a container the hook owns.
type heldPointers struct {
	forbiddenMin  *int
	forbiddenBody *jsonschema.Schema
	content       *jsonschema.Schema
	constMap      map[string]any
	enumInner     []any
	example       map[string]any
	rawDefault    []byte
}

// keepingInterpreter declares through the canvas and the facade, keeping every
// value it declared so the test can write through it after generation. The
// tag value names the group of keywords the field takes, since a const and an
// enum on one field would conflict.
func (h *heldPointers) keepingInterpreter(_ context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
	c := field.Constraints()

	switch tag.Value {
	case "forbid":
		for _, forbidden := range []*jsonschema.Schema{{MinLength: h.forbiddenMin}, h.forbiddenBody} {
			err := c.ForbidSchema(forbidden)
			if err != nil {
				return fmt.Errorf("forbid schema: %w", err)
			}
		}

		field.Canvas.ContentSchema = h.content

	case "const":
		err := c.SetConst(h.constMap)
		if err != nil {
			return fmt.Errorf("pin const: %w", err)
		}

		field.Canvas.Examples = []any{h.example}
		field.Canvas.Default = h.rawDefault

	case "enum":
		err := c.SetEnum([]any{h.enumInner})
		if err != nil {
			return fmt.Errorf("set enum: %w", err)
		}
	}

	return nil
}

// TestHookPointersArePrivateCopies pins that a hook declares through its
// canvas and its return values alone, and that the output shares nothing with
// the hook. A write through Parent or Base lands on a copy the generator never
// reads back, apart from Parent.Required. A write after generation through a
// pointer or container the hook kept (a forbidden sub-schema and a bound
// inside it, a content schema, a const map, an enum member list, an examples
// element, a raw default) never reaches the rendered schema. The overlay used
// to copy the canvas's pointers and slice headers straight onto the output,
// so every one of those writes changed the re-marshaled schema.
func TestHookPointersArePrivateCopies(t *testing.T) {
	t.Parallel()

	t.Run("a write through a view", func(t *testing.T) {
		t.Parallel()

		type doc struct {
			A string `json:"a" mut:"x"`
			B string `json:"b"`
		}

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTagInterpreter("mut", viewMutator{}))
		require.NoError(t, err)

		got, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, stringtest.Input(`
			{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{
					"a":{"type":"string","title":"through the canvas"},
					"b":{"type":"string"}
				},
				"required":["a","b"],
				"additionalProperties":false
			}
		`), string(got))
	})

	t.Run("a write through a kept pointer", func(t *testing.T) {
		t.Parallel()

		type doc struct {
			Forbid *string `json:"forbid" keep:"forbid"`
			Const  string  `json:"const"  keep:"const"`
			Enum   string  `json:"enum"   keep:"enum"`
		}

		three := 3
		held := &heldPointers{
			forbiddenMin:  &three,
			forbiddenBody: &jsonschema.Schema{MaxLength: new(7), Pattern: "^x"},
			content:       &jsonschema.Schema{Type: "object"},
			constMap:      map[string]any{"k": "v"},
			enumInner:     []any{"p"},
			example:       map[string]any{"e": 1},
			rawDefault:    []byte(`"d"`),
		}

		s, err := jsonschema.GenerateFor[doc](t.Context(),
			jsonschema.WithTagInterpreter("keep", jsonschema.TagInterpreterFunc(held.keepingInterpreter)))
		require.NoError(t, err)

		before, err := json.Marshal(s)
		require.NoError(t, err)

		for _, want := range []string{
			`"minLength":3`, `"maxLength":7`, `"pattern":"^x"`,
			`"contentSchema":{"type":"object"}`, `"const":{"k":"v"}`,
			`"enum":[["p"]]`, `"examples":[{"e":1}]`, `"default":"d"`,
		} {
			assert.Contains(t, string(before), want, "the declaration must render before the writes")
		}

		three = 99
		held.forbiddenBody.MaxLength = new(1)
		held.forbiddenBody.Pattern = "^mutated"
		held.content.Type = "string"
		held.constMap["k"] = "MUTATED"
		held.enumInner[0] = "MUTATED"
		held.example["e"] = "MUTATED"
		held.rawDefault[1] = 'X'

		after, err := json.Marshal(s)
		require.NoError(t, err)
		assert.JSONEq(t, string(before), string(after),
			"a write through a pointer the hook kept must not reach the output")
	})
}

// TestHookParentRequiredIsReadBack pins the one write the generator reads
// back from the parent view: a name an interpreter appends to Required.
func TestHookParentRequiredIsReadBack(t *testing.T) {
	t.Parallel()

	type doc struct {
		A string `json:"a,omitempty" req:"x"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("req", jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
				field.Parent.Required = append(field.Parent.Required, field.Name)

				return nil
			},
		)))
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, s.Required)
}

// slotChild is the referenced type an extender grafts a branch onto.
type slotChild struct {
	X int `json:"x"`
}

// slotParent holds one reference the extender edits in place and one it
// leaves alone.
type slotParent struct {
	Grafted *slotChild `json:"grafted" jsonschema:"description=grafted"`
	Plain   *slotChild `json:"plain"`
}

// TestExtenderSlotAdditionsLandOnTheChild pins the node-backed-with-additions
// outcome: an extender that appends an allOf branch to a child reference's
// slot, and touches nothing the slot already carried, has the branch emitted
// on that reference beneath the null wrapper and beside the tag description,
// exactly as if it had written on the child's own base.
func TestExtenderSlotAdditionsLandOnTheChild(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[slotParent](t.Context(),
		jsonschema.WithTypeSchemaExtenderFor[slotParent](
			func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
				grafted := ts.Value.Properties["grafted"]
				grafted.AllOf = append(grafted.AllOf, &jsonschema.Schema{MinProperties: new(1)})

				return nil
			},
		))
	require.NoError(t, err)

	got, err := json.Marshal(s.Properties["grafted"])
	require.NoError(t, err)
	assert.JSONEq(t, stringtest.Input(`
		{
			"description":"grafted",
			"anyOf":[
				{"$ref":"#/$defs/slotChild","allOf":[{"minProperties":1}]},
				{"type":"null"}
			]
		}
	`), string(got))

	plain, err := json.Marshal(s.Properties["plain"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"anyOf":[{"$ref":"#/$defs/slotChild"},{"type":"null"}]}`, string(plain))
}

// TestExtenderReplacedSlotIsEmittedAsWritten pins the literal outcome: a slot
// whose pristine fields the extender changed is the extender's own schema, and
// the field node behind it (its tag description included) no longer renders.
func TestExtenderReplacedSlotIsEmittedAsWritten(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[slotParent](t.Context(),
		jsonschema.WithTypeSchemaExtenderFor[slotParent](
			func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
				ts.Value.Properties["grafted"].Ref = ""
				ts.Value.Properties["grafted"].Type = "integer"

				return nil
			},
		))
	require.NoError(t, err)

	got, err := json.Marshal(s.Properties["grafted"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"integer"}`, string(got))
}

// nestedOuter holds an inline struct, so an extender's view reaches the
// child's own property slots rather than a $ref to a def.
type nestedOuter struct {
	Addr struct {
		City *string `json:"city" jsonschema:"minLength=1"`
	} `json:"addr"`
}

// TestExtenderNestedSlotEditsReachTheChild pins that an extender edit below a
// child slot is an addition on the node it lands on rather than a replacement
// of the child. The child keeps its null decision and its tag facts, so the
// schema still accepts what the type marshals, while a child whose own shape
// the extender changed is emitted as written.
func TestExtenderNestedSlotEditsReachTheChild(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		extend func(value *jsonschema.Schema)
		want   string
		// Zero requires the schema to accept the marshaled zero value of the
		// type, which carries a null city.
		zero   bool
		accept []string
		reject []string
	}{
		"grandchild description keeps the child's facts": {
			extend: func(value *jsonschema.Schema) {
				value.Properties["addr"].Properties["city"].Description = "the city"
			},
			want: stringtest.Input(`
				{
					"type":"object",
					"properties":{
						"city":{
							"minLength":1,
							"anyOf":[{"type":"string","description":"the city"},{"type":"null"}]
						}
					},
					"required":["city"],
					"additionalProperties":false
				}
			`),
			zero:   true,
			accept: []string{`{"addr":{"city":"x"}}`},
			reject: []string{`{"addr":{"city":""}}`},
		},
		"property added under the child": {
			extend: func(value *jsonschema.Schema) {
				value.Properties["addr"].Properties["zip"] = &jsonschema.Schema{Type: "string"}
			},
			want: stringtest.Input(`
				{
					"type":"object",
					"properties":{
						"city":{
							"minLength":1,
							"anyOf":[{"type":"string"},{"type":"null"}]
						},
						"zip":{"type":"string"}
					},
					"required":["city"],
					"additionalProperties":false
				}
			`),
			zero:   true,
			accept: []string{`{"addr":{"city":null,"zip":"x"}}`},
			reject: []string{`{"addr":{"city":"","zip":"x"}}`, `{"addr":{"city":null,"zip":1}}`},
		},
		"child type replaced is emitted as written": {
			extend: func(value *jsonschema.Schema) {
				value.Properties["addr"] = &jsonschema.Schema{Type: "integer"}
			},
			want:   `{"type":"integer"}`,
			accept: []string{`{"addr":1}`},
			reject: []string{`{"addr":{"city":null}}`},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := jsonschema.GenerateFor[nestedOuter](t.Context(),
				jsonschema.WithTypeSchemaExtenderFor[nestedOuter](
					func(_ context.Context, _ jsonschema.TypeContext, ts *jsonschema.TypeSchema) error {
						tc.extend(ts.Value)

						return nil
					},
				))
			require.NoError(t, err)

			got, err := json.Marshal(s.Properties["addr"])
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))

			v, err := jsonschema.Compile(t.Context(), s)
			require.NoError(t, err)

			if tc.zero {
				zero, err := json.Marshal(nestedOuter{})
				require.NoError(t, err)
				require.NoError(t, v.ValidateJSON(t.Context(), zero), "marshaled zero value %s", zero)
			}

			for _, instance := range tc.accept {
				require.NoError(t, v.ValidateJSON(t.Context(), []byte(instance)), instance)
			}

			for _, instance := range tc.reject {
				require.Error(t, v.ValidateJSON(t.Context(), []byte(instance)), instance)
			}
		})
	}
}

// TestGenerateFor_TypeOverrideDropsOrphanedDefUnderCollision pins that
// reachability keys on def identity under a base-name collision, and that
// the orphan joins no collision group. Field A registers alpha.Widget's def
// first, then the type= override detaches its only reference. Field B's
// reference to beta.Widget shares the base name "Widget"; resolving it to
// the earlier-registered alpha.Widget would retain that def as an
// unreferenced $defs entry, and grouping the two would prefix the survivor
// although the output holds no other Widget.
func TestGenerateFor_TypeOverrideDropsOrphanedDefUnderCollision(t *testing.T) {
	t.Parallel()

	type Root struct {
		A alpha.Widget `json:"a" jsonschema:"type=integer"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "integer", s.Properties["a"].Type)
	require.Equal(t, "#/$defs/Widget", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "Widget")
	assert.NotContains(t, s.Defs, "alpha_Widget",
		"a def orphaned by a type= override must be dropped even when its base name collides with a live def")
	assert.NotContains(t, s.Defs, "beta_Widget",
		"a def orphaned by a type= override must not prefix the live def it collides with")
}

// TestGenerateFor_OrphanedDefNeverPrefixesName pins that the key an emitted
// def takes does not depend on the orphaned types a run happens to reflect:
// a root that also touches an orphaned twin of the same base name emits the
// key the root without the twin does.
func TestGenerateFor_OrphanedDefNeverPrefixesName(t *testing.T) {
	t.Parallel()

	type Alone struct {
		B beta.Widget `json:"b"`
	}

	type WithTwin struct {
		A alpha.Widget `json:"a" jsonschema:"type=integer"`
		B beta.Widget  `json:"b"`
	}

	alone, err := jsonschema.GenerateFor[Alone](t.Context())
	require.NoError(t, err)

	withTwin, err := jsonschema.GenerateFor[WithTwin](t.Context())
	require.NoError(t, err)

	assert.Equal(t, alone.Properties["b"].Ref, withTwin.Properties["b"].Ref)
	assert.Equal(t, alone.Defs, withTwin.Defs)
}

// TestGenerateFor_RootInliningUnderBaseNameCollision pins that the
// reachability walk resolves a ref node's $ref string to the def the node
// links, under a base-name collision. Each field ref's payload is aliased
// into its parent's Properties, so the scan can reach it through the parent
// before the node walk claims it; a string shared by both Knot defs would
// then resolve to the first-registered claimant -- the orphaned alpha.Knot
// def, whose body's back-reference to KnotRoot would falsely report the
// root's def as referenced elsewhere and suppress root inlining. The string
// the scan sees is the per-entry token before naming and the final key
// after it, and each names beta.Knot alone.
func TestGenerateFor_RootInliningUnderBaseNameCollision(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.KnotRoot](t.Context())
	require.NoError(t, err)

	assert.Empty(t, s.Ref,
		"the root's def is referenced from nowhere once alpha.Knot is orphaned, so the root must inline")
	assert.Equal(t, "object", s.Type)
	require.Contains(t, s.Properties, "b")
	assert.Equal(t, "#/$defs/Knot", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "Knot")
	assert.NotContains(t, s.Defs, "alpha_Knot",
		"the orphaned colliding def must be dropped")
	assert.NotContains(t, s.Defs, "beta_Knot",
		"the orphaned colliding def must not prefix the live def")
	assert.NotContains(t, s.Defs, "KnotRoot",
		"an inlined root leaves no def behind")
}

// TestHookCanvasContainersAreNotAliased pins that the rendered schema shares
// no container with a slice a hook assigned to its canvas. The overlay copies
// an enum or examples header from the canvas as is, so an interpreter that
// kept the slice could reach the output after Generate returned.
func TestHookCanvasContainersAreNotAliased(t *testing.T) {
	t.Parallel()

	type doc struct {
		X string `json:"x" keep:"x"`
	}

	vals := []any{"a"}
	examples := []any{"a"}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("keep", jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
				field.Canvas.Enum = vals
				field.Canvas.Examples = examples

				return nil
			},
		)))
	require.NoError(t, err)

	vals[0] = "b"
	examples[0] = "b"

	assert.Equal(t, []any{"a"}, s.Properties["x"].Enum)
	assert.Equal(t, []any{"a"}, s.Properties["x"].Examples)
}

// TestHookCanvasEmptyEnumRefused pins that an empty enum written straight
// onto a field's canvas fails generation. The facade refuses one, but a
// direct canvas write consults nothing, and an empty enum would leave a
// struct that rejects every instance while its JSON form, which omits the
// keyword, accepts them all.
func TestHookCanvasEmptyEnumRefused(t *testing.T) {
	t.Parallel()

	type doc struct {
		X string   `empty:"x" json:"x"`
		Y []string `json:"y"`
	}

	tests := map[string]struct {
		write func(field jsonschema.FieldContext)
		want  string
	}{
		"field canvas": {
			write: func(field jsonschema.FieldContext) { field.Canvas.Enum = []any{} },
			want:  `field "x": authored canvas: keyword "enum"`,
		},
		"nil enum is absent": {
			write: func(field jsonschema.FieldContext) { field.Canvas.Enum = nil },
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.GenerateFor[doc](t.Context(),
				jsonschema.WithTagInterpreter("empty", jsonschema.TagInterpreterFunc(
					func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
						tc.write(field)

						return nil
					},
				)))
			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tagmodel.ErrNoValues)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestHookCanvasKeywordRefused pins that a keyword written on a canvas that
// generation never reads fails generation, naming the field and the
// keyword, while the canvas tree's own wiring and a write the overlay reads
// pass. The overlay copies the authored rows and allOf alone, so a type,
// required, or applicator write and an Extra entry would otherwise vanish
// with no trace.
func TestHookCanvasKeywordRefused(t *testing.T) {
	t.Parallel()

	type doc struct {
		X string         `json:"x" write:"x"`
		Y []string       `json:"y" write:"y"`
		M map[string]int `json:"m" write:"m"`
		T [2]int         `json:"t" write:"t"`
	}

	tests := map[string]struct {
		write func(field jsonschema.FieldContext)
		want  string
	}{
		"types": {
			write: func(field jsonschema.FieldContext) {
				if field.Name == "x" {
					field.Canvas.Types = []string{"string", "number"}
				}
			},
			want: `field "x": authored canvas: keyword "type"`,
		},
		"extra": {
			write: func(field jsonschema.FieldContext) {
				if field.Name == "x" {
					field.Canvas.Extra = map[string]any{"x-nullable": true}
				}
			},
			want: `field "x": authored canvas: keyword "Extra"`,
		},
		"required": {
			write: func(field jsonschema.FieldContext) {
				if field.Name == "x" {
					field.Canvas.Required = []string{"x"}
				}
			},
			want: `field "x": authored canvas: keyword "required"`,
		},
		"replaced items slot": {
			write: func(field jsonschema.FieldContext) {
				if field.Name == "y" {
					field.Canvas.Items = &jsonschema.Schema{MinLength: new(1)}
				}
			},
			want: `field "y": authored canvas: keyword "items"`,
		},
		"element types": {
			write: func(field jsonschema.FieldContext) {
				if field.Name == "y" {
					field.ElementContexts()[0].Canvas.Types = []string{"string", "null"}
				}
			},
			want: `field "y": element: authored canvas: keyword "type"`,
		},
		"wiring alone passes": {
			write: func(jsonschema.FieldContext) {},
		},
		"allOf passes": {
			write: func(field jsonschema.FieldContext) {
				field.Canvas.AllOf = []*jsonschema.Schema{{MinLength: new(1)}}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonschema.GenerateFor[doc](t.Context(),
				jsonschema.WithTagInterpreter("write", jsonschema.TagInterpreterFunc(
					func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
						tc.write(field)

						return nil
					},
				)))
			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, jsonschema.ErrCanvasKeyword)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestHookCanvasConstOnUnrestrictedKeepsNull pins that a const an
// interpreter writes on an unrestricted occurrence (an interface, a pointer
// to a raw message) rides the value branch beside a null branch. The
// occurrence admits null on its own, and a nil interface or pointer marshals
// as null, so the const must not swallow it.
func TestHookCanvasConstOnUnrestrictedKeepsNull(t *testing.T) {
	t.Parallel()

	type doc struct {
		A any             `const:"x" json:"a"`
		P *jsontext.Value `const:"x" json:"p"`
	}

	s, err := jsonschema.GenerateFor[doc](t.Context(),
		jsonschema.WithTagInterpreter("const", jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
				var value any = 1

				field.Canvas.Const = &value

				return nil
			},
		)))
	require.NoError(t, err)

	for _, name := range []string{"a", "p"} {
		raw, err := json.Marshal(s.Properties[name])
		require.NoError(t, err)
		assert.JSONEq(t, `{"anyOf":[{"const":1},{"type":"null"}]}`, string(raw), name)
	}
}

// TestFieldHookSeesFinalRefUnderCollision pins that a field-level hook, which
// runs after the $defs keys are settled, reads the final $ref in its Base
// rather than the provisional token a type-level hook sees. The interpreter
// records what it read for each field and the test compares it against the
// keys the output carries, under a base-name collision so the two differ
// from the bare type name.
func TestFieldHookSeesFinalRefUnderCollision(t *testing.T) {
	t.Parallel()

	type root struct {
		A alpha.Widget `json:"a" seen:"x"`
		B beta.Widget  `json:"b" seen:"x"`
	}

	seen := map[string]string{}

	s, err := jsonschema.GenerateFor[root](t.Context(),
		jsonschema.WithTagInterpreter("seen", jsonschema.TagInterpreterFunc(
			func(_ context.Context, field jsonschema.FieldContext, _ jsonschema.Tag) error {
				seen[field.Name] = field.Base.Ref

				return nil
			},
		)))
	require.NoError(t, err)

	want := map[string]string{"a": "#/$defs/alpha_Widget", "b": "#/$defs/beta_Widget"}
	assert.Equal(t, want, seen)
	assert.Equal(t, want["a"], s.Properties["a"].Ref)
	assert.Equal(t, want["b"], s.Properties["b"].Ref)
}

// typeReader is a tag interpreter that records the type keyword each field's
// Base carries, keyed by the tag value.
type typeReader struct{ seen map[string]string }

func (r typeReader) Interpret(_ context.Context, field jsonschema.FieldContext, tag jsonschema.Tag) error {
	r.seen[tag.Value] = field.Base.Type

	return nil
}

// TestHookViewCarriesContainerType pins that a hook reads a nilable
// container's type off FieldContext.Base. The node's payload leaves the type
// for render to place beside the null decision, and the view restores it the
// same way, so a hook dispatching on Base.Type sees the type the rendered
// schema carries.
func TestHookViewCarriesContainerType(t *testing.T) {
	t.Parallel()

	type doc struct {
		L []int          `json:"l" see:"list"`
		M map[string]int `json:"m" see:"map"`
		B []byte         `json:"b" see:"bytes"`
		P *[]int         `json:"p" see:"pointer"`
		S string         `json:"s" see:"string"`
	}

	reader := typeReader{seen: map[string]string{}}

	_, err := jsonschema.GenerateFor[doc](t.Context(), jsonschema.WithTagInterpreter("see", reader))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"list":    "array",
		"map":     "object",
		"bytes":   "string",
		"pointer": "array",
		"string":  "string",
	}, reader.seen)
}
