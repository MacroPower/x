package jsonschema_test

import (
	"context"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.jacobcolvin.com/x/stringtest"

	"go.jacobcolvin.com/x/jsonschema"
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

// TestHookPointersArePrivateCopies pins that a hook declares through its
// canvas and its return values alone. A write through Parent or Base lands on
// a copy the generator never reads back, apart from Parent.Required.
func TestHookPointersArePrivateCopies(t *testing.T) {
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

func TestGenerateFor_TypeOverrideDropsOrphanedDefUnderCollision(t *testing.T) {
	t.Parallel()

	// Field A registers alpha.Widget's def first, then its only reference is
	// detached by the type= override. Field B's reference to beta.Widget carries
	// the provisional "#/$defs/Widget" token until render; reachability must not
	// resolve that token to the earlier-registered alpha.Widget def, which would
	// retain it as an unreferenced $defs entry.
	type Root struct {
		A alpha.Widget `json:"a" jsonschema:"type=integer"`
		B beta.Widget  `json:"b"`
	}

	s, err := jsonschema.GenerateFor[Root](t.Context())
	require.NoError(t, err)

	assert.Equal(t, "integer", s.Properties["a"].Type)
	require.Equal(t, "#/$defs/beta_Widget", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "beta_Widget")
	assert.NotContains(t, s.Defs, "alpha_Widget",
		"a def orphaned by a type= override must be dropped even when its base name collides with a live def")
}

// TestGenerateFor_RootInliningUnderBaseNameCollision pins that the
// reachability walk never string-resolves a ref node's provisional token.
// Each field ref's payload is aliased into its parent's Properties, so the
// scan can reach it through the parent before the node walk claims it; the
// provisional "#/$defs/Knot" on B's ref would then resolve to the
// first-registered claimant -- the orphaned alpha.Knot def, whose body's
// back-reference to KnotRoot would falsely report the root's def as
// referenced elsewhere and suppress root inlining.
func TestGenerateFor_RootInliningUnderBaseNameCollision(t *testing.T) {
	t.Parallel()

	s, err := jsonschema.GenerateFor[alpha.KnotRoot](t.Context())
	require.NoError(t, err)

	assert.Empty(t, s.Ref,
		"the root's def is referenced from nowhere once alpha.Knot is orphaned, so the root must inline")
	assert.Equal(t, "object", s.Type)
	require.Contains(t, s.Properties, "b")
	assert.Equal(t, "#/$defs/beta_Knot", s.Properties["b"].Ref)

	require.Contains(t, s.Defs, "beta_Knot")
	assert.NotContains(t, s.Defs, "alpha_Knot",
		"the orphaned colliding def must be dropped")
	assert.NotContains(t, s.Defs, "KnotRoot",
		"an inlined root leaves no def behind")
}
